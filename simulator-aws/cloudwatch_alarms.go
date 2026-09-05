package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/fxamacker/cbor/v2"
)

// CloudWatch metric alarms.
//
// Real clients reach the alarm API over all three wire protocols the metric
// API uses, so the alarm surface mirrors it: the Go SDK / terraform provider
// use rpc-v2-cbor (POST /service/GraniteServiceVersion20100801/operation/<Op>),
// and the aws CLI / botocore use either awsJson1.0 (X-Amz-Target
// GraniteServiceVersion20100801.<Op>) or the legacy query protocol (Action=...,
// form params, XML) depending on the botocore version. All three surfaces share
// the cwAlarms store and the state evaluator below, so an alarm created via one
// protocol is visible through the others.

// CWAlarm holds a metric alarm's configuration. StateValue is not stored: it is
// evaluated against the live metric data at describe time, the way real
// CloudWatch continuously re-evaluates.
type CWAlarm struct {
	AlarmName               string            `json:"AlarmName" cbor:"AlarmName"`
	AlarmArn                string            `json:"AlarmArn" cbor:"AlarmArn"`
	AlarmDescription        string            `json:"AlarmDescription,omitempty" cbor:"AlarmDescription,omitempty"`
	Namespace               string            `json:"Namespace" cbor:"Namespace"`
	MetricName              string            `json:"MetricName" cbor:"MetricName"`
	Dimensions              []CWDimension     `json:"Dimensions,omitempty" cbor:"Dimensions,omitempty"`
	Statistic               string            `json:"Statistic,omitempty" cbor:"Statistic,omitempty"`
	ExtendedStatistic       string            `json:"ExtendedStatistic,omitempty" cbor:"ExtendedStatistic,omitempty"`
	Period                  int32             `json:"Period" cbor:"Period"`
	EvaluationPeriods       int32             `json:"EvaluationPeriods" cbor:"EvaluationPeriods"`
	Threshold               float64           `json:"Threshold" cbor:"Threshold"`
	ComparisonOperator      string            `json:"ComparisonOperator" cbor:"ComparisonOperator"`
	TreatMissingData        string            `json:"TreatMissingData,omitempty" cbor:"TreatMissingData,omitempty"`
	Unit                    string            `json:"Unit,omitempty" cbor:"Unit,omitempty"`
	ActionsEnabled          bool              `json:"ActionsEnabled" cbor:"ActionsEnabled"`
	AlarmActions            []string          `json:"AlarmActions,omitempty" cbor:"AlarmActions,omitempty"`
	OKActions               []string          `json:"OKActions,omitempty" cbor:"OKActions,omitempty"`
	InsufficientDataActions []string          `json:"InsufficientDataActions,omitempty" cbor:"InsufficientDataActions,omitempty"`
	Tags                    map[string]string `json:"Tags,omitempty" cbor:"Tags,omitempty"`
	// ManualState / ManualStateReason hold a temporary SetAlarmState override
	// that takes precedence over the metric-derived evaluation until cleared.
	ManualState       string `json:"-" cbor:"-"`
	ManualStateReason string `json:"-" cbor:"-"`
	// StateValue / StateReason / StateUpdatedTimestamp hold the last state the
	// background evaluator derived and dispatched actions for. DescribeAlarms
	// continues to evaluate live metric data so an alarm whose metric just
	// changed reflects it immediately; these fields persist the dispatched
	// state so transitions are detectable across evaluator ticks.
	StateValue            string `json:"StateValue,omitempty" cbor:"StateValue,omitempty"`
	StateReason           string `json:"StateReason,omitempty" cbor:"StateReason,omitempty"`
	StateUpdatedTimestamp int64  `json:"StateUpdatedTimestamp,omitempty" cbor:"StateUpdatedTimestamp,omitempty"`
}

// cwAlarmByArn finds an alarm by its ARN (the resource id the tagging API uses).
func cwAlarmByArn(arn string) (CWAlarm, bool) {
	for _, a := range cwAlarms.List() {
		if a.AlarmArn == arn {
			return a, true
		}
	}
	return CWAlarm{}, false
}

type cwTagKV struct {
	Key   string `json:"Key" cbor:"Key"`
	Value string `json:"Value" cbor:"Value"`
}

// cwResolveAlarmTags converts the request's tag list to a map. When the request
// carries no tags it preserves the alarm's existing tags — PutMetricAlarm only
// sets tags on creation; updates manage them through TagResource/UntagResource.
func cwResolveAlarmTags(reqTags []cwTagKV, alarmName string) map[string]string {
	if len(reqTags) > 0 {
		m := make(map[string]string, len(reqTags))
		for _, t := range reqTags {
			m[t.Key] = t.Value
		}
		return m
	}
	if existing, ok := cwAlarms.Get(alarmName); ok {
		return existing.Tags
	}
	return nil
}

var cwAlarms sim.Store[CWAlarm]

func cwAlarmArn(name string) string {
	return fmt.Sprintf("arn:aws:cloudwatch:%s:%s:alarm:%s", awsRegion(), awsAccountID(), name)
}

