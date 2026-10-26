package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/fxamacker/cbor/v2"
)

// PutLogAlarm creates the alarm and the service-managed CloudWatch Logs
// scheduled query behind it; DescribeAlarms reads it back as a LogAlarm.
//
// A read derives the state from the log data present: each of the latest
// QueryResultsToEvaluate schedule windows runs the query, with the
// AggregationExpression appended as its `stats` stage, through the Insights
// engine, and the alarm is in ALARM when QueryResultsToAlarm windows breach.

type CWLogAlarm struct {
	AlarmName               string                 `json:"AlarmName"`
	AlarmArn                string                 `json:"AlarmArn"`
	AlarmDescription        string                 `json:"AlarmDescription,omitempty"`
	QueryString             string                 `json:"QueryString"`
	LogGroupIdentifiers     []string               `json:"LogGroupIdentifiers,omitempty"`
	QueryARN                string                 `json:"QueryARN"`
	ScheduledQueryRoleARN   string                 `json:"ScheduledQueryRoleARN"`
	ScheduleExpression      string                 `json:"ScheduleExpression"`
	StartTimeOffset         int64                  `json:"StartTimeOffset"`
	EndTimeOffset           int64                  `json:"EndTimeOffset"`
	AggregationExpression   string                 `json:"AggregationExpression"`
	QueryResultsToEvaluate  int32                  `json:"QueryResultsToEvaluate"`
	QueryResultsToAlarm     int32                  `json:"QueryResultsToAlarm"`
	Threshold               float64                `json:"Threshold"`
	ComparisonOperator      string                 `json:"ComparisonOperator"`
	TreatMissingData        string                 `json:"TreatMissingData,omitempty"`
	ActionsEnabled          bool                   `json:"ActionsEnabled"`
	OKActions               []string               `json:"OKActions,omitempty"`
	AlarmActions            []string               `json:"AlarmActions,omitempty"`
	InsufficientDataActions []string               `json:"InsufficientDataActions,omitempty"`
	ActionLogLineCount      *int32                 `json:"ActionLogLineCount,omitempty"`
	ActionLogLineRoleArn    string                 `json:"ActionLogLineRoleArn,omitempty"`
	ActionConfiguration     *cwActionConfiguration `json:"ActionConfiguration,omitempty"`

	ConfigurationUpdated int64             `json:"-"`
	Tags                 map[string]string `json:"-"`
}

var cwLogAlarms sim.Store[CWLogAlarm]

// CloudWatch names the managed query after the alarm it serves.
func cwLogAlarmScheduledQueryName(alarmName string) string {
	return "AlarmManagedQuery-" + alarmName
}

func registerCloudWatchLogAlarmsJSON(r *AWSRouter) {
	r.Register("GraniteServiceVersion20100801.PutLogAlarm", handleCWJSONPutLogAlarm)
}

func registerCloudWatchLogAlarmsCBOR(srv *sim.Server) {
	cwCBOR(srv, "PutLogAlarm", handleCWCBORPutLogAlarm)
}

