package main

import (
	"encoding/json"
	"strconv"
)

// CloudWatch Embedded Metric Format (EMF) extraction.
//
// Real CloudWatch automatically extracts metrics from EMF-formatted log events
// written to ANY log group: a log event whose JSON message carries an
// `_aws.CloudWatchMetrics` block declares one or more metrics whose values +
// dimension values live as sibling fields in the same root object. The
// extracted metrics become queryable through the ordinary metric APIs
// (ListMetrics / GetMetricStatistics / GetMetricData) and alarmable — with no
// PutMetricData call. This is the standard ECS/Fargate EMF-over-stdout →
// awslogs → CloudWatch Logs path.
//
// Spec: https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html

// emfMetricDirective is one entry of the EMF `_aws.CloudWatchMetrics` array.
type emfMetricDirective struct {
	Namespace  string     `json:"Namespace"`
	Dimensions [][]string `json:"Dimensions"`
	Metrics    []struct {
		Name string `json:"Name"`
		Unit string `json:"Unit"`
	} `json:"Metrics"`
}

// extractEMFMetrics parses a CloudWatch EMF log message and returns the metrics
// it declares, resolving each metric's value and each dimension's value from the
// same root JSON object. A non-EMF message (not JSON, or no
// `_aws.CloudWatchMetrics` block) yields nothing. fallbackTSMillis (the log
// event timestamp) is used when the EMF metadata omits its own `Timestamp`.
func extractEMFMetrics(message string, fallbackTSMillis int64) []CWMetricDatum {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &root); err != nil {
		return nil
	}
	awsRaw, ok := root["_aws"]
	if !ok {
		return nil
	}
	var awsBlock struct {
		Timestamp         int64                `json:"Timestamp"`
		CloudWatchMetrics []emfMetricDirective `json:"CloudWatchMetrics"`
	}
	if err := json.Unmarshal(awsRaw, &awsBlock); err != nil || len(awsBlock.CloudWatchMetrics) == 0 {
		return nil
	}
	tsMillis := awsBlock.Timestamp
	if tsMillis == 0 {
		tsMillis = fallbackTSMillis
	}
	tsSec := float64(tsMillis) / 1000.0

	// rootString resolves a root field to a string dimension value (EMF allows
	// numeric dimension values, which CloudWatch stringifies).
	rootString := func(key string) (string, bool) {
		raw, ok := root[key]
		if !ok {
			return "", false
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s, true
		}
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			return strconv.FormatFloat(f, 'f', -1, 64), true
		}
		return "", false
	}
	// rootValues resolves a root field to one or more numeric values — EMF
	// permits a scalar or an array of samples for a single metric name.
	rootValues := func(key string) []float64 {
		raw, ok := root[key]
		if !ok {
			return nil
		}
		var single float64
		if json.Unmarshal(raw, &single) == nil {
			return []float64{single}
		}
		var arr []float64
		if json.Unmarshal(raw, &arr) == nil {
			return arr
		}
		return nil
	}

	var out []CWMetricDatum
	for _, dir := range awsBlock.CloudWatchMetrics {
		if dir.Namespace == "" {
			continue
		}
		dimSets := dir.Dimensions
		if len(dimSets) == 0 {
			// No dimension sets declared → a single no-dimension series.
			dimSets = [][]string{{}}
		}
		for _, m := range dir.Metrics {
			if m.Name == "" {
				continue
			}
			values := rootValues(m.Name)
			if len(values) == 0 {
				continue
			}
			for _, dimKeys := range dimSets {
				dims := make([]CWDimension, 0, len(dimKeys))
				complete := true
				for _, dk := range dimKeys {
					dv, ok := rootString(dk)
					if !ok {
						// A dimension key with no sibling value can't form a
						// valid series — skip this dimension set, as CloudWatch does.
						complete = false
						break
					}
					dims = append(dims, CWDimension{Name: dk, Value: dv})
				}
				if !complete {
					continue
				}
				for _, v := range values {
					out = append(out, CWMetricDatum{
						Namespace:  dir.Namespace,
						MetricName: m.Name,
						Dimensions: dims,
						Value:      v,
						Timestamp:  tsSec,
						Unit:       m.Unit,
					})
				}
			}
		}
	}
	return out
}
