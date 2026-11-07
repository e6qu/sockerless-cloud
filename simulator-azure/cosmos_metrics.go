package main

import (
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
	"github.com/e6qu/sockerless-cloud/sim/logstore"
	"github.com/google/uuid"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.DocumentDB's own metric, usage and metric-definition reads (the
// armcosmos metric clients, distinct from Microsoft.Insights metrics) and the
// online/offline-region operations. Each series aggregates the samples the
// data plane recorded (cosmos_metric_samples.go) per grain; a container is
// one partition key range, "0".

// The legacy $filter spells a name as "name.value eq 'Total Requests'".
var cosmosMetricFilterRE = regexp.MustCompile(`name\.value eq '([^']*)'`)

// "timeGrain eq duration'PT5M'"
var cosmosFilterTimeGrainRE = regexp.MustCompile(`timeGrain eq duration'([^']*)'`)

// "startTime eq 2024-01-01T00:00:00Z"
var cosmosFilterTimeRE = regexp.MustCompile(`(startTime|endTime) eq ([0-9TZ:+\-.]+)`)

func registerCosmosMetrics(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.DocumentDB/databaseAccounts"
	acct := armBase + "/{account}"
	db := acct + "/databases/{databaseRid}"
	coll := db + "/collections/{collectionRid}"
	regionColl := acct + "/region/{region}/databases/{databaseRid}/collections/{collectionRid}"

	cosmosMetricLog = logstore.New(
		sim.MakeStore[logstore.Entry[cosmosMetricSample]](srv.DB(), "cosmos_metric_samples"),
		sim.MakeStore[logstore.Head](srv.DB(), "cosmos_metric_heads"))
	kvstore.StartSweeper(srv, "azure cosmos metric retention sweep", time.Hour, cosmosSweepMetricSamples)

	srv.HandleFunc("GET "+acct+"/metrics", handleCosmosListMetrics)
	srv.HandleFunc("GET "+acct+"/metricDefinitions", handleCosmosListMetricDefinitions)
	srv.HandleFunc("GET "+acct+"/usages", handleCosmosListUsages)
	srv.HandleFunc("GET "+db+"/metrics", handleCosmosListMetrics)
	srv.HandleFunc("GET "+db+"/usages", handleCosmosListUsages)
	srv.HandleFunc("GET "+db+"/metricDefinitions", handleCosmosListMetricDefinitions)
	srv.HandleFunc("GET "+coll+"/metrics", handleCosmosListMetrics)
	srv.HandleFunc("GET "+coll+"/usages", handleCosmosListUsages)
	srv.HandleFunc("GET "+coll+"/metricDefinitions", handleCosmosListMetricDefinitions)

	srv.HandleFunc("GET "+acct+"/region/{region}/metrics", handleCosmosListMetrics)
	srv.HandleFunc("GET "+regionColl+"/metrics", handleCosmosListMetrics)

	srv.HandleFunc("GET "+coll+"/partitions/metrics", handleCosmosListPartitionMetrics)
	srv.HandleFunc("GET "+coll+"/partitions/usages", handleCosmosListPartitionUsages)
	srv.HandleFunc("GET "+regionColl+"/partitions/metrics", handleCosmosListPartitionMetrics)
	srv.HandleFunc("GET "+coll+"/partitionKeyRangeId/{partitionKeyRangeId}/metrics", handleCosmosListPartitionMetrics)
	srv.HandleFunc("GET "+regionColl+"/partitionKeyRangeId/{partitionKeyRangeId}/metrics", handleCosmosListPartitionMetrics)

	srv.HandleFunc("GET "+acct+"/percentile/metrics", handleCosmosListPercentileMetrics)
	srv.HandleFunc("GET "+acct+"/sourceRegion/{sourceRegion}/targetRegion/{targetRegion}/percentile/metrics", handleCosmosListPercentileMetrics)
	srv.HandleFunc("GET "+acct+"/targetRegion/{targetRegion}/percentile/metrics", handleCosmosListPercentileMetrics)

	srv.HandleFunc("POST "+acct+"/onlineRegion", handleCosmosOnlineRegion)
	srv.HandleFunc("POST "+acct+"/offlineRegion", handleCosmosOfflineRegion)
}

type cosmosMetricFilter struct {
	names     []string
	timeGrain string
	start     time.Time
	end       time.Time
}

// An unspecified window defaults to the last hour at a five-minute grain.
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

// The subset of ISO 8601 durations Azure uses for time grains (PT1M, PT5M,
// PT1H, P1D); anything else falls back to five minutes.
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

// Bounded, so a wide window cannot produce an unbounded slice.
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

func cosmosMetricName(name string) map[string]any {
	return map[string]any{"value": name, "localizedValue": name}
}

func cosmosMetricUnit(name string) string {
	for _, d := range cosmosMetricDefs {
		if d.name == name {
			return d.unit
		}
	}
	return "Count"
}

func cosmosBuildMetrics(r *http.Request, f cosmosMetricFilter) []map[string]any {
	samples := cosmosMetricScopeOf(r).samples()
	step := cosmosTimeGrainDuration(f.timeGrain)
	ts := cosmosMetricTimestamps(f)
	out := make([]map[string]any, 0, len(f.names))
	for _, name := range f.names {
		values := make([]map[string]any, 0, len(ts))
		for _, t := range ts {
			values = append(values, cosmosMetricValue(name, samples, t, t.Add(step)))
		}
		out = append(out, map[string]any{
			"startTime":    f.start.UTC().Format(time.RFC3339),
			"endTime":      f.end.UTC().Format(time.RFC3339),
			"timeGrain":    f.timeGrain,
			"unit":         cosmosMetricUnit(name),
			"name":         cosmosMetricName(name),
			"metricValues": values,
		})
	}
	return out
}

func handleCosmosListMetrics(w http.ResponseWriter, r *http.Request) {
	f := parseCosmosMetricFilter(r, "Total Requests")
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": cosmosBuildMetrics(r, f)})
}

// cosmosPartitionID names a container's one physical partition, stable for the
// container.
func cosmosPartitionID(r *http.Request) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("cosmos-partition:"+sim.PathParam(r, "account")+"/"+sim.PathParam(r, "databaseRid")+"/"+sim.PathParam(r, "collectionRid"))).String()
}

