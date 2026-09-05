package main

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Monitor legacy (Microsoft.DocumentDB-native) metric, usage and
// metric-definition surfaces for Cosmos DB accounts, plus the
// online-/offline-region long-running operations. These are the per-account
// metric GETs the armcosmos DatabaseAccounts/Database/Collection/
// CollectionPartition/Percentile/...Region clients call (distinct from the
// generic Microsoft.Insights metrics API). The simulator does not run the
// Azure Monitor aggregation pipeline for Cosmos platform telemetry, so the
// series it returns are faithfully shaped (per the swagger Metric / Usage /
// PercentileMetric schemas) with the simulator's true aggregate — zero — at
// each timestamp across the requested time grain. Region, partition and
// partition-key-range coordinates are echoed back so a caller sees the
// dimension it asked for.

// cosmosMetricFilterRE extracts a metric name from the Azure Monitor legacy
// $filter ("name.value eq 'Total Requests'").
var cosmosMetricFilterRE = regexp.MustCompile(`name\.value eq '([^']*)'`)

// cosmosFilterTimeGrainRE extracts the ISO-8601 duration time grain
// ("timeGrain eq duration'PT5M'").
var cosmosFilterTimeGrainRE = regexp.MustCompile(`timeGrain eq duration'([^']*)'`)

// cosmosFilterTimeRE extracts a startTime/endTime clause value
// ("startTime eq 2024-01-01T00:00:00Z").
var cosmosFilterTimeRE = regexp.MustCompile(`(startTime|endTime) eq ([0-9TZ:+\-.]+)`)

func registerCosmosMetrics(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts"
	acct := armBase + "/{account}"
	db := acct + "/databases/{databaseRid}"
	coll := db + "/collections/{collectionRid}"
	regionColl := acct + "/region/{region}/databases/{databaseRid}/collections/{collectionRid}"

	// Account-, database- and collection-level metrics / usages / definitions.
	srv.HandleFunc("GET "+acct+"/metrics", handleCosmosListMetrics)
	srv.HandleFunc("GET "+acct+"/metricDefinitions", handleCosmosListMetricDefinitions)
	srv.HandleFunc("GET "+acct+"/usages", handleCosmosListUsages)
	srv.HandleFunc("GET "+db+"/metrics", handleCosmosListMetrics)
	srv.HandleFunc("GET "+db+"/usages", handleCosmosListUsages)
	srv.HandleFunc("GET "+db+"/metricDefinitions", handleCosmosListMetricDefinitions)
	srv.HandleFunc("GET "+coll+"/metrics", handleCosmosListMetrics)
	srv.HandleFunc("GET "+coll+"/usages", handleCosmosListUsages)
	srv.HandleFunc("GET "+coll+"/metricDefinitions", handleCosmosListMetricDefinitions)

	// Region-scoped metric GETs.
	srv.HandleFunc("GET "+acct+"/region/{region}/metrics", handleCosmosListMetrics)
	srv.HandleFunc("GET "+regionColl+"/metrics", handleCosmosListMetrics)

	// Partition- and partition-key-range-scoped metrics / usages.
	srv.HandleFunc("GET "+coll+"/partitions/metrics", handleCosmosListPartitionMetrics)
	srv.HandleFunc("GET "+coll+"/partitions/usages", handleCosmosListPartitionUsages)
	srv.HandleFunc("GET "+regionColl+"/partitions/metrics", handleCosmosListPartitionMetrics)
	srv.HandleFunc("GET "+coll+"/partitionKeyRangeId/{partitionKeyRangeId}/metrics", handleCosmosListPartitionMetrics)
	srv.HandleFunc("GET "+regionColl+"/partitionKeyRangeId/{partitionKeyRangeId}/metrics", handleCosmosListPartitionMetrics)

	// Percentile metrics (account, source→target, target).
	srv.HandleFunc("GET "+acct+"/percentile/metrics", handleCosmosListPercentileMetrics)
	srv.HandleFunc("GET "+acct+"/sourceRegion/{sourceRegion}/targetRegion/{targetRegion}/percentile/metrics", handleCosmosListPercentileMetrics)
	srv.HandleFunc("GET "+acct+"/targetRegion/{targetRegion}/percentile/metrics", handleCosmosListPercentileMetrics)

	// Online/offline a regional replica (long-running operations).
	srv.HandleFunc("POST "+acct+"/onlineRegion", handleCosmosOnlineRegion)
	srv.HandleFunc("POST "+acct+"/offlineRegion", handleCosmosOfflineRegion)
}

// cosmosMetricFilter is the parsed Azure Monitor legacy metric $filter.
type cosmosMetricFilter struct {
	names     []string
	timeGrain string
	start     time.Time
	end       time.Time
}