// Shared by the awsJson1.0 and rpc-v2-cbor surfaces.
type cwPutLogAlarmInput struct {
	AlarmName                   string `json:"AlarmName" cbor:"AlarmName"`
	AlarmDescription            string `json:"AlarmDescription" cbor:"AlarmDescription"`
	ScheduledQueryConfiguration *struct {
		QueryString           string   `json:"QueryString" cbor:"QueryString"`
		LogGroupIdentifiers   []string `json:"LogGroupIdentifiers" cbor:"LogGroupIdentifiers"`
		QueryARN              string   `json:"QueryARN" cbor:"QueryARN"`
		ScheduledQueryRoleARN string   `json:"ScheduledQueryRoleARN" cbor:"ScheduledQueryRoleARN"`
		ScheduleConfiguration *struct {
			ScheduleExpression string `json:"ScheduleExpression" cbor:"ScheduleExpression"`
			StartTimeOffset    int64  `json:"StartTimeOffset" cbor:"StartTimeOffset"`
			EndTimeOffset      int64  `json:"EndTimeOffset" cbor:"EndTimeOffset"`
		} `json:"ScheduleConfiguration" cbor:"ScheduleConfiguration"`
		AggregationExpression string    `json:"AggregationExpression" cbor:"AggregationExpression"`
		Tags                  []cwTagKV `json:"Tags" cbor:"Tags"`
	} `json:"ScheduledQueryConfiguration" cbor:"ScheduledQueryConfiguration"`
	ActionLogLineCount      *int32                 `json:"ActionLogLineCount" cbor:"ActionLogLineCount"`
	ActionLogLineRoleArn    string                 `json:"ActionLogLineRoleArn" cbor:"ActionLogLineRoleArn"`
	ActionsEnabled          *bool                  `json:"ActionsEnabled" cbor:"ActionsEnabled"`
	OKActions               []string               `json:"OKActions" cbor:"OKActions"`
	AlarmActions            []string               `json:"AlarmActions" cbor:"AlarmActions"`
	InsufficientDataActions []string               `json:"InsufficientDataActions" cbor:"InsufficientDataActions"`
	QueryResultsToEvaluate  *int32                 `json:"QueryResultsToEvaluate" cbor:"QueryResultsToEvaluate"`
	QueryResultsToAlarm     *int32                 `json:"QueryResultsToAlarm" cbor:"QueryResultsToAlarm"`
	Threshold               *float64               `json:"Threshold" cbor:"Threshold"`
	ComparisonOperator      string                 `json:"ComparisonOperator" cbor:"ComparisonOperator"`
	TreatMissingData        string                 `json:"TreatMissingData" cbor:"TreatMissingData"`
	Tags                    []cwTagKV              `json:"Tags" cbor:"Tags"`
	ActionConfiguration     *cwActionConfiguration `json:"ActionConfiguration" cbor:"ActionConfiguration"`
}

// ok=false means the request is rejected with code, msg and status.
func cwValidateLogAlarm(req *cwPutLogAlarmInput) (code, msg string, status int, ok bool) {
	switch {
	case req.AlarmName == "":
		return "MissingParameter", "The parameter AlarmName is required.", http.StatusBadRequest, false
	case req.ScheduledQueryConfiguration == nil:
		return "MissingParameter", "The parameter ScheduledQueryConfiguration is required.", http.StatusBadRequest, false
	case req.ScheduledQueryConfiguration.QueryString == "":
		return "MissingParameter", "The parameter ScheduledQueryConfiguration.QueryString is required.", http.StatusBadRequest, false
	case req.ScheduledQueryConfiguration.ScheduledQueryRoleARN == "":
		return "MissingParameter", "The parameter ScheduledQueryConfiguration.ScheduledQueryRoleARN is required.", http.StatusBadRequest, false
	case req.ScheduledQueryConfiguration.AggregationExpression == "":
		return "MissingParameter", "The parameter ScheduledQueryConfiguration.AggregationExpression is required.", http.StatusBadRequest, false
	case req.ScheduledQueryConfiguration.ScheduleConfiguration == nil:
		return "MissingParameter", "The parameter ScheduledQueryConfiguration.ScheduleConfiguration is required.", http.StatusBadRequest, false
	case req.ScheduledQueryConfiguration.ScheduleConfiguration.ScheduleExpression == "":
		return "MissingParameter", "The parameter ScheduledQueryConfiguration.ScheduleConfiguration.ScheduleExpression is required.", http.StatusBadRequest, false
	case req.QueryResultsToEvaluate == nil || *req.QueryResultsToEvaluate < 1:
		return "InvalidParameterValue", "QueryResultsToEvaluate must be at least 1.", http.StatusBadRequest, false
	case req.QueryResultsToAlarm == nil || *req.QueryResultsToAlarm < 1:
		return "InvalidParameterValue", "QueryResultsToAlarm must be at least 1.", http.StatusBadRequest, false
	case *req.QueryResultsToAlarm > *req.QueryResultsToEvaluate:
		return "InvalidParameterValue", "QueryResultsToAlarm must not exceed QueryResultsToEvaluate.", http.StatusBadRequest, false
	case req.Threshold == nil:
		return "MissingParameter", "The parameter Threshold is required.", http.StatusBadRequest, false
	case req.ComparisonOperator == "":
		return "MissingParameter", "The parameter ComparisonOperator is required.", http.StatusBadRequest, false
	}
	sc := req.ScheduledQueryConfiguration.ScheduleConfiguration
	if sc.StartTimeOffset < 1 {
		return "InvalidParameterValue", "StartTimeOffset must be at least 1 second.", http.StatusBadRequest, false
	}
	if sc.EndTimeOffset < 0 || sc.EndTimeOffset >= sc.StartTimeOffset {
		return "InvalidParameterValue", "EndTimeOffset must be non-negative and less than StartTimeOffset.", http.StatusBadRequest, false
	}
	// Alarm names are unique across alarm types.
	if _, taken := cwAlarms.Get(req.AlarmName); taken {
		return "ResourceConflict", "A metric alarm named " + req.AlarmName + " already exists.", http.StatusConflict, false
	}
	if _, taken := cwCompositeAlarms.Get(req.AlarmName); taken {
		return "ResourceConflict", "A composite alarm named " + req.AlarmName + " already exists.", http.StatusConflict, false
	}
	if code, msg, ok := cwValidateActionConfiguration(req.ActionConfiguration); !ok {
		return code, msg, http.StatusBadRequest, false
	}
	return "", "", 0, true
}