// cwAlarmBreaches reports whether a datapoint value crosses the threshold for
// the comparison operator (the four scalar operators; anomaly-band operators
// aren't modelled).
func cwAlarmBreaches(v, threshold float64, op string) bool {
	switch op {
	case "GreaterThanThreshold":
		return v > threshold
	case "GreaterThanOrEqualToThreshold":
		return v >= threshold
	case "LessThanThreshold":
		return v < threshold
	case "LessThanOrEqualToThreshold":
		return v <= threshold
	default:
		return false
	}
}

// cwEvaluateAlarmState derives the alarm's StateValue (OK / ALARM /
// INSUFFICIENT_DATA) from the metric data over the most recent
// EvaluationPeriods windows. With no datapoints in the window it honours
// TreatMissingData; otherwise it ALARMs only when every evaluated datapoint
// breaches.
func cwEvaluateAlarmState(a CWAlarm) (state, reason string) {
	period := a.Period
	if period <= 0 {
		period = 60
	}
	evalPeriods := a.EvaluationPeriods
	if evalPeriods <= 0 {
		evalPeriods = 1
	}
	now := time.Now().UTC().Unix()
	windowStart := now - int64(evalPeriods)*int64(period)

	data, _ := cwMetrics.Get(metricsKey(a.Namespace, a.MetricName, a.Dimensions))
	buckets := map[int64][]float64{}
	for _, d := range data {
		ts := int64(d.Timestamp)
		if ts < windowStart || ts > now {
			continue
		}
		b := (ts / int64(period)) * int64(period)
		buckets[b] = append(buckets[b], d.Value)
	}

	if len(buckets) == 0 {
		switch a.TreatMissingData {
		case "notBreaching":
			return "OK", "Threshold not crossed: missing data treated as not breaching"
		case "breaching":
			return "ALARM", "Threshold crossed: missing data treated as breaching"
		default: // "missing" (default) and "ignore"
			return "INSUFFICIENT_DATA", "Insufficient Data: no datapoints were received"
		}
	}

	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] > keys[j] }) // newest first

	evaluated, breaching := 0, 0
	for _, k := range keys {
		if evaluated >= int(evalPeriods) {
			break
		}
		evaluated++
		if cwAlarmBreaches(cwApplyAlarmStat(a, buckets[k]), a.Threshold, a.ComparisonOperator) {
			breaching++
		}
	}
	if evaluated >= int(evalPeriods) && breaching == evaluated {
		return "ALARM", fmt.Sprintf("Threshold Crossed: %d datapoints were %s the threshold (%g)", breaching, a.ComparisonOperator, a.Threshold)
	}
	return "OK", "Threshold not crossed"
}

// cwApplyAlarmStat reduces a bucket by the alarm's statistic — a percentile when
// ExtendedStatistic (e.g. "p99") is set, otherwise the named Statistic.
func cwApplyAlarmStat(a CWAlarm, vals []float64) float64 {
	if a.ExtendedStatistic != "" {
		if p, ok := cwParsePercentile(a.ExtendedStatistic); ok {
			return cwPercentile(vals, p)
		}
	}
	stat := a.Statistic
	if stat == "" {
		stat = "Average"
	}
	return cwApplyStat(stat, vals)
}