// parseCosmosMetricFilter reads the names, time grain and time window from the
// request's $filter query parameter, defaulting an unspecified window to the
// last hour at a five-minute grain.
func parseCosmosMetricFilter(r *http.Request, defaultName string) cosmosMetricFilter {
	raw := r.URL.Query().Get("$filter")
	f := cosmosMetricFilter{timeGrain: "PT5M"}
	for _, m := range cosmosMetricFilterRE.FindAllStringSubmatch(raw, -1) {
		if m[1] != "" {
			f.names = append(f.names, m[1])
		}
	}
	if len(f.names) == 0 {
		f.names = []string{defaultName}
	}
	if m := cosmosFilterTimeGrainRE.FindStringSubmatch(raw); m != nil {
		f.timeGrain = m[1]
	}
	for _, m := range cosmosFilterTimeRE.FindAllStringSubmatch(raw, -1) {
		t, err := time.Parse(time.RFC3339, m[2])
		if err != nil {
			continue
		}
		if m[1] == "startTime" {
			f.start = t
		} else {
			f.end = t
		}
	}
	if f.end.IsZero() {
		f.end = time.Now().UTC()
	}
	if f.start.IsZero() {
		f.start = f.end.Add(-time.Hour)
	}
	return f
}

// cosmosTimeGrainDuration parses the subset of ISO-8601 durations Azure uses
// for metric time grains (PT1M, PT5M, PT1H, P1D); it falls back to five
// minutes for anything it cannot parse.
func cosmosTimeGrainDuration(tg string) time.Duration {
	switch strings.ToUpper(tg) {
	case "PT1M":
		return time.Minute
	case "PT5M":
		return 5 * time.Minute
	case "PT15M":
		return 15 * time.Minute
	case "PT30M":
		return 30 * time.Minute
	case "PT1H", "PT60M":
		return time.Hour
	case "P1D", "PT24H":
		return 24 * time.Hour
	}
	return 5 * time.Minute
}

// cosmosMetricTimestamps returns the data-point timestamps spanning the
// filter's window at its time grain (bounded so a wide window cannot produce an
// unbounded slice).
func cosmosMetricTimestamps(f cosmosMetricFilter) []time.Time {
	step := cosmosTimeGrainDuration(f.timeGrain)
	var ts []time.Time
	for t := f.start; !t.After(f.end) && len(ts) < 1440; t = t.Add(step) {
		ts = append(ts, t.UTC())
	}
	if len(ts) == 0 {
		ts = []time.Time{f.end.UTC()}
	}
	return ts
}

// cosmosZeroMetricValue is one data point carrying the simulator's true
// aggregate (zero) at a timestamp.
func cosmosZeroMetricValue(ts time.Time) map[string]any {
	return map[string]any{
		"timestamp": ts.Format(time.RFC3339),
		"_count":    0,
		"average":   0.0,
		"minimum":   0.0,
		"maximum":   0.0,
		"total":     0.0,
	}
}

func cosmosMetricName(name string) map[string]any {
	return map[string]any{"value": name, "localizedValue": name}
}

// cosmosBuildMetrics shapes a MetricListResult-style series for each requested
// metric name across the filter window.
func cosmosBuildMetrics(f cosmosMetricFilter, unit string) []map[string]any {
	ts := cosmosMetricTimestamps(f)
	out := make([]map[string]any, 0, len(f.names))
	for _, name := range f.names {
		values := make([]map[string]any, 0, len(ts))
		for _, t := range ts {
			values = append(values, cosmosZeroMetricValue(t))
		}
		out = append(out, map[string]any{
			"startTime":    f.start.UTC().Format(time.RFC3339),
			"endTime":      f.end.UTC().Format(time.RFC3339),
			"timeGrain":    f.timeGrain,
			"unit":         unit,
			"name":         cosmosMetricName(name),
			"metricValues": values,
		})
	}
	return out
}

func handleCosmosListMetrics(w http.ResponseWriter, r *http.Request) {
	f := parseCosmosMetricFilter(r, "Total Requests")
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": cosmosBuildMetrics(f, "Count")})
}

