package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// Amazon RDS Performance Insights, the "pi" API: AWS JSON 1.1 under the
// PerformanceInsightsv20180227 target prefix, and rpc-v2-cbor, which the SDK
// and the CLI speak, bridged onto the same handlers.

const piTarget = "PerformanceInsightsv20180227"

type piReport struct {
	ReportId    string
	Resource    string
	ServiceType string
	CreateTime  float64
	StartTime   float64
	EndTime     float64
	Status      string
	Insights    []piInsight
	Tags        map[string]string
}

type piInsight struct {
	InsightId          string
	InsightType        string
	Context            string
	StartTime          float64
	EndTime            float64
	Severity           string
	SupportingInsights []piInsight
	Description        string
	Recommendations    []piRecommendation
	InsightData        []piData
	BaselineData       []piData
}

type piRecommendation struct {
	RecommendationId          string
	RecommendationDescription string
}

type piData struct {
	Metric      string
	DisplayName string
	Dimensions  map[string]string
	Value       float64
}

var piReports sim.Store[piReport]

var piOperations = map[string]http.HandlerFunc{
	"CreatePerformanceAnalysisReport":              handlePICreatePerformanceAnalysisReport,
	"DeletePerformanceAnalysisReport":              handlePIDeletePerformanceAnalysisReport,
	"DescribeDimensionKeys":                        handlePIDescribeDimensionKeys,
	"GetDimensionKeyDetails":                       handlePIGetDimensionKeyDetails,
	"GetPerformanceAnalysisReport":                 handlePIGetPerformanceAnalysisReport,
	"GetResourceMetadata":                          handlePIGetResourceMetadata,
	"GetResourceMetrics":                           handlePIGetResourceMetrics,
	"ListAvailableResourceDimensions":              handlePIListAvailableResourceDimensions,
	"ListAvailableResourceMetrics":                 handlePIListAvailableResourceMetrics,
	"ListPerformanceAnalysisReportRecommendations": handlePIListPerformanceAnalysisReportRecommendations,
	"ListPerformanceAnalysisReports":               handlePIListPerformanceAnalysisReports,
	"ListTagsForResource":                          handlePIListTagsForResource,
	"TagResource":                                  handlePITagResource,
	"UntagResource":                                handlePIUntagResource,
}

func registerPerformanceInsights(r *AWSRouter, srv *sim.Server) {
	piReports = sim.MakeStore[piReport](srv.DB(), "pi_reports")
	model := awsCBORModel{model: "pi", target: piTarget, contentType: "application/x-amz-json-1.1",
		operations: piCBOROperations, shapes: piCBORShapes}
	r.Register("PerformanceInsightsv20180227.CreatePerformanceAnalysisReport", handlePICreatePerformanceAnalysisReport)
	r.Register("PerformanceInsightsv20180227.DeletePerformanceAnalysisReport", handlePIDeletePerformanceAnalysisReport)
	r.Register("PerformanceInsightsv20180227.DescribeDimensionKeys", handlePIDescribeDimensionKeys)
	r.Register("PerformanceInsightsv20180227.GetDimensionKeyDetails", handlePIGetDimensionKeyDetails)
	r.Register("PerformanceInsightsv20180227.GetPerformanceAnalysisReport", handlePIGetPerformanceAnalysisReport)
	r.Register("PerformanceInsightsv20180227.GetResourceMetadata", handlePIGetResourceMetadata)
	r.Register("PerformanceInsightsv20180227.GetResourceMetrics", handlePIGetResourceMetrics)
	r.Register("PerformanceInsightsv20180227.ListAvailableResourceDimensions", handlePIListAvailableResourceDimensions)
	r.Register("PerformanceInsightsv20180227.ListAvailableResourceMetrics", handlePIListAvailableResourceMetrics)
	r.Register("PerformanceInsightsv20180227.ListPerformanceAnalysisReportRecommendations", handlePIListPerformanceAnalysisReportRecommendations)
	r.Register("PerformanceInsightsv20180227.ListPerformanceAnalysisReports", handlePIListPerformanceAnalysisReports)
	r.Register("PerformanceInsightsv20180227.ListTagsForResource", handlePIListTagsForResource)
	r.Register("PerformanceInsightsv20180227.TagResource", handlePITagResource)
	r.Register("PerformanceInsightsv20180227.UntagResource", handlePIUntagResource)
	for op, handler := range piOperations {
		srv.HandleFunc("POST /service/"+piTarget+"/operation/"+op,
			cloudTrailRecordedREST(op, "pi.amazonaws.com", nil, piCBORAuthorized(op, awsCBORBridge(model, op, handler))))
	}
}

// piCBORAuthorized authenticates a signed rpc-v2-cbor request and authorizes
// it against the resource its body names.
func piCBORAuthorized(op string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		if iamAccessKeyIDFromRequest(r) != "" {
			if _, serr := sigv4Verify(r, body); serr != nil {
				jsonCode, _ := sigv4ErrorCodes(serr.kind)
				awsCBORWriteError(w, "pi", jsonCode, serr.message, http.StatusForbidden)
				return
			}
			var input map[string]any
			if len(body) > 0 {
				_ = cwCBORDecMode.Unmarshal(body, &input)
			}
			context := piConditionContext(op, input)
			for _, resource := range piRequestARNs(r, op, input) {
				denial, allowed, registered := iamAuthorizeDenial(r, "pi:"+op, resource, context)
				if registered && !allowed {
					awsCBORWriteError(w, "pi", "NotAuthorizedException", denial.message(), http.StatusForbidden)
					return
				}
			}
		}
		h(w, r)
	}
}

// piConditionContext carries the keys an rpc-v2-cbor request settles, which
// the awsJson gate reads from a JSON body: the dimensions it reads and the
// tags it sets.
func piConditionContext(op string, input map[string]any) map[string][]string {
	ctx := map[string][]string{}
	if dims := piRequestedDimensions(op, input); len(dims) > 0 {
		ctx["pi:Dimensions"] = dims
	}
	tags, _ := input["Tags"].([]any)
	for _, raw := range tags {
		if tag, ok := raw.(map[string]any); ok {
			key, _ := tag["Key"].(string)
			value, _ := tag["Value"].(string)
			ctx["aws:RequestTag/"+key] = []string{value}
			ctx["aws:TagKeys"] = append(ctx["aws:TagKeys"], key)
		}
	}
	keys, _ := input["TagKeys"].([]any)
	for _, raw := range keys {
		if key, ok := raw.(string); ok {
			ctx["aws:TagKeys"] = append(ctx["aws:TagKeys"], key)
		}
	}
	return ctx
}

