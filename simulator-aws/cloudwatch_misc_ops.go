package main

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudWatch GetMetricData (awsJson / query — the cbor surface lives in
// cloudwatch_metrics.go), alarm mute rules, and the cross-resource tagging API
// (TagResource / UntagResource / ListTagsForResource over alarms, metric
// streams, and insight rules).

// CWMuteSchedule mirrors the Schedule shape on a mute rule's Rule.
type CWMuteSchedule struct {
	Expression string `json:"Expression,omitempty" cbor:"Expression,omitempty"`
	Duration   string `json:"Duration,omitempty" cbor:"Duration,omitempty"`
	Timezone   string `json:"Timezone,omitempty" cbor:"Timezone,omitempty"`
}

// CWAlarmMuteRule holds an alarm mute rule.
type CWAlarmMuteRule struct {
	Name        string            `json:"Name"`
	Arn         string            `json:"Arn"`
	Description string            `json:"Description,omitempty"`
	Schedule    CWMuteSchedule    `json:"Schedule"`
	MuteType    string            `json:"MuteType"`
	AlarmNames  []string          `json:"AlarmNames,omitempty"`
	StartDate   int64             `json:"-"`
	ExpireDate  int64             `json:"-"`
	Updated     int64             `json:"-"`
	Tags        map[string]string `json:"-"`
}

var cwAlarmMuteRules sim.Store[CWAlarmMuteRule]

func cwAlarmMuteRuleArn(name string) string {
	return "arn:aws:cloudwatch:" + awsRegion() + ":" + awsAccountID() + ":alarm-mute-rule:" + name
}

// cwMuteRuleStatus derives the rule's lifecycle status from its expiry: EXPIRED
// once past ExpireDate, ACTIVE when the window has started, otherwise SCHEDULED.
func cwMuteRuleStatus(r CWAlarmMuteRule) string {
	now := time.Now().UTC().Unix()
	if r.ExpireDate != 0 && now >= r.ExpireDate {
		return "EXPIRED"
	}
	if r.StartDate != 0 && now >= r.StartDate {
		return "ACTIVE"
	}
	return "SCHEDULED"
}

func cwListAlarmMuteRules(alarmName string) []CWAlarmMuteRule {
	out := make([]CWAlarmMuteRule, 0)
	for _, r := range cwAlarmMuteRules.List() {
		if alarmName != "" {
			matched := false
			for _, n := range r.AlarmNames {
				if n == alarmName {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── GetMetricData (awsJson) ─────────────────────────────────────────────────

func registerCloudWatchMiscJSON(r *sim.AWSRouter) {
	r.Register("GraniteServiceVersion20100801.GetMetricData", handleCWJSONGetMetricData)
	r.Register("GraniteServiceVersion20100801.PutAlarmMuteRule", handleCWJSONPutAlarmMuteRule)
	r.Register("GraniteServiceVersion20100801.GetAlarmMuteRule", handleCWJSONGetAlarmMuteRule)
	r.Register("GraniteServiceVersion20100801.DeleteAlarmMuteRule", handleCWJSONDeleteAlarmMuteRule)
	r.Register("GraniteServiceVersion20100801.ListAlarmMuteRules", handleCWJSONListAlarmMuteRules)
	r.Register("GraniteServiceVersion20100801.TagResource", handleCWJSONTagResource)
	r.Register("GraniteServiceVersion20100801.UntagResource", handleCWJSONUntagResource)
	r.Register("GraniteServiceVersion20100801.ListTagsForResource", handleCWJSONListTagsForResource)
}

type cwJSONMetricDataQuery struct {
	Id         string `json:"Id"`
	MetricStat *struct {
		Metric struct {
			Namespace  string        `json:"Namespace"`
			MetricName string        `json:"MetricName"`
			Dimensions []CWDimension `json:"Dimensions"`
		} `json:"Metric"`
		Period int32  `json:"Period"`
		Stat   string `json:"Stat"`
	} `json:"MetricStat"`
	Label      string `json:"Label"`
	ReturnData *bool  `json:"ReturnData"`
}

func handleCWJSONGetMetricData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MetricDataQueries []cwJSONMetricDataQuery `json:"MetricDataQueries"`
		StartTime         float64                 `json:"StartTime"`
		EndTime           float64                 `json:"EndTime"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	results := make([]map[string]any, 0, len(req.MetricDataQueries))
	for _, q := range req.MetricDataQueries {
		res := map[string]any{
			"Id":         q.Id,
			"Label":      q.Label,
			"StatusCode": "Complete",
		}
		var values []float64
		var timestamps []float64
		if q.MetricStat != nil {
			m := q.MetricStat.Metric
			if data, ok := cwMetrics.Get(metricsKey(m.Namespace, m.MetricName, m.Dimensions)); ok {
				vals, ts := cwAggregate(data, req.StartTime, req.EndTime, q.MetricStat.Period, q.MetricStat.Stat)
				for i, v := range vals {
					values = append(values, v)
					timestamps = append(timestamps, float64(ts[i].Unix()))
				}
			}
		}
		// Emit the recorded datapoints; an empty result set when nothing was
		// pushed (no fabricated points). Values are Doubles — render with a
		// decimal so the awsJson deserializer reads them as floats, not ints.
		valNums := make([]any, 0, len(values))
		for _, v := range values {
			valNums = append(valNums, cwJSONStat(v))
		}
		if q.Label == "" {
			delete(res, "Label")
		}
		res["Values"] = valNums
		res["Timestamps"] = timestamps
		results = append(results, res)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"MetricDataResults": results})
}

func handleCWJSONPutAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"Name"`
		Description string `json:"Description"`
		Rule        *struct {
			Schedule CWMuteSchedule `json:"Schedule"`
		} `json:"Rule"`
		MuteTargets *struct {
			AlarmNames []string `json:"AlarmNames"`
		} `json:"MuteTargets"`
		Tags       []cwTagKV `json:"Tags"`
		StartDate  float64   `json:"StartDate"`
		ExpireDate float64   `json:"ExpireDate"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AWSError(w, "MissingParameter", "The parameter Name is required.", http.StatusBadRequest)
		return
	}
	var schedule CWMuteSchedule
	if req.Rule != nil {
		schedule = req.Rule.Schedule
	}
	var alarmNames []string
	if req.MuteTargets != nil {
		alarmNames = req.MuteTargets.AlarmNames
	}
	cwPutAlarmMuteRule(req.Name, req.Description, schedule, alarmNames, int64(req.StartDate), int64(req.ExpireDate), req.Tags)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func cwPutAlarmMuteRule(name, description string, schedule CWMuteSchedule, alarmNames []string, startDate, expireDate int64, tags []cwTagKV) {
	// MuteType is RECURRING for a cron-based schedule (no fixed end), SINGLE for a
	// one-time window — real CloudWatch classifies by the schedule expression.
	muteType := "SINGLE"
	if expireDate == 0 {
		muteType = "RECURRING"
	}
	existing, exists := cwAlarmMuteRules.Get(name)
	mr := CWAlarmMuteRule{
		Name:        name,
		Arn:         cwAlarmMuteRuleArn(name),
		Description: description,
		Schedule:    schedule,
		MuteType:    muteType,
		AlarmNames:  alarmNames,
		StartDate:   startDate,
		ExpireDate:  expireDate,
		Updated:     time.Now().UTC().Unix(),
	}
	if len(tags) > 0 {
		mr.Tags = map[string]string{}
		for _, t := range tags {
			mr.Tags[t.Key] = t.Value
		}
	} else if exists {
		mr.Tags = existing.Tags
	}
	cwAlarmMuteRules.Put(name, mr)
}

func handleCWJSONGetAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmMuteRuleName string `json:"AlarmMuteRuleName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	mr, ok := cwAlarmMuteRules.Get(req.AlarmMuteRuleName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Alarm mute rule %s does not exist", req.AlarmMuteRuleName)
		return
	}
	out := map[string]any{
		"Name":                 mr.Name,
		"AlarmMuteRuleArn":     mr.Arn,
		"Description":          mr.Description,
		"Rule":                 map[string]any{"Schedule": mr.Schedule},
		"MuteTargets":          map[string]any{"AlarmNames": mr.AlarmNames},
		"Status":               cwMuteRuleStatus(mr),
		"MuteType":             mr.MuteType,
		"LastUpdatedTimestamp": float64(mr.Updated),
	}
	if mr.StartDate != 0 {
		out["StartDate"] = float64(mr.StartDate)
	}
	if mr.ExpireDate != 0 {
		out["ExpireDate"] = float64(mr.ExpireDate)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWJSONDeleteAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmMuteRuleName string `json:"AlarmMuteRuleName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	// DeleteAlarmMuteRule is idempotent — deleting an absent rule succeeds.
	cwAlarmMuteRules.Delete(req.AlarmMuteRuleName)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONListAlarmMuteRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName string `json:"AlarmName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	summaries := make([]map[string]any, 0)
	for _, mr := range cwListAlarmMuteRules(req.AlarmName) {
		s := map[string]any{
			"AlarmMuteRuleArn":     mr.Arn,
			"Status":               cwMuteRuleStatus(mr),
			"MuteType":             mr.MuteType,
			"LastUpdatedTimestamp": float64(mr.Updated),
		}
		if mr.ExpireDate != 0 {
			s["ExpireDate"] = float64(mr.ExpireDate)
		}
		summaries = append(summaries, s)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AlarmMuteRuleSummaries": summaries})
}

// ── cross-resource tagging (awsJson) ────────────────────────────────────────

// cwResourceTags returns the tag map for any CloudWatch ARN the tagging API
// covers (metric alarm, composite alarm, metric stream, insight rule), and a
// setter that writes the updated map back.
func cwResourceTags(arn string) (map[string]string, func(map[string]string), bool) {
	if a, ok := cwAlarmByArn(arn); ok {
		return a.Tags, func(m map[string]string) { cwAlarms.Update(a.AlarmName, func(x *CWAlarm) { x.Tags = m }) }, true
	}
	if ca, ok := cwCompositeAlarmByArn(arn); ok {
		return ca.Tags, func(m map[string]string) {
			cwCompositeAlarms.Update(ca.AlarmName, func(x *CWCompositeAlarm) { x.Tags = m })
		}, true
	}
	if s, ok := cwMetricStreamByArn(arn); ok {
		return s.Tags, func(m map[string]string) { cwMetricStreams.Update(s.Name, func(x *CWMetricStream) { x.Tags = m }) }, true
	}
	if ir, ok := cwInsightRuleByArn(arn); ok {
		return ir.Tags, func(m map[string]string) { cwInsightRules.Update(ir.Name, func(x *CWInsightRule) { x.Tags = m }) }, true
	}
	if mr, ok := cwAlarmMuteRuleByArn(arn); ok {
		return mr.Tags, func(m map[string]string) { cwAlarmMuteRules.Update(mr.Name, func(x *CWAlarmMuteRule) { x.Tags = m }) }, true
	}
	return nil, nil, false
}

func cwAlarmMuteRuleByArn(arn string) (CWAlarmMuteRule, bool) {
	for _, r := range cwAlarmMuteRules.List() {
		if r.Arn == arn {
			return r, true
		}
	}
	return CWAlarmMuteRule{}, false
}

func handleCWJSONTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string    `json:"ResourceARN"`
		Tags        []cwTagKV `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	current, setter, ok := cwResourceTags(req.ResourceARN)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Unknown resource %s", req.ResourceARN)
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
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	current, setter, ok := cwResourceTags(req.ResourceARN)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Unknown resource %s", req.ResourceARN)
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
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	current, _, ok := cwResourceTags(req.ResourceARN)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Unknown resource %s", req.ResourceARN)
		return
	}
	tags := make([]cwTagKV, 0, len(current))
	for _, k := range cwSortedKeys(current) {
		tags = append(tags, cwTagKV{Key: k, Value: current[k]})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}

// ── rpc-v2-cbor surface (Go SDK) ────────────────────────────────────────────

// GetMetricData, TagResource, UntagResource and ListTagsForResource already have
// cbor routes (registered by cloudwatch_metrics.go / cloudwatch_alarms.go); the
// tag handlers there resolve every CloudWatch resource through cwResourceTags, so
// the cbor surface only needs the mute-rule operations registered here.
func registerCloudWatchMiscCBOR(srv *sim.Server) {
	cwCBOR(srv, "PutAlarmMuteRule", handleCWCBORPutAlarmMuteRule)
	cwCBOR(srv, "GetAlarmMuteRule", handleCWCBORGetAlarmMuteRule)
	cwCBOR(srv, "DeleteAlarmMuteRule", handleCWCBORDeleteAlarmMuteRule)
	cwCBOR(srv, "ListAlarmMuteRules", handleCWCBORListAlarmMuteRules)
}

func handleCWCBORPutAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `cbor:"Name"`
		Description string `cbor:"Description"`
		Rule        *struct {
			Schedule CWMuteSchedule `cbor:"Schedule"`
		} `cbor:"Rule"`
		MuteTargets *struct {
			AlarmNames []string `cbor:"AlarmNames"`
		} `cbor:"MuteTargets"`
		Tags       []cwTagKV `cbor:"Tags"`
		StartDate  time.Time `cbor:"StartDate"`
		ExpireDate time.Time `cbor:"ExpireDate"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.Name == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter Name is required.", http.StatusBadRequest)
		return
	}
	var schedule CWMuteSchedule
	if req.Rule != nil {
		schedule = req.Rule.Schedule
	}
	var alarmNames []string
	if req.MuteTargets != nil {
		alarmNames = req.MuteTargets.AlarmNames
	}
	var startSec, expireSec int64
	if !req.StartDate.IsZero() {
		startSec = req.StartDate.Unix()
	}
	if !req.ExpireDate.IsZero() {
		expireSec = req.ExpireDate.Unix()
	}
	cwPutAlarmMuteRule(req.Name, req.Description, schedule, alarmNames, startSec, expireSec, req.Tags)
	cwWriteCBOR(w, map[string]any{})
}

type cborRule struct {
	Schedule CWMuteSchedule `cbor:"Schedule"`
}

type cborMuteTargets struct {
	AlarmNames []string `cbor:"AlarmNames"`
}

func handleCWCBORGetAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmMuteRuleName string `cbor:"AlarmMuteRuleName"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	mr, ok := cwAlarmMuteRules.Get(req.AlarmMuteRuleName)
	if !ok {
		cwWriteCBORErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Alarm mute rule %s does not exist", req.AlarmMuteRuleName)
		return
	}
	out := map[string]any{
		"Name":                 mr.Name,
		"AlarmMuteRuleArn":     mr.Arn,
		"Description":          mr.Description,
		"Rule":                 cborRule{Schedule: mr.Schedule},
		"MuteTargets":          cborMuteTargets{AlarmNames: mr.AlarmNames},
		"Status":               cwMuteRuleStatus(mr),
		"MuteType":             mr.MuteType,
		"LastUpdatedTimestamp": time.Unix(mr.Updated, 0).UTC(),
	}
	if mr.StartDate != 0 {
		out["StartDate"] = time.Unix(mr.StartDate, 0).UTC()
	}
	if mr.ExpireDate != 0 {
		out["ExpireDate"] = time.Unix(mr.ExpireDate, 0).UTC()
	}
	cwWriteCBOR(w, out)
}

func handleCWCBORDeleteAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmMuteRuleName string `cbor:"AlarmMuteRuleName"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	cwAlarmMuteRules.Delete(req.AlarmMuteRuleName)
	cwWriteCBOR(w, map[string]any{})
}

type cborAlarmMuteRuleSummary struct {
	AlarmMuteRuleArn     string    `cbor:"AlarmMuteRuleArn"`
	ExpireDate           time.Time `cbor:"ExpireDate,omitempty"`
	Status               string    `cbor:"Status"`
	MuteType             string    `cbor:"MuteType"`
	LastUpdatedTimestamp time.Time `cbor:"LastUpdatedTimestamp"`
}

func handleCWCBORListAlarmMuteRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName string `cbor:"AlarmName"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	summaries := make([]cborAlarmMuteRuleSummary, 0)
	for _, mr := range cwListAlarmMuteRules(req.AlarmName) {
		s := cborAlarmMuteRuleSummary{
			AlarmMuteRuleArn:     mr.Arn,
			Status:               cwMuteRuleStatus(mr),
			MuteType:             mr.MuteType,
			LastUpdatedTimestamp: time.Unix(mr.Updated, 0).UTC(),
		}
		if mr.ExpireDate != 0 {
			s.ExpireDate = time.Unix(mr.ExpireDate, 0).UTC()
		}
		summaries = append(summaries, s)
	}
	cwWriteCBOR(w, map[string]any{"AlarmMuteRuleSummaries": summaries})
}

// ── query surface (older aws CLI) ───────────────────────────────────────────

func registerCloudWatchMiscQuery(r *sim.AWSQueryRouter) {
	r.Register("GetMetricData", handleCWQueryGetMetricData)
	r.Register("PutAlarmMuteRule", handleCWQueryPutAlarmMuteRule)
	r.Register("GetAlarmMuteRule", handleCWQueryGetAlarmMuteRule)
	r.Register("DeleteAlarmMuteRule", handleCWQueryDeleteAlarmMuteRule)
	r.Register("ListAlarmMuteRules", handleCWQueryListAlarmMuteRules)
	r.Register("TagResource", handleCWQueryTagResource)
	r.Register("UntagResource", handleCWQueryUntagResource)
	r.Register("ListTagsForResource", handleCWQueryListTagsForResource)
}

func handleCWQueryGetMetricData(w http.ResponseWriter, r *http.Request) {
	startSec := cwParseTimeUnix(r.FormValue("StartTime"))
	endSec := cwParseTimeUnix(r.FormValue("EndTime"))
	var b []byte
	b = append(b, []byte("<MetricDataResults>")...)
	for i := 1; ; i++ {
		base := "MetricDataQueries.member." + strconv.Itoa(i)
		id := r.FormValue(base + ".Id")
		if id == "" {
			break
		}
		ns := r.FormValue(base + ".MetricStat.Metric.Namespace")
		mn := r.FormValue(base + ".MetricStat.Metric.MetricName")
		dims := cwQueryDimensions(r, base+".MetricStat.Metric.Dimensions")
		stat := r.FormValue(base + ".MetricStat.Stat")
		var period int32 = 60
		if p := r.FormValue(base + ".MetricStat.Period"); p != "" {
			if pv, err := strconv.ParseInt(p, 10, 32); err == nil && pv > 0 {
				period = int32(pv)
			}
		}
		var values []float64
		var timestamps []time.Time
		if data, ok := cwMetrics.Get(metricsKey(ns, mn, dims)); ok {
			values, timestamps = cwAggregate(data, startSec, endSec, period, stat)
		}
		b = append(b, []byte("<member>")...)
		b = cwQueryAppendf(b, "<Id>%s</Id><StatusCode>Complete</StatusCode>", xmlEscape(id))
		b = append(b, []byte("<Timestamps>")...)
		for _, ts := range timestamps {
			b = cwQueryAppendf(b, "<member>%s</member>", ts.Format(time.RFC3339))
		}
		b = append(b, []byte("</Timestamps><Values>")...)
		for _, v := range values {
			b = cwQueryAppendf(b, "<member>%s</member>", cwFormatFloat(v))
		}
		b = append(b, []byte("</Values></member>")...)
	}
	b = append(b, []byte("</MetricDataResults>")...)
	cwQueryResult(w, "GetMetricData", string(b))
}

func handleCWQueryPutAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		cwQueryError(w, "MissingParameter", "The parameter Name is required.")
		return
	}
	var alarmNames []string
	for i := 1; ; i++ {
		v := r.FormValue("MuteTargets.AlarmNames.member." + strconv.Itoa(i))
		if v == "" {
			break
		}
		alarmNames = append(alarmNames, v)
	}
	startDate := int64(cwParseTimeUnix(r.FormValue("StartDate")))
	expireDate := int64(cwParseTimeUnix(r.FormValue("ExpireDate")))
	schedule := CWMuteSchedule{
		Expression: r.FormValue("Rule.Schedule.Expression"),
		Duration:   r.FormValue("Rule.Schedule.Duration"),
		Timezone:   r.FormValue("Rule.Schedule.Timezone"),
	}
	cwPutAlarmMuteRule(name, r.FormValue("Description"), schedule, alarmNames, startDate, expireDate, nil)
	cwQueryEmptyResponse(w, "PutAlarmMuteRule")
}

func handleCWQueryGetAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	mr, ok := cwAlarmMuteRules.Get(r.FormValue("AlarmMuteRuleName"))
	if !ok {
		cwQueryError(w, "ResourceNotFoundException", "Alarm mute rule "+r.FormValue("AlarmMuteRuleName")+" does not exist")
		return
	}
	var b []byte
	b = cwQueryAppendf(b, "<Name>%s</Name><AlarmMuteRuleArn>%s</AlarmMuteRuleArn>", xmlEscape(mr.Name), xmlEscape(mr.Arn))
	if mr.Description != "" {
		b = cwQueryAppendf(b, "<Description>%s</Description>", xmlEscape(mr.Description))
	}
	b = append(b, []byte("<Rule><Schedule>")...)
	if mr.Schedule.Expression != "" {
		b = cwQueryAppendf(b, "<Expression>%s</Expression>", xmlEscape(mr.Schedule.Expression))
	}
	if mr.Schedule.Duration != "" {
		b = cwQueryAppendf(b, "<Duration>%s</Duration>", xmlEscape(mr.Schedule.Duration))
	}
	if mr.Schedule.Timezone != "" {
		b = cwQueryAppendf(b, "<Timezone>%s</Timezone>", xmlEscape(mr.Schedule.Timezone))
	}
	b = append(b, []byte("</Schedule></Rule>")...)
	b = append(b, []byte("<MuteTargets><AlarmNames>")...)
	for _, n := range mr.AlarmNames {
		b = cwQueryAppendf(b, "<member>%s</member>", xmlEscape(n))
	}
	b = append(b, []byte("</AlarmNames></MuteTargets>")...)
	b = cwQueryAppendf(b, "<Status>%s</Status><MuteType>%s</MuteType>", cwMuteRuleStatus(mr), xmlEscape(mr.MuteType))
	b = cwQueryAppendf(b, "<LastUpdatedTimestamp>%s</LastUpdatedTimestamp>", time.Unix(mr.Updated, 0).UTC().Format(time.RFC3339))
	cwQueryResult(w, "GetAlarmMuteRule", string(b))
}

func handleCWQueryDeleteAlarmMuteRule(w http.ResponseWriter, r *http.Request) {
	cwAlarmMuteRules.Delete(r.FormValue("AlarmMuteRuleName"))
	cwQueryEmptyResponse(w, "DeleteAlarmMuteRule")
}

func handleCWQueryListAlarmMuteRules(w http.ResponseWriter, r *http.Request) {
	var b []byte
	b = append(b, []byte("<AlarmMuteRuleSummaries>")...)
	for _, mr := range cwListAlarmMuteRules(r.FormValue("AlarmName")) {
		b = append(b, []byte("<member>")...)
		b = cwQueryAppendf(b, "<AlarmMuteRuleArn>%s</AlarmMuteRuleArn><Status>%s</Status>", xmlEscape(mr.Arn), cwMuteRuleStatus(mr))
		b = cwQueryAppendf(b, "<MuteType>%s</MuteType><LastUpdatedTimestamp>%s</LastUpdatedTimestamp>",
			xmlEscape(mr.MuteType), time.Unix(mr.Updated, 0).UTC().Format(time.RFC3339))
		if mr.ExpireDate != 0 {
			b = cwQueryAppendf(b, "<ExpireDate>%s</ExpireDate>", time.Unix(mr.ExpireDate, 0).UTC().Format(time.RFC3339))
		}
		b = append(b, []byte("</member>")...)
	}
	b = append(b, []byte("</AlarmMuteRuleSummaries>")...)
	cwQueryResult(w, "ListAlarmMuteRules", string(b))
}

func handleCWQueryTagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceARN")
	current, setter, ok := cwResourceTags(arn)
	if !ok {
		cwQueryError(w, "ResourceNotFoundException", "Unknown resource "+arn)
		return
	}
	merged := map[string]string{}
	for k, v := range current {
		merged[k] = v
	}
	for i := 1; ; i++ {
		k := r.FormValue("Tags.member." + strconv.Itoa(i) + ".Key")
		if k == "" {
			break
		}
		merged[k] = r.FormValue("Tags.member." + strconv.Itoa(i) + ".Value")
	}
	setter(merged)
	cwQueryResult(w, "TagResource", "")
}

func handleCWQueryUntagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceARN")
	current, setter, ok := cwResourceTags(arn)
	if !ok {
		cwQueryError(w, "ResourceNotFoundException", "Unknown resource "+arn)
		return
	}
	merged := map[string]string{}
	for k, v := range current {
		merged[k] = v
	}
	for _, k := range cwQueryStringList(r, "TagKeys") {
		delete(merged, k)
	}
	setter(merged)
	cwQueryResult(w, "UntagResource", "")
}

func handleCWQueryListTagsForResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceARN")
	current, _, ok := cwResourceTags(arn)
	if !ok {
		cwQueryError(w, "ResourceNotFoundException", "Unknown resource "+arn)
		return
	}
	var b []byte
	b = append(b, []byte("<Tags>")...)
	for _, k := range cwSortedKeys(current) {
		b = cwQueryAppendf(b, "<member><Key>%s</Key><Value>%s</Value></member>", xmlEscape(k), xmlEscape(current[k]))
	}
	b = append(b, []byte("</Tags>")...)
	cwQueryResult(w, "ListTagsForResource", string(b))
}
