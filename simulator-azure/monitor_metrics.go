package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Monitor platform metrics: the points a resource's data plane records
// and the metrics and metricDefinitions reads of the resource's own
// Microsoft.Insights provider, which list them per time grain and
// aggregation.

// AzureMetricPoint is one value of a resource's metric at a moment.
type AzureMetricPoint struct {
	ID         string            `json:"id"`
	ResourceID string            `json:"resourceId"`
	Metric     string            `json:"metric"`
	Unit       string            `json:"unit"`
	Value      float64           `json:"value"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
	At         time.Time         `json:"at"`
}

var azureMetricPoints sim.Store[AzureMetricPoint]

// azureMetricRetention is how long Azure Monitor keeps platform metrics.
const azureMetricRetention = 93 * 24 * time.Hour

func azureRecordMetric(resourceID, metric, unit string, value float64, dimensions map[string]string, at time.Time) {
	if azureMetricPoints == nil {
		return
	}
	id := sim.NewUUID()
	azureMetricPoints.Put(id, AzureMetricPoint{
		ID: id, ResourceID: strings.ToLower(resourceID), Metric: metric, Unit: unit, Value: value, Dimensions: dimensions, At: at.UTC(),
	})
}

// azureMetricDefinition is one metric a resource type reports.
type azureMetricDefinition struct {
	Name        string
	Display     string
	Description string
	Unit        string
	Primary     string
	Dimensions  []string
}

// azureNATGatewayMetrics are the platform metrics of Microsoft.Network/natGateways.
var azureNATGatewayMetrics = []azureMetricDefinition{
	{Name: "ByteCount", Display: "Bytes", Description: "Total bytes transmitted within time period", Unit: "Bytes", Primary: "Total", Dimensions: []string{"Protocol", "Direction"}},
	{Name: "PacketCount", Display: "Packets", Description: "Total packets transmitted within time period", Unit: "Count", Primary: "Total", Dimensions: []string{"Protocol", "Direction"}},
	{Name: "DroppedPackets", Display: "Dropped Packets", Description: "Count of dropped packets", Unit: "Count", Primary: "Total"},
	{Name: "SNATConnectionCount", Display: "SNAT Connection Count", Description: "Total concurrent active connections", Unit: "Count", Primary: "Total", Dimensions: []string{"Protocol", "ConnectionState"}},
	{Name: "TotalConnectionCount", Display: "Total SNAT Connection Count", Description: "Total number of active SNAT connections", Unit: "Count", Primary: "Total", Dimensions: []string{"Protocol"}},
	{Name: "DatapathAvailability", Display: "Datapath Availability", Description: "NAT Gateway Datapath Availability", Unit: "Count", Primary: "Average"},
}

var azureMetricAggregations = []string{"Total", "Average", "Count", "Minimum", "Maximum"}

// azureMetricNamespace is a resource type whose platform metrics the
// simulator records, with the resources of that type a subscription holds
// in a region.
type azureMetricNamespace struct {
	Namespace string
	Metrics   []azureMetricDefinition
	Resources func(subscription, region string) []string
}

var azureMetricNamespaces = []azureMetricNamespace{
	{Namespace: "Microsoft.Network/natGateways", Metrics: azureNATGatewayMetrics, Resources: azureNATGatewaysIn},
}

func azureMetricNamespaceNamed(name string) (azureMetricNamespace, bool) {
	for _, ns := range azureMetricNamespaces {
		if strings.EqualFold(ns.Namespace, name) {
			return ns, true
		}
	}
	return azureMetricNamespace{}, false
}

// azureSameRegion compares a location as a resource stores it with one a
// query names: "East US" and "eastus" are one region.
func azureSameRegion(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, " ", "")) }
	return norm(a) == norm(b)
}

func azureNATGatewaysIn(subscription, region string) []string {
	prefix := strings.ToLower(armSubscriptionID(subscription) + "/")
	var ids []string
	for _, gw := range azureNatGateways.Filter(func(gw NatGateway) bool {
		return strings.HasPrefix(strings.ToLower(gw.ID), prefix) && azureSameRegion(gw.Location, region)
	}) {
		ids = append(ids, gw.ID)
	}
	sort.Strings(ids)
	return ids
}

func registerMonitorMetrics(srv *sim.Server) {
	azureMetricPoints = sim.MakeStore[AzureMetricPoint](srv.DB(), "monitor_metric_points")
	nat, _ := azureMetricNamespaceNamed("Microsoft.Network/natGateways")
	natBase := azureNetworkArmBase() + "/natGateways/{name}/providers"
	srv.HandleFunc("GET "+natBase+"/Microsoft.Insights/metricDefinitions", func(w http.ResponseWriter, r *http.Request) {
		resourceID, ok := azureMetricResource(w, r, "natGateways")
		if !ok {
			return
		}
		azureWriteMetricDefinitions(w, resourceID, nat)
	})
	srv.HandleFunc("GET "+natBase+"/Microsoft.Insights/metrics", func(w http.ResponseWriter, r *http.Request) {
		resourceID, ok := azureMetricResource(w, r, "natGateways")
		if !ok {
			return
		}
		gw, _ := azureNatGateways.Get(resourceID)
		query, ok := azureMetricsQueryOf(w, r.URL.Query())
		if !ok {
			return
		}
		azureWriteMetrics(w, query, resourceID, gw.Location, nat, []string{resourceID}, false)
	})
	srv.HandleFunc("GET "+natBase+"/microsoft.insights/metricNamespaces", func(w http.ResponseWriter, r *http.Request) {
		resourceID, ok := azureMetricResource(w, r, "natGateways")
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{{
			"id":             resourceID + "/providers/microsoft.insights/metricNamespaces/" + nat.Namespace,
			"name":           nat.Namespace,
			"type":           "Microsoft.Insights/metricNamespaces",
			"classification": "Platform",
			"properties":     map[string]any{"metricNamespaceName": nat.Namespace},
		}}})
	})

	subBase := "/subscriptions/{subscriptionId}/providers/Microsoft.Insights"
	srv.HandleFunc("GET "+subBase+"/metricDefinitions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("region") == "" {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The query parameter 'region' is required.")
			return
		}
		scope := armSubscriptionID(sim.PathParam(r, "subscriptionId"))
		namespaces := azureMetricNamespaces
		if name := q.Get("metricnamespace"); name != "" {
			ns, ok := azureMetricNamespaceNamed(name)
			if !ok {
				sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
				return
			}
			namespaces = []azureMetricNamespace{ns}
		}
		var value []map[string]any
		for _, ns := range namespaces {
			value = append(value, azureMetricDefinitionValues(scope, ns)...)
		}
		if value == nil {
			value = []map[string]any{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
	})
	subscriptionMetrics := func(w http.ResponseWriter, r *http.Request, q url.Values) {
		region := q.Get("region")
		if region == "" {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The query parameter 'region' is required.")
			return
		}
		name := q.Get("metricnamespace")
		if name == "" {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "A metric namespace is required for a subscription-scoped metrics query.")
			return
		}
		ns, ok := azureMetricNamespaceNamed(name)
		if !ok {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The metric namespace %q is not supported for a subscription-scoped metrics query.", name)
			return
		}
		query, ok := azureMetricsQueryOf(w, q)
		if !ok {
			return
		}
		subscription := sim.PathParam(r, "subscriptionId")
		azureWriteMetrics(w, query, armSubscriptionID(subscription), region, ns, ns.Resources(subscription, region), true)
	}
	srv.HandleFunc("GET "+subBase+"/metrics", func(w http.ResponseWriter, r *http.Request) {
		subscriptionMetrics(w, r, r.URL.Query())
	})
	srv.HandleFunc("POST "+subBase+"/metrics", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The request body is not valid JSON: %v", err)
			return
		}
		for field, key := range azureMetricsBodyParameters {
			v, present := body[field]
			if !present || v == nil {
				continue
			}
			if q.Has(key) {
				AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The parameter %q is specified in both the body and the query.", field)
				return
			}
			q.Set(key, fmt.Sprint(v))
		}
		subscriptionMetrics(w, r, q)
	})
}

// azureMetricsBodyParameters maps SubscriptionScopeMetricsRequestBodyParameters
// fields to the query parameters they stand for.
var azureMetricsBodyParameters = map[string]string{
	"timespan":            "timespan",
	"interval":            "interval",
	"metricNames":         "metricnames",
	"aggregation":         "aggregation",
	"filter":              "$filter",
	"top":                 "top",
	"orderBy":             "orderby",
	"rollUpBy":            "rollupby",
	"resultType":          "resultType",
	"metricNamespace":     "metricnamespace",
	"autoAdjustTimegrain": "AutoAdjustTimegrain",
	"validateDimensions":  "ValidateDimensions",
}

// azureMetricResource is the resource the metrics read names, which has to
// exist as Azure Monitor answers ResourceNotFound otherwise.
func azureMetricResource(w http.ResponseWriter, r *http.Request, collection string) (string, bool) {
	resourceID := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", collection, sim.PathParam(r, "name"))
	if _, ok := azureNatGateways.Get(resourceID); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource '%s' was not found.", resourceID)
		return "", false
	}
	return resourceID, true
}

func azureMetricDefinitionValues(scope string, ns azureMetricNamespace) []map[string]any {
	value := make([]map[string]any, 0, len(ns.Metrics))
	for _, m := range ns.Metrics {
		dimensions := make([]map[string]any, 0, len(m.Dimensions))
		for _, d := range m.Dimensions {
			dimensions = append(dimensions, map[string]any{"value": d, "localizedValue": d})
		}
		value = append(value, map[string]any{
			"id":                        scope + "/providers/microsoft.insights/metricdefinitions/" + m.Name,
			"resourceId":                scope,
			"namespace":                 ns.Namespace,
			"name":                      map[string]any{"value": m.Name, "localizedValue": m.Display},
			"displayDescription":        m.Description,
			"isDimensionRequired":       false,
			"unit":                      m.Unit,
			"primaryAggregationType":    m.Primary,
			"supportedAggregationTypes": azureMetricAggregations,
			"metricAvailabilities": []map[string]any{
				{"timeGrain": "PT1M", "retention": "P93D"},
				{"timeGrain": "PT5M", "retention": "P93D"},
				{"timeGrain": "PT15M", "retention": "P93D"},
				{"timeGrain": "PT30M", "retention": "P93D"},
				{"timeGrain": "PT1H", "retention": "P93D"},
				{"timeGrain": "PT6H", "retention": "P93D"},
				{"timeGrain": "PT12H", "retention": "P93D"},
				{"timeGrain": "P1D", "retention": "P93D"},
			},
			"dimensions": dimensions,
		})
	}
	return value
}

func azureWriteMetricDefinitions(w http.ResponseWriter, resourceID string, ns azureMetricNamespace) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": azureMetricDefinitionValues(resourceID, ns)})
}

var azureIntervalPattern = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?)?$`)

