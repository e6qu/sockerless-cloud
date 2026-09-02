package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/fxamacker/cbor/v2"
)

// CloudWatch alarm management beyond create/describe/delete: action toggles,
// manual state, history, the metric→alarm reverse lookup, and composite alarms.
//
// These share cwAlarms (metric alarms) and add cwCompositeAlarms +
// cwAlarmHistory. As with the rest of the CloudWatch surface, every operation
// is served on all three protocols a real client uses: awsJson1.0 (current aws
// CLI / botocore), rpc-v2-cbor (the Go SDK), and the legacy query protocol
// (older aws CLI). A change made through one protocol is visible through the
// others because they share the same stores.

// CWCompositeAlarm holds a composite alarm's configuration. Its StateValue is
// stored (manual via SetAlarmState) rather than evaluated — composite alarms
// combine other alarms through an AlarmRule expression the sim does not
// evaluate, so an unset state reports INSUFFICIENT_DATA.
type CWCompositeAlarm struct {
	AlarmName               string            `json:"AlarmName"`
	AlarmArn                string            `json:"AlarmArn"`
	AlarmDescription        string            `json:"AlarmDescription,omitempty"`
	AlarmRule               string            `json:"AlarmRule"`
	ActionsEnabled          bool              `json:"ActionsEnabled"`
	AlarmActions            []string          `json:"AlarmActions,omitempty"`
	OKActions               []string          `json:"OKActions,omitempty"`
	InsufficientDataActions []string          `json:"InsufficientDataActions,omitempty"`
	StateValue              string            `json:"StateValue,omitempty"`
	StateReason             string            `json:"StateReason,omitempty"`
	UpdatedTimestamp        int64             `json:"-"`
	Tags                    map[string]string `json:"Tags,omitempty"`
}

// CWAlarmHistoryItem is one entry in an alarm's history (state change,
// configuration update, or action). Stored newest-last; describe returns by
// ScanBy order.
type CWAlarmHistoryItem struct {
	AlarmName       string `json:"AlarmName"`
	AlarmType       string `json:"AlarmType"`
	Timestamp       int64  `json:"Timestamp"`
	HistoryItemType string `json:"HistoryItemType"`
	HistorySummary  string `json:"HistorySummary"`
	HistoryData     string `json:"HistoryData"`
}

var (
	cwCompositeAlarms sim.Store[CWCompositeAlarm]
	cwAlarmHistory    sim.Store[[]CWAlarmHistoryItem]
)

func cwRecordAlarmHistory(name, alarmType, itemType, summary, data string) {
	item := CWAlarmHistoryItem{
		AlarmName:       name,
		AlarmType:       alarmType,
		Timestamp:       time.Now().UTC().Unix(),
		HistoryItemType: itemType,
		HistorySummary:  summary,
		HistoryData:     data,
	}
	cwAlarmHistory.Upsert(name, func(h *[]CWAlarmHistoryItem) {
		*h = append(*h, item)
	})
}

func cwCompositeAlarmByArn(arn string) (CWCompositeAlarm, bool) {
	for _, a := range cwCompositeAlarms.List() {
		if a.AlarmArn == arn {
			return a, true
		}
	}
	return CWCompositeAlarm{}, false
}

// cwSetAlarmActionsEnabled toggles ActionsEnabled on a set of metric and/or
// composite alarms (the Enable/DisableAlarmActions operations). Unknown names
// are silently ignored, matching real CloudWatch.
func cwSetAlarmActionsEnabled(names []string, enabled bool) {
	for _, n := range names {
		if cwAlarms.Update(n, func(a *CWAlarm) { a.ActionsEnabled = enabled }) {
			cwRecordAlarmHistory(n, "MetricAlarm", "ConfigurationUpdate", "Alarm action state updated", "")
			continue
		}
		if cwCompositeAlarms.Update(n, func(a *CWCompositeAlarm) { a.ActionsEnabled = enabled }) {
			cwRecordAlarmHistory(n, "CompositeAlarm", "ConfigurationUpdate", "Alarm action state updated", "")
		}
	}
}

