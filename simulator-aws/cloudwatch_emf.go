package main

import (
	"encoding/json"
	"strconv"
)

// CloudWatch extracts metrics from Embedded Metric Format log events written
// to any log group, with no PutMetricData call: the `_aws.CloudWatchMetrics`
// block declares metrics whose values and dimension values are sibling fields.
//
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html

type emfMetricDirective struct {
	Namespace  string     `json:"Namespace"`
	Dimensions [][]string `json:"Dimensions"`
	Metrics    []struct {
		Name string `json:"Name"`
		Unit string `json:"Unit"`
	} `json:"Metrics"`
}

// A non-EMF message yields nothing. fallbackTSMillis applies when the metadata
// omits Timestamp.
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

	// CloudWatch stringifies numeric dimension values.
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
	// A metric value may be a scalar or an array of samples.
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
			// No declared dimension sets means one series without dimensions.
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
						// CloudWatch skips a dimension set with a key that has no value.
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
