package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// PutAnomalyDetector, DescribeAnomalyDetectors and DeleteAnomalyDetector, as
// the API Reference defines them: a detector is a SingleMetricAnomalyDetector
// (Namespace, MetricName, Dimensions and Stat, or the deprecated top-level
// copies of them) or a MetricMathAnomalyDetector (MetricDataQueries, one of
// which returns the single time series the model learns), and has an
// AnomalyDetectorId.

const (
	cwAnomalySingleMetric      = "SINGLE_METRIC"
	cwAnomalyMetricMath        = "METRIC_MATH"
	cwAnomalyMaxExcludedRanges = 10
	cwAnomalyMaxDescribeIDs    = 50
	cwAnomalyMaxResults        = 100
	cwAnomalyMathMaxQueries    = 10
	cwAnomalyMathMaxPeriod     = 3600
	cwAnomalyMathMinPeriod     = 60
)

type CWAnomalyDetector struct {
	Id             string                 `json:"Id,omitempty"`
	Type           string                 `json:"Type,omitempty"`
	AccountId      string                 `json:"AccountId,omitempty"`
	Namespace      string                 `json:"Namespace"`
	MetricName     string                 `json:"MetricName"`
	Stat           string                 `json:"Stat"`
	Dimensions     []CWDimension          `json:"Dimensions,omitempty"`
	MathQueries    []CWMetricDataQuery    `json:"MathQueries,omitempty"`
	Configuration  CWAnomalyConfiguration `json:"Configuration"`
	PeriodicSpikes *bool                  `json:"PeriodicSpikes,omitempty"`
	StateValue     string                 `json:"StateValue,omitempty"`
	TrainedPoints  int                    `json:"TrainedPoints,omitempty"`
	TrainedNewest  int64                  `json:"TrainedNewest,omitempty"`
	TrainedFrom    int64                  `json:"TrainedFrom,omitempty"`
	TrainedTo      int64                  `json:"TrainedTo,omitempty"`
}

func (d CWAnomalyDetector) kind() string {
	if d.Type == "" {
		return cwAnomalySingleMetric
	}
	return d.Type
}

func (d CWAnomalyDetector) key() string {
	if d.kind() == cwAnomalyMetricMath {
		return cwMathAnomalyKey(d.MathQueries)
	}
	return cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions)
}

var cwAnomalyDetectors sim.Store[CWAnomalyDetector]

func cwAnomalyKey(namespace, metricName, stat string, dims []CWDimension) string {
	return metricsKey(namespace, metricName, dims) + "/stat=" + stat
}

// cwMathAnomalyKey identifies a metric math detector by its queries, sorted
// by Id, with the returned one marked.
func cwMathAnomalyKey(queries []CWMetricDataQuery) string {
	sorted := append([]CWMetricDataQuery(nil), queries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Id < sorted[j].Id })
	var b strings.Builder
	b.WriteString("math")
	for _, q := range sorted {
		fmt.Fprintf(&b, "|%s;%t;%s;%d;%s", q.Id, q.returns(), q.Expression, q.Period, q.AccountId)
		if ms := q.MetricStat; ms != nil {
			fmt.Fprintf(&b, ";%s;%d;%s;%s", metricsKey(ms.Metric.Namespace, ms.Metric.MetricName, ms.Metric.Dimensions), ms.Period, ms.Stat, ms.Unit)
		}
	}
	return b.String()
}