// cwSetAlarmState applies a manual StateValue to a metric or composite alarm
// (SetAlarmState). A metric alarm's manual state is held until the next
// evaluation re-derives it, matching real CloudWatch's temporary override.
func cwSetAlarmState(name, state, reason, reasonData string) bool {
	if cwAlarms.Update(name, func(a *CWAlarm) {
		a.ManualState = state
		a.ManualStateReason = reason
	}) {
		cwRecordAlarmHistory(name, "MetricAlarm", "StateUpdate", "Alarm updated from previous state to "+state, reasonData)
		ecsRequestServiceReconcileForAlarm(name)
		return true
	}
	if cwCompositeAlarms.Update(name, func(a *CWCompositeAlarm) {
		a.StateValue = state
		a.StateReason = reason
		a.UpdatedTimestamp = time.Now().UTC().Unix()
	}) {
		cwRecordAlarmHistory(name, "CompositeAlarm", "StateUpdate", "Alarm updated from previous state to "+state, reasonData)
		return true
	}
	return false
}

// cwAlarmsForMetric returns the metric alarms watching a given metric (the
// DescribeAlarmsForMetric reverse lookup). Dimensions, when supplied, must match
// exactly.
func cwAlarmsForMetric(namespace, metricName string, dims []CWDimension) []CWAlarm {
	out := make([]CWAlarm, 0)
	for _, a := range cwAlarms.List() {
		if a.Namespace != namespace || a.MetricName != metricName {
			continue
		}
		if len(dims) > 0 && metricsKey("", "", a.Dimensions) != metricsKey("", "", dims) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AlarmName < out[j].AlarmName })
	return out
}

// cwAlarmHistoryFor returns an alarm's history filtered by item type, ordered by
// ScanBy (TIMESTAMP_DESCENDING default).
func cwAlarmHistoryFor(name, itemType, scanBy string) []CWAlarmHistoryItem {
	var items []CWAlarmHistoryItem
	collect := func(h []CWAlarmHistoryItem) {
		for _, it := range h {
			if itemType != "" && it.HistoryItemType != itemType {
				continue
			}
			items = append(items, it)
		}
	}
	if name != "" {
		if h, ok := cwAlarmHistory.Get(name); ok {
			collect(h)
		}
	} else {
		for _, h := range cwAlarmHistory.List() {
			collect(h)
		}
	}
	desc := scanBy != "TimestampAscending"
	sort.Slice(items, func(i, j int) bool {
		if desc {
			return items[i].Timestamp > items[j].Timestamp
		}
		return items[i].Timestamp < items[j].Timestamp
	})
	return items
}

// ── awsJson1.0 surface ──────────────────────────────────────────────────────

func registerCloudWatchAlarmOpsJSON(r *sim.AWSRouter) {
	r.Register("GraniteServiceVersion20100801.EnableAlarmActions", handleCWJSONEnableAlarmActions)
	r.Register("GraniteServiceVersion20100801.DisableAlarmActions", handleCWJSONDisableAlarmActions)
	r.Register("GraniteServiceVersion20100801.SetAlarmState", handleCWJSONSetAlarmState)
	r.Register("GraniteServiceVersion20100801.DescribeAlarmHistory", handleCWJSONDescribeAlarmHistory)
	r.Register("GraniteServiceVersion20100801.DescribeAlarmsForMetric", handleCWJSONDescribeAlarmsForMetric)
	r.Register("GraniteServiceVersion20100801.PutCompositeAlarm", handleCWJSONPutCompositeAlarm)
}

func handleCWJSONEnableAlarmActions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmNames []string `json:"AlarmNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	cwSetAlarmActionsEnabled(req.AlarmNames, true)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONDisableAlarmActions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmNames []string `json:"AlarmNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	cwSetAlarmActionsEnabled(req.AlarmNames, false)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONSetAlarmState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName       string `json:"AlarmName"`
		StateValue      string `json:"StateValue"`
		StateReason     string `json:"StateReason"`
		StateReasonData string `json:"StateReasonData"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.AlarmName == "" || req.StateValue == "" || req.StateReason == "" {
		sim.AWSError(w, "MissingParameter", "AlarmName, StateValue and StateReason are required.", http.StatusBadRequest)
		return
	}
	if !cwSetAlarmState(req.AlarmName, req.StateValue, req.StateReason, req.StateReasonData) {
		sim.AWSErrorf(w, "ResourceNotFound", http.StatusBadRequest, "Alarm %s does not exist", req.AlarmName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONDescribeAlarmHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName       string `json:"AlarmName"`
		HistoryItemType string `json:"HistoryItemType"`
		ScanBy          string `json:"ScanBy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	items := cwAlarmHistoryFor(req.AlarmName, req.HistoryItemType, req.ScanBy)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"AlarmName":       it.AlarmName,
			"AlarmType":       it.AlarmType,
			"Timestamp":       float64(it.Timestamp),
			"HistoryItemType": it.HistoryItemType,
			"HistorySummary":  it.HistorySummary,
			"HistoryData":     it.HistoryData,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AlarmHistoryItems": out})
}