func cwApplyPutLogAlarm(req *cwPutLogAlarmInput) CWLogAlarm {
	sq := req.ScheduledQueryConfiguration
	sc := sq.ScheduleConfiguration
	actionsEnabled := true
	if req.ActionsEnabled != nil {
		actionsEnabled = *req.ActionsEnabled
	}

	queryName := cwLogAlarmScheduledQueryName(req.AlarmName)
	queryArn := cwScheduledQueryArn(queryName)
	nowMillis := time.Now().UTC().UnixMilli()
	existingQuery, hadQuery := cwScheduledQ.Get(queryArn)
	created := nowMillis
	if hadQuery {
		created = existingQuery.CreationTime
	}
	cwScheduledQ.Put(queryArn, CWScheduledQuery{
		ScheduledQueryArn:   queryArn,
		Name:                queryName,
		Description:         "Service-managed scheduled query for CloudWatch log alarm " + req.AlarmName,
		QueryLanguage:       "CWLI",
		QueryString:         sq.QueryString,
		LogGroupIdentifiers: sq.LogGroupIdentifiers,
		ScheduleExpression:  sc.ScheduleExpression,
		StartTimeOffset:     sc.StartTimeOffset,
		ExecutionRoleArn:    sq.ScheduledQueryRoleARN,
		State:               "ENABLED",
		CreationTime:        created,
		LastUpdatedTime:     nowMillis,
	})

	alarm := CWLogAlarm{
		AlarmName:               req.AlarmName,
		AlarmArn:                cwAlarmArn(req.AlarmName),
		AlarmDescription:        req.AlarmDescription,
		QueryString:             sq.QueryString,
		LogGroupIdentifiers:     sq.LogGroupIdentifiers,
		QueryARN:                queryArn,
		ScheduledQueryRoleARN:   sq.ScheduledQueryRoleARN,
		ScheduleExpression:      sc.ScheduleExpression,
		StartTimeOffset:         sc.StartTimeOffset,
		EndTimeOffset:           sc.EndTimeOffset,
		AggregationExpression:   sq.AggregationExpression,
		QueryResultsToEvaluate:  *req.QueryResultsToEvaluate,
		QueryResultsToAlarm:     *req.QueryResultsToAlarm,
		Threshold:               *req.Threshold,
		ComparisonOperator:      req.ComparisonOperator,
		TreatMissingData:        req.TreatMissingData,
		ActionsEnabled:          actionsEnabled,
		OKActions:               req.OKActions,
		AlarmActions:            req.AlarmActions,
		InsufficientDataActions: req.InsufficientDataActions,
		ActionLogLineCount:      req.ActionLogLineCount,
		ActionLogLineRoleArn:    req.ActionLogLineRoleArn,
		ActionConfiguration:     req.ActionConfiguration,
		ConfigurationUpdated:    time.Now().UTC().Unix(),
	}
	tags := req.Tags
	if len(tags) == 0 {
		tags = sq.Tags
	}
	if len(tags) > 0 {
		alarm.Tags = map[string]string{}
		for _, t := range tags {
			alarm.Tags[t.Key] = t.Value
		}
	} else if existing, ok := cwLogAlarms.Get(req.AlarmName); ok {
		alarm.Tags = existing.Tags
	}
	cwLogAlarms.Put(req.AlarmName, alarm)
	return alarm
}