// azureParseInterval reads an ISO 8601 duration of days, hours and minutes.
func azureParseInterval(s string) (time.Duration, bool) {
	m := azureIntervalPattern.FindStringSubmatch(strings.ToUpper(s))
	if m == nil {
		return 0, false
	}
	var d time.Duration
	for i, unit := range []time.Duration{24 * time.Hour, time.Hour, time.Minute} {
		if m[i+1] != "" {
			n, err := strconv.Atoi(m[i+1])
			if err != nil {
				return 0, false
			}
			d += time.Duration(n) * unit
		}
	}
	return d, d > 0
}

// azureMetricsQuery is a metrics read's window, grain, metric names and
// aggregations.
type azureMetricsQuery struct {
	start, end    time.Time
	interval      time.Duration
	intervalLabel string
	wanted        map[string]bool
	aggregations  []string
}

// azureMetricsQueryOf reads a metrics query: the timespan asked for, or the
// last hour, in buckets of the interval asked for, or a minute, with the
// aggregations asked for, or each metric's primary one. A dimension filter
// is not applied: each series sums its dimensions' values.
func azureMetricsQueryOf(w http.ResponseWriter, q url.Values) (azureMetricsQuery, bool) {
	query := azureMetricsQuery{end: time.Now().UTC(), interval: time.Minute, intervalLabel: "PT1M", wanted: map[string]bool{}}
	query.start = query.end.Add(-time.Hour)
	if span := q.Get("timespan"); span != "" {
		from, to, ok := strings.Cut(span, "/")
		if !ok {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The timespan %q is not two ISO 8601 times separated by a slash.", span)
			return query, false
		}
		var err error
		if query.start, err = time.Parse(time.RFC3339Nano, from); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The timespan start %q is not an ISO 8601 time.", from)
			return query, false
		}
		if query.end, err = time.Parse(time.RFC3339Nano, to); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The timespan end %q is not an ISO 8601 time.", to)
			return query, false
		}
		query.start, query.end = query.start.UTC(), query.end.UTC()
	}
	if s := q.Get("interval"); s != "" {
		if strings.EqualFold(s, "FULL") {
			query.interval, query.intervalLabel = query.end.Sub(query.start), "FULL"
		} else {
			d, ok := azureParseInterval(s)
			if !ok {
				AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The interval %q is not an ISO 8601 duration.", s)
				return query, false
			}
			query.interval, query.intervalLabel = d, strings.ToUpper(s)
		}
	}
	if query.interval <= 0 {
		query.interval = time.Minute
	}
	for _, name := range strings.Split(q.Get("metricnames"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			query.wanted[strings.ToLower(name)] = true
		}
	}
	for _, a := range strings.Split(q.Get("aggregation"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			query.aggregations = append(query.aggregations, a)
		}
	}
	return query, true
}