// cwQueryClosure answers the query id and every query it reads, directly or
// through other expressions, with id returning data and the rest not.
func cwQueryClosure(all []CWMetricDataQuery, id string) []CWMetricDataQuery {
	byID := map[string]CWMetricDataQuery{}
	for _, q := range all {
		byID[q.Id] = q
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(qid string) {
		q, ok := byID[qid]
		if !ok || seen[qid] {
			return
		}
		seen[qid] = true
		if q.Expression == "" || cwIsMetricsInsightsQuery(q.Expression) {
			return
		}
		node, err := cwParseMetricExpression(q.Expression)
		if err != nil {
			return
		}
		for _, ref := range cwExpressionReferences(node) {
			visit(ref)
		}
		if cwCallsFunction(node, "METRICS") {
			for _, other := range all {
				if other.MetricStat != nil {
					visit(other.Id)
				}
			}
		}
	}
	visit(id)
	yes, no := true, false
	out := make([]CWMetricDataQuery, 0, len(seen))
	for _, q := range all {
		if !seen[q.Id] {
			continue
		}
		q.Label = ""
		q.ReturnData = &no
		if q.Id == id {
			q.ReturnData = &yes
		}
		out = append(out, q)
	}
	return out
}

func cwExpressionReferences(n *cwMathNode) []string {
	if n == nil {
		return nil
	}
	var out []string
	if n.kind == "id" {
		out = append(out, n.name)
	}
	out = append(out, cwExpressionReferences(n.left)...)
	out = append(out, cwExpressionReferences(n.right)...)
	for _, a := range n.args {
		out = append(out, cwExpressionReferences(a)...)
	}
	return out
}

// cwValidateMathAnomalyQueries holds a metric math detector to "Anomaly
// detection on metric math": exactly one query returns data and it returns a
// single time series; at most 10 metrics and expressions; no METRICS, SEARCH
// or DB_PERF_INSIGHTS; at most one ANOMALY_DETECTION_BAND in an expression;
// every metric at one period, from one minute to one hour.
func cwValidateMathAnomalyQueries(queries []CWMetricDataQuery) *cwAnomalyProblem {
	if len(queries) == 0 {
		return cwAnomalyFail("InvalidParameterValue", "The MetricMathAnomalyDetector must include MetricDataQueries.")
	}
	if len(queries) > cwAnomalyMathMaxQueries {
		return cwAnomalyFail("InvalidParameterValue", "Anomaly detection on metric math supports at most %d metrics and math expressions.", cwAnomalyMathMaxQueries)
	}
	returning := 0
	period := int32(0)
	for _, q := range queries {
		if q.returns() {
			returning++
		}
		if q.Expression != "" {
			if cwIsMetricsInsightsQuery(q.Expression) {
				continue
			}
			node, err := cwParseMetricExpression(q.Expression)
			if err != nil {
				return cwAnomalyFail("InvalidParameterValue", "Error in expression %s: %s", q.Id, err.Error())
			}
			for _, fn := range []string{"METRICS", "SEARCH", "DB_PERF_INSIGHTS"} {
				if cwCallsFunction(node, fn) {
					return cwAnomalyFail("InvalidParameterValue", "Anomaly detection on metric math does not support the %s function.", fn)
				}
			}
			if cwCountCalls(node, "ANOMALY_DETECTION_BAND") > 1 {
				return cwAnomalyFail("InvalidParameterValue", "Anomaly detection on metric math does not support more than one ANOMALY_DETECTION_BAND in an expression.")
			}
		}
		if ms := q.MetricStat; ms != nil {
			if ms.Period < cwAnomalyMathMinPeriod {
				return cwAnomalyFail("InvalidParameterValue", "Anomaly detection on metric math does not support high-resolution metrics.")
			}
			if ms.Period > cwAnomalyMathMaxPeriod {
				return cwAnomalyFail("InvalidParameterValue", "Anomaly detection on metric math does not support periods longer than one hour.")
			}
			if period != 0 && ms.Period != period {
				return cwAnomalyFail("InvalidParameterValue", "Anomaly detection on metric math does not support metrics with different periods.")
			}
			period = ms.Period
		}
	}
	if returning != 1 {
		return cwAnomalyFail("InvalidParameterValue", "Exactly one of the MetricDataQueries must have ReturnData set to true.")
	}
	return nil
}

func cwCountCalls(n *cwMathNode, name string) int {
	if n == nil {
		return 0
	}
	count := 0
	if n.kind == "call" && n.name == name {
		count++
	}
	count += cwCountCalls(n.left, name) + cwCountCalls(n.right, name)
	for _, a := range n.args {
		count += cwCountCalls(a, name)
	}
	return count
}

func cwMathDetectorReturned(d CWAnomalyDetector) CWMetricDataQuery {
	for _, q := range d.MathQueries {
		if q.returns() {
			return q
		}
	}
	return CWMetricDataQuery{}
}

// cwAnomalyRequest is the protocol-neutral input of the three operations.
type cwAnomalyRequest struct {
	Namespace, MetricName, Stat string
	Dimensions                  []CWDimension
	Single                      *CWAnomalyDetector
	Math                        []CWMetricDataQuery
	HasMath                     bool
	Configuration               *CWAnomalyConfiguration
	PeriodicSpikes              *bool
	Id                          string
	Ids                         []string
	Types                       []string
	MaxResults                  int
	NextToken                   string
}

func (req cwAnomalyRequest) topLevel() bool {
	return req.Namespace != "" || req.MetricName != "" || req.Stat != "" || len(req.Dimensions) > 0
}

type cwAnomalyProblem struct{ code, msg string }

func (p *cwAnomalyProblem) Error() string { return p.msg }

func cwAnomalyFail(code, format string, args ...any) *cwAnomalyProblem {
	return &cwAnomalyProblem{code: code, msg: fmt.Sprintf(format, args...)}
}

// identity resolves which detector a Put or Delete names.
func (req cwAnomalyRequest) identity() (CWAnomalyDetector, *cwAnomalyProblem) {
	switch {
	case req.HasMath && (req.Single != nil || req.topLevel()):
		return CWAnomalyDetector{}, cwAnomalyFail("InvalidParameterCombination", "When using MetricMathAnomalyDetector, you cannot include Namespace, MetricName, Dimensions, Stat or SingleMetricAnomalyDetector.")
	case req.Single != nil && req.topLevel():
		return CWAnomalyDetector{}, cwAnomalyFail("InvalidParameterCombination", "When using SingleMetricAnomalyDetector, you cannot include Namespace, MetricName, Dimensions, Stat or MetricMathAnomalyDetector.")
	case req.HasMath:
		if problem := cwValidateMathAnomalyQueries(req.Math); problem != nil {
			return CWAnomalyDetector{}, problem
		}
		return CWAnomalyDetector{Type: cwAnomalyMetricMath, MathQueries: req.Math}, nil
	case req.Single != nil:
		d := *req.Single
		d.Type = cwAnomalySingleMetric
		if d.Namespace == "" || d.MetricName == "" || d.Stat == "" {
			return d, cwAnomalyFail("MissingParameter", "SingleMetricAnomalyDetector must include Namespace, MetricName and Stat.")
		}
		return d, nil
	}
	if req.Namespace == "" || req.MetricName == "" || req.Stat == "" {
		return CWAnomalyDetector{}, cwAnomalyFail("MissingParameter", "Namespace, MetricName and Stat are required.")
	}
	return CWAnomalyDetector{Type: cwAnomalySingleMetric, Namespace: req.Namespace, MetricName: req.MetricName, Stat: req.Stat, Dimensions: req.Dimensions}, nil
}

func cwAnomalyPut(req cwAnomalyRequest) (string, *cwAnomalyProblem) {
	d, problem := req.identity()
	if problem != nil {
		return "", problem
	}
	if c := req.Configuration; c != nil {
		if len(c.ExcludedTimeRanges) > cwAnomalyMaxExcludedRanges {
			return "", cwAnomalyFail("InvalidParameterValue", "You can specify as many as %d ExcludedTimeRanges.", cwAnomalyMaxExcludedRanges)
		}
		if c.MetricTimezone != "" {
			if _, err := time.LoadLocation(c.MetricTimezone); err != nil {
				return "", cwAnomalyFail("InvalidParameterValue", "The MetricTimezone is not a valid tz database time zone.")
			}
		}
		if s := c.AdaptationSpeed; s != "" && s != "FAST" && s != "NORMAL" && s != "SLOW" {
			return "", cwAnomalyFail("InvalidParameterValue", "AdaptationSpeed must be one of FAST, NORMAL or SLOW.")
		}
		if s := c.OutlierFilteringStrength; s != "" && s != "MINIMAL" && s != "MODERATE" && s != "STRICT" {
			return "", cwAnomalyFail("InvalidParameterValue", "OutlierFilteringStrength must be one of MINIMAL, MODERATE or STRICT.")
		}
		d.Configuration = *c
	}
	d.PeriodicSpikes = req.PeriodicSpikes
	return cwPutAnomalyDetector(d), nil
}

// cwPutAnomalyDetector keeps a detector's Id and training and re-trains it on
// the next pass, as a changed configuration changes what the model learns
// from.
func cwPutAnomalyDetector(d CWAnomalyDetector) string {
	if d.AccountId == "" {
		d.AccountId = awsAccountID()
	}
	key := d.key()
	if existing, ok := cwAnomalyDetectors.Get(key); ok {
		d.Id = existing.Id
		d.StateValue, d.TrainedFrom, d.TrainedTo = existing.StateValue, existing.TrainedFrom, existing.TrainedTo
		d.TrainedPoints = -1
	}
	if d.Id == "" {
		d.Id = sim.NewUUID()
	}
	cwAnomalyDetectors.Put(key, d)
	return d.Id
}

func cwAnomalyByID(id string) (CWAnomalyDetector, bool) {
	for _, d := range cwAnomalyDetectors.List() {
		if d.Id == id {
			return d, true
		}
	}
	return CWAnomalyDetector{}, false
}

func cwAnomalyDelete(req cwAnomalyRequest) *cwAnomalyProblem {
	var key string
	if req.Id != "" {
		if req.HasMath || req.Single != nil || req.topLevel() {
			return cwAnomalyFail("InvalidParameterCombination", "When using AnomalyDetectorId, you cannot also identify the detector by its metric.")
		}
		d, ok := cwAnomalyByID(req.Id)
		if !ok {
			return cwAnomalyFail("ResourceNotFoundException", "No anomaly detector found for the given metric.")
		}
		key = d.key()
	} else {
		d, problem := req.identity()
		if problem != nil {
			return problem
		}
		key = d.key()
	}
	if _, ok := cwAnomalyDetectors.Get(key); !ok {
		return cwAnomalyFail("ResourceNotFoundException", "No anomaly detector found for the given metric.")
	}
	cwAnomalyDetectors.Delete(key)
	return nil
}

func cwDimensionsInclude(have, want []CWDimension) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// cwAnomalyDescribe filters by AnomalyDetectorIds, or by Namespace,
// MetricName, Dimensions and AnomalyDetectorTypes (SINGLE_METRIC by
// default), and pages by MaxResults.
func cwAnomalyDescribe(req cwAnomalyRequest) ([]CWAnomalyDetector, string, *cwAnomalyProblem) {
	if len(req.Ids) > 0 && (req.Namespace != "" || req.MetricName != "" || len(req.Dimensions) > 0 || len(req.Types) > 0) {
		return nil, "", cwAnomalyFail("InvalidParameterCombination", "If you specify AnomalyDetectorIds, you cannot also specify Namespace, MetricName, Dimensions or AnomalyDetectorTypes.")
	}
	if len(req.Ids) > cwAnomalyMaxDescribeIDs {
		return nil, "", cwAnomalyFail("InvalidParameterValue", "You can specify up to %d AnomalyDetectorIds.", cwAnomalyMaxDescribeIDs)
	}
	if req.MaxResults < 0 || req.MaxResults > cwAnomalyMaxResults {
		return nil, "", cwAnomalyFail("InvalidParameterValue", "The maximum value of MaxResults is %d.", cwAnomalyMaxResults)
	}
	types := req.Types
	if len(types) == 0 {
		types = []string{cwAnomalySingleMetric}
	}
	for _, t := range types {
		if t != cwAnomalySingleMetric && t != cwAnomalyMetricMath {
			return nil, "", cwAnomalyFail("InvalidParameterValue", "AnomalyDetectorTypes must be SINGLE_METRIC or METRIC_MATH.")
		}
	}
	all := cwAnomalyDetectors.List()
	out := make([]CWAnomalyDetector, 0)
	for _, d := range all {
		switch {
		case len(req.Ids) > 0:
			if !cwStrIn(d.Id, req.Ids) {
				continue
			}
		case !cwStrIn(d.kind(), types):
			continue
		case req.Namespace != "" && d.Namespace != req.Namespace,
			req.MetricName != "" && d.MetricName != req.MetricName,
			!cwDimensionsInclude(d.Dimensions, req.Dimensions):
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].MetricName != out[j].MetricName {
			return out[i].MetricName < out[j].MetricName
		}
		return out[i].Id < out[j].Id
	})
	offset := 0
	if req.NextToken != "" {
		n, err := strconv.Atoi(req.NextToken)
		if err != nil || n < 0 || n > len(out) {
			return nil, "", cwAnomalyFail("InvalidNextToken", "The service returned an invalid NextToken.")
		}
		offset = n
	}
	out = out[offset:]
	next := ""
	if req.MaxResults > 0 && len(out) > req.MaxResults {
		out = out[:req.MaxResults]
		next = strconv.Itoa(offset + req.MaxResults)
	}
	return out, next, nil
}

