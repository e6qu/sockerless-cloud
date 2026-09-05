package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/fxamacker/cbor/v2"
	"github.com/rs/zerolog"
)

// cwEncMode emits time.Time values as CBOR tag-1 (epoch), which is what the
// smithy rpc-v2-cbor protocol expects for timestamps (a bare integer is
// rejected by the SDK).
var cwEncMode, _ = cbor.EncOptions{Time: cbor.TimeUnixDynamic, TimeTag: cbor.EncTagRequired}.EncMode()

// cwReadBody reads the request body, transparently decompressing it when the
// SDK sends gzip — aws-sdk-go-v2 request-compresses PutMetricData (and may
// compress GetMetricData), so the CBOR lives behind a gzip wrapper.
func cwReadBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return cwDecompress(body)
}

// cwDecompress unwraps a request body the SDK gzip-compressed, leaving an
// uncompressed body untouched.
func cwDecompress(body []byte) ([]byte, error) {
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		return io.ReadAll(gz)
	}
	return body, nil
}

// CloudWatch Metrics types

type CWMetricDatum struct {
	Namespace  string        `json:"namespace" cbor:"Namespace"`
	MetricName string        `json:"metricName" cbor:"MetricName"`
	Dimensions []CWDimension `json:"dimensions,omitempty" cbor:"Dimensions,omitempty"`
	Value      float64       `json:"value" cbor:"Value"`
	Timestamp  float64       `json:"timestamp" cbor:"Timestamp"`
	Unit       string        `json:"unit,omitempty" cbor:"Unit,omitempty"`
}

type CWDimension struct {
	Name  string `json:"Name" cbor:"Name"`
	Value string `json:"Value" cbor:"Value"`
}

// State store for metrics
var (
	cwMetrics    sim.Store[[]CWMetricDatum]
	cwEvalLogger zerolog.Logger
)

func registerCloudWatchMetrics(srv *sim.Server, startBackgroundEvaluator bool) {
	cwEvalLogger = srv.Logger()
	cwMetrics = sim.MakeStore[[]CWMetricDatum](srv.DB(), "cw_metrics")
	cwAlarms = sim.MakeStore[CWAlarm](srv.DB(), "cw_alarms")
	cwCompositeAlarms = sim.MakeStore[CWCompositeAlarm](srv.DB(), "cw_composite_alarms")
	cwAlarmHistory = sim.MakeStore[[]CWAlarmHistoryItem](srv.DB(), "cw_alarm_history")
	cwMetricStreams = sim.MakeStore[CWMetricStream](srv.DB(), "cw_metric_streams")
	cwAnomalyDetectors = sim.MakeStore[CWAnomalyDetector](srv.DB(), "cw_anomaly_detectors")
	cwInsightRules = sim.MakeStore[CWInsightRule](srv.DB(), "cw_insight_rules")
	cwAlarmMuteRules = sim.MakeStore[CWAlarmMuteRule](srv.DB(), "cw_alarm_mute_rules")
	cwDashboards = sim.MakeStore[CWDashboard](srv.DB(), "cw_dashboards")
	cwLogAlarms = sim.MakeStore[CWLogAlarm](srv.DB(), "cw_log_alarms")

	// Smithy RPCv2 CBOR uses URL path routing
	cwCBOR(srv, "GetMetricData", handleCWGetMetricData)
	cwCBOR(srv, "PutMetricData", handleCWPutMetricData)
	registerCloudWatchAlarmsCBOR(srv)
	registerCloudWatchLogAlarmsCBOR(srv)
	registerCloudWatchAlarmOpsCBOR(srv)
	registerCloudWatchMetricStreamsCBOR(srv)
	registerCloudWatchAnomalyInsightCBOR(srv)
	registerCloudWatchMiscCBOR(srv)
	registerCloudWatchDashboardsCBOR(srv)

	if startBackgroundEvaluator {
		startCWAlarmEvaluator(srv)
	}
}

// GetMetricData request/response types (CBOR)
type getMetricDataRequest struct {
	StartTime         time.Time         `cbor:"StartTime"`
	EndTime           time.Time         `cbor:"EndTime"`
	MetricDataQueries []metricDataQuery `cbor:"MetricDataQueries"`
}

type metricDataQuery struct {
	Id         string      `cbor:"Id"`
	MetricStat *metricStat `cbor:"MetricStat,omitempty"`
}

type metricStat struct {
	Metric *metricRef `cbor:"Metric"`
	Period int32      `cbor:"Period"`
	Stat   string     `cbor:"Stat"`
}

type metricRef struct {
	Namespace  string        `cbor:"Namespace"`
	MetricName string        `cbor:"MetricName"`
	Dimensions []CWDimension `cbor:"Dimensions,omitempty"`
}