func handleCosmosListPartitionMetrics(w http.ResponseWriter, r *http.Request) {
	f := parseCosmosMetricFilter(r, "Max RUs Per Second")
	metrics := cosmosBuildMetrics(f, "Count")
	pkRange := sim.PathParam(r, "partitionKeyRangeId")
	if pkRange == "" {
		pkRange = "0"
	}
	for _, m := range metrics {
		m["partitionId"] = generateUUID()
		m["partitionKeyRangeId"] = pkRange
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": metrics})
}

// cosmosPercentileValue is a percentile data point: the MetricValue aggregates
// plus the P10–P99 distribution (all zero — the simulator records no latency
// telemetry).
func cosmosPercentileValue(ts time.Time) map[string]any {
	v := cosmosZeroMetricValue(ts)
	for _, p := range []string{"P10", "P25", "P50", "P75", "P90", "P95", "P99"} {
		v[p] = 0.0
	}
	return v
}

func handleCosmosListPercentileMetrics(w http.ResponseWriter, r *http.Request) {
	f := parseCosmosMetricFilter(r, "Probabilistic Bounded Staleness")
	ts := cosmosMetricTimestamps(f)
	out := make([]map[string]any, 0, len(f.names))
	for _, name := range f.names {
		values := make([]map[string]any, 0, len(ts))
		for _, t := range ts {
			values = append(values, cosmosPercentileValue(t))
		}
		out = append(out, map[string]any{
			"startTime":    f.start.UTC().Format(time.RFC3339),
			"endTime":      f.end.UTC().Format(time.RFC3339),
			"timeGrain":    f.timeGrain,
			"unit":         "Milliseconds",
			"name":         cosmosMetricName(name),
			"metricValues": values,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

// cosmosUsageDefs are the real Cosmos DB account usages a quota report carries.
var cosmosUsageDefs = []struct {
	name string
	unit string
}{
	{"Document Count", "Count"},
	{"Data Stored", "Bytes"},
	{"Index Size", "Bytes"},
}

func cosmosBuildUsages() []map[string]any {
	out := make([]map[string]any, 0, len(cosmosUsageDefs))
	for _, u := range cosmosUsageDefs {
		out = append(out, map[string]any{
			"unit":         u.unit,
			"name":         cosmosMetricName(u.name),
			"quotaPeriod":  "PT5M",
			"limit":        0,
			"currentValue": 0,
		})
	}
	return out
}

func handleCosmosListUsages(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": cosmosBuildUsages()})
}

func handleCosmosListPartitionUsages(w http.ResponseWriter, _ *http.Request) {
	usages := cosmosBuildUsages()
	for _, u := range usages {
		u["partitionId"] = generateUUID()
		u["partitionKeyRangeId"] = "0"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": usages})
}

// cosmosMetricDefs are the real Microsoft.DocumentDB metric definitions a
// databaseAccounts/databases/collections metricDefinitions list returns.
var cosmosMetricDefs = []struct {
	name string
	unit string
	agg  string
}{
	{"Total Requests", "Count", "Total"},
	{"Total Request Units", "Count", "Total"},
	{"Document Count", "Count", "Maximum"},
	{"Data Usage", "Bytes", "Maximum"},
	{"Index Usage", "Bytes", "Maximum"},
	{"Available Storage", "Bytes", "Maximum"},
	{"Service Availability", "Percent", "Average"},
}

func handleCosmosListMetricDefinitions(w http.ResponseWriter, r *http.Request) {
	resourceURI := r.URL.Path
	availabilities := []map[string]any{
		{"timeGrain": "PT5M", "retention": "P2D"},
		{"timeGrain": "PT1H", "retention": "P14D"},
		{"timeGrain": "P1D", "retention": "P60D"},
	}
	out := make([]map[string]any, 0, len(cosmosMetricDefs))
	for _, d := range cosmosMetricDefs {
		out = append(out, map[string]any{
			"metricAvailabilities":   availabilities,
			"primaryAggregationType": d.agg,
			"unit":                   d.unit,
			"resourceUri":            resourceURI,
			"name":                   cosmosMetricName(d.name),
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

// ── Online / offline region (long-running operations) ────────────────────────

func handleCosmosOnlineRegion(w http.ResponseWriter, r *http.Request) {
	cosmosRegionTransition(w, r, true)
}

func handleCosmosOfflineRegion(w http.ResponseWriter, r *http.Request) {
	cosmosRegionTransition(w, r, false)
}

// cosmosRegionTransition validates an online-/offline-region request against
// the account's configured regions and replies with an accepted long-running
// operation. Bringing a region offline is rejected for the write region
// (failover priority 0), matching real Azure.
func cosmosRegionTransition(w http.ResponseWriter, r *http.Request, online bool) {
	sub := sim.PathParam(r, "subscriptionId")
	id := cosmosAccountID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "account"))
	a, ok := cosmosAccounts.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Cosmos DB account not found: %s", id)
		return
	}
	var req struct {
		Region string `json:"region"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid region body: %v", err)
		return
	}
	if req.Region == "" {
		AzureError(w, "BadRequest", "The 'region' property is required.", http.StatusBadRequest)
		return
	}
	var match map[string]any
	for _, loc := range cosmosLocationMaps(a.Properties["locations"]) {
		if name, _ := loc["locationName"].(string); strings.EqualFold(name, req.Region) {
			match = loc
			break
		}
	}
	if match == nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "region %q is not configured on account %s", req.Region, a.Name)
		return
	}
	if !online && cosmosToInt(match["failoverPriority"]) == 0 {
		AzureError(w, "BadRequest", "The write region cannot be taken offline.", http.StatusBadRequest)
		return
	}

	opID := issueAzureAsyncOperation(nil)
	apiVersion := r.URL.Query().Get("api-version")
	location := strings.ToLower(strings.ReplaceAll(req.Region, " ", ""))
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.DocumentDB", location, "operationStatuses", opID, apiVersion)
	resultURL := azureAsyncOperationHeader(r, sub, "Microsoft.DocumentDB", location, "operationResults", opID, apiVersion)
	writeAzureAsyncCreateHeaders(w, opURL, resultURL)
	w.WriteHeader(http.StatusAccepted)
}