func registerCloudWatchAnomalyJSON(r *AWSRouter) {
	r.Register("GraniteServiceVersion20100801.PutAnomalyDetector", handleCWJSONPutAnomalyDetector)
	r.Register("GraniteServiceVersion20100801.DescribeAnomalyDetectors", handleCWJSONDescribeAnomalyDetectors)
	r.Register("GraniteServiceVersion20100801.DeleteAnomalyDetector", handleCWJSONDeleteAnomalyDetector)
}

func registerCloudWatchAnomalyCBOR(srv *sim.Server) {
	cwCBOR(srv, "PutAnomalyDetector", handleCWCBORPutAnomalyDetector)
	cwCBOR(srv, "DescribeAnomalyDetectors", handleCWCBORDescribeAnomalyDetectors)
	cwCBOR(srv, "DeleteAnomalyDetector", handleCWCBORDeleteAnomalyDetector)
}

func registerCloudWatchAnomalyQuery(r *AWSQueryRouter) {
	r.Register("PutAnomalyDetector", handleCWQueryPutAnomalyDetector)
	r.Register("DescribeAnomalyDetectors", handleCWQueryDescribeAnomalyDetectors)
	r.Register("DeleteAnomalyDetector", handleCWQueryDeleteAnomalyDetector)
}

type cwAnomalySingleJSON struct {
	AccountId  string        `json:"AccountId" cbor:"AccountId"`
	Namespace  string        `json:"Namespace" cbor:"Namespace"`
	MetricName string        `json:"MetricName" cbor:"MetricName"`
	Stat       string        `json:"Stat" cbor:"Stat"`
	Dimensions []CWDimension `json:"Dimensions" cbor:"Dimensions"`
}