// piRequestARNs are the resources a request names, in the ARN formats the
// Service Reference declares for pi.
func piRequestARNs(r *http.Request, op string, input map[string]any) []string {
	str := func(k string) string { s, _ := input[k].(string); return s }
	if arn := str("ResourceARN"); arn != "" {
		return []string{arn}
	}
	region := iamRequestedRegion(r)
	if region == "" {
		region = awsRegion()
	}
	prefix := "arn:aws:pi:" + region + ":" + awsAccountID() + ":"
	service, identifier := str("ServiceType"), str("Identifier")
	if identifier == "" {
		return []string{"*"}
	}
	for _, t := range iamActionResourceTypes["pi:"+op] {
		if t == "metric-resource" {
			return []string{prefix + "metrics/" + service + "/" + identifier}
		}
		if t == "perf-reports-resource" {
			report := str("AnalysisReportId")
			if report == "" {
				report = "*"
			}
			return []string{prefix + "perf-reports/" + service + "/" + identifier + "/" + report}
		}
	}
	return []string{"*"}
}

func piError(w http.ResponseWriter, code string, status int, format string, args ...any) {
	awsWriteError(w, awsErrorEnvelope{Protocol: awsJSON11, Model: "pi"}, status, code, fmt.Sprintf(format, args...), "")
}

func piInvalid(w http.ResponseWriter, format string, args ...any) {
	piError(w, "InvalidArgumentException", http.StatusBadRequest, format, args...)
}

func piRead(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := sim.ReadJSON(r, v); err != nil {
		piInvalid(w, "Invalid request body: %v", err)
		return false
	}
	return true
}

func piBadToken(w http.ResponseWriter, _ string) {
	piInvalid(w, "The NextToken is not valid.")
}

// piResolve finds the DB instance a request's ServiceType and Identifier
// name: its DbiResourceId, or its DB instance identifier. Performance
// Insights serves only an instance with it turned on.
func piResolve(w http.ResponseWriter, serviceType, identifier string) (RDSInstance, dbengine.Engine, bool) {
	if serviceType == "" || identifier == "" {
		piInvalid(w, "ServiceType and Identifier are required.")
		return RDSInstance{}, dbengine.Engine{}, false
	}
	if serviceType != "RDS" {
		piInvalid(w, "ServiceType %s does not name a database this account holds.", serviceType)
		return RDSInstance{}, dbengine.Engine{}, false
	}
	for _, instance := range rdsInstances.List() {
		if instance.DbiResourceId != identifier && instance.DBInstanceIdentifier != identifier {
			continue
		}
		if !instance.PerformanceInsightsEnabled {
			piInvalid(w, "Performance Insights is not enabled for %s.", identifier)
			return RDSInstance{}, dbengine.Engine{}, false
		}
		engine, ok := rdsInstanceEngine(instance)
		if !ok {
			piInvalid(w, "Performance Insights does not support the engine of %s.", identifier)
			return RDSInstance{}, dbengine.Engine{}, false
		}
		return instance, engine, true
	}
	piInvalid(w, "No resource %s exists for service type %s.", identifier, serviceType)
	return RDSInstance{}, dbengine.Engine{}, false
}

func piTime(v *float64) time.Time {
	if v == nil {
		return time.Time{}
	}
	whole, frac := math.Modf(*v)
	return time.Unix(int64(whole), int64(frac*1e9))
}