type getMetricDataResponse struct {
	MetricDataResults []metricDataResult `cbor:"MetricDataResults"`
}

type metricDataResult struct {
	Id         string      `cbor:"Id"`
	StatusCode string      `cbor:"StatusCode"`
	Values     []float64   `cbor:"Values"`
	Timestamps []time.Time `cbor:"Timestamps"`
}

func handleCWGetMetricData(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req getMetricDataRequest
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}

	var results []metricDataResult

	for _, q := range req.MetricDataQueries {
		result := metricDataResult{
			Id:         q.Id,
			StatusCode: "Complete",
		}
		if q.MetricStat != nil && q.MetricStat.Metric != nil {
			m := q.MetricStat.Metric
			// Serve the actual datapoints recorded via PutMetricData, bucketed by
			// Period and reduced by the requested statistic — the real CloudWatch
			// behaviour, for every namespace (ECS/ContainerInsights included). If
			// nothing was pushed there are no datapoints: the API-only sim does
			// not measure live container utilization, so it reports none rather
			// than fabricating a value.
			if data, ok := cwMetrics.Get(metricsKey(m.Namespace, m.MetricName, m.Dimensions)); ok {
				result.Values, result.Timestamps = cwAggregate(data,
					float64(req.StartTime.Unix()), float64(req.EndTime.Unix()), q.MetricStat.Period, q.MetricStat.Stat)
			}
		}
		results = append(results, result)
	}

	resp := getMetricDataResponse{MetricDataResults: results}
	data, err := cwEncMode.Marshal(resp)
	if err != nil {
		cwWriteCBORError(w, "InternalFailure", "Failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/cbor")
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// cwAggregate buckets datapoints into Period-second windows within
// [startSec, endSec) and reduces each bucket with the requested statistic,
// returning one value per bucket. Buckets are ordered newest-first to match
// CloudWatch's default ScanBy=TimestampDescending.
func cwAggregate(data []CWMetricDatum, startSec, endSec float64, period int32, stat string) (values []float64, timestamps []time.Time) {
	if period <= 0 {
		period = 60
	}
	p := int64(period)
	buckets := map[int64][]float64{}
	for _, d := range data {
		if d.Timestamp < startSec || d.Timestamp >= endSec {
			continue
		}
		b := (int64(d.Timestamp) / p) * p
		buckets[b] = append(buckets[b], d.Value)
	}
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] > keys[j] })
	for _, k := range keys {
		values = append(values, cwApplyStat(stat, buckets[k]))
		timestamps = append(timestamps, time.Unix(k, 0).UTC())
	}
	return values, timestamps
}

// cwApplyStat reduces a bucket of datapoints by a CloudWatch statistic.
func cwApplyStat(stat string, vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	switch stat {
	case "Sum":
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		return sum
	case "Minimum":
		m := vals[0]
		for _, v := range vals {
			if v < m {
				m = v
			}
		}
		return m
	case "Maximum":
		m := vals[0]
		for _, v := range vals {
			if v > m {
				m = v
			}
		}
		return m
	case "SampleCount":
		return float64(len(vals))
	default: // Average
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		return sum / float64(len(vals))
	}
}

// PutMetricData request type
type putMetricDataRequest struct {
	Namespace  string          `cbor:"Namespace"`
	MetricData []putMetricItem `cbor:"MetricData"`
}

type putMetricItem struct {
	MetricName string        `cbor:"MetricName"`
	Dimensions []CWDimension `cbor:"Dimensions,omitempty"`
	Value      float64       `cbor:"Value"`
	Timestamp  time.Time     `cbor:"Timestamp"`
	Unit       string        `cbor:"Unit,omitempty"`
}

func handleCWPutMetricData(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req putMetricDataRequest
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}

	for _, item := range req.MetricData {
		ts := item.Timestamp
		if ts.IsZero() {
			ts = time.Now().UTC() // real CloudWatch defaults an omitted timestamp to now
		}
		datum := CWMetricDatum{
			Namespace:  req.Namespace,
			MetricName: item.MetricName,
			Dimensions: item.Dimensions,
			Value:      item.Value,
			Timestamp:  float64(ts.Unix()),
			Unit:       item.Unit,
		}
		cwStoreDatum(datum)
	}

	// Empty CBOR response
	data, _ := cbor.Marshal(map[string]any{})
	w.Header().Set("Content-Type", "application/cbor")
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func metricsKey(namespace, metricName string, dims []CWDimension) string {
	key := namespace + "/" + metricName
	for _, d := range dims {
		key += fmt.Sprintf("/%s=%s", d.Name, d.Value)
	}
	return key
}