type cwAnomalyRequestJSON struct {
	AnomalyDetectorId           string               `json:"AnomalyDetectorId"`
	AnomalyDetectorIds          []string             `json:"AnomalyDetectorIds"`
	AnomalyDetectorTypes        []string             `json:"AnomalyDetectorTypes"`
	MaxResults                  int                  `json:"MaxResults"`
	NextToken                   string               `json:"NextToken"`
	Namespace                   string               `json:"Namespace"`
	MetricName                  string               `json:"MetricName"`
	Stat                        string               `json:"Stat"`
	Dimensions                  []CWDimension        `json:"Dimensions"`
	SingleMetricAnomalyDetector *cwAnomalySingleJSON `json:"SingleMetricAnomalyDetector"`
	MetricMathAnomalyDetector   *struct {
		MetricDataQueries []CWMetricDataQuery `json:"MetricDataQueries"`
	} `json:"MetricMathAnomalyDetector"`
	Configuration *struct {
		ExcludedTimeRanges []struct {
			StartTime float64 `json:"StartTime"`
			EndTime   float64 `json:"EndTime"`
		} `json:"ExcludedTimeRanges"`
		MetricTimezone           string `json:"MetricTimezone"`
		AdaptationSpeed          string `json:"AdaptationSpeed"`
		OutlierFilteringStrength string `json:"OutlierFilteringStrength"`
	} `json:"Configuration"`
	MetricCharacteristics *struct {
		PeriodicSpikes *bool `json:"PeriodicSpikes"`
	} `json:"MetricCharacteristics"`
}