func piEpoch(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

// piGroups are the dimension groups each engine family offers, as the API
// Reference's DimensionGroup lists them.
func piGroups(family dbengine.Family) map[string][]string {
	groups := map[string][]string{
		"db":                 {"db.name"},
		"db.host":            {"db.host.id", "db.host.name"},
		"db.sql":             {"db.sql.id", "db.sql.db_id", "db.sql.statement", "db.sql.tokenized_id"},
		"db.sql_tokenized":   {"db.sql_tokenized.id", "db.sql_tokenized.db_id", "db.sql_tokenized.statement"},
		"db.user":            {"db.user.id", "db.user.name"},
		"db.wait_event":      {"db.wait_event.name", "db.wait_event.type"},
		"db.wait_event_type": {"db.wait_event_type.name"},
	}
	if family == dbengine.Postgres {
		groups["db.application"] = []string{"db.application.name"}
		groups["db.session_type"] = []string{"db.session_type.name"}
	}
	return groups
}

func piDimensionValue(s piSession, dimension string) string {
	switch dimension {
	case "db.name":
		return s.Database
	case "db.application.name":
		return s.Application
	case "db.host.id":
		return s.HostID
	case "db.host.name":
		return s.HostName
	case "db.session_type.name":
		return s.SessionType
	case "db.sql.id":
		return s.SQLID
	case "db.sql.db_id":
		return s.SQLDBID
	case "db.sql.statement":
		return s.Statement
	case "db.sql.tokenized_id", "db.sql_tokenized.id":
		return s.TokenizedID
	case "db.sql_tokenized.db_id":
		return s.TokenizedDB
	case "db.sql_tokenized.statement":
		return s.TokenizedSQL
	case "db.user.id":
		return s.UserID
	case "db.user.name":
		return s.UserName
	case "db.wait_event.name":
		return s.WaitEvent
	case "db.wait_event.type", "db.wait_event_type.name":
		return s.WaitType
	}
	return ""
}

type piGroupBy struct {
	Group      string   `json:"Group"`
	Dimensions []string `json:"Dimensions"`
	Limit      *int     `json:"Limit"`
}

// piCheckGroup validates a DimensionGroup against the engine's groups.
func piCheckGroup(w http.ResponseWriter, family dbengine.Family, g *piGroupBy) bool {
	dims, ok := piGroups(family)[g.Group]
	if !ok {
		piInvalid(w, "The dimension group %s is not valid for this resource.", g.Group)
		return false
	}
	for _, d := range g.Dimensions {
		found := false
		for _, known := range dims {
			found = found || known == d
		}
		if !found {
			piInvalid(w, "The dimension %s is not in the dimension group %s.", d, g.Group)
			return false
		}
	}
	if len(g.Dimensions) == 0 {
		g.Dimensions = dims
	}
	if g.Limit != nil && (*g.Limit < 1 || *g.Limit > 25) {
		piInvalid(w, "Limit must be from 1 to 25.")
		return false
	}
	return true
}

func piFilterAdmits(filter map[string]string, s piSession) bool {
	for dim, value := range filter {
		if piDimensionValue(s, dim) != value {
			return false
		}
	}
	return true
}

func piCheckFilter(w http.ResponseWriter, family dbengine.Family, filter map[string]string) bool {
	for dim := range filter {
		known := false
		for _, dims := range piGroups(family) {
			for _, d := range dims {
				known = known || d == dim
			}
		}
		if !known {
			piInvalid(w, "The filter dimension %s is not valid for this resource.", dim)
			return false
		}
	}
	return true
}

// piKey identifies a session's values of a group's dimensions.
func piKey(s piSession, dims []string) (string, map[string]string) {
	values := make(map[string]string, len(dims))
	parts := make([]string, 0, len(dims))
	for _, d := range dims {
		v := piDimensionValue(s, d)
		values[d] = v
		parts = append(parts, v)
	}
	return strings.Join(parts, "\x00"), values
}

// piPeriods are the periods GetResourceMetrics and DescribeDimensionKeys
// accept; without one, the API picks the one that returns at most a few
// hundred points.
var piPeriods = []int64{1, 60, 300, 3600, 86400}

func piPeriod(w http.ResponseWriter, requested *int, start, end time.Time) (int64, bool) {
	if requested != nil {
		for _, p := range piPeriods {
			if int64(*requested) == p {
				return p, true
			}
		}
		piInvalid(w, "PeriodInSeconds must be one of 1, 60, 300, 3600 or 86400.")
		return 0, false
	}
	span := end.Unix() - start.Unix()
	for _, p := range piPeriods {
		if span/p <= 200 {
			return p, true
		}
	}
	return 86400, true
}

func piTimes(w http.ResponseWriter, start, end *float64) (time.Time, time.Time, bool) {
	if start == nil || end == nil {
		piInvalid(w, "StartTime and EndTime are required.")
		return time.Time{}, time.Time{}, false
	}
	s, e := piTime(start), piTime(end)
	if !s.Before(e) {
		piInvalid(w, "StartTime must be earlier than EndTime.")
		return time.Time{}, time.Time{}, false
	}
	return s, e, true
}

// piLoadMetric splits db.load.avg into db.load and avg; db.sampledload is the
// same per-second samples.
func piLoadMetric(metric string) (stat string, ok bool) {
	for _, base := range []string{"db.load.", "db.sampledload."} {
		if rest, found := strings.CutPrefix(metric, base); found {
			switch rest {
			case "avg", "min", "max", "sum", "sample_count":
				return rest, true
			}
		}
	}
	return "", false
}

func piCounterStat(metric string) (string, string, bool) {
	i := strings.LastIndex(metric, ".")
	if i < 0 {
		return "", "", false
	}
	switch metric[i+1:] {
	case "avg", "min", "max", "sum", "sample_count":
		return metric[:i], metric[i+1:], true
	}
	return "", "", false
}

func piReduce(stat string, values []float64, samples int) float64 {
	switch stat {
	case "sample_count":
		return float64(samples)
	case "sum":
		total := 0.0
		for _, v := range values {
			total += v
		}
		return total
	case "min", "max":
		if len(values) == 0 {
			return 0
		}
		m := values[0]
		for _, v := range values {
			if stat == "min" && v < m || stat == "max" && v > m {
				m = v
			}
		}
		return m
	}
	if samples == 0 {
		return 0
	}
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total / float64(samples)
}

// piLoadSeries buckets the load of the sessions filter admits into periods,
// split by key when key is set. Each bucket holds, per sampled second, how
// many matching sessions were active.
type piBucket struct {
	counts map[string][]float64
	seen   int
}

func piLoadBuckets(minutes []piMinute, start, end time.Time, period int64, filter map[string]string, key func(piSession) string) map[int64]*piBucket {
	out := map[int64]*piBucket{}
	for _, m := range minutes {
		for at, sessions := range m.Samples {
			if at < start.Unix() || at >= end.Unix() {
				continue
			}
			b := (at - start.Unix()) / period
			bucket := out[b]
			if bucket == nil {
				bucket = &piBucket{counts: map[string][]float64{}}
				out[b] = bucket
			}
			bucket.seen++
			perKey := map[string]float64{}
			for _, s := range sessions {
				if !piFilterAdmits(filter, s) {
					continue
				}
				k := ""
				if key != nil {
					k = key(s)
				}
				perKey[k]++
			}
			for k, v := range perKey {
				bucket.counts[k] = append(bucket.counts[k], v)
			}
		}
	}
	return out
}

func piAligned(start, end time.Time, period int64) (time.Time, time.Time) {
	s := (start.Unix() / period) * period
	e := ((end.Unix() + period - 1) / period) * period
	return time.Unix(s, 0).UTC(), time.Unix(e, 0).UTC()
}

type piMetricQuery struct {
	Metric  string            `json:"Metric"`
	GroupBy *piGroupBy        `json:"GroupBy"`
	Filter  map[string]string `json:"Filter"`
}

func handlePIGetResourceMetrics(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType     string          `json:"ServiceType"`
		Identifier      string          `json:"Identifier"`
		MetricQueries   []piMetricQuery `json:"MetricQueries"`
		StartTime       *float64        `json:"StartTime"`
		EndTime         *float64        `json:"EndTime"`
		PeriodInSeconds *int            `json:"PeriodInSeconds"`
		MaxResults      *int32          `json:"MaxResults"`
		NextToken       string          `json:"NextToken"`
		PeriodAlignment string          `json:"PeriodAlignment"`
	}
	if !piRead(w, r, &req) {
		return
	}
	instance, engine, ok := piResolve(w, req.ServiceType, req.Identifier)
	if !ok {
		return
	}
	start, end, ok := piTimes(w, req.StartTime, req.EndTime)
	if !ok {
		return
	}
	if len(req.MetricQueries) == 0 || len(req.MetricQueries) > 15 {
		piInvalid(w, "MetricQueries holds from 1 to 15 queries.")
		return
	}
	period, ok := piPeriod(w, req.PeriodInSeconds, start, end)
	if !ok {
		return
	}
	switch req.PeriodAlignment {
	case "", "END_TIME", "START_TIME":
	default:
		piInvalid(w, "PeriodAlignment must be END_TIME or START_TIME.")
		return
	}
	alignedStart, alignedEnd := piAligned(start, end, period)
	minutes := piSampledMinutes(instance.DbiResourceId, alignedStart, alignedEnd)
	stamp := func(bucket int64) float64 {
		t := alignedStart.Unix() + bucket*period
		if req.PeriodAlignment != "START_TIME" {
			t += period
		}
		return float64(t)
	}
	var list []map[string]any
	for _, q := range req.MetricQueries {
		if q.Filter != nil && !piCheckFilter(w, engine.Family, q.Filter) {
			return
		}
		if stat, isLoad := piLoadMetric(q.Metric); isLoad {
			series, ok := piLoadSeries(w, engine.Family, minutes, alignedStart, alignedEnd, period, q, stat, stamp)
			if !ok {
				return
			}
			list = append(list, series...)
			continue
		}
		name, stat, ok := piCounterStat(q.Metric)
		counter, known := piFindCounter(engine.Family, name)
		if !ok || !known {
			piInvalid(w, "The metric %s is not available for this resource.", q.Metric)
			return
		}
		if q.GroupBy != nil {
			piInvalid(w, "Only db.load can be grouped by a dimension group.")
			return
		}
		list = append(list, map[string]any{
			"Key":        map[string]any{"Metric": q.Metric},
			"DataPoints": piCounterPoints(instance.DbiResourceId, counter, stat, alignedStart, alignedEnd, period, stamp),
		})
	}
	page, next, ok := awsSeqPage(w, piBadToken, list, false, req.NextToken, awsMaxResults(req.MaxResults), 25)
	if !ok {
		return
	}
	out := map[string]any{
		"AlignedStartTime": piEpoch(alignedStart),
		"AlignedEndTime":   piEpoch(alignedEnd),
		"Identifier":       instance.DbiResourceId,
		"MetricList":       page,
	}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// piLoadSeries answers db.load for a query: the total, and with GroupBy the
// series of each of the top keys, ordered by their load.
func piLoadSeries(w http.ResponseWriter, family dbengine.Family, minutes []piMinute, start, end time.Time, period int64,
	q piMetricQuery, stat string, stamp func(int64) float64) ([]map[string]any, bool) {
	points := func(buckets map[int64]*piBucket, key string) []map[string]any {
		n := (end.Unix() - start.Unix()) / period
		out := make([]map[string]any, 0, n)
		for b := int64(0); b < n; b++ {
			bucket := buckets[b]
			if bucket == nil || bucket.seen == 0 {
				continue
			}
			out = append(out, map[string]any{"Timestamp": stamp(b), "Value": cwJSONStat(piReduce(stat, bucket.counts[key], bucket.seen))})
		}
		return out
	}
	total := piLoadBuckets(minutes, start, end, period, q.Filter, nil)
	series := []map[string]any{{"Key": map[string]any{"Metric": q.Metric}, "DataPoints": points(total, "")}}
	if q.GroupBy == nil {
		return series, true
	}
	if !piCheckGroup(w, family, q.GroupBy) {
		return nil, false
	}
	labels := map[string]map[string]string{}
	keyed := piLoadBuckets(minutes, start, end, period, q.Filter, func(s piSession) string {
		k, values := piKey(s, q.GroupBy.Dimensions)
		labels[k] = values
		return k
	})
	sums := map[string]float64{}
	for _, b := range keyed {
		for k, counts := range b.counts {
			for _, c := range counts {
				sums[k] += c
			}
		}
	}
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if sums[keys[i]] != sums[keys[j]] {
			return sums[keys[i]] > sums[keys[j]]
		}
		return keys[i] < keys[j]
	})
	limit := 10
	if q.GroupBy.Limit != nil {
		limit = *q.GroupBy.Limit
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	for _, k := range keys {
		series = append(series, map[string]any{"Key": map[string]any{"Metric": q.Metric, "Dimensions": labels[k]}, "DataPoints": points(keyed, k)})
	}
	return series, true
}