func handleCWJSONDescribeAlarmsForMetric(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string        `json:"Namespace"`
		MetricName string        `json:"MetricName"`
		Dimensions []CWDimension `json:"Dimensions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Namespace == "" || req.MetricName == "" {
		sim.AWSError(w, "MissingParameter", "Namespace and MetricName are required.", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	out := make([]map[string]any, 0)
	for _, a := range cwAlarmsForMetric(req.Namespace, req.MetricName, req.Dimensions) {
		out = append(out, cwJSONMetricAlarm(a, now))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"MetricAlarms": out})
}

func handleCWJSONPutCompositeAlarm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName               string    `json:"AlarmName"`
		AlarmRule               string    `json:"AlarmRule"`
		AlarmDescription        string    `json:"AlarmDescription"`
		ActionsEnabled          *bool     `json:"ActionsEnabled"`
		AlarmActions            []string  `json:"AlarmActions"`
		OKActions               []string  `json:"OKActions"`
		InsufficientDataActions []string  `json:"InsufficientDataActions"`
		Tags                    []cwTagKV `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.AlarmName == "" {
		sim.AWSError(w, "MissingParameter", "The parameter AlarmName is required.", http.StatusBadRequest)
		return
	}
	if req.AlarmRule == "" {
		sim.AWSError(w, "MissingParameter", "The parameter AlarmRule is required.", http.StatusBadRequest)
		return
	}
	cwPutCompositeAlarm(req.AlarmName, req.AlarmRule, req.AlarmDescription, req.ActionsEnabled,
		req.AlarmActions, req.OKActions, req.InsufficientDataActions, req.Tags)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// cwJSONMetricAlarm renders a metric alarm as the awsJson MetricAlarm shape,
// deriving its state from the live metric data (or honouring a manual override).
func cwJSONMetricAlarm(a CWAlarm, now time.Time) map[string]any {
	state, reason := cwAlarmEffectiveState(a)
	dims := make([]map[string]string, 0, len(a.Dimensions))
	for _, d := range a.Dimensions {
		dims = append(dims, map[string]string{"Name": d.Name, "Value": d.Value})
	}
	entry := map[string]any{
		"AlarmName":             a.AlarmName,
		"AlarmArn":              a.AlarmArn,
		"AlarmDescription":      a.AlarmDescription,
		"Namespace":             a.Namespace,
		"MetricName":            a.MetricName,
		"Dimensions":            dims,
		"Period":                a.Period,
		"EvaluationPeriods":     a.EvaluationPeriods,
		"Threshold":             cwJSONStat(a.Threshold),
		"ComparisonOperator":    a.ComparisonOperator,
		"TreatMissingData":      a.TreatMissingData,
		"ActionsEnabled":        a.ActionsEnabled,
		"AlarmActions":          a.AlarmActions,
		"StateValue":            state,
		"StateReason":           reason,
		"StateUpdatedTimestamp": float64(now.Unix()),
	}
	if a.ExtendedStatistic != "" {
		entry["ExtendedStatistic"] = a.ExtendedStatistic
	} else if a.Statistic != "" {
		entry["Statistic"] = a.Statistic
	}
	return entry
}

func cwPutCompositeAlarm(name, rule, desc string, actionsEnabled *bool, alarmActions, okActions, idActions []string, tags []cwTagKV) {
	enabled := true
	if actionsEnabled != nil {
		enabled = *actionsEnabled
	}
	existing, exists := cwCompositeAlarms.Get(name)
	ca := CWCompositeAlarm{
		AlarmName:               name,
		AlarmArn:                cwAlarmArn(name),
		AlarmRule:               rule,
		AlarmDescription:        desc,
		ActionsEnabled:          enabled,
		AlarmActions:            alarmActions,
		OKActions:               okActions,
		InsufficientDataActions: idActions,
		UpdatedTimestamp:        time.Now().UTC().Unix(),
	}
	if exists {
		ca.StateValue = existing.StateValue
		ca.StateReason = existing.StateReason
	}
	if len(tags) > 0 {
		ca.Tags = map[string]string{}
		for _, t := range tags {
			ca.Tags[t.Key] = t.Value
		}
	} else if exists {
		ca.Tags = existing.Tags
	}
	cwCompositeAlarms.Put(name, ca)
	cwRecordAlarmHistory(name, "CompositeAlarm", "ConfigurationUpdate", "Composite alarm updated", "")
}

// cwAlarmEffectiveState returns a metric alarm's state, preferring a manual
// SetAlarmState override over the metric-derived evaluation.
//
// The read derives the state rather than reporting the one the evaluator
// recorded, and both go through cwEvaluateAlarmState, so the two never disagree
// about what the metric data means. What the evaluator's recorded state adds is
// transition detection: it is the previous state a dispatch is decided against,
// which is why it is written only there. Evaluating from the read path instead
// would put action dispatch — and the ECS reconciliation that follows it — on
// every DescribeAlarms, which is work a read has no business doing.
func cwAlarmEffectiveState(a CWAlarm) (state, reason string) {
	if a.ManualState != "" {
		return a.ManualState, a.ManualStateReason
	}
	return cwEvaluateAlarmState(a)
}

// ── rpc-v2-cbor surface (Go SDK) ────────────────────────────────────────────

func registerCloudWatchAlarmOpsCBOR(srv *sim.Server) {
	cwCBOR(srv, "EnableAlarmActions", handleCWCBOREnableAlarmActions)
	cwCBOR(srv, "DisableAlarmActions", handleCWCBORDisableAlarmActions)
	cwCBOR(srv, "SetAlarmState", handleCWCBORSetAlarmState)
	cwCBOR(srv, "DescribeAlarmHistory", handleCWCBORDescribeAlarmHistory)
	cwCBOR(srv, "DescribeAlarmsForMetric", handleCWCBORDescribeAlarmsForMetric)
	cwCBOR(srv, "PutCompositeAlarm", handleCWCBORPutCompositeAlarm)
}

// cwCBOR mounts a CloudWatch rpc-v2-cbor operation under the shared
// GraniteServiceVersion20100801 service path, wrapped in CloudTrail recording
// like the existing alarm/metric cbor routes.
func cwCBOR(srv *sim.Server, op string, h http.HandlerFunc) {
	srv.HandleFunc("POST /service/GraniteServiceVersion20100801/operation/"+op,
		cloudTrailRecordedREST(op, "monitoring.amazonaws.com", nil, cloudWatchCBORAuthorized(op, h)))
}

// cloudWatchCBORAuthorized applies the same SigV4 and call-time IAM contract
// as the query-protocol Amazon CloudWatch endpoint. RPCv2-CBOR is path-routed
// rather than Action-routed, so the operation name comes from the modeled
// route.
func cloudWatchCBORAuthorized(op string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		// Preserve the established unsigned-request passthrough for direct
		// in-process consumers. Official SDK and CLI requests are signed;
		// signed requests are authenticated and registered principals are
		// authorized exactly as on the query-protocol route.
		if iamAccessKeyIDFromRequest(r) != "" {
			if _, serr := sigv4Verify(r, body); serr != nil {
				jsonCode, _ := sigv4ErrorCodes(serr.kind)
				cwWriteCBORError(w, jsonCode, serr.message, http.StatusForbidden)
				return
			}
			allowed, principalARN, registered := iamAuthorize(r, "cloudwatch:"+op, "*")
			if registered && !allowed {
				cwWriteCBORError(w, "AccessDenied",
					fmt.Sprintf("User: %s is not authorized to perform: cloudwatch:%s", principalARN, op),
					http.StatusForbidden)
				return
			}
		}
		h(w, r)
	}
}