func handleCWJSONPutLogAlarm(w http.ResponseWriter, r *http.Request) {
	var req cwPutLogAlarmInput
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if code, msg, status, ok := cwValidateLogAlarm(&req); !ok {
		AWSError(w, code, msg, status)
		return
	}
	cwApplyPutLogAlarm(&req)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWCBORPutLogAlarm(w http.ResponseWriter, r *http.Request) {
	raw, err := cwReadBody(r)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	var req cwPutLogAlarmInput
	if err := cbor.Unmarshal(raw, &req); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "Invalid CBOR request", http.StatusBadRequest)
		return
	}
	if code, msg, status, ok := cwValidateLogAlarm(&req); !ok {
		cwWriteCBORError(w, code, msg, status)
		return
	}
	cwApplyPutLogAlarm(&req)
	cwWriteCBOR(w, map[string]any{})
}

func cwListLogAlarms(names []string) []CWLogAlarm {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	out := make([]CWLogAlarm, 0)
	for _, a := range cwLogAlarms.List() {
		if len(want) > 0 && !want[a.AlarmName] {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AlarmName < out[j].AlarmName })
	return out
}

// An unparsable expression yields 0, which the evaluator reports as an
// evaluation failure rather than guessing a period.
func cwScheduleIntervalSeconds(expr string) int64 {
	s := strings.TrimSpace(expr)
	if !strings.HasPrefix(strings.ToLower(s), "rate(") || !strings.HasSuffix(s, ")") {
		return 0
	}
	inner := strings.TrimSpace(s[len("rate(") : len(s)-1])
	var n int64
	var unit string
	if _, err := fmt.Sscanf(inner, "%d %s", &n, &unit); err != nil || n <= 0 {
		return 0
	}
	switch strings.TrimSuffix(strings.ToLower(unit), "s") {
	case "minute":
		return n * 60
	case "hour":
		return n * 3600
	case "day":
		return n * 86400
	default:
		return 0
	}
}

// found=false means the window produced no row: a missing datapoint.
func cwLogAlarmWindowValue(a CWLogAlarm, startSec, endSec int64) (value float64, found bool) {
	records, _ := cwGatherRecords(a.LogGroupIdentifiers, startSec, endSec)
	// The scheduled query reduces a window's rows by appending the
	// AggregationExpression as `stats`.
	query := a.QueryString + " | stats " + a.AggregationExpression
	rows, err := cwRunInsightsQuery(query, records, 0)
	if err != nil || len(rows) == 0 {
		return 0, false
	}
	for _, cell := range rows[0] {
		field, _ := cell["field"].(string)
		if strings.HasPrefix(field, "@") {
			continue
		}
		raw, _ := cell["value"].(string)
		var f float64
		if _, err := fmt.Sscanf(raw, "%g", &f); err != nil {
			continue
		}
		return f, true
	}
	return 0, false
}