// piCounterBuckets reduces a counter's per-minute values into periods, by
// period index.
func piCounterBuckets(resource string, c piCounter, stat string, start, end time.Time, period int64) map[int64]float64 {
	minutes := piSampledMinutes(resource, start.Add(-2*time.Minute), end)
	buckets := map[int64][]float64{}
	var prev map[string]float64
	for _, m := range minutes {
		if m.Counters == nil {
			continue
		}
		at := int64(m.Counters["_at"])
		if at >= start.Unix() && at < end.Unix() {
			var v float64
			ok := false
			if prev != nil {
				v, ok = piCounterValue(c, prev, m.Counters)
			} else if c.Kind == piGauge {
				v, ok = m.Counters[c.Source]
			}
			if ok {
				b := (at - start.Unix()) / period
				buckets[b] = append(buckets[b], v)
			}
		}
		prev = m.Counters
	}
	out := make(map[int64]float64, len(buckets))
	for b, values := range buckets {
		out[b] = piReduce(stat, values, len(values))
	}
	return out
}

func piCounterPoints(resource string, c piCounter, stat string, start, end time.Time, period int64, stamp func(int64) float64) []map[string]any {
	buckets := piCounterBuckets(resource, c, stat, start, end, period)
	n := (end.Unix() - start.Unix()) / period
	out := []map[string]any{}
	for b := int64(0); b < n; b++ {
		if v, ok := buckets[b]; ok {
			out = append(out, map[string]any{"Timestamp": stamp(b), "Value": cwJSONStat(v)})
		}
	}
	return out
}

func handlePIDescribeDimensionKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType       string            `json:"ServiceType"`
		Identifier        string            `json:"Identifier"`
		StartTime         *float64          `json:"StartTime"`
		EndTime           *float64          `json:"EndTime"`
		Metric            string            `json:"Metric"`
		PeriodInSeconds   *int              `json:"PeriodInSeconds"`
		GroupBy           *piGroupBy        `json:"GroupBy"`
		AdditionalMetrics []string          `json:"AdditionalMetrics"`
		PartitionBy       *piGroupBy        `json:"PartitionBy"`
		Filter            map[string]string `json:"Filter"`
		MaxResults        *int32            `json:"MaxResults"`
		NextToken         string            `json:"NextToken"`
	}
	if !piRead(w, r, &req) {
		return
	}
	instance, engine, ok := piResolve(w, req.ServiceType, req.Identifier)
	if !ok {
		return
	}
	start, end, ok := piTimes(w, req.StartTime, req.EndTime)
	if !ok {
		return
	}
	stat, isLoad := piLoadMetric(req.Metric)
	if !isLoad || stat != "avg" {
		piInvalid(w, "The metric must be db.load.avg or db.sampledload.avg.")
		return
	}
	if req.GroupBy == nil || !piCheckGroup(w, engine.Family, req.GroupBy) {
		if req.GroupBy == nil {
			piInvalid(w, "GroupBy is required.")
		}
		return
	}
	if req.PartitionBy != nil && !piCheckGroup(w, engine.Family, req.PartitionBy) {
		return
	}
	if req.Filter != nil && !piCheckFilter(w, engine.Family, req.Filter) {
		return
	}
	if len(req.AdditionalMetrics) > 0 {
		piInvalid(w, "The additional metrics %s are SQL statistics this resource does not collect.", strings.Join(req.AdditionalMetrics, ", "))
		return
	}
	period, ok := piPeriod(w, req.PeriodInSeconds, start, end)
	if !ok {
		return
	}
	alignedStart, alignedEnd := piAligned(start, end, period)
	minutes := piSampledMinutes(instance.DbiResourceId, alignedStart, alignedEnd)
	samples := 0
	totals := map[string]float64{}
	labels := map[string]map[string]string{}
	partitions := map[string]map[string]float64{}
	partitionLabels := map[string]map[string]string{}
	for _, m := range minutes {
		for at, sessions := range m.Samples {
			if at < alignedStart.Unix() || at >= alignedEnd.Unix() {
				continue
			}
			samples++
			for _, s := range sessions {
				if !piFilterAdmits(req.Filter, s) {
					continue
				}
				k, values := piKey(s, req.GroupBy.Dimensions)
				labels[k] = values
				totals[k]++
				if req.PartitionBy != nil {
					pk, pv := piKey(s, req.PartitionBy.Dimensions)
					partitionLabels[pk] = pv
					if partitions[k] == nil {
						partitions[k] = map[string]float64{}
					}
					partitions[k][pk]++
				}
			}
		}
	}
	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if totals[keys[i]] != totals[keys[j]] {
			return totals[keys[i]] > totals[keys[j]]
		}
		return keys[i] < keys[j]
	})
	limit := 10
	if req.GroupBy.Limit != nil {
		limit = *req.GroupBy.Limit
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	partitionKeys := make([]string, 0, len(partitionLabels))
	for pk := range partitionLabels {
		partitionKeys = append(partitionKeys, pk)
	}
	sort.Strings(partitionKeys)
	descriptions := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		d := map[string]any{"Dimensions": labels[k], "Total": cwJSONStat(totals[k] / float64(max(samples, 1)))}
		if req.PartitionBy != nil {
			values := make([]any, 0, len(partitionKeys))
			for _, pk := range partitionKeys {
				values = append(values, cwJSONStat(partitions[k][pk]/float64(max(samples, 1))))
			}
			d["Partitions"] = values
		}
		descriptions = append(descriptions, d)
	}
	page, next, ok := awsSeqPage(w, piBadToken, descriptions, false, req.NextToken, awsMaxResults(req.MaxResults), 10)
	if !ok {
		return
	}
	out := map[string]any{"AlignedStartTime": piEpoch(alignedStart), "AlignedEndTime": piEpoch(alignedEnd), "Keys": page}
	if req.PartitionBy != nil {
		pks := make([]map[string]any, 0, len(partitionKeys))
		for _, pk := range partitionKeys {
			pks = append(pks, map[string]any{"Dimensions": partitionLabels[pk]})
		}
		out["PartitionKeys"] = pks
	}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handlePIGetDimensionKeyDetails(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType         string   `json:"ServiceType"`
		Identifier          string   `json:"Identifier"`
		Group               string   `json:"Group"`
		GroupIdentifier     string   `json:"GroupIdentifier"`
		RequestedDimensions []string `json:"RequestedDimensions"`
	}
	if !piRead(w, r, &req) {
		return
	}
	if _, _, ok := piResolve(w, req.ServiceType, req.Identifier); !ok {
		return
	}
	var dimension string
	switch req.Group {
	case "db.sql":
		dimension = "db.sql.statement"
	case "db.sql_tokenized":
		dimension = "db.sql_tokenized.statement"
	default:
		piInvalid(w, "GetDimensionKeyDetails supports the db.sql and db.sql_tokenized groups for this resource.")
		return
	}
	for _, d := range req.RequestedDimensions {
		if d != dimension {
			piInvalid(w, "The dimension %s is not available for the group %s.", d, req.Group)
			return
		}
	}
	if req.GroupIdentifier == "" {
		piInvalid(w, "GroupIdentifier is required.")
		return
	}
	detail := map[string]any{"Dimension": dimension}
	if statement, ok := piStatements.Get(req.GroupIdentifier); ok && statement.Tokenized == (req.Group == "db.sql_tokenized") {
		detail["Value"] = statement.Text
		detail["Status"] = "AVAILABLE"
	} else {
		detail["Status"] = "UNAVAILABLE"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Dimensions": []map[string]any{detail}})
}

func handlePIGetResourceMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType string `json:"ServiceType"`
		Identifier  string `json:"Identifier"`
	}
	if !piRead(w, r, &req) {
		return
	}
	instance, engine, ok := piResolve(w, req.ServiceType, req.Identifier)
	if !ok {
		return
	}
	features := map[string]any{}
	if engine.Family == dbengine.MySQL {
		status := "UNKNOWN"
		if on, err := piPerformanceSchemaOn(instance, engine); err == nil {
			status = "DISABLED"
			if on {
				status = "ENABLED"
			}
		}
		features["performance_schema"] = map[string]any{"Status": status}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Identifier": instance.DbiResourceId, "Features": features})
}

