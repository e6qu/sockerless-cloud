package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// cwJSONStat renders a statistic value as a JSON number that always carries a
// decimal point. The awsJson deserializer (unlike the query XML one) trusts the
// literal JSON number type, so a bare 42 reads back as an int — real CloudWatch
// returns the Double-typed statistics as 42.0.
func cwJSONStat(f float64) json.Number {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return json.Number(s)
}

// CloudWatch metrics — awsJson1.0 surface.
//
// Current botocore / aws CLI send CloudWatch over awsJson1.0 (POST /,
// X-Amz-Target: GraniteServiceVersion20100801.<Op>, Content-Type
// application/x-amz-json-1.0, x-amzn-query-mode: true) — the same awsQuery→
// awsJson migration SQS went through. Older CLIs still use the legacy query
// protocol (cloudwatch_metrics_query.go) and the Go SDK uses rpc-v2-cbor
// (cloudwatch_metrics.go). All three serve the SAME `cwMetrics` store +
// period-bucketing + statistic helpers, so data pushed via any protocol reads
// back through any other.

func registerCloudWatchMetricsJSON(r *AWSRouter) {
	r.Register("GraniteServiceVersion20100801.PutMetricData", handleCWJSONPutMetricData)
	r.Register("GraniteServiceVersion20100801.GetMetricStatistics", handleCWJSONGetMetricStatistics)
	r.Register("GraniteServiceVersion20100801.ListMetrics", handleCWJSONListMetrics)
}

func cwStoreDatum(datum CWMetricDatum) {
	key := metricsKey(datum.Namespace, datum.MetricName, datum.Dimensions)
	cwMetrics.Upsert(key, func(existing *[]CWMetricDatum) { *existing = append(*existing, datum) })
	cwDeliverMetricDatum(datum)
}

func handleCWJSONPutMetricData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string `json:"Namespace"`
		MetricData []struct {
			MetricName string        `json:"MetricName"`
			Value      float64       `json:"Value"`
			Unit       string        `json:"Unit"`
			Timestamp  float64       `json:"Timestamp"`
			Dimensions []CWDimension `json:"Dimensions"`
		} `json:"MetricData"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Namespace == "" {
		AWSError(w, "MissingParameter", "The parameter Namespace is required.", http.StatusBadRequest)
		return
	}
	for _, m := range req.MetricData {
		if m.MetricName == "" {
			continue
		}
		ts := time.Now().UTC()
		if m.Timestamp != 0 {
			ts = time.Unix(int64(m.Timestamp), 0).UTC()
		}
		cwStoreDatum(CWMetricDatum{
			Namespace:  req.Namespace,
			MetricName: m.MetricName,
			Dimensions: m.Dimensions,
			Value:      m.Value,
			Timestamp:  float64(ts.Unix()),
			Unit:       m.Unit,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONGetMetricStatistics(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string        `json:"Namespace"`
		MetricName string        `json:"MetricName"`
		Dimensions []CWDimension `json:"Dimensions"`
		StartTime  float64       `json:"StartTime"`
		EndTime    float64       `json:"EndTime"`
		Period     int64         `json:"Period"`
		Statistics []string      `json:"Statistics"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	period := req.Period
	if period <= 0 {
		period = 60
	}
	stats := req.Statistics
	if len(stats) == 0 {
		stats = []string{"Average"}
	}

	data, _ := cwMetrics.Get(metricsKey(req.Namespace, req.MetricName, req.Dimensions))
	unit := "None"
	buckets := map[int64][]float64{}
	for _, d := range data {
		if req.StartTime != 0 && d.Timestamp < req.StartTime {
			continue
		}
		if req.EndTime != 0 && d.Timestamp >= req.EndTime {
			continue
		}
		if d.Unit != "" {
			unit = d.Unit
		}
		b := (int64(d.Timestamp) / period) * period
		buckets[b] = append(buckets[b], d.Value)
	}
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	datapoints := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		dp := map[string]any{
			"Timestamp": float64(k),
			"Unit":      unit,
		}
		for _, s := range stats {
			dp[s] = cwJSONStat(cwApplyStat(s, buckets[k]))
		}
		datapoints = append(datapoints, dp)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Label":      req.MetricName,
		"Datapoints": datapoints,
	})
}

func handleCWJSONListMetrics(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string `json:"Namespace"`
		MetricName string `json:"MetricName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}

	type metricKey struct{ namespace, name, dims string }
	seen := map[metricKey]CWMetricDatum{}
	for _, series := range cwMetrics.List() {
		for _, d := range series {
			if req.Namespace != "" && d.Namespace != req.Namespace {
				continue
			}
			if req.MetricName != "" && d.MetricName != req.MetricName {
				continue
			}
			seen[metricKey{d.Namespace, d.MetricName, fmt.Sprintf("%v", d.Dimensions)}] = d
		}
	}
	metrics := make([]CWMetricDatum, 0, len(seen))
	for _, d := range seen {
		metrics = append(metrics, d)
	}
	sort.Slice(metrics, func(i, j int) bool {
		if metrics[i].Namespace != metrics[j].Namespace {
			return metrics[i].Namespace < metrics[j].Namespace
		}
		return metrics[i].MetricName < metrics[j].MetricName
	})

	out := make([]map[string]any, 0, len(metrics))
	for _, d := range metrics {
		dims := make([]map[string]string, 0, len(d.Dimensions))
		for _, dim := range d.Dimensions {
			dims = append(dims, map[string]string{"Name": dim.Name, "Value": dim.Value})
		}
		out = append(out, map[string]any{
			"Namespace":  d.Namespace,
			"MetricName": d.MetricName,
			"Dimensions": dims,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Metrics": out})
}