// cwReadCBOR reads and CBOR-decodes a request body into v, writing the protocol
// error and returning false on failure.
func cwReadCBOR(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return false
	}
	if err := cbor.Unmarshal(raw, v); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return false
	}
	return true
}

func handleCWCBOREnableAlarmActions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmNames []string `cbor:"AlarmNames"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	cwSetAlarmActionsEnabled(req.AlarmNames, true)
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORDisableAlarmActions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmNames []string `cbor:"AlarmNames"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	cwSetAlarmActionsEnabled(req.AlarmNames, false)
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORSetAlarmState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName       string `cbor:"AlarmName"`
		StateValue      string `cbor:"StateValue"`
		StateReason     string `cbor:"StateReason"`
		StateReasonData string `cbor:"StateReasonData"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.AlarmName == "" || req.StateValue == "" || req.StateReason == "" {
		cwWriteCBORError(w, "MissingParameter", "AlarmName, StateValue and StateReason are required.", http.StatusBadRequest)
		return
	}
	if !cwSetAlarmState(req.AlarmName, req.StateValue, req.StateReason, req.StateReasonData) {
		cwWriteCBORErrorf(w, "ResourceNotFound", http.StatusBadRequest, "Alarm %s does not exist", req.AlarmName)
		return
	}
	cwWriteCBOR(w, map[string]any{})
}

type cborAlarmHistoryItem struct {
	AlarmName       string    `cbor:"AlarmName"`
	AlarmType       string    `cbor:"AlarmType"`
	Timestamp       time.Time `cbor:"Timestamp"`
	HistoryItemType string    `cbor:"HistoryItemType"`
	HistorySummary  string    `cbor:"HistorySummary"`
	HistoryData     string    `cbor:"HistoryData"`
}

func handleCWCBORDescribeAlarmHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName       string `cbor:"AlarmName"`
		HistoryItemType string `cbor:"HistoryItemType"`
		ScanBy          string `cbor:"ScanBy"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	items := cwAlarmHistoryFor(req.AlarmName, req.HistoryItemType, req.ScanBy)
	out := make([]cborAlarmHistoryItem, 0, len(items))
	for _, it := range items {
		out = append(out, cborAlarmHistoryItem{
			AlarmName:       it.AlarmName,
			AlarmType:       it.AlarmType,
			Timestamp:       time.Unix(it.Timestamp, 0).UTC(),
			HistoryItemType: it.HistoryItemType,
			HistorySummary:  it.HistorySummary,
			HistoryData:     it.HistoryData,
		})
	}
	cwWriteCBOR(w, map[string]any{"AlarmHistoryItems": out})
}