func (j cwAnomalyRequestJSON) request() cwAnomalyRequest {
	req := cwAnomalyRequest{
		Namespace: j.Namespace, MetricName: j.MetricName, Stat: j.Stat, Dimensions: j.Dimensions,
		Id: j.AnomalyDetectorId, Ids: j.AnomalyDetectorIds, Types: j.AnomalyDetectorTypes,
		MaxResults: j.MaxResults, NextToken: j.NextToken,
	}
	if s := j.SingleMetricAnomalyDetector; s != nil {
		req.Single = &CWAnomalyDetector{AccountId: s.AccountId, Namespace: s.Namespace, MetricName: s.MetricName, Stat: s.Stat, Dimensions: s.Dimensions}
	}
	if m := j.MetricMathAnomalyDetector; m != nil {
		req.HasMath, req.Math = true, m.MetricDataQueries
	}
	if c := j.Configuration; c != nil {
		conf := CWAnomalyConfiguration{MetricTimezone: c.MetricTimezone, AdaptationSpeed: c.AdaptationSpeed, OutlierFilteringStrength: c.OutlierFilteringStrength}
		for _, r := range c.ExcludedTimeRanges {
			conf.ExcludedTimeRanges = append(conf.ExcludedTimeRanges, CWAnomalyExcludedRange{StartTime: int64(r.StartTime), EndTime: int64(r.EndTime)})
		}
		req.Configuration = &conf
	}
	if mc := j.MetricCharacteristics; mc != nil {
		req.PeriodicSpikes = mc.PeriodicSpikes
	}
	return req
}

func handleCWJSONPutAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var j cwAnomalyRequestJSON
	if err := sim.ReadJSON(r, &j); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	id, problem := cwAnomalyPut(j.request())
	if problem != nil {
		AWSError(w, problem.code, problem.msg, http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AnomalyDetectorId": id})
}

func handleCWJSONDescribeAnomalyDetectors(w http.ResponseWriter, r *http.Request) {
	var j cwAnomalyRequestJSON
	if err := sim.ReadJSON(r, &j); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	detectors, next, problem := cwAnomalyDescribe(j.request())
	if problem != nil {
		AWSError(w, problem.code, problem.msg, http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0, len(detectors))
	for _, d := range detectors {
		out = append(out, cwAnomalyDetectorDocument(d, func(t int64) any { return float64(t) }))
	}
	resp := map[string]any{"AnomalyDetectors": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleCWJSONDeleteAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var j cwAnomalyRequestJSON
	if err := sim.ReadJSON(r, &j); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if problem := cwAnomalyDelete(j.request()); problem != nil {
		AWSError(w, problem.code, problem.msg, http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// cwAnomalyDetectorDocument renders an AnomalyDetector for the JSON and CBOR
// protocols; timestamp renders an epoch second as the protocol's timestamp.
func cwAnomalyDetectorDocument(d CWAnomalyDetector, timestamp func(int64) any) map[string]any {
	dims := make([]map[string]string, 0, len(d.Dimensions))
	for _, dim := range d.Dimensions {
		dims = append(dims, map[string]string{"Name": dim.Name, "Value": dim.Value})
	}
	ranges := make([]map[string]any, 0, len(d.Configuration.ExcludedTimeRanges))
	for _, r := range d.Configuration.ExcludedTimeRanges {
		ranges = append(ranges, map[string]any{"StartTime": timestamp(r.StartTime), "EndTime": timestamp(r.EndTime)})
	}
	configuration := map[string]any{"ExcludedTimeRanges": ranges}
	if d.Configuration.MetricTimezone != "" {
		configuration["MetricTimezone"] = d.Configuration.MetricTimezone
	}
	if d.Configuration.AdaptationSpeed != "" {
		configuration["AdaptationSpeed"] = d.Configuration.AdaptationSpeed
	}
	if d.Configuration.OutlierFilteringStrength != "" {
		configuration["OutlierFilteringStrength"] = d.Configuration.OutlierFilteringStrength
	}
	out := map[string]any{
		"AnomalyDetectorId":     d.Id,
		"Configuration":         configuration,
		"MetricCharacteristics": cwAnomalyCharacteristics(d),
		"StateValue":            d.state(),
	}
	if d.kind() == cwAnomalyMetricMath {
		out["MetricMathAnomalyDetector"] = map[string]any{"MetricDataQueries": d.MathQueries}
		return out
	}
	out["Namespace"], out["MetricName"], out["Dimensions"], out["Stat"] = d.Namespace, d.MetricName, dims, d.Stat
	out["SingleMetricAnomalyDetector"] = map[string]any{
		"AccountId":  d.AccountId,
		"Namespace":  d.Namespace,
		"MetricName": d.MetricName,
		"Dimensions": dims,
		"Stat":       d.Stat,
	}
	return out
}

func cwAnomalyCharacteristics(d CWAnomalyDetector) map[string]any {
	out := map[string]any{}
	if d.PeriodicSpikes != nil {
		out["PeriodicSpikes"] = *d.PeriodicSpikes
	}
	return out
}

type cwAnomalyRequestCBOR struct {
	AnomalyDetectorId           string               `cbor:"AnomalyDetectorId"`
	AnomalyDetectorIds          []string             `cbor:"AnomalyDetectorIds"`
	AnomalyDetectorTypes        []string             `cbor:"AnomalyDetectorTypes"`
	MaxResults                  int                  `cbor:"MaxResults"`
	NextToken                   string               `cbor:"NextToken"`
	Namespace                   string               `cbor:"Namespace"`
	MetricName                  string               `cbor:"MetricName"`
	Stat                        string               `cbor:"Stat"`
	Dimensions                  []CWDimension        `cbor:"Dimensions"`
	SingleMetricAnomalyDetector *cwAnomalySingleJSON `cbor:"SingleMetricAnomalyDetector"`
	MetricMathAnomalyDetector   *struct {
		MetricDataQueries []CWMetricDataQuery `cbor:"MetricDataQueries"`
	} `cbor:"MetricMathAnomalyDetector"`
	Configuration *struct {
		ExcludedTimeRanges []struct {
			StartTime time.Time `cbor:"StartTime"`
			EndTime   time.Time `cbor:"EndTime"`
		} `cbor:"ExcludedTimeRanges"`
		MetricTimezone           string `cbor:"MetricTimezone"`
		AdaptationSpeed          string `cbor:"AdaptationSpeed"`
		OutlierFilteringStrength string `cbor:"OutlierFilteringStrength"`
	} `cbor:"Configuration"`
	MetricCharacteristics *struct {
		PeriodicSpikes *bool `cbor:"PeriodicSpikes"`
	} `cbor:"MetricCharacteristics"`
}

func (c cwAnomalyRequestCBOR) request() cwAnomalyRequest {
	j := cwAnomalyRequestJSON{
		AnomalyDetectorId: c.AnomalyDetectorId, AnomalyDetectorIds: c.AnomalyDetectorIds, AnomalyDetectorTypes: c.AnomalyDetectorTypes,
		MaxResults: c.MaxResults, NextToken: c.NextToken,
		Namespace: c.Namespace, MetricName: c.MetricName, Stat: c.Stat, Dimensions: c.Dimensions,
		SingleMetricAnomalyDetector: c.SingleMetricAnomalyDetector,
	}
	req := j.request()
	if m := c.MetricMathAnomalyDetector; m != nil {
		req.HasMath, req.Math = true, m.MetricDataQueries
	}
	if conf := c.Configuration; conf != nil {
		out := CWAnomalyConfiguration{MetricTimezone: conf.MetricTimezone, AdaptationSpeed: conf.AdaptationSpeed, OutlierFilteringStrength: conf.OutlierFilteringStrength}
		for _, r := range conf.ExcludedTimeRanges {
			out.ExcludedTimeRanges = append(out.ExcludedTimeRanges, CWAnomalyExcludedRange{StartTime: r.StartTime.Unix(), EndTime: r.EndTime.Unix()})
		}
		req.Configuration = &out
	}
	if mc := c.MetricCharacteristics; mc != nil {
		req.PeriodicSpikes = mc.PeriodicSpikes
	}
	return req
}

func handleCWCBORPutAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var c cwAnomalyRequestCBOR
	if !cwReadCBOR(w, r, &c) {
		return
	}
	id, problem := cwAnomalyPut(c.request())
	if problem != nil {
		cwWriteCBORError(w, problem.code, problem.msg, http.StatusBadRequest)
		return
	}
	cwWriteCBOR(w, map[string]any{"AnomalyDetectorId": id})
}

func handleCWCBORDescribeAnomalyDetectors(w http.ResponseWriter, r *http.Request) {
	var c cwAnomalyRequestCBOR
	if !cwReadCBOR(w, r, &c) {
		return
	}
	detectors, next, problem := cwAnomalyDescribe(c.request())
	if problem != nil {
		cwWriteCBORError(w, problem.code, problem.msg, http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0, len(detectors))
	for _, d := range detectors {
		out = append(out, cwAnomalyDetectorDocument(d, func(t int64) any { return time.Unix(t, 0).UTC() }))
	}
	resp := map[string]any{"AnomalyDetectors": out}
	if next != "" {
		resp["NextToken"] = next
	}
	cwWriteCBOR(w, resp)
}

func handleCWCBORDeleteAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var c cwAnomalyRequestCBOR
	if !cwReadCBOR(w, r, &c) {
		return
	}
	if problem := cwAnomalyDelete(c.request()); problem != nil {
		cwWriteCBORError(w, problem.code, problem.msg, http.StatusBadRequest)
		return
	}
	cwWriteCBOR(w, map[string]any{})
}

func cwQueryAnomalyRequest(r *http.Request) (cwAnomalyRequest, string) {
	req := cwAnomalyRequest{
		Namespace:  r.FormValue("Namespace"),
		MetricName: r.FormValue("MetricName"),
		Stat:       r.FormValue("Stat"),
		Dimensions: cwQueryDimensions(r, "Dimensions"),
		Id:         r.FormValue("AnomalyDetectorId"),
		Ids:        cwQueryStringList(r, "AnomalyDetectorIds"),
		Types:      cwQueryStringList(r, "AnomalyDetectorTypes"),
		NextToken:  r.FormValue("NextToken"),
	}
	if v := r.FormValue("MaxResults"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return req, "The parameter MaxResults must be an integer."
		}
		req.MaxResults = n
	}
	const single = "SingleMetricAnomalyDetector."
	if r.FormValue(single+"Namespace") != "" || r.FormValue(single+"MetricName") != "" || r.FormValue(single+"Stat") != "" || len(cwQueryDimensions(r, single+"Dimensions")) > 0 {
		req.Single = &CWAnomalyDetector{
			AccountId:  r.FormValue(single + "AccountId"),
			Namespace:  r.FormValue(single + "Namespace"),
			MetricName: r.FormValue(single + "MetricName"),
			Stat:       r.FormValue(single + "Stat"),
			Dimensions: cwQueryDimensions(r, single+"Dimensions"),
		}
	}
	queries, invalid := cwQueryMetricDataQueries(r, "MetricMathAnomalyDetector.MetricDataQueries")
	if invalid != "" {
		return req, invalid
	}
	if len(queries) > 0 {
		req.HasMath, req.Math = true, queries
	}
	conf := CWAnomalyConfiguration{
		MetricTimezone:           r.FormValue("Configuration.MetricTimezone"),
		AdaptationSpeed:          r.FormValue("Configuration.AdaptationSpeed"),
		OutlierFilteringStrength: r.FormValue("Configuration.OutlierFilteringStrength"),
	}
	for i := 1; ; i++ {
		base := "Configuration.ExcludedTimeRanges.member." + strconv.Itoa(i)
		start, end := r.FormValue(base+".StartTime"), r.FormValue(base+".EndTime")
		if start == "" && end == "" {
			break
		}
		conf.ExcludedTimeRanges = append(conf.ExcludedTimeRanges, CWAnomalyExcludedRange{StartTime: int64(cwParseTimeUnix(start)), EndTime: int64(cwParseTimeUnix(end))})
	}
	req.Configuration = &conf
	if v := r.FormValue("MetricCharacteristics.PeriodicSpikes"); v != "" {
		spikes := v == "true"
		req.PeriodicSpikes = &spikes
	}
	return req, ""
}

func handleCWQueryPutAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	req, invalid := cwQueryAnomalyRequest(r)
	if invalid != "" {
		cwQueryError(w, "InvalidParameterValue", invalid)
		return
	}
	id, problem := cwAnomalyPut(req)
	if problem != nil {
		cwQueryError(w, problem.code, problem.msg)
		return
	}
	cwQueryResult(w, "PutAnomalyDetector", "<AnomalyDetectorId>"+xmlEscape(id)+"</AnomalyDetectorId>")
}

func handleCWQueryDescribeAnomalyDetectors(w http.ResponseWriter, r *http.Request) {
	req, invalid := cwQueryAnomalyRequest(r)
	if invalid != "" {
		cwQueryError(w, "InvalidParameterValue", invalid)
		return
	}
	detectors, next, problem := cwAnomalyDescribe(req)
	if problem != nil {
		cwQueryError(w, problem.code, problem.msg)
		return
	}
	var b []byte
	b = append(b, "<AnomalyDetectors>"...)
	for _, d := range detectors {
		b = append(b, "<member>"...)
		b = cwQueryAppendf(b, "<AnomalyDetectorId>%s</AnomalyDetectorId>", xmlEscape(d.Id))
		dims := cwQueryDimensionsXML(d.Dimensions)
		if d.kind() == cwAnomalySingleMetric {
			b = cwQueryAppendf(b, "<Namespace>%s</Namespace><MetricName>%s</MetricName>%s<Stat>%s</Stat>", xmlEscape(d.Namespace), xmlEscape(d.MetricName), dims, xmlEscape(d.Stat))
			b = cwQueryAppendf(b, "<SingleMetricAnomalyDetector><AccountId>%s</AccountId><Namespace>%s</Namespace><MetricName>%s</MetricName>%s<Stat>%s</Stat></SingleMetricAnomalyDetector>",
				xmlEscape(d.AccountId), xmlEscape(d.Namespace), xmlEscape(d.MetricName), dims, xmlEscape(d.Stat))
		} else {
			b = cwQueryAppendf(b, "<MetricMathAnomalyDetector>%s</MetricMathAnomalyDetector>", cwMetricDataQueriesXML("MetricDataQueries", d.MathQueries))
		}
		b = append(b, "<Configuration><ExcludedTimeRanges>"...)
		for _, rg := range d.Configuration.ExcludedTimeRanges {
			b = cwQueryAppendf(b, "<member><StartTime>%s</StartTime><EndTime>%s</EndTime></member>",
				time.Unix(rg.StartTime, 0).UTC().Format(time.RFC3339), time.Unix(rg.EndTime, 0).UTC().Format(time.RFC3339))
		}
		b = append(b, "</ExcludedTimeRanges>"...)
		if d.Configuration.MetricTimezone != "" {
			b = cwQueryAppendf(b, "<MetricTimezone>%s</MetricTimezone>", xmlEscape(d.Configuration.MetricTimezone))
		}
		if d.Configuration.AdaptationSpeed != "" {
			b = cwQueryAppendf(b, "<AdaptationSpeed>%s</AdaptationSpeed>", d.Configuration.AdaptationSpeed)
		}
		if d.Configuration.OutlierFilteringStrength != "" {
			b = cwQueryAppendf(b, "<OutlierFilteringStrength>%s</OutlierFilteringStrength>", d.Configuration.OutlierFilteringStrength)
		}
		b = append(b, "</Configuration><MetricCharacteristics>"...)
		if d.PeriodicSpikes != nil {
			b = cwQueryAppendf(b, "<PeriodicSpikes>%t</PeriodicSpikes>", *d.PeriodicSpikes)
		}
		b = cwQueryAppendf(b, "</MetricCharacteristics><StateValue>%s</StateValue></member>", d.state())
	}
	b = append(b, "</AnomalyDetectors>"...)
	if next != "" {
		b = cwQueryAppendf(b, "<NextToken>%s</NextToken>", next)
	}
	cwQueryResult(w, "DescribeAnomalyDetectors", string(b))
}

func cwQueryDimensionsXML(dims []CWDimension) string {
	var b strings.Builder
	b.WriteString("<Dimensions>")
	for _, d := range dims {
		fmt.Fprintf(&b, "<member><Name>%s</Name><Value>%s</Value></member>", xmlEscape(d.Name), xmlEscape(d.Value))
	}
	b.WriteString("</Dimensions>")
	return b.String()
}

func handleCWQueryDeleteAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	req, invalid := cwQueryAnomalyRequest(r)
	if invalid != "" {
		cwQueryError(w, "InvalidParameterValue", invalid)
		return
	}
	if problem := cwAnomalyDelete(req); problem != nil {
		cwQueryError(w, problem.code, problem.msg)
		return
	}
	cwQueryResult(w, "DeleteAnomalyDetector", "")
}
