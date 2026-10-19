package main

import (
	"fmt"
	"net/http"
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

func registerMonitorMetrics(srv *sim.Server) {
	azureMetricPoints = sim.MakeStore[AzureMetricPoint](srv.DB(), "monitor_metric_points")
	natBase := azureNetworkArmBase() + "/natGateways/{name}/providers/Microsoft.Insights"
	srv.HandleFunc("GET "+natBase+"/metricDefinitions", func(w http.ResponseWriter, r *http.Request) {
		resourceID, ok := azureMetricResource(w, r, "natGateways")
		if !ok {
			return
		}
		azureWriteMetricDefinitions(w, resourceID, "Microsoft.Network/natGateways", azureNATGatewayMetrics)
	})
	srv.HandleFunc("GET "+natBase+"/metrics", func(w http.ResponseWriter, r *http.Request) {
		resourceID, ok := azureMetricResource(w, r, "natGateways")
		if !ok {
			return
		}
		gw, _ := azureNatGateways.Get(resourceID)
		azureListMetrics(w, r, resourceID, gw.Location, "Microsoft.Network/natGateways", azureNATGatewayMetrics)
	})
}

// azureMetricResource is the resource the metrics read names, which has to
// exist as Azure Monitor answers ResourceNotFound otherwise.
func azureMetricResource(w http.ResponseWriter, r *http.Request, collection string) (string, bool) {
	resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/%s/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), collection, sim.PathParam(r, "name"))
	if _, ok := azureNatGateways.Get(resourceID); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource '%s' was not found.", resourceID)
		return "", false
	}
	return resourceID, true
}

func azureWriteMetricDefinitions(w http.ResponseWriter, resourceID, namespace string, metrics []azureMetricDefinition) {
	value := make([]map[string]any, 0, len(metrics))
	for _, m := range metrics {
		dimensions := make([]map[string]any, 0, len(m.Dimensions))
		for _, d := range m.Dimensions {
			dimensions = append(dimensions, map[string]any{"value": d, "localizedValue": d})
		}
		value = append(value, map[string]any{
			"id":                        resourceID + "/providers/microsoft.insights/metricdefinitions/" + m.Name,
			"resourceId":                resourceID,
			"namespace":                 namespace,
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
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
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

// azureListMetrics answers a metrics read: the named metrics, or every one,
// over the timespan asked for, or the last hour, in buckets of the interval
// asked for, or a minute, with the aggregations asked for, or the metric's
// primary one. A dimension filter is not applied: each series sums its
// dimensions' values.
func azureListMetrics(w http.ResponseWriter, r *http.Request, resourceID, region, namespace string, metrics []azureMetricDefinition) {
	q := r.URL.Query()
	end := time.Now().UTC()
	start := end.Add(-time.Hour)
	if span := q.Get("timespan"); span != "" {
		from, to, ok := strings.Cut(span, "/")
		if !ok {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The timespan %q is not two ISO 8601 times separated by a slash.", span)
			return
		}
		var err error
		if start, err = time.Parse(time.RFC3339Nano, from); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The timespan start %q is not an ISO 8601 time.", from)
			return
		}
		if end, err = time.Parse(time.RFC3339Nano, to); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The timespan end %q is not an ISO 8601 time.", to)
			return
		}
		start, end = start.UTC(), end.UTC()
	}
	interval := time.Minute
	intervalLabel := "PT1M"
	if s := q.Get("interval"); s != "" {
		if strings.EqualFold(s, "FULL") {
			interval = end.Sub(start)
			intervalLabel = "FULL"
		} else {
			d, ok := azureParseInterval(s)
			if !ok {
				AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The interval %q is not an ISO 8601 duration.", s)
				return
			}
			interval, intervalLabel = d, strings.ToUpper(s)
		}
	}
	if interval <= 0 {
		interval = time.Minute
	}
	wanted := map[string]bool{}
	for _, name := range strings.Split(q.Get("metricnames"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			wanted[strings.ToLower(name)] = true
		}
	}
	var aggregations []string
	for _, a := range strings.Split(q.Get("aggregation"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			aggregations = append(aggregations, a)
		}
	}
	// A read is when a point past the retention goes.
	cutoff := time.Now().Add(-azureMetricRetention)
	for _, p := range azureMetricPoints.Filter(func(p AzureMetricPoint) bool { return p.At.Before(cutoff) }) {
		azureMetricPoints.Delete(p.ID)
	}
	points := azureMetricPoints.Filter(func(p AzureMetricPoint) bool {
		return p.ResourceID == strings.ToLower(resourceID) && !p.At.Before(start) && p.At.Before(end)
	})
	value := make([]map[string]any, 0, len(metrics))
	for _, m := range metrics {
		if len(wanted) > 0 && !wanted[strings.ToLower(m.Name)] {
			continue
		}
		metricAggregations := aggregations
		if len(metricAggregations) == 0 {
			metricAggregations = []string{m.Primary}
		}
		buckets := map[int64][]float64{}
		for _, p := range points {
			if p.Metric != m.Name {
				continue
			}
			slot := p.At.Sub(start) / interval
			buckets[int64(slot)] = append(buckets[int64(slot)], p.Value)
		}
		var data []map[string]any
		for at := start; at.Before(end); at = at.Add(interval) {
			point := map[string]any{"timeStamp": at.Format(time.RFC3339)}
			if values, ok := buckets[int64(at.Sub(start)/interval)]; ok {
				for _, a := range metricAggregations {
					point[strings.ToLower(a)] = azureAggregate(a, values)
				}
			}
			data = append(data, point)
		}
		if data == nil {
			data = []map[string]any{}
		}
		value = append(value, map[string]any{
			"id":                 resourceID + "/providers/Microsoft.Insights/metrics/" + m.Name,
			"type":               "Microsoft.Insights/metrics",
			"name":               map[string]any{"value": m.Name, "localizedValue": m.Display},
			"displayDescription": m.Description,
			"unit":               m.Unit,
			"timeseries":         []map[string]any{{"metadatavalues": []any{}, "data": data}},
			"errorCode":          "Success",
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"cost":           0,
		"timespan":       start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339),
		"interval":       intervalLabel,
		"value":          value,
		"namespace":      namespace,
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