func handleCWCBORDescribeAlarmsForMetric(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string        `cbor:"Namespace"`
		MetricName string        `cbor:"MetricName"`
		Dimensions []CWDimension `cbor:"Dimensions"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.Namespace == "" || req.MetricName == "" {
		cwWriteCBORError(w, "MissingParameter", "Namespace and MetricName are required.", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	alarms := make([]cborMetricAlarm, 0)
	for _, a := range cwAlarmsForMetric(req.Namespace, req.MetricName, req.Dimensions) {
		state, reason := cwAlarmEffectiveState(a)
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
	cwWriteCBOR(w, map[string]any{"MetricAlarms": alarms})
}

func handleCWCBORPutCompositeAlarm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName               string    `cbor:"AlarmName"`
		AlarmRule               string    `cbor:"AlarmRule"`
		AlarmDescription        string    `cbor:"AlarmDescription"`
		ActionsEnabled          *bool     `cbor:"ActionsEnabled"`
		AlarmActions            []string  `cbor:"AlarmActions"`
		OKActions               []string  `cbor:"OKActions"`
		InsufficientDataActions []string  `cbor:"InsufficientDataActions"`
		Tags                    []cwTagKV `cbor:"Tags"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.AlarmName == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter AlarmName is required.", http.StatusBadRequest)
		return
	}
	if req.AlarmRule == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter AlarmRule is required.", http.StatusBadRequest)
		return
	}
	cwPutCompositeAlarm(req.AlarmName, req.AlarmRule, req.AlarmDescription, req.ActionsEnabled,
		req.AlarmActions, req.OKActions, req.InsufficientDataActions, req.Tags)
	cwWriteCBOR(w, map[string]any{})
}

// ── query surface (older aws CLI) ───────────────────────────────────────────