func cwEvaluateLogAlarmState(a CWLogAlarm) (state, reason, evaluationState string) {
	interval := cwScheduleIntervalSeconds(a.ScheduleExpression)
	if interval <= 0 {
		return "INSUFFICIENT_DATA",
			"Insufficient Data: the scheduled query's schedule expression could not be evaluated",
			"EVALUATION_FAILURE"
	}
	now := time.Now().UTC().Unix()
	evaluated, breaching, missing := 0, 0, 0
	for i := int64(0); i < int64(a.QueryResultsToEvaluate); i++ {
		runAt := now - i*interval
		start := runAt - a.StartTimeOffset
		end := runAt - a.EndTimeOffset
		v, ok := cwLogAlarmWindowValue(a, start, end)
		if !ok {
			missing++
			continue
		}
		evaluated++
		if cwAlarmBreaches(v, a.Threshold, a.ComparisonOperator) {
			breaching++
		}
	}

	if evaluated == 0 {
		switch a.TreatMissingData {
		case "notBreaching":
			return "OK", "Threshold not crossed: missing data treated as not breaching", ""
		case "breaching":
			return "ALARM", "Threshold crossed: missing data treated as breaching", ""
		default:
			return "INSUFFICIENT_DATA", "Insufficient Data: the scheduled query returned no results", ""
		}
	}
	if breaching >= int(a.QueryResultsToAlarm) {
		return "ALARM",
			fmt.Sprintf("Threshold Crossed: %d out of %d query results were %s the threshold (%g)",
				breaching, a.QueryResultsToEvaluate, a.ComparisonOperator, a.Threshold),
			cwLogAlarmPartial(missing)
	}
	return "OK", "Threshold not crossed", cwLogAlarmPartial(missing)
}

// PARTIAL_DATA when some evaluated windows produced no result.
func cwLogAlarmPartial(missing int) string {
	if missing > 0 {
		return "PARTIAL_DATA"
	}
	return ""
}

func cwLogAlarmScheduledQueryConfig(a CWLogAlarm) map[string]any {
	cfg := map[string]any{
		"QueryString":           a.QueryString,
		"QueryARN":              a.QueryARN,
		"ScheduledQueryRoleARN": a.ScheduledQueryRoleARN,
		"AggregationExpression": a.AggregationExpression,
		"ScheduleConfiguration": map[string]any{
			"ScheduleExpression": a.ScheduleExpression,
			"StartTimeOffset":    a.StartTimeOffset,
			"EndTimeOffset":      a.EndTimeOffset,
		},
	}
	if len(a.LogGroupIdentifiers) > 0 {
		cfg["LogGroupIdentifiers"] = a.LogGroupIdentifiers
	}
	return cfg
}

// awsJson1.0 timestamps are epoch-second numbers.
func cwJSONLogAlarm(a CWLogAlarm, now time.Time) map[string]any {
	state, reason, evalState := cwEvaluateLogAlarmState(a)
	out := map[string]any{
		"AlarmName":                          a.AlarmName,
		"AlarmArn":                           a.AlarmArn,
		"AlarmDescription":                   a.AlarmDescription,
		"AlarmConfigurationUpdatedTimestamp": float64(a.ConfigurationUpdated),
		"ActionsEnabled":                     a.ActionsEnabled,
		"OKActions":                          a.OKActions,
		"AlarmActions":                       a.AlarmActions,
		"InsufficientDataActions":            a.InsufficientDataActions,
		"StateValue":                         state,
		"StateReason":                        reason,
		"StateUpdatedTimestamp":              float64(now.Unix()),
		"StateTransitionedTimestamp":         float64(now.Unix()),
		"ScheduledQueryConfiguration":        cwLogAlarmScheduledQueryConfig(a),
		"QueryResultsToEvaluate":             a.QueryResultsToEvaluate,
		"QueryResultsToAlarm":                a.QueryResultsToAlarm,
		"Threshold":                          a.Threshold,
		"ComparisonOperator":                 a.ComparisonOperator,
	}
	if a.TreatMissingData != "" {
		out["TreatMissingData"] = a.TreatMissingData
	}
	if evalState != "" {
		out["EvaluationState"] = evalState
	}
	if a.ActionLogLineCount != nil {
		out["ActionLogLineCount"] = *a.ActionLogLineCount
	}
	if a.ActionLogLineRoleArn != "" {
		out["ActionLogLineRoleArn"] = a.ActionLogLineRoleArn
	}
	if a.ActionConfiguration != nil {
		out["ActionConfiguration"] = a.ActionConfiguration
	}
	return out
}