// azureWriteMetrics answers a metrics read over resources: one series per
// metric, or with splitByResource one per resource of the metric, carrying
// the resource's ID as its Microsoft.ResourceId metadata value, as a
// subscription-scoped read does.
func azureWriteMetrics(w http.ResponseWriter, query azureMetricsQuery, scope, region string, ns azureMetricNamespace, resources []string, splitByResource bool) {
	// A read is when a point past the retention goes.
	cutoff := time.Now().Add(-azureMetricRetention)
	for _, p := range azureMetricPoints.Filter(func(p AzureMetricPoint) bool { return p.At.Before(cutoff) }) {
		azureMetricPoints.Delete(p.ID)
	}
	inScope := map[string]bool{}
	for _, id := range resources {
		inScope[strings.ToLower(id)] = true
	}
	points := azureMetricPoints.Filter(func(p AzureMetricPoint) bool {
		return inScope[p.ResourceID] && !p.At.Before(query.start) && p.At.Before(query.end)
	})
	value := make([]map[string]any, 0, len(ns.Metrics))
	for _, m := range ns.Metrics {
		if len(query.wanted) > 0 && !query.wanted[strings.ToLower(m.Name)] {
			continue
		}
		aggregations := query.aggregations
		if len(aggregations) == 0 {
			aggregations = []string{m.Primary}
		}
		series := func(resource string) map[string]any {
			buckets := map[int64][]float64{}
			for _, p := range points {
				if p.Metric != m.Name || (resource != "" && p.ResourceID != strings.ToLower(resource)) {
					continue
				}
				slot := int64(p.At.Sub(query.start) / query.interval)
				buckets[slot] = append(buckets[slot], p.Value)
			}
			data := []map[string]any{}
			for at := query.start; at.Before(query.end); at = at.Add(query.interval) {
				point := map[string]any{"timeStamp": at.Format(time.RFC3339)}
				if values, ok := buckets[int64(at.Sub(query.start)/query.interval)]; ok {
					for _, a := range aggregations {
						point[strings.ToLower(a)] = azureAggregate(a, values)
					}
				}
				data = append(data, point)
			}
			metadata := []any{}
			if resource != "" {
				metadata = append(metadata, map[string]any{
					"name":  map[string]any{"value": "Microsoft.ResourceId", "localizedValue": "Microsoft.ResourceId"},
					"value": resource,
				})
			}
			return map[string]any{"metadatavalues": metadata, "data": data}
		}
		timeseries := []map[string]any{}
		if splitByResource {
			for _, id := range resources {
				timeseries = append(timeseries, series(id))
			}
		} else {
			timeseries = append(timeseries, series(""))
		}
		value = append(value, map[string]any{
			"id":                 scope + "/providers/Microsoft.Insights/metrics/" + m.Name,
			"type":               "Microsoft.Insights/metrics",
			"name":               map[string]any{"value": m.Name, "localizedValue": m.Display},
			"displayDescription": m.Description,
			"unit":               m.Unit,
			"timeseries":         timeseries,
			"errorCode":          "Success",
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"cost":           0,
		"timespan":       query.start.Format(time.RFC3339) + "/" + query.end.Format(time.RFC3339),
		"interval":       query.intervalLabel,
		"value":          value,
		"namespace":      ns.Namespace,
		"resourceregion": region,
	})
}

func azureAggregate(aggregation string, values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	total := 0.0
	for _, v := range values {
		total += v
	}
	switch strings.ToLower(aggregation) {
	case "average":
		return total / float64(len(values))
	case "count":
		return float64(len(values))
	case "minimum":
		return sorted[0]
	case "maximum":
		return sorted[len(sorted)-1]
	}
	return total
}