func registerCloudWatchAlarmOpsQuery(r *sim.AWSQueryRouter) {
	r.Register("EnableAlarmActions", handleCWQueryEnableAlarmActions)
	r.Register("DisableAlarmActions", handleCWQueryDisableAlarmActions)
	r.Register("SetAlarmState", handleCWQuerySetAlarmState)
	r.Register("DescribeAlarmHistory", handleCWQueryDescribeAlarmHistory)
	r.Register("DescribeAlarmsForMetric", handleCWQueryDescribeAlarmsForMetric)
	r.Register("PutCompositeAlarm", handleCWQueryPutCompositeAlarm)
}

func handleCWQueryEnableAlarmActions(w http.ResponseWriter, r *http.Request) {
	cwSetAlarmActionsEnabled(cwQueryStringList(r, "AlarmNames"), true)
	cwQueryEmptyResponse(w, "EnableAlarmActions")
}

func handleCWQueryDisableAlarmActions(w http.ResponseWriter, r *http.Request) {
	cwSetAlarmActionsEnabled(cwQueryStringList(r, "AlarmNames"), false)
	cwQueryEmptyResponse(w, "DisableAlarmActions")
}

func handleCWQuerySetAlarmState(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("AlarmName")
	state := r.FormValue("StateValue")
	reason := r.FormValue("StateReason")
	if name == "" || state == "" || reason == "" {
		cwQueryError(w, "MissingParameter", "AlarmName, StateValue and StateReason are required.")
		return
	}
	if !cwSetAlarmState(name, state, reason, r.FormValue("StateReasonData")) {
		cwQueryError(w, "ResourceNotFound", "Alarm "+name+" does not exist")
		return
	}
	cwQueryEmptyResponse(w, "SetAlarmState")
}

func handleCWQueryDescribeAlarmHistory(w http.ResponseWriter, r *http.Request) {
	items := cwAlarmHistoryFor(r.FormValue("AlarmName"), r.FormValue("HistoryItemType"), r.FormValue("ScanBy"))
	var members []byte
	for _, it := range items {
		members = append(members, []byte("<member>")...)
		members = cwQueryAppendf(members, "<AlarmName>%s</AlarmName><AlarmType>%s</AlarmType>", xmlEscape(it.AlarmName), xmlEscape(it.AlarmType))
		members = cwQueryAppendf(members, "<Timestamp>%s</Timestamp>", time.Unix(it.Timestamp, 0).UTC().Format(time.RFC3339))
		members = cwQueryAppendf(members, "<HistoryItemType>%s</HistoryItemType><HistorySummary>%s</HistorySummary>",
			xmlEscape(it.HistoryItemType), xmlEscape(it.HistorySummary))
		if it.HistoryData != "" {
			members = cwQueryAppendf(members, "<HistoryData>%s</HistoryData>", xmlEscape(it.HistoryData))
		}
		members = append(members, []byte("</member>")...)
	}
	cwQueryResult(w, "DescribeAlarmHistory", "<AlarmHistoryItems>"+string(members)+"</AlarmHistoryItems>")
}

func handleCWQueryDescribeAlarmsForMetric(w http.ResponseWriter, r *http.Request) {
	ns := r.FormValue("Namespace")
	mn := r.FormValue("MetricName")
	if ns == "" || mn == "" {
		cwQueryError(w, "MissingParameter", "Namespace and MetricName are required.")
		return
	}
	now := time.Now().UTC()
	members := cwQueryMetricAlarmsXML(cwAlarmsForMetric(ns, mn, cwQueryDimensions(r, "Dimensions")), now)
	cwQueryResult(w, "DescribeAlarmsForMetric", "<MetricAlarms>"+members+"</MetricAlarms>")
}

func handleCWQueryPutCompositeAlarm(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("AlarmName")
	rule := r.FormValue("AlarmRule")
	if name == "" {
		cwQueryError(w, "MissingParameter", "The parameter AlarmName is required.")
		return
	}
	if rule == "" {
		cwQueryError(w, "MissingParameter", "The parameter AlarmRule is required.")
		return
	}
	var enabled *bool
	if v := r.FormValue("ActionsEnabled"); v != "" {
		b := v == "true"
		enabled = &b
	}
	cwPutCompositeAlarm(name, rule, r.FormValue("AlarmDescription"), enabled,
		cwQueryStringList(r, "AlarmActions"), cwQueryStringList(r, "OKActions"),
		cwQueryStringList(r, "InsufficientDataActions"), nil)
	cwQueryEmptyResponse(w, "PutCompositeAlarm")
}
