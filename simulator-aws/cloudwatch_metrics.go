package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/fxamacker/cbor/v2"
	"github.com/rs/zerolog"
)

// rpc-v2-cbor timestamps are CBOR tag 1 (epoch); the SDK rejects a bare
// integer.
var cwEncMode, _ = cbor.EncOptions{Time: cbor.TimeUnixDynamic, TimeTag: cbor.EncTagRequired}.EncMode()

// The SDK gzip-compresses PutMetricData and may compress GetMetricData.
func cwReadBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return cwDecompress(body)
}

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

	// Smithy RPCv2 CBOR routes by URL path.
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
		startCWAnomalyTrainer(srv)
	}
}

type getMetricDataRequest struct {
	StartTime         time.Time           `cbor:"StartTime"`
	EndTime           time.Time           `cbor:"EndTime"`
	ScanBy            string              `cbor:"ScanBy"`
	MetricDataQueries []CWMetricDataQuery `cbor:"MetricDataQueries"`
}

type metricDataResult struct {
	Id         string      `cbor:"Id"`
	Label      string      `cbor:"Label,omitempty"`
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
	outputs, err := cwGetMetricData(req.MetricDataQueries, req.StartTime, req.EndTime, req.ScanBy)
	if err != nil {
		cwWriteCBORError(w, "ValidationError", err.Error(), http.StatusBadRequest)
		return
	}
	results := make([]metricDataResult, 0, len(outputs))
	for _, o := range outputs {
		results = append(results, metricDataResult{Id: o.Id, Label: o.Label, StatusCode: "Complete", Values: o.Values, Timestamps: o.Timestamps})
	}
	data, err := cwEncMode.Marshal(map[string]any{"MetricDataResults": results})
	if err != nil {
		cwWriteCBORError(w, "InternalFailure", "Failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/cbor")
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

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
	default:
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		return sum / float64(len(vals))
	}
}

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
			ts = time.Now().UTC() // CloudWatch defaults an omitted timestamp to now.
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