func handlePIListAvailableResourceDimensions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType       string   `json:"ServiceType"`
		Identifier        string   `json:"Identifier"`
		Metrics           []string `json:"Metrics"`
		MaxResults        *int32   `json:"MaxResults"`
		NextToken         string   `json:"NextToken"`
		AuthorizedActions []string `json:"AuthorizedActions"`
	}
	if !piRead(w, r, &req) {
		return
	}
	instance, engine, ok := piResolve(w, req.ServiceType, req.Identifier)
	if !ok {
		return
	}
	if len(req.Metrics) == 0 || len(req.Metrics) > 5 {
		piInvalid(w, "Metrics holds from 1 to 5 metric types.")
		return
	}
	for _, action := range req.AuthorizedActions {
		switch action {
		case "DescribeDimensionKeys", "GetDimensionKeyDetails", "GetResourceMetrics":
		default:
			piInvalid(w, "AuthorizedActions holds DescribeDimensionKeys, GetDimensionKeyDetails and GetResourceMetrics.")
			return
		}
	}
	groups := piAuthorizedGroups(r, instance, engine.Family, req.AuthorizedActions)
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	var entries []map[string]any
	for _, metric := range req.Metrics {
		if metric != "db.load" && metric != "db.sampledload" {
			piInvalid(w, "The metric type %s has no dimensions.", metric)
			return
		}
		details := make([]map[string]any, 0, len(names))
		for _, g := range names {
			dims := make([]map[string]string, 0, len(groups[g]))
			for _, d := range groups[g] {
				dims = append(dims, map[string]string{"Identifier": d})
			}
			details = append(details, map[string]any{"Group": g, "Dimensions": dims})
		}
		entries = append(entries, map[string]any{"Metric": metric, "Groups": details})
	}
	page, next, ok := awsSeqPage(w, piBadToken, entries, false, req.NextToken, awsMaxResults(req.MaxResults), 25)
	if !ok {
		return
	}
	out := map[string]any{"MetricDimensions": page}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// piAuthorizedGroups keeps the dimensions each of actions may read: the
// intersection the fine-grained access page describes, decided by the
// caller's policies with pi:Dimensions set to the one dimension.
func piAuthorizedGroups(r *http.Request, instance RDSInstance, family dbengine.Family, actions []string) map[string][]string {
	groups := piGroups(family)
	if len(actions) == 0 || iamAccessKeyIDFromRequest(r) == "" {
		return groups
	}
	region := iamRequestedRegion(r)
	if region == "" {
		region = awsRegion()
	}
	resource := "arn:aws:pi:" + region + ":" + awsAccountID() + ":metrics/RDS/" + instance.DbiResourceId
	out := map[string][]string{}
	for group, dims := range groups {
		for _, dim := range dims {
			allowed := true
			for _, action := range actions {
				_, ok, registered := iamAuthorizeDenial(r, "pi:"+action, resource, map[string][]string{"pi:Dimensions": {dim}})
				allowed = allowed && (ok || !registered)
			}
			if allowed {
				out[group] = append(out[group], dim)
			}
		}
	}
	return out
}

func handlePIListAvailableResourceMetrics(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType string   `json:"ServiceType"`
		Identifier  string   `json:"Identifier"`
		MetricTypes []string `json:"MetricTypes"`
		MaxResults  *int32   `json:"MaxResults"`
		NextToken   string   `json:"NextToken"`
	}
	if !piRead(w, r, &req) {
		return
	}
	_, engine, ok := piResolve(w, req.ServiceType, req.Identifier)
	if !ok {
		return
	}
	if len(req.MetricTypes) == 0 {
		piInvalid(w, "MetricTypes is required.")
		return
	}
	var metrics []map[string]any
	for _, kind := range req.MetricTypes {
		if kind != "os" && kind != "db" {
			piInvalid(w, "The metric type %s is not valid. Valid metric types are os and db.", kind)
			return
		}
		for _, c := range piCountersFor(engine.Family) {
			if !strings.HasPrefix(c.Metric, kind+".") {
				continue
			}
			m := map[string]any{"Metric": c.Metric, "Unit": c.Unit}
			if c.Description != "" {
				m["Description"] = c.Description
			}
			metrics = append(metrics, m)
		}
	}
	page, next, ok := awsSeqPage(w, piBadToken, metrics, false, req.NextToken, awsMaxResults(req.MaxResults), 25)
	if !ok {
		return
	}
	out := map[string]any{"Metrics": page}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func piReportKey(resource, id string) string { return resource + "|" + id }

func piNewReportID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return "report-" + hex.EncodeToString(b)[:17]
}

func piTagsJSON(tags map[string]string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]string{"Key": k, "Value": tags[k]})
	}
	return out
}

type piTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

func piCheckTags(w http.ResponseWriter, tags []piTag) bool {
	if len(tags) > 200 {
		piInvalid(w, "A resource holds at most 200 tags.")
		return false
	}
	for _, t := range tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			piInvalid(w, "The tag %q is not valid.", t.Key)
			return false
		}
	}
	return true
}

func handlePICreatePerformanceAnalysisReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType string   `json:"ServiceType"`
		Identifier  string   `json:"Identifier"`
		StartTime   *float64 `json:"StartTime"`
		EndTime     *float64 `json:"EndTime"`
		Tags        []piTag  `json:"Tags"`
	}
	if !piRead(w, r, &req) {
		return
	}
	instance, _, ok := piResolve(w, req.ServiceType, req.Identifier)
	if !ok {
		return
	}
	if req.StartTime == nil {
		piInvalid(w, "StartTime is required.")
		return
	}
	now := time.Now()
	end := now
	if req.EndTime != nil {
		end = piTime(req.EndTime)
	}
	start := piTime(req.StartTime)
	if !start.Before(end) {
		piInvalid(w, "StartTime must be earlier than EndTime.")
		return
	}
	if end.After(now) {
		piInvalid(w, "EndTime can't be later than the current time.")
		return
	}
	if !piCheckTags(w, req.Tags) {
		return
	}
	tags := map[string]string{}
	for _, t := range req.Tags {
		tags[t.Key] = t.Value
	}
	report := piReport{ReportId: piNewReportID(), Resource: instance.DbiResourceId, ServiceType: req.ServiceType,
		CreateTime: piEpoch(now), StartTime: piEpoch(start), EndTime: piEpoch(end), Status: "RUNNING", Tags: tags}
	piReports.Put(piReportKey(report.Resource, report.ReportId), report)
	insights := piAnalyze(instance.DbiResourceId, start, end)
	piReports.Update(piReportKey(report.Resource, report.ReportId), func(stored *piReport) {
		stored.Insights = insights
		stored.Status = "SUCCEEDED"
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AnalysisReportId": report.ReportId})
}

// piAnalyze compares the DB load in [start, end) with the load over the
// equally long period before it. A load above that baseline is a HighDBLoad
// insight, supported by the SQL statements that carried the load.
func piAnalyze(resource string, start, end time.Time) []piInsight {
	span := end.Sub(start)
	load := func(from, to time.Time) (float64, map[string]float64, int) {
		sessions, samples := 0.0, 0
		bySQL := map[string]float64{}
		for _, m := range piSampledMinutes(resource, from, to) {
			for at, active := range m.Samples {
				if at < from.Unix() || at >= to.Unix() {
					continue
				}
				samples++
				sessions += float64(len(active))
				for _, s := range active {
					if s.TokenizedID != "" {
						bySQL[s.TokenizedID]++
					}
				}
			}
		}
		if samples == 0 {
			return 0, bySQL, 0
		}
		return sessions / float64(samples), bySQL, samples
	}
	current, bySQL, samples := load(start, end)
	baseline, _, _ := load(start.Add(-span), start)
	if samples == 0 || current == 0 || current <= baseline {
		return []piInsight{}
	}
	ratio := math.Inf(1)
	if baseline > 0 {
		ratio = current / baseline
	}
	severity := "LOW"
	switch {
	case ratio >= 10:
		severity = "HIGH"
	case ratio >= 2:
		severity = "MEDIUM"
	}
	above := "above a baseline without load"
	if !math.IsInf(ratio, 1) {
		above = fmt.Sprintf("%sx above baseline", cwLabelNumber(math.Round(ratio*10)/10))
	}
	insight := piInsight{
		InsightId:   piInsightID(),
		InsightType: "HighDBLoad",
		Context:     "CAUSAL",
		StartTime:   piEpoch(start),
		EndTime:     piEpoch(end),
		Severity:    severity,
		Description: fmt.Sprintf("A %s severity insight found between %s to %s, where there was an unusually high DB load %s.",
			strings.ToLower(severity), start.UTC().Format("15:04"), end.UTC().Format("15:04"), above),
		InsightData:  []piData{{Metric: "db.load.avg", DisplayName: "DB load", Value: current}},
		BaselineData: []piData{{Metric: "db.load.avg", DisplayName: "DB load", Value: baseline}},
	}
	if len(bySQL) > 0 {
		ids := make([]string, 0, len(bySQL))
		total := 0.0
		for id, v := range bySQL {
			ids = append(ids, id)
			total += v
		}
		sort.Slice(ids, func(i, j int) bool {
			if bySQL[ids[i]] != bySQL[ids[j]] {
				return bySQL[ids[i]] > bySQL[ids[j]]
			}
			return ids[i] < ids[j]
		})
		if len(ids) > 5 {
			ids = ids[:5]
		}
		share := 0.0
		data := make([]piData, 0, len(ids))
		for _, id := range ids {
			share += bySQL[id]
			data = append(data, piData{Metric: "db.load.avg", DisplayName: "DB load", Dimensions: map[string]string{"db.sql_tokenized.id": id},
				Value: bySQL[id] / float64(samples)})
		}
		percent := math.Round(100 * share / float64(max(int(math.Round(current*float64(samples))), 1)))
		insight.SupportingInsights = []piInsight{{
			InsightId:   piInsightID(),
			InsightType: "DominatingSQLs",
			Context:     "CONTEXTUAL",
			StartTime:   piEpoch(start),
			EndTime:     piEpoch(end),
			Severity:    severity,
			Description: fmt.Sprintf("%d SQL statements carried %s%% of the DB load.", len(ids), cwLabelNumber(math.Min(percent, 100))),
			InsightData: data,
		}}
		insight.Recommendations = []piRecommendation{{
			RecommendationId: "recommendation-1",
			RecommendationDescription: fmt.Sprintf("Investigate the following SQLs that contributed to %s%% of the total DBLoad during that time period: %s",
				cwLabelNumber(math.Min(percent, 100)), strings.Join(ids, ", ")),
		}}
	}
	return []piInsight{insight}
}

func piInsightID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return "insight-" + hex.EncodeToString(b)[:17]
}

func piDataJSON(data []piData) []map[string]any {
	out := make([]map[string]any, 0, len(data))
	for _, d := range data {
		m := map[string]any{"Metric": d.Metric, "DisplayName": d.DisplayName, "Value": cwJSONStat(d.Value)}
		if len(d.Dimensions) > 0 {
			m["Dimensions"] = d.Dimensions
		}
		out = append(out, map[string]any{"PerformanceInsightsMetric": m})
	}
	return out
}

func piRecommendationsJSON(recs []piRecommendation) []map[string]any {
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		out = append(out, map[string]any{"RecommendationId": rec.RecommendationId, "RecommendationDescription": rec.RecommendationDescription})
	}
	return out
}

func piInsightJSON(in piInsight) map[string]any {
	out := map[string]any{
		"InsightId": in.InsightId, "InsightType": in.InsightType, "Context": in.Context,
		"StartTime": in.StartTime, "EndTime": in.EndTime, "Severity": in.Severity, "Description": in.Description,
		"Recommendations": piRecommendationsJSON(in.Recommendations), "InsightData": piDataJSON(in.InsightData),
		"BaselineData": piDataJSON(in.BaselineData),
	}
	supporting := make([]map[string]any, 0, len(in.SupportingInsights))
	for _, s := range in.SupportingInsights {
		supporting = append(supporting, piInsightJSON(s))
	}
	out["SupportingInsights"] = supporting
	return out
}

func piFindReport(w http.ResponseWriter, serviceType, identifier, id string) (piReport, bool) {
	instance, _, ok := piResolve(w, serviceType, identifier)
	if !ok {
		return piReport{}, false
	}
	report, found := piReports.Get(piReportKey(instance.DbiResourceId, id))
	if !found {
		piInvalid(w, "The analysis report %s does not exist.", id)
		return piReport{}, false
	}
	return report, true
}

func handlePIGetPerformanceAnalysisReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType      string `json:"ServiceType"`
		Identifier       string `json:"Identifier"`
		AnalysisReportId string `json:"AnalysisReportId"`
		TextFormat       string `json:"TextFormat"`
		AcceptLanguage   string `json:"AcceptLanguage"`
	}
	if !piRead(w, r, &req) {
		return
	}
	switch req.TextFormat {
	case "", "PLAIN_TEXT", "MARKDOWN":
	default:
		piInvalid(w, "TextFormat must be PLAIN_TEXT or MARKDOWN.")
		return
	}
	if req.AcceptLanguage != "" && req.AcceptLanguage != "EN_US" {
		piInvalid(w, "AcceptLanguage must be EN_US.")
		return
	}
	report, ok := piFindReport(w, req.ServiceType, req.Identifier, req.AnalysisReportId)
	if !ok {
		return
	}
	insights := make([]map[string]any, 0, len(report.Insights))
	for _, in := range report.Insights {
		insights = append(insights, piInsightJSON(in))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AnalysisReport": map[string]any{
		"AnalysisReportId": report.ReportId, "Identifier": report.Resource, "ServiceType": report.ServiceType,
		"CreateTime": report.CreateTime, "StartTime": report.StartTime, "EndTime": report.EndTime,
		"Status": report.Status, "Insights": insights,
	}})
}

func handlePIListPerformanceAnalysisReports(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType string `json:"ServiceType"`
		Identifier  string `json:"Identifier"`
		NextToken   string `json:"NextToken"`
		MaxResults  *int32 `json:"MaxResults"`
		ListTags    bool   `json:"ListTags"`
	}
	if !piRead(w, r, &req) {
		return
	}
	instance, _, ok := piResolve(w, req.ServiceType, req.Identifier)
	if !ok {
		return
	}
	var reports []piReport
	for _, entry := range piReports.ListPrefix(instance.DbiResourceId + "|") {
		reports = append(reports, entry.Item)
	}
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].StartTime != reports[j].StartTime {
			return reports[i].StartTime < reports[j].StartTime
		}
		return reports[i].ReportId < reports[j].ReportId
	})
	page, next, ok := awsSeqPage(w, piBadToken, reports, false, req.NextToken, awsMaxResults(req.MaxResults), 25)
	if !ok {
		return
	}
	items := make([]map[string]any, 0, len(page))
	for _, rep := range page {
		item := map[string]any{"AnalysisReportId": rep.ReportId, "CreateTime": rep.CreateTime, "StartTime": rep.StartTime,
			"EndTime": rep.EndTime, "Status": rep.Status}
		if req.ListTags {
			item["Tags"] = piTagsJSON(rep.Tags)
		}
		items = append(items, item)
	}
	out := map[string]any{"AnalysisReports": items}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handlePIListPerformanceAnalysisReportRecommendations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType       string   `json:"ServiceType"`
		Identifier        string   `json:"Identifier"`
		AnalysisReportId  string   `json:"AnalysisReportId"`
		RecommendationIds []string `json:"RecommendationIds"`
		MaxResults        *int32   `json:"MaxResults"`
		NextToken         string   `json:"NextToken"`
	}
	if !piRead(w, r, &req) {
		return
	}
	report, ok := piFindReport(w, req.ServiceType, req.Identifier, req.AnalysisReportId)
	if !ok {
		return
	}
	wanted := map[string]bool{}
	for _, id := range req.RecommendationIds {
		wanted[id] = true
	}
	var recs []piRecommendation
	var collect func([]piInsight)
	collect = func(insights []piInsight) {
		for _, in := range insights {
			for _, rec := range in.Recommendations {
				if len(wanted) == 0 || wanted[rec.RecommendationId] {
					recs = append(recs, rec)
				}
			}
			collect(in.SupportingInsights)
		}
	}
	collect(report.Insights)
	page, next, ok := awsSeqPage(w, piBadToken, recs, false, req.NextToken, awsMaxResults(req.MaxResults), 25)
	if !ok {
		return
	}
	out := map[string]any{"Recommendations": piRecommendationsJSON(page)}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handlePIDeletePerformanceAnalysisReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType      string `json:"ServiceType"`
		Identifier       string `json:"Identifier"`
		AnalysisReportId string `json:"AnalysisReportId"`
	}
	if !piRead(w, r, &req) {
		return
	}
	report, ok := piFindReport(w, req.ServiceType, req.Identifier, req.AnalysisReportId)
	if !ok {
		return
	}
	piReports.Delete(piReportKey(report.Resource, report.ReportId))
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// piTaggedReport resolves a perf-reports ARN to its report.
func piTaggedReport(w http.ResponseWriter, serviceType, arn string) (piReport, bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[2] != "pi" || parts[4] != awsAccountID() {
		piInvalid(w, "The resource ARN %s is not valid.", arn)
		return piReport{}, false
	}
	path := strings.Split(parts[5], "/")
	if len(path) != 4 || path[0] != "perf-reports" || path[1] != serviceType {
		piInvalid(w, "The resource ARN %s does not name a performance analysis report.", arn)
		return piReport{}, false
	}
	report, ok := piReports.Get(piReportKey(path[2], path[3]))
	if !ok {
		piInvalid(w, "The resource %s does not exist.", arn)
		return piReport{}, false
	}
	return report, true
}

func handlePITagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType string  `json:"ServiceType"`
		ResourceARN string  `json:"ResourceARN"`
		Tags        []piTag `json:"Tags"`
	}
	if !piRead(w, r, &req) || !piCheckTags(w, req.Tags) {
		return
	}
	report, ok := piTaggedReport(w, req.ServiceType, req.ResourceARN)
	if !ok {
		return
	}
	merged := map[string]string{}
	for k, v := range report.Tags {
		merged[k] = v
	}
	for _, t := range req.Tags {
		merged[t.Key] = t.Value
	}
	if len(merged) > 200 {
		piInvalid(w, "A resource holds at most 200 tags.")
		return
	}
	piReports.Update(piReportKey(report.Resource, report.ReportId), func(stored *piReport) { stored.Tags = merged })
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePIUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType string   `json:"ServiceType"`
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if !piRead(w, r, &req) {
		return
	}
	report, ok := piTaggedReport(w, req.ServiceType, req.ResourceARN)
	if !ok {
		return
	}
	piReports.Update(piReportKey(report.Resource, report.ReportId), func(stored *piReport) {
		for _, k := range req.TagKeys {
			delete(stored.Tags, k)
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePIListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceType string `json:"ServiceType"`
		ResourceARN string `json:"ResourceARN"`
	}
	if !piRead(w, r, &req) {
		return
	}
	report, ok := piTaggedReport(w, req.ServiceType, req.ResourceARN)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": piTagsJSON(report.Tags)})
}