// cwParsePercentile parses "p99" / "p99.9" into the percentile 99 / 99.9.
func cwParsePercentile(s string) (float64, bool) {
	if len(s) < 2 || (s[0] != 'p' && s[0] != 'P') {
		return 0, false
	}
	f, err := strconv.ParseFloat(s[1:], 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// cwPercentile returns the linear-interpolated p-th percentile (0..100) of vals.
func cwPercentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	switch {
	case p <= 0:
		return sorted[0]
	case p >= 100:
		return sorted[len(sorted)-1]
	}
	rank := (p / 100) * float64(len(sorted)-1)
	lo := int(rank)
	frac := rank - float64(lo)
	if lo+1 < len(sorted) {
		return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
	}
	return sorted[lo]
}

// cwListAlarms returns alarms filtered by an optional name set, sorted by name.
func cwListAlarms(names []string) []CWAlarm {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	out := make([]CWAlarm, 0)
	for _, a := range cwAlarms.List() {
		if len(want) > 0 && !want[a.AlarmName] {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AlarmName < out[j].AlarmName })
	return out
}

// ── awsJson1.0 surface (aws CLI) ───────────────────────────────────────────

func registerCloudWatchAlarmsJSON(r *AWSRouter) {
	r.Register("GraniteServiceVersion20100801.PutMetricAlarm", handleCWJSONPutMetricAlarm)
	r.Register("GraniteServiceVersion20100801.DescribeAlarms", handleCWJSONDescribeAlarms)
	r.Register("GraniteServiceVersion20100801.DeleteAlarms", handleCWJSONDeleteAlarms)
}

func handleCWJSONPutMetricAlarm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName               string        `json:"AlarmName"`
		AlarmDescription        string        `json:"AlarmDescription"`
		Namespace               string        `json:"Namespace"`
		MetricName              string        `json:"MetricName"`
		Dimensions              []CWDimension `json:"Dimensions"`
		Statistic               string        `json:"Statistic"`
		ExtendedStatistic       string        `json:"ExtendedStatistic"`
		Period                  int32         `json:"Period"`
		EvaluationPeriods       int32         `json:"EvaluationPeriods"`
		Threshold               float64       `json:"Threshold"`
		ComparisonOperator      string        `json:"ComparisonOperator"`
		TreatMissingData        string        `json:"TreatMissingData"`
		Unit                    string        `json:"Unit"`
		ActionsEnabled          *bool         `json:"ActionsEnabled"`
		AlarmActions            []string      `json:"AlarmActions"`
		OKActions               []string      `json:"OKActions"`
		InsufficientDataActions []string      `json:"InsufficientDataActions"`
		Tags                    []cwTagKV     `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.AlarmName == "" {
		AWSError(w, "MissingParameter", "The parameter AlarmName is required.", http.StatusBadRequest)
		return
	}
	if req.ComparisonOperator == "" {
		AWSError(w, "MissingParameter", "The parameter ComparisonOperator is required.", http.StatusBadRequest)
		return
	}
	if code, msg, ok := cwValidateMetricAlarm(req.MetricName, req.Period, req.EvaluationPeriods); !ok {
		AWSError(w, code, msg, http.StatusBadRequest)
		return
	}
	actionsEnabled := true
	if req.ActionsEnabled != nil {
		actionsEnabled = *req.ActionsEnabled
	}
	// A new or updated alarm is a fresh entity: the alarm record itself
	// carries no StateValue, so the evaluator treats the next transition as
	// coming from INSUFFICIENT_DATA.
	cwAlarms.Put(req.AlarmName, CWAlarm{
		AlarmName:               req.AlarmName,
		AlarmArn:                cwAlarmArn(req.AlarmName),
		AlarmDescription:        req.AlarmDescription,
		Namespace:               req.Namespace,
		MetricName:              req.MetricName,
		Dimensions:              req.Dimensions,
		Statistic:               req.Statistic,
		ExtendedStatistic:       req.ExtendedStatistic,
		Period:                  req.Period,
		EvaluationPeriods:       req.EvaluationPeriods,
		Threshold:               req.Threshold,
		ComparisonOperator:      req.ComparisonOperator,
		TreatMissingData:        req.TreatMissingData,
		Unit:                    req.Unit,
		ActionsEnabled:          actionsEnabled,
		AlarmActions:            req.AlarmActions,
		OKActions:               req.OKActions,
		InsufficientDataActions: req.InsufficientDataActions,
		Tags:                    cwResolveAlarmTags(req.Tags, req.AlarmName),
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONDescribeAlarms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmNames []string `json:"AlarmNames"`
		StateValue string   `json:"StateValue"`
		AlarmTypes []string `json:"AlarmTypes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	resp := map[string]any{}
	if cwWantAlarmType(req.AlarmTypes, "MetricAlarm") {
		out := make([]map[string]any, 0)
		for _, a := range cwListAlarms(req.AlarmNames) {
			if req.StateValue != "" {
				state, _ := cwAlarmEffectiveState(a)
				if req.StateValue != state {
					continue
				}
			}
			out = append(out, cwJSONMetricAlarm(a, now))
		}
		resp["MetricAlarms"] = out
	}
	if cwWantAlarmType(req.AlarmTypes, "CompositeAlarm") {
		comps := make([]map[string]any, 0)
		for _, ca := range cwListCompositeAlarms(req.AlarmNames) {
			comps = append(comps, cwJSONCompositeAlarm(ca, now))
		}
		resp["CompositeAlarms"] = comps
	}
	if cwWantAlarmType(req.AlarmTypes, "LogAlarm") {
		logs := make([]map[string]any, 0)
		for _, la := range cwListLogAlarms(req.AlarmNames) {
			view := cwJSONLogAlarm(la, now)
			if req.StateValue != "" && view["StateValue"] != req.StateValue {
				continue
			}
			logs = append(logs, view)
		}
		resp["LogAlarms"] = logs
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// cwJSONCompositeAlarm renders a composite alarm as the awsJson CompositeAlarm
// shape.
func cwJSONCompositeAlarm(ca CWCompositeAlarm, now time.Time) map[string]any {
	state := ca.StateValue
	if state == "" {
		state = "INSUFFICIENT_DATA"
	}
	return map[string]any{
		"AlarmName":             ca.AlarmName,
		"AlarmArn":              ca.AlarmArn,
		"AlarmDescription":      ca.AlarmDescription,
		"AlarmRule":             ca.AlarmRule,
		"ActionsEnabled":        ca.ActionsEnabled,
		"AlarmActions":          ca.AlarmActions,
		"StateValue":            state,
		"StateReason":           ca.StateReason,
		"StateUpdatedTimestamp": float64(now.Unix()),
	}
}

func handleCWJSONDeleteAlarms(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmNames []string `json:"AlarmNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	for _, n := range req.AlarmNames {
		if !cwAlarmExists(n) {
			AWSErrorf(w, "ResourceNotFound", http.StatusBadRequest, "Alarm %s does not exist", n)
			return
		}
	}
	cwDeleteAlarms(req.AlarmNames)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ── rpc-v2-cbor surface (Go SDK / terraform) ───────────────────────────────

func registerCloudWatchAlarmsCBOR(srv *sim.Server) {
	cwCBOR(srv, "PutMetricAlarm", handleCWCBORPutMetricAlarm)
	cwCBOR(srv, "DescribeAlarms", handleCWCBORDescribeAlarms)
	cwCBOR(srv, "DeleteAlarms", handleCWCBORDeleteAlarms)
	cwCBOR(srv, "ListTagsForResource", handleCWCBORListTagsForResource)
	cwCBOR(srv, "TagResource", handleCWCBORTagResource)
	cwCBOR(srv, "UntagResource", handleCWCBORUntagResource)
}

func cwWriteCBOR(w http.ResponseWriter, v any) {
	data, err := cwEncMode.Marshal(v)
	if err != nil {
		AWSError(w, "InternalFailure", "Failed to encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// cwWriteCBORError writes an rpc-v2-cbor protocol error: the Go SDK rejects any
// response to a cbor request that lacks the `Smithy-Protocol: rpc-v2-cbor`
// header, and reads the error code from the cbor body's `__type` and the message
// from `message` (verified against aws-sdk-go-v2 cloudwatch's getProtocolErrorInfo).
// The plain JSON `AWSError` shape is only valid for the awsJson surfaces.
func cwWriteCBORError(w http.ResponseWriter, code, message string, status int) {
	data, err := cwEncMode.Marshal(map[string]any{"__type": code, "message": message})
	if err != nil {
		AWSError(w, "InternalFailure", "Failed to encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// cwWriteCBORErrorf is the formatted-message form of cwWriteCBORError (argument
// order mirrors AWSErrorf: code, status, format, args).
func cwWriteCBORErrorf(w http.ResponseWriter, code string, status int, format string, args ...any) {
	cwWriteCBORError(w, code, fmt.Sprintf(format, args...), status)
}

func handleCWCBORPutMetricAlarm(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		AlarmName               string        `cbor:"AlarmName"`
		AlarmDescription        string        `cbor:"AlarmDescription"`
		Namespace               string        `cbor:"Namespace"`
		MetricName              string        `cbor:"MetricName"`
		Dimensions              []CWDimension `cbor:"Dimensions"`
		Statistic               string        `cbor:"Statistic"`
		ExtendedStatistic       string        `cbor:"ExtendedStatistic"`
		Period                  int32         `cbor:"Period"`
		EvaluationPeriods       int32         `cbor:"EvaluationPeriods"`
		Threshold               float64       `cbor:"Threshold"`
		ComparisonOperator      string        `cbor:"ComparisonOperator"`
		TreatMissingData        string        `cbor:"TreatMissingData"`
		Unit                    string        `cbor:"Unit"`
		ActionsEnabled          *bool         `cbor:"ActionsEnabled"`
		AlarmActions            []string      `cbor:"AlarmActions"`
		OKActions               []string      `cbor:"OKActions"`
		InsufficientDataActions []string      `cbor:"InsufficientDataActions"`
		Tags                    []cwTagKV     `cbor:"Tags"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	if req.AlarmName == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter AlarmName is required.", http.StatusBadRequest)
		return
	}
	if code, msg, ok := cwValidateMetricAlarm(req.MetricName, req.Period, req.EvaluationPeriods); !ok {
		cwWriteCBORError(w, code, msg, http.StatusBadRequest)
		return
	}
	actionsEnabled := true
	if req.ActionsEnabled != nil {
		actionsEnabled = *req.ActionsEnabled
	}
	// A new or updated alarm is a fresh entity: the alarm record itself
	// carries no StateValue, so the evaluator treats the next transition as
	// coming from INSUFFICIENT_DATA.
	cwAlarms.Put(req.AlarmName, CWAlarm{
		AlarmName:               req.AlarmName,
		AlarmArn:                cwAlarmArn(req.AlarmName),
		AlarmDescription:        req.AlarmDescription,
		Namespace:               req.Namespace,
		MetricName:              req.MetricName,
		Dimensions:              req.Dimensions,
		Statistic:               req.Statistic,
		ExtendedStatistic:       req.ExtendedStatistic,
		Period:                  req.Period,
		EvaluationPeriods:       req.EvaluationPeriods,
		Threshold:               req.Threshold,
		ComparisonOperator:      req.ComparisonOperator,
		TreatMissingData:        req.TreatMissingData,
		Unit:                    req.Unit,
		ActionsEnabled:          actionsEnabled,
		AlarmActions:            req.AlarmActions,
		OKActions:               req.OKActions,
		InsufficientDataActions: req.InsufficientDataActions,
		Tags:                    cwResolveAlarmTags(req.Tags, req.AlarmName),
	})
	cwWriteCBOR(w, map[string]any{})
}

// ── alarm tagging (cbor; terraform's transparent-tagging read path) ─────────

func handleCWCBORListTagsForResource(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		ResourceARN string `cbor:"ResourceARN"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	current, _, ok := cwResourceTags(req.ResourceARN)
	if !ok {
		cwWriteCBORErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Unknown resource %s", req.ResourceARN)
		return
	}
	tags := make([]cwTagKV, 0, len(current))
	for _, k := range cwSortedKeys(current) {
		tags = append(tags, cwTagKV{Key: k, Value: current[k]})
	}
	cwWriteCBOR(w, map[string]any{"Tags": tags})
}

func handleCWCBORTagResource(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		ResourceARN string    `cbor:"ResourceARN"`
		Tags        []cwTagKV `cbor:"Tags"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	current, setter, ok := cwResourceTags(req.ResourceARN)
	if !ok {
		cwWriteCBORErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Unknown resource %s", req.ResourceARN)
		return
	}
	merged := map[string]string{}
	for k, v := range current {
		merged[k] = v
	}
	for _, t := range req.Tags {
		merged[t.Key] = t.Value
	}
	setter(merged)
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORUntagResource(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		ResourceARN string   `cbor:"ResourceARN"`
		TagKeys     []string `cbor:"TagKeys"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	current, setter, ok := cwResourceTags(req.ResourceARN)
	if !ok {
		cwWriteCBORErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Unknown resource %s", req.ResourceARN)
		return
	}
	merged := map[string]string{}
	for k, v := range current {
		merged[k] = v
	}
	for _, k := range req.TagKeys {
		delete(merged, k)
	}
	setter(merged)
	cwWriteCBOR(w, map[string]any{})
}

func cwSortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cborMetricAlarm is the DescribeAlarms response shape, config plus the
// evaluated state. time.Time fields ride cwEncMode's tag-1 encoding.
type cborMetricAlarm struct {
	AlarmName             string        `cbor:"AlarmName"`
	AlarmArn              string        `cbor:"AlarmArn"`
	AlarmDescription      string        `cbor:"AlarmDescription,omitempty"`
	Namespace             string        `cbor:"Namespace"`
	MetricName            string        `cbor:"MetricName"`
	Dimensions            []CWDimension `cbor:"Dimensions,omitempty"`
	Statistic             string        `cbor:"Statistic,omitempty"`
	ExtendedStatistic     string        `cbor:"ExtendedStatistic,omitempty"`
	Period                int32         `cbor:"Period"`
	EvaluationPeriods     int32         `cbor:"EvaluationPeriods"`
	Threshold             float64       `cbor:"Threshold"`
	ComparisonOperator    string        `cbor:"ComparisonOperator"`
	TreatMissingData      string        `cbor:"TreatMissingData,omitempty"`
	ActionsEnabled        bool          `cbor:"ActionsEnabled"`
	AlarmActions          []string      `cbor:"AlarmActions,omitempty"`
	StateValue            string        `cbor:"StateValue"`
	StateReason           string        `cbor:"StateReason,omitempty"`
	StateUpdatedTimestamp time.Time     `cbor:"StateUpdatedTimestamp"`
}

func handleCWCBORDescribeAlarms(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		AlarmNames []string `cbor:"AlarmNames"`
		StateValue string   `cbor:"StateValue"`
		AlarmTypes []string `cbor:"AlarmTypes"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	resp := map[string]any{}
	if cwWantAlarmType(req.AlarmTypes, "MetricAlarm") {
		alarms := make([]cborMetricAlarm, 0)
		for _, a := range cwListAlarms(req.AlarmNames) {
			state, reason := cwAlarmEffectiveState(a)
			if req.StateValue != "" && req.StateValue != state {
				continue
			}
			alarms = append(alarms, cborMetricAlarm{
				AlarmName:             a.AlarmName,
				AlarmArn:              a.AlarmArn,
				AlarmDescription:      a.AlarmDescription,
				Namespace:             a.Namespace,
				MetricName:            a.MetricName,
				Dimensions:            a.Dimensions,
				Statistic:             a.Statistic,
				ExtendedStatistic:     a.ExtendedStatistic,
				Period:                a.Period,
				EvaluationPeriods:     a.EvaluationPeriods,
				Threshold:             a.Threshold,
				ComparisonOperator:    a.ComparisonOperator,
				TreatMissingData:      a.TreatMissingData,
				ActionsEnabled:        a.ActionsEnabled,
				AlarmActions:          a.AlarmActions,
				StateValue:            state,
				StateReason:           reason,
				StateUpdatedTimestamp: now,
			})
		}
		resp["MetricAlarms"] = alarms
	}
	if cwWantAlarmType(req.AlarmTypes, "CompositeAlarm") {
		comps := make([]cborCompositeAlarm, 0)
		for _, ca := range cwListCompositeAlarms(req.AlarmNames) {
			comps = append(comps, cborCompositeAlarmOf(ca, now))
		}
		resp["CompositeAlarms"] = comps
	}
	if cwWantAlarmType(req.AlarmTypes, "LogAlarm") {
		logs := make([]cborLogAlarm, 0)
		for _, la := range cwListLogAlarms(req.AlarmNames) {
			view := cborLogAlarmOf(la, now)
			if req.StateValue != "" && req.StateValue != view.StateValue {
				continue
			}
			logs = append(logs, view)
		}
		resp["LogAlarms"] = logs
	}
	cwWriteCBOR(w, resp)
}

// cwWantAlarmType reports whether an alarm type should be included given the
// (possibly empty) AlarmTypes filter. An empty filter means MetricAlarm only,
// matching real DescribeAlarms' default.
func cwWantAlarmType(filter []string, t string) bool {
	if len(filter) == 0 {
		return t == "MetricAlarm"
	}
	for _, f := range filter {
		if f == t {
			return true
		}
	}
	return false
}

// cwListCompositeAlarms returns composite alarms filtered by an optional name
// set, sorted by name.
func cwListCompositeAlarms(names []string) []CWCompositeAlarm {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	out := make([]CWCompositeAlarm, 0)
	for _, a := range cwCompositeAlarms.List() {
		if len(want) > 0 && !want[a.AlarmName] {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AlarmName < out[j].AlarmName })
	return out
}

// cborCompositeAlarm is the DescribeAlarms CompositeAlarm response shape.
type cborCompositeAlarm struct {
	AlarmName             string    `cbor:"AlarmName"`
	AlarmArn              string    `cbor:"AlarmArn"`
	AlarmDescription      string    `cbor:"AlarmDescription,omitempty"`
	AlarmRule             string    `cbor:"AlarmRule"`
	ActionsEnabled        bool      `cbor:"ActionsEnabled"`
	AlarmActions          []string  `cbor:"AlarmActions,omitempty"`
	StateValue            string    `cbor:"StateValue"`
	StateReason           string    `cbor:"StateReason,omitempty"`
	StateUpdatedTimestamp time.Time `cbor:"StateUpdatedTimestamp"`
}

func cborCompositeAlarmOf(ca CWCompositeAlarm, now time.Time) cborCompositeAlarm {
	state := ca.StateValue
	if state == "" {
		state = "INSUFFICIENT_DATA"
	}
	return cborCompositeAlarm{
		AlarmName:             ca.AlarmName,
		AlarmArn:              ca.AlarmArn,
		AlarmDescription:      ca.AlarmDescription,
		AlarmRule:             ca.AlarmRule,
		ActionsEnabled:        ca.ActionsEnabled,
		AlarmActions:          ca.AlarmActions,
		StateValue:            state,
		StateReason:           ca.StateReason,
		StateUpdatedTimestamp: now,
	}
}

func handleCWCBORDeleteAlarms(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req struct {
		AlarmNames []string `cbor:"AlarmNames"`
	}
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	for _, n := range req.AlarmNames {
		if !cwAlarmExists(n) {
			cwWriteCBORErrorf(w, "ResourceNotFound", http.StatusBadRequest, "Alarm %s does not exist", n)
			return
		}
	}
	cwDeleteAlarms(req.AlarmNames)
	cwWriteCBOR(w, map[string]any{})
}

// cwAlarmExists reports whether a name resolves to a metric, composite or log
// alarm — the three alarm types DeleteAlarms accepts.
func cwAlarmExists(name string) bool {
	if _, ok := cwAlarms.Get(name); ok {
		return true
	}
	if _, ok := cwCompositeAlarms.Get(name); ok {
		return true
	}
	_, ok := cwLogAlarms.Get(name)
	return ok
}

// cwDeleteAlarms removes the named metric, composite and log alarms.
func cwDeleteAlarms(names []string) {
	for _, n := range names {
		cwAlarms.Delete(n)
		cwCompositeAlarms.Delete(n)
		cwDeleteLogAlarm(n)
	}
}

// ── query surface (botocore / older aws CLI) ───────────────────────────────

func registerCloudWatchAlarmsQuery(r *AWSQueryRouter) {
	r.Register("PutMetricAlarm", handleCWQueryPutMetricAlarm)
	r.Register("DescribeAlarms", handleCWQueryDescribeAlarms)
	r.Register("DeleteAlarms", handleCWQueryDeleteAlarms)
}

func cwQueryStringList(r *http.Request, prefix string) []string {
	var out []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.member.%d", prefix, i))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

// cwValidateMetricAlarm enforces the required-numeric-parameter validation
// PutMetricAlarm needs: EvaluationPeriods is required (minimum 1), and a
// single-metric alarm (MetricName set) requires Period. Without this a missing
// or non-numeric value parses to 0 and the alarm is silently created with a
// nonsensical configuration. The (code, message) mirror this handler's existing
// missing-required-parameter convention (as used for AlarmName /
// ComparisonOperator) — the `MissingParameter` query-protocol framework error.
func cwValidateMetricAlarm(metricName string, period, evalPeriods int32) (code, msg string, ok bool) {
	if evalPeriods < 1 {
		return "MissingParameter", "The parameter EvaluationPeriods is required.", false
	}
	if metricName != "" && period < 1 {
		return "MissingParameter", "The parameter Period is required.", false
	}
	return "", "", true
}

func handleCWQueryPutMetricAlarm(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("AlarmName")
	if name == "" {
		cwQueryError(w, "MissingParameter", "The parameter AlarmName is required.")
		return
	}
	if r.FormValue("ComparisonOperator") == "" {
		cwQueryError(w, "MissingParameter", "The parameter ComparisonOperator is required.")
		return
	}
	period, _ := strconv.Atoi(r.FormValue("Period"))
	evalPeriods, _ := strconv.Atoi(r.FormValue("EvaluationPeriods"))
	if code, msg, ok := cwValidateMetricAlarm(r.FormValue("MetricName"), int32(period), int32(evalPeriods)); !ok {
		cwQueryError(w, code, msg)
		return
	}
	var threshold float64
	if ts := r.FormValue("Threshold"); ts != "" {
		tv, err := strconv.ParseFloat(ts, 64)
		if err != nil {
			cwQueryError(w, "InvalidParameterValue", "The parameter Threshold must be a double.")
			return
		}
		threshold = tv
	}
	actionsEnabled := true
	if v := r.FormValue("ActionsEnabled"); v != "" {
		actionsEnabled = v == "true"
	}
	var tags []cwTagKV
	for i := 1; ; i++ {
		k := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
		if k == "" {
			break
		}
		tags = append(tags, cwTagKV{Key: k, Value: r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))})
	}
	// A new or updated alarm is a fresh entity: the alarm record itself
	// carries no StateValue, so the evaluator treats the next transition as
	// coming from INSUFFICIENT_DATA.
	cwAlarms.Put(name, CWAlarm{
		AlarmName:               name,
		AlarmArn:                cwAlarmArn(name),
		AlarmDescription:        r.FormValue("AlarmDescription"),
		Namespace:               r.FormValue("Namespace"),
		MetricName:              r.FormValue("MetricName"),
		Dimensions:              cwQueryDimensions(r, "Dimensions"),
		Statistic:               r.FormValue("Statistic"),
		ExtendedStatistic:       r.FormValue("ExtendedStatistic"),
		Period:                  int32(period),
		EvaluationPeriods:       int32(evalPeriods),
		Threshold:               threshold,
		ComparisonOperator:      r.FormValue("ComparisonOperator"),
		TreatMissingData:        r.FormValue("TreatMissingData"),
		Unit:                    r.FormValue("Unit"),
		ActionsEnabled:          actionsEnabled,
		AlarmActions:            cwQueryStringList(r, "AlarmActions"),
		OKActions:               cwQueryStringList(r, "OKActions"),
		InsufficientDataActions: cwQueryStringList(r, "InsufficientDataActions"),
		Tags:                    cwResolveAlarmTags(tags, name),
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<PutMetricAlarmResponse %s><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></PutMetricAlarmResponse>`,
		cwQueryXmlns, generateUUID())
}

func handleCWQueryDescribeAlarms(w http.ResponseWriter, r *http.Request) {
	names := cwQueryStringList(r, "AlarmNames")
	stateFilter := r.FormValue("StateValue")
	alarmTypes := cwQueryStringList(r, "AlarmTypes")
	now := time.Now().UTC()
	var members strings.Builder
	if cwWantAlarmType(alarmTypes, "MetricAlarm") {
		for _, a := range cwListAlarms(names) {
			state, reason := cwAlarmEffectiveState(a)
			if stateFilter != "" && stateFilter != state {
				continue
			}
			members.WriteString("<member>")
			fmt.Fprintf(&members, "<AlarmName>%s</AlarmName><AlarmArn>%s</AlarmArn>", xmlEscape(a.AlarmName), xmlEscape(a.AlarmArn))
			if a.AlarmDescription != "" {
				fmt.Fprintf(&members, "<AlarmDescription>%s</AlarmDescription>", xmlEscape(a.AlarmDescription))
			}
			fmt.Fprintf(&members, "<Namespace>%s</Namespace><MetricName>%s</MetricName>", xmlEscape(a.Namespace), xmlEscape(a.MetricName))
			members.WriteString("<Dimensions>")
			for _, dim := range a.Dimensions {
				fmt.Fprintf(&members, "<member><Name>%s</Name><Value>%s</Value></member>", xmlEscape(dim.Name), xmlEscape(dim.Value))
			}
			members.WriteString("</Dimensions>")
			if a.ExtendedStatistic != "" {
				fmt.Fprintf(&members, "<ExtendedStatistic>%s</ExtendedStatistic>", xmlEscape(a.ExtendedStatistic))
			} else if a.Statistic != "" {
				fmt.Fprintf(&members, "<Statistic>%s</Statistic>", xmlEscape(a.Statistic))
			}
			fmt.Fprintf(&members, "<Period>%d</Period><EvaluationPeriods>%d</EvaluationPeriods>",
				a.Period, a.EvaluationPeriods)
			fmt.Fprintf(&members, "<Threshold>%s</Threshold><ComparisonOperator>%s</ComparisonOperator>",
				cwFormatFloat(a.Threshold), xmlEscape(a.ComparisonOperator))
			if a.TreatMissingData != "" {
				fmt.Fprintf(&members, "<TreatMissingData>%s</TreatMissingData>", xmlEscape(a.TreatMissingData))
			}
			fmt.Fprintf(&members, "<ActionsEnabled>%t</ActionsEnabled>", a.ActionsEnabled)
			members.WriteString("<AlarmActions>")
			for _, act := range a.AlarmActions {
				fmt.Fprintf(&members, "<member>%s</member>", xmlEscape(act))
			}
			members.WriteString("</AlarmActions>")
			fmt.Fprintf(&members, "<StateValue>%s</StateValue><StateReason>%s</StateReason><StateUpdatedTimestamp>%s</StateUpdatedTimestamp>",
				state, xmlEscape(reason), now.Format(time.RFC3339))
			members.WriteString("</member>")
		}
	}
	var composites strings.Builder
	if cwWantAlarmType(alarmTypes, "CompositeAlarm") {
		for _, ca := range cwListCompositeAlarms(names) {
			state := ca.StateValue
			if state == "" {
				state = "INSUFFICIENT_DATA"
			}
			composites.WriteString("<member>")
			fmt.Fprintf(&composites, "<AlarmName>%s</AlarmName><AlarmArn>%s</AlarmArn>", xmlEscape(ca.AlarmName), xmlEscape(ca.AlarmArn))
			if ca.AlarmDescription != "" {
				fmt.Fprintf(&composites, "<AlarmDescription>%s</AlarmDescription>", xmlEscape(ca.AlarmDescription))
			}
			fmt.Fprintf(&composites, "<AlarmRule>%s</AlarmRule><ActionsEnabled>%t</ActionsEnabled>", xmlEscape(ca.AlarmRule), ca.ActionsEnabled)
			fmt.Fprintf(&composites, "<StateValue>%s</StateValue><StateUpdatedTimestamp>%s</StateUpdatedTimestamp>",
				state, now.Format(time.RFC3339))
			composites.WriteString("</member>")
		}
	}
	var logAlarms strings.Builder
	if cwWantAlarmType(alarmTypes, "LogAlarm") {
		for _, la := range cwListLogAlarms(names) {
			state, reason, evalState := cwEvaluateLogAlarmState(la)
			if stateFilter != "" && stateFilter != state {
				continue
			}
			logAlarms.WriteString("<member>")
			fmt.Fprintf(&logAlarms, "<AlarmName>%s</AlarmName><AlarmArn>%s</AlarmArn>", xmlEscape(la.AlarmName), xmlEscape(la.AlarmArn))
			if la.AlarmDescription != "" {
				fmt.Fprintf(&logAlarms, "<AlarmDescription>%s</AlarmDescription>", xmlEscape(la.AlarmDescription))
			}
			logAlarms.WriteString("<ScheduledQueryConfiguration>")
			fmt.Fprintf(&logAlarms, "<QueryString>%s</QueryString><QueryARN>%s</QueryARN><ScheduledQueryRoleARN>%s</ScheduledQueryRoleARN><AggregationExpression>%s</AggregationExpression>",
				xmlEscape(la.QueryString), xmlEscape(la.QueryARN), xmlEscape(la.ScheduledQueryRoleARN), xmlEscape(la.AggregationExpression))
			logAlarms.WriteString("<LogGroupIdentifiers>")
			for _, g := range la.LogGroupIdentifiers {
				fmt.Fprintf(&logAlarms, "<member>%s</member>", xmlEscape(g))
			}
			logAlarms.WriteString("</LogGroupIdentifiers>")
			fmt.Fprintf(&logAlarms, "<ScheduleConfiguration><ScheduleExpression>%s</ScheduleExpression><StartTimeOffset>%d</StartTimeOffset><EndTimeOffset>%d</EndTimeOffset></ScheduleConfiguration>",
				xmlEscape(la.ScheduleExpression), la.StartTimeOffset, la.EndTimeOffset)
			logAlarms.WriteString("</ScheduledQueryConfiguration>")
			fmt.Fprintf(&logAlarms, "<QueryResultsToEvaluate>%d</QueryResultsToEvaluate><QueryResultsToAlarm>%d</QueryResultsToAlarm>",
				la.QueryResultsToEvaluate, la.QueryResultsToAlarm)
			fmt.Fprintf(&logAlarms, "<Threshold>%s</Threshold><ComparisonOperator>%s</ComparisonOperator>",
				cwFormatFloat(la.Threshold), xmlEscape(la.ComparisonOperator))
			if la.TreatMissingData != "" {
				fmt.Fprintf(&logAlarms, "<TreatMissingData>%s</TreatMissingData>", xmlEscape(la.TreatMissingData))
			}
			fmt.Fprintf(&logAlarms, "<ActionsEnabled>%t</ActionsEnabled>", la.ActionsEnabled)
			logAlarms.WriteString("<AlarmActions>")
			for _, act := range la.AlarmActions {
				fmt.Fprintf(&logAlarms, "<member>%s</member>", xmlEscape(act))
			}
			logAlarms.WriteString("</AlarmActions>")
			if evalState != "" {
				fmt.Fprintf(&logAlarms, "<EvaluationState>%s</EvaluationState>", xmlEscape(evalState))
			}
			fmt.Fprintf(&logAlarms, "<StateValue>%s</StateValue><StateReason>%s</StateReason><StateUpdatedTimestamp>%s</StateUpdatedTimestamp>",
				state, xmlEscape(reason), now.Format(time.RFC3339))
			logAlarms.WriteString("</member>")
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeAlarmsResponse %s><DescribeAlarmsResult><MetricAlarms>%s</MetricAlarms><CompositeAlarms>%s</CompositeAlarms><LogAlarms>%s</LogAlarms></DescribeAlarmsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></DescribeAlarmsResponse>`,
		cwQueryXmlns, members.String(), composites.String(), logAlarms.String(), generateUUID())
}

func handleCWQueryDeleteAlarms(w http.ResponseWriter, r *http.Request) {
	names := cwQueryStringList(r, "AlarmNames")
	for _, n := range names {
		if !cwAlarmExists(n) {
			cwQueryError(w, "ResourceNotFound", fmt.Sprintf("Alarm %s does not exist", n))
			return
		}
	}
	cwDeleteAlarms(names)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteAlarmsResponse %s><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></DeleteAlarmsResponse>`,
		cwQueryXmlns, generateUUID())
}