// rpc-v2-cbor timestamps are CBOR times.
type cborLogAlarm struct {
	AlarmName                          string                 `cbor:"AlarmName"`
	AlarmArn                           string                 `cbor:"AlarmArn"`
	AlarmDescription                   string                 `cbor:"AlarmDescription,omitempty"`
	AlarmConfigurationUpdatedTimestamp time.Time              `cbor:"AlarmConfigurationUpdatedTimestamp"`
	ActionsEnabled                     bool                   `cbor:"ActionsEnabled"`
	OKActions                          []string               `cbor:"OKActions,omitempty"`
	AlarmActions                       []string               `cbor:"AlarmActions,omitempty"`
	InsufficientDataActions            []string               `cbor:"InsufficientDataActions,omitempty"`
	StateValue                         string                 `cbor:"StateValue"`
	StateReason                        string                 `cbor:"StateReason,omitempty"`
	StateUpdatedTimestamp              time.Time              `cbor:"StateUpdatedTimestamp"`
	StateTransitionedTimestamp         time.Time              `cbor:"StateTransitionedTimestamp"`
	ScheduledQueryConfiguration        map[string]any         `cbor:"ScheduledQueryConfiguration"`
	QueryResultsToEvaluate             int32                  `cbor:"QueryResultsToEvaluate"`
	QueryResultsToAlarm                int32                  `cbor:"QueryResultsToAlarm"`
	Threshold                          float64                `cbor:"Threshold"`
	ComparisonOperator                 string                 `cbor:"ComparisonOperator"`
	TreatMissingData                   string                 `cbor:"TreatMissingData,omitempty"`
	EvaluationState                    string                 `cbor:"EvaluationState,omitempty"`
	ActionLogLineCount                 *int32                 `cbor:"ActionLogLineCount,omitempty"`
	ActionLogLineRoleArn               string                 `cbor:"ActionLogLineRoleArn,omitempty"`
	ActionConfiguration                *cwActionConfiguration `cbor:"ActionConfiguration,omitempty"`
}

func cborLogAlarmOf(a CWLogAlarm, now time.Time) cborLogAlarm {
	state, reason, evalState := cwEvaluateLogAlarmState(a)
	return cborLogAlarm{
		AlarmName:                          a.AlarmName,
		AlarmArn:                           a.AlarmArn,
		AlarmDescription:                   a.AlarmDescription,
		AlarmConfigurationUpdatedTimestamp: time.Unix(a.ConfigurationUpdated, 0).UTC(),
		ActionsEnabled:                     a.ActionsEnabled,
		OKActions:                          a.OKActions,
		AlarmActions:                       a.AlarmActions,
		InsufficientDataActions:            a.InsufficientDataActions,
		StateValue:                         state,
		StateReason:                        reason,
		StateUpdatedTimestamp:              now,
		StateTransitionedTimestamp:         now,
		ScheduledQueryConfiguration:        cwLogAlarmScheduledQueryConfig(a),
		QueryResultsToEvaluate:             a.QueryResultsToEvaluate,
		QueryResultsToAlarm:                a.QueryResultsToAlarm,
		Threshold:                          a.Threshold,
		ComparisonOperator:                 a.ComparisonOperator,
		TreatMissingData:                   a.TreatMissingData,
		EvaluationState:                    evalState,
		ActionLogLineCount:                 a.ActionLogLineCount,
		ActionLogLineRoleArn:               a.ActionLogLineRoleArn,
		ActionConfiguration:                a.ActionConfiguration,
	}
}

func cwDeleteLogAlarm(name string) {
	if _, ok := cwLogAlarms.Get(name); !ok {
		return
	}
	cwLogAlarms.Delete(name)
	cwScheduledQ.Delete(cwScheduledQueryArn(cwLogAlarmScheduledQueryName(name)))
}