func handleCosmosListPartitionMetrics(w http.ResponseWriter, r *http.Request) {
	f := parseCosmosMetricFilter(r, "Max RUs Per Second")
	metrics := cosmosBuildMetrics(r, f)
	pkRange := sim.PathParam(r, "partitionKeyRangeId")
	if pkRange == "" {
		pkRange = "0"
	}
	for _, m := range metrics {
		m["partitionId"] = cosmosPartitionID(r)
		m["partitionKeyRangeId"] = pkRange
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": metrics})
}

// Replication latency percentiles: every region reads the one store a write
// lands in, so each percentile, and the aggregates, are zero.
func cosmosPercentileValue(ts time.Time) map[string]any {
	v := map[string]any{"timestamp": ts.Format(time.RFC3339), "_count": 0, "average": 0.0, "minimum": 0.0, "maximum": 0.0, "total": 0.0}
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

var cosmosUsageDefs = []struct {
	name string
	unit string
}{
	{"Document Count", "Count"},
	{"Data Stored", "Bytes"},
	{"Index Size", "Bytes"},
}

// The documents, data bytes and index bytes the scope holds now.
func cosmosBuildUsages(r *http.Request) []map[string]any {
	scope := cosmosMetricScopeOf(r)
	var current cosmosMetricSample
	seen := map[string]bool{}
	prefix := scope.account + "/"
	if scope.db != "" {
		prefix += scope.db + "/"
		if scope.coll != "" {
			prefix += scope.coll + "/"
		}
	}
	for _, d := range cosmosDocs.ListPrefix(prefix) {
		parts := strings.SplitN(d.ID, "/", 4)
		if len(parts) < 4 {
			continue
		}
		key := parts[1] + "/" + parts[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		snap := cosmosContainerSnapshot(scope.account, parts[1], parts[2])
		current.Documents += snap.Documents
		current.DataBytes += snap.DataBytes
		current.IndexBytes += snap.IndexBytes
	}
	values := map[string]int64{"Document Count": current.Documents, "Data Stored": current.DataBytes, "Index Size": current.IndexBytes}
	out := make([]map[string]any, 0, len(cosmosUsageDefs))
	for _, u := range cosmosUsageDefs {
		out = append(out, map[string]any{
			"unit":         u.unit,
			"name":         cosmosMetricName(u.name),
			"quotaPeriod":  "PT5M",
			"limit":        0,
			"currentValue": values[u.name],
		})
	}
	return out
}

func handleCosmosListUsages(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": cosmosBuildUsages(r)})
}

func handleCosmosListPartitionUsages(w http.ResponseWriter, r *http.Request) {
	usages := cosmosBuildUsages(r)
	for _, u := range usages {
		u["partitionId"] = cosmosPartitionID(r)
		u["partitionKeyRangeId"] = "0"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": usages})
}

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

func handleCosmosOnlineRegion(w http.ResponseWriter, r *http.Request) {
	cosmosRegionTransition(w, r, true)
}

func handleCosmosOfflineRegion(w http.ResponseWriter, r *http.Request) {
	cosmosRegionTransition(w, r, false)
}

// Azure refuses to take the write region (failover priority 0) offline.
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
	writeAzureAsyncCreateHeaders(w, opID, opURL, resultURL)
	w.WriteHeader(http.StatusAccepted)
}
