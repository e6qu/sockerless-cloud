package main

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Anomaly detectors have no name: (Namespace, MetricName, Stat, Dimensions)
// identifies one. Insight rules are named, ENABLED or DISABLED.

type CWAnomalyDetector struct {
	Namespace      string                 `json:"Namespace"`
	MetricName     string                 `json:"MetricName"`
	Stat           string                 `json:"Stat"`
	Dimensions     []CWDimension          `json:"Dimensions,omitempty"`
	Configuration  CWAnomalyConfiguration `json:"Configuration"`
	PeriodicSpikes *bool                  `json:"PeriodicSpikes,omitempty"`
	StateValue     string                 `json:"StateValue,omitempty"`
	TrainedPoints  int                    `json:"TrainedPoints,omitempty"`
	TrainedNewest  int64                  `json:"TrainedNewest,omitempty"`
	TrainedFrom    int64                  `json:"TrainedFrom,omitempty"`
	TrainedTo      int64                  `json:"TrainedTo,omitempty"`
}

// cwPutAnomalyDetector keeps a detector's training and re-trains it on the
// next pass, as a changed configuration changes what the model learns from.
func cwPutAnomalyDetector(d CWAnomalyDetector) {
	key := cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions)
	if existing, ok := cwAnomalyDetectors.Get(key); ok {
		d.StateValue, d.TrainedFrom, d.TrainedTo = existing.StateValue, existing.TrainedFrom, existing.TrainedTo
		d.TrainedPoints = -1
	}
	cwAnomalyDetectors.Put(key, d)
}

type CWInsightRule struct {
	Name                   string            `json:"Name"`
	State                  string            `json:"State"`
	Schema                 string            `json:"Schema"`
	Definition             string            `json:"Definition"`
	ApplyOnTransformedLogs bool              `json:"ApplyOnTransformedLogs"`
	Tags                   map[string]string `json:"-"`
	EnabledWindows         []CWInsightWindow `json:"EnabledWindows,omitempty"`
}

var (
	cwAnomalyDetectors sim.Store[CWAnomalyDetector]
	cwInsightRules     sim.Store[CWInsightRule]
)

func cwAnomalyKey(namespace, metricName, stat string, dims []CWDimension) string {
	return metricsKey(namespace, metricName, dims) + "/stat=" + stat
}

func cwListAnomalyDetectors(namespace, metricName string) []CWAnomalyDetector {
	out := make([]CWAnomalyDetector, 0)
	for _, d := range cwAnomalyDetectors.List() {
		if namespace != "" && d.Namespace != namespace {
			continue
		}
		if metricName != "" && d.MetricName != metricName {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].MetricName < out[j].MetricName
	})
	return out
}

func cwInsightRuleArn(name string) string {
	return "arn:aws:cloudwatch:" + awsRegion() + ":" + awsAccountID() + ":insight-rule/" + name
}

func cwInsightRuleByArn(arn string) (CWInsightRule, bool) {
	for _, ir := range cwInsightRules.List() {
		if cwInsightRuleArn(ir.Name) == arn {
			return ir, true
		}
	}
	return CWInsightRule{}, false
}

func cwListInsightRules() []CWInsightRule {
	out := cwInsightRules.List()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func cwSetInsightRulesState(names []string, state string) {
	now := time.Now().UnixMilli()
	for _, n := range names {
		cwInsightRules.Update(n, func(ir *CWInsightRule) { ir.setState(state, now) })
	}
}

// setState opens an ENABLED window or closes the open one.
func (ir *CWInsightRule) setState(state string, nowMillis int64) {
	open := len(ir.EnabledWindows) > 0 && ir.EnabledWindows[len(ir.EnabledWindows)-1].To == 0
	switch {
	case state == "ENABLED" && !open:
		ir.EnabledWindows = append(ir.EnabledWindows, CWInsightWindow{From: nowMillis})
	case state != "ENABLED" && open:
		ir.EnabledWindows[len(ir.EnabledWindows)-1].To = nowMillis
	}
	ir.State = state
}

func registerCloudWatchAnomalyInsightJSON(r *AWSRouter) {
	r.Register("GraniteServiceVersion20100801.PutAnomalyDetector", handleCWJSONPutAnomalyDetector)
	r.Register("GraniteServiceVersion20100801.DescribeAnomalyDetectors", handleCWJSONDescribeAnomalyDetectors)
	r.Register("GraniteServiceVersion20100801.DeleteAnomalyDetector", handleCWJSONDeleteAnomalyDetector)
	r.Register("GraniteServiceVersion20100801.PutInsightRule", handleCWJSONPutInsightRule)
	r.Register("GraniteServiceVersion20100801.DescribeInsightRules", handleCWJSONDescribeInsightRules)
	r.Register("GraniteServiceVersion20100801.EnableInsightRules", handleCWJSONEnableInsightRules)
	r.Register("GraniteServiceVersion20100801.DisableInsightRules", handleCWJSONDisableInsightRules)
	r.Register("GraniteServiceVersion20100801.DeleteInsightRules", handleCWJSONDeleteInsightRules)
}

type cwAnomalyDetectorReq struct {
	Namespace     string        `json:"Namespace"`
	MetricName    string        `json:"MetricName"`
	Stat          string        `json:"Stat"`
	Dimensions    []CWDimension `json:"Dimensions"`
	Configuration *struct {
		ExcludedTimeRanges []struct {
			StartTime float64 `json:"StartTime"`
			EndTime   float64 `json:"EndTime"`
		} `json:"ExcludedTimeRanges"`
		MetricTimezone string `json:"MetricTimezone"`
	} `json:"Configuration"`
	MetricCharacteristics *struct {
		PeriodicSpikes *bool `json:"PeriodicSpikes"`
	} `json:"MetricCharacteristics"`
	SingleMetricAnomalyDetector *struct {
		Namespace  string        `json:"Namespace"`
		MetricName string        `json:"MetricName"`
		Stat       string        `json:"Stat"`
		Dimensions []CWDimension `json:"Dimensions"`
	} `json:"SingleMetricAnomalyDetector"`
}

// Newer SDKs send the SingleMetricAnomalyDetector nesting, older ones the
// top-level fields.
func (req cwAnomalyDetectorReq) resolve() CWAnomalyDetector {
	d := CWAnomalyDetector{Namespace: req.Namespace, MetricName: req.MetricName, Stat: req.Stat, Dimensions: req.Dimensions}
	if c := req.Configuration; c != nil {
		d.Configuration.MetricTimezone = c.MetricTimezone
		for _, r := range c.ExcludedTimeRanges {
			d.Configuration.ExcludedTimeRanges = append(d.Configuration.ExcludedTimeRanges, CWAnomalyExcludedRange{StartTime: int64(r.StartTime), EndTime: int64(r.EndTime)})
		}
	}
	if mc := req.MetricCharacteristics; mc != nil {
		d.PeriodicSpikes = mc.PeriodicSpikes
	}
	if s := req.SingleMetricAnomalyDetector; s != nil {
		if s.Namespace != "" {
			d.Namespace = s.Namespace
		}
		if s.MetricName != "" {
			d.MetricName = s.MetricName
		}
		if s.Stat != "" {
			d.Stat = s.Stat
		}
		if len(s.Dimensions) > 0 {
			d.Dimensions = s.Dimensions
		}
	}
	return d
}

func handleCWJSONPutAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var req cwAnomalyDetectorReq
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	d := req.resolve()
	if d.Namespace == "" || d.MetricName == "" || d.Stat == "" {
		AWSError(w, "MissingParameter", "Namespace, MetricName and Stat are required.", http.StatusBadRequest)
		return
	}
	if d.Configuration.MetricTimezone != "" {
		if _, err := time.LoadLocation(d.Configuration.MetricTimezone); err != nil {
			AWSError(w, "InvalidParameterValue", "The MetricTimezone is not a valid tz database time zone.", http.StatusBadRequest)
			return
		}
	}
	cwPutAnomalyDetector(d)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONDescribeAnomalyDetectors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string `json:"Namespace"`
		MetricName string `json:"MetricName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0)
	for _, d := range cwListAnomalyDetectors(req.Namespace, req.MetricName) {
		out = append(out, cwJSONAnomalyDetector(d))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AnomalyDetectors": out})
}

func cwJSONAnomalyDetector(d CWAnomalyDetector) map[string]any {
	dims := make([]map[string]string, 0, len(d.Dimensions))
	for _, dim := range d.Dimensions {
		dims = append(dims, map[string]string{"Name": dim.Name, "Value": dim.Value})
	}
	ranges := make([]map[string]any, 0, len(d.Configuration.ExcludedTimeRanges))
	for _, r := range d.Configuration.ExcludedTimeRanges {
		ranges = append(ranges, map[string]any{"StartTime": float64(r.StartTime), "EndTime": float64(r.EndTime)})
	}
	configuration := map[string]any{"ExcludedTimeRanges": ranges}
	if d.Configuration.MetricTimezone != "" {
		configuration["MetricTimezone"] = d.Configuration.MetricTimezone
	}
	characteristics := map[string]any{}
	if d.PeriodicSpikes != nil {
		characteristics["PeriodicSpikes"] = *d.PeriodicSpikes
	}
	return map[string]any{
		"Namespace":             d.Namespace,
		"MetricName":            d.MetricName,
		"Dimensions":            dims,
		"Stat":                  d.Stat,
		"Configuration":         configuration,
		"MetricCharacteristics": characteristics,
		"StateValue":            d.state(),
		"SingleMetricAnomalyDetector": map[string]any{
			"Namespace":  d.Namespace,
			"MetricName": d.MetricName,
			"Dimensions": dims,
			"Stat":       d.Stat,
		},
	}
}

func handleCWJSONDeleteAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var req cwAnomalyDetectorReq
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	d := req.resolve()
	key := cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions)
	if _, ok := cwAnomalyDetectors.Get(key); !ok {
		AWSError(w, "ResourceNotFoundException", "No anomaly detector found for the given metric.", http.StatusBadRequest)
		return
	}
	cwAnomalyDetectors.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONPutInsightRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleName               string    `json:"RuleName"`
		RuleState              string    `json:"RuleState"`
		RuleDefinition         string    `json:"RuleDefinition"`
		ApplyOnTransformedLogs bool      `json:"ApplyOnTransformedLogs"`
		Tags                   []cwTagKV `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.RuleName == "" {
		AWSError(w, "MissingParameter", "The parameter RuleName is required.", http.StatusBadRequest)
		return
	}
	if req.RuleDefinition == "" {
		AWSError(w, "MissingParameter", "The parameter RuleDefinition is required.", http.StatusBadRequest)
		return
	}
	if err := cwPutInsightRule(req.RuleName, req.RuleState, req.RuleDefinition, req.ApplyOnTransformedLogs, req.Tags); err != nil {
		AWSError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func cwPutInsightRule(name, state, definition string, applyOnTransformed bool, tags []cwTagKV) error {
	if _, err := cwParseInsightRuleDefinition(definition); err != nil {
		return err
	}
	if state == "" {
		state = "ENABLED"
	}
	if state != "ENABLED" && state != "DISABLED" {
		return cwRuleProblem("The RuleState must be ENABLED or DISABLED.")
	}
	existing, exists := cwInsightRules.Get(name)
	ir := CWInsightRule{
		Name:                   name,
		Schema:                 `{"Name":"CloudWatchLogRule","Version":1}`,
		Definition:             definition,
		ApplyOnTransformedLogs: applyOnTransformed,
		EnabledWindows:         existing.EnabledWindows,
	}
	ir.setState(state, time.Now().UnixMilli())
	if len(tags) > 0 {
		ir.Tags = map[string]string{}
		for _, t := range tags {
			ir.Tags[t.Key] = t.Value
		}
	} else if exists {
		ir.Tags = existing.Tags
	}
	cwInsightRules.Put(name, ir)
	return nil
}

func handleCWJSONDescribeInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0)
	for _, ir := range cwListInsightRules() {
		out = append(out, map[string]any{
			"Name":                   ir.Name,
			"State":                  ir.State,
			"Schema":                 ir.Schema,
			"Definition":             ir.Definition,
			"ManagedRule":            false,
			"ApplyOnTransformedLogs": ir.ApplyOnTransformedLogs,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"InsightRules": out})
}

func handleCWJSONEnableInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleNames []string `json:"RuleNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	cwSetInsightRulesState(req.RuleNames, "ENABLED")
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Failures": []any{}})
}

func handleCWJSONDisableInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleNames []string `json:"RuleNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	cwSetInsightRulesState(req.RuleNames, "DISABLED")
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Failures": []any{}})
}

func handleCWJSONDeleteInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleNames []string `json:"RuleNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	for _, n := range req.RuleNames {
		cwInsightRules.Delete(n)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Failures": []any{}})
}

func registerCloudWatchAnomalyInsightCBOR(srv *sim.Server) {
	cwCBOR(srv, "PutAnomalyDetector", handleCWCBORPutAnomalyDetector)
	cwCBOR(srv, "DescribeAnomalyDetectors", handleCWCBORDescribeAnomalyDetectors)
	cwCBOR(srv, "DeleteAnomalyDetector", handleCWCBORDeleteAnomalyDetector)
	cwCBOR(srv, "PutInsightRule", handleCWCBORPutInsightRule)
	cwCBOR(srv, "DescribeInsightRules", handleCWCBORDescribeInsightRules)
	cwCBOR(srv, "EnableInsightRules", handleCWCBOREnableInsightRules)
	cwCBOR(srv, "DisableInsightRules", handleCWCBORDisableInsightRules)
	cwCBOR(srv, "DeleteInsightRules", handleCWCBORDeleteInsightRules)
}

type cwAnomalyDetectorCBORReq struct {
	Namespace             string                    `cbor:"Namespace"`
	MetricName            string                    `cbor:"MetricName"`
	Stat                  string                    `cbor:"Stat"`
	Dimensions            []CWDimension             `cbor:"Dimensions"`
	Configuration         *cborAnomalyConfiguration `cbor:"Configuration"`
	MetricCharacteristics *struct {
		PeriodicSpikes *bool `cbor:"PeriodicSpikes"`
	} `cbor:"MetricCharacteristics"`
	SingleMetricAnomalyDetector *struct {
		Namespace  string        `cbor:"Namespace"`
		MetricName string        `cbor:"MetricName"`
		Stat       string        `cbor:"Stat"`
		Dimensions []CWDimension `cbor:"Dimensions"`
	} `cbor:"SingleMetricAnomalyDetector"`
}

type cborAnomalyConfiguration struct {
	ExcludedTimeRanges []cborAnomalyRange `cbor:"ExcludedTimeRanges"`
	MetricTimezone     string             `cbor:"MetricTimezone,omitempty"`
}

type cborAnomalyRange struct {
	StartTime time.Time `cbor:"StartTime"`
	EndTime   time.Time `cbor:"EndTime"`
}

func cborAnomalyConfigurationOf(c CWAnomalyConfiguration) cborAnomalyConfiguration {
	out := cborAnomalyConfiguration{ExcludedTimeRanges: []cborAnomalyRange{}, MetricTimezone: c.MetricTimezone}
	for _, r := range c.ExcludedTimeRanges {
		out.ExcludedTimeRanges = append(out.ExcludedTimeRanges, cborAnomalyRange{StartTime: time.Unix(r.StartTime, 0).UTC(), EndTime: time.Unix(r.EndTime, 0).UTC()})
	}
	return out
}

func (req cwAnomalyDetectorCBORReq) resolve() CWAnomalyDetector {
	d := CWAnomalyDetector{Namespace: req.Namespace, MetricName: req.MetricName, Stat: req.Stat, Dimensions: req.Dimensions}
	if c := req.Configuration; c != nil {
		d.Configuration.MetricTimezone = c.MetricTimezone
		for _, r := range c.ExcludedTimeRanges {
			d.Configuration.ExcludedTimeRanges = append(d.Configuration.ExcludedTimeRanges, CWAnomalyExcludedRange{StartTime: r.StartTime.Unix(), EndTime: r.EndTime.Unix()})
		}
	}
	if mc := req.MetricCharacteristics; mc != nil {
		d.PeriodicSpikes = mc.PeriodicSpikes
	}
	if s := req.SingleMetricAnomalyDetector; s != nil {
		if s.Namespace != "" {
			d.Namespace = s.Namespace
		}
		if s.MetricName != "" {
			d.MetricName = s.MetricName
		}
		if s.Stat != "" {
			d.Stat = s.Stat
		}
		if len(s.Dimensions) > 0 {
			d.Dimensions = s.Dimensions
		}
	}
	return d
}

func handleCWCBORPutAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var req cwAnomalyDetectorCBORReq
	if !cwReadCBOR(w, r, &req) {
		return
	}
	d := req.resolve()
	if d.Namespace == "" || d.MetricName == "" || d.Stat == "" {
		cwWriteCBORError(w, "MissingParameter", "Namespace, MetricName and Stat are required.", http.StatusBadRequest)
		return
	}
	if d.Configuration.MetricTimezone != "" {
		if _, err := time.LoadLocation(d.Configuration.MetricTimezone); err != nil {
			cwWriteCBORError(w, "InvalidParameterValue", "The MetricTimezone is not a valid tz database time zone.", http.StatusBadRequest)
			return
		}
	}
	cwPutAnomalyDetector(d)
	cwWriteCBOR(w, map[string]any{})
}

type cborAnomalyDetector struct {
	Namespace                   string                     `cbor:"Namespace"`
	MetricName                  string                     `cbor:"MetricName"`
	Dimensions                  []CWDimension              `cbor:"Dimensions,omitempty"`
	Stat                        string                     `cbor:"Stat"`
	Configuration               cborAnomalyConfiguration   `cbor:"Configuration"`
	MetricCharacteristics       map[string]any             `cbor:"MetricCharacteristics"`
	StateValue                  string                     `cbor:"StateValue"`
	SingleMetricAnomalyDetector cborSingleMetricAnomalyDet `cbor:"SingleMetricAnomalyDetector"`
}

type cborSingleMetricAnomalyDet struct {
	Namespace  string        `cbor:"Namespace"`
	MetricName string        `cbor:"MetricName"`
	Dimensions []CWDimension `cbor:"Dimensions,omitempty"`
	Stat       string        `cbor:"Stat"`
}

func handleCWCBORDescribeAnomalyDetectors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string `cbor:"Namespace"`
		MetricName string `cbor:"MetricName"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	out := make([]cborAnomalyDetector, 0)
	for _, d := range cwListAnomalyDetectors(req.Namespace, req.MetricName) {
		out = append(out, cborAnomalyDetector{
			Namespace:                   d.Namespace,
			MetricName:                  d.MetricName,
			Dimensions:                  d.Dimensions,
			Stat:                        d.Stat,
			Configuration:               cborAnomalyConfigurationOf(d.Configuration),
			MetricCharacteristics:       cwAnomalyCharacteristics(d),
			StateValue:                  d.state(),
			SingleMetricAnomalyDetector: cborSingleMetricAnomalyDet{Namespace: d.Namespace, MetricName: d.MetricName, Dimensions: d.Dimensions, Stat: d.Stat},
		})
	}
	cwWriteCBOR(w, map[string]any{"AnomalyDetectors": out})
}

func handleCWCBORDeleteAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	var req cwAnomalyDetectorCBORReq
	if !cwReadCBOR(w, r, &req) {
		return
	}
	d := req.resolve()
	key := cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions)
	if _, ok := cwAnomalyDetectors.Get(key); !ok {
		cwWriteCBORError(w, "ResourceNotFoundException", "No anomaly detector found for the given metric.", http.StatusBadRequest)
		return
	}
	cwAnomalyDetectors.Delete(key)
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORPutInsightRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleName               string    `cbor:"RuleName"`
		RuleState              string    `cbor:"RuleState"`
		RuleDefinition         string    `cbor:"RuleDefinition"`
		ApplyOnTransformedLogs bool      `cbor:"ApplyOnTransformedLogs"`
		Tags                   []cwTagKV `cbor:"Tags"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.RuleName == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter RuleName is required.", http.StatusBadRequest)
		return
	}
	if req.RuleDefinition == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter RuleDefinition is required.", http.StatusBadRequest)
		return
	}
	if err := cwPutInsightRule(req.RuleName, req.RuleState, req.RuleDefinition, req.ApplyOnTransformedLogs, req.Tags); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return
	}
	cwWriteCBOR(w, map[string]any{})
}

type cborInsightRule struct {
	Name                   string `cbor:"Name"`
	State                  string `cbor:"State"`
	Schema                 string `cbor:"Schema"`
	Definition             string `cbor:"Definition"`
	ManagedRule            bool   `cbor:"ManagedRule"`
	ApplyOnTransformedLogs bool   `cbor:"ApplyOnTransformedLogs"`
}

func handleCWCBORDescribeInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	out := make([]cborInsightRule, 0)
	for _, ir := range cwListInsightRules() {
		out = append(out, cborInsightRule{
			Name:                   ir.Name,
			State:                  ir.State,
			Schema:                 ir.Schema,
			Definition:             ir.Definition,
			ManagedRule:            false,
			ApplyOnTransformedLogs: ir.ApplyOnTransformedLogs,
		})
	}
	cwWriteCBOR(w, map[string]any{"InsightRules": out})
}

func handleCWCBOREnableInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleNames []string `cbor:"RuleNames"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	cwSetInsightRulesState(req.RuleNames, "ENABLED")
	cwWriteCBOR(w, map[string]any{"Failures": []any{}})
}

func handleCWCBORDisableInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleNames []string `cbor:"RuleNames"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	cwSetInsightRulesState(req.RuleNames, "DISABLED")
	cwWriteCBOR(w, map[string]any{"Failures": []any{}})
}

func handleCWCBORDeleteInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleNames []string `cbor:"RuleNames"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	for _, n := range req.RuleNames {
		cwInsightRules.Delete(n)
	}
	cwWriteCBOR(w, map[string]any{"Failures": []any{}})
}

func registerCloudWatchAnomalyInsightQuery(r *AWSQueryRouter) {
	r.Register("PutAnomalyDetector", handleCWQueryPutAnomalyDetector)
	r.Register("DescribeAnomalyDetectors", handleCWQueryDescribeAnomalyDetectors)
	r.Register("DeleteAnomalyDetector", handleCWQueryDeleteAnomalyDetector)
	r.Register("PutInsightRule", handleCWQueryPutInsightRule)
	r.Register("DescribeInsightRules", handleCWQueryDescribeInsightRules)
	r.Register("EnableInsightRules", handleCWQueryEnableInsightRules)
	r.Register("DisableInsightRules", handleCWQueryDisableInsightRules)
	r.Register("DeleteInsightRules", handleCWQueryDeleteInsightRules)
}

// Accept the top-level parameters and the SingleMetricAnomalyDetector nesting.
func cwQueryAnomalyIdentity(r *http.Request) CWAnomalyDetector {
	d := CWAnomalyDetector{
		Namespace:  r.FormValue("Namespace"),
		MetricName: r.FormValue("MetricName"),
		Stat:       r.FormValue("Stat"),
		Dimensions: cwQueryDimensions(r, "Dimensions"),
	}
	if ns := r.FormValue("SingleMetricAnomalyDetector.Namespace"); ns != "" {
		d.Namespace = ns
	}
	if mn := r.FormValue("SingleMetricAnomalyDetector.MetricName"); mn != "" {
		d.MetricName = mn
	}
	if st := r.FormValue("SingleMetricAnomalyDetector.Stat"); st != "" {
		d.Stat = st
	}
	if dims := cwQueryDimensions(r, "SingleMetricAnomalyDetector.Dimensions"); len(dims) > 0 {
		d.Dimensions = dims
	}
	d.Configuration.MetricTimezone = r.FormValue("Configuration.MetricTimezone")
	for i := 1; ; i++ {
		base := "Configuration.ExcludedTimeRanges.member." + strconv.Itoa(i)
		start, end := r.FormValue(base+".StartTime"), r.FormValue(base+".EndTime")
		if start == "" && end == "" {
			break
		}
		d.Configuration.ExcludedTimeRanges = append(d.Configuration.ExcludedTimeRanges,
			CWAnomalyExcludedRange{StartTime: int64(cwParseTimeUnix(start)), EndTime: int64(cwParseTimeUnix(end))})
	}
	if v := r.FormValue("MetricCharacteristics.PeriodicSpikes"); v != "" {
		spikes := v == "true"
		d.PeriodicSpikes = &spikes
	}
	return d
}

func cwAnomalyCharacteristics(d CWAnomalyDetector) map[string]any {
	out := map[string]any{}
	if d.PeriodicSpikes != nil {
		out["PeriodicSpikes"] = *d.PeriodicSpikes
	}
	return out
}

func handleCWQueryPutAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	d := cwQueryAnomalyIdentity(r)
	if d.Namespace == "" || d.MetricName == "" || d.Stat == "" {
		cwQueryError(w, "MissingParameter", "Namespace, MetricName and Stat are required.")
		return
	}
	if d.Configuration.MetricTimezone != "" {
		if _, err := time.LoadLocation(d.Configuration.MetricTimezone); err != nil {
			cwQueryError(w, "InvalidParameterValue", "The MetricTimezone is not a valid tz database time zone.")
			return
		}
	}
	cwPutAnomalyDetector(d)
	cwQueryResult(w, "PutAnomalyDetector", "")
}

func handleCWQueryDescribeAnomalyDetectors(w http.ResponseWriter, r *http.Request) {
	var b []byte
	b = append(b, []byte("<AnomalyDetectors>")...)
	for _, d := range cwListAnomalyDetectors(r.FormValue("Namespace"), r.FormValue("MetricName")) {
		b = append(b, []byte("<member>")...)
		b = cwQueryAppendf(b, "<Namespace>%s</Namespace><MetricName>%s</MetricName>", xmlEscape(d.Namespace), xmlEscape(d.MetricName))
		b = append(b, []byte("<Dimensions>")...)
		for _, dim := range d.Dimensions {
			b = cwQueryAppendf(b, "<member><Name>%s</Name><Value>%s</Value></member>", xmlEscape(dim.Name), xmlEscape(dim.Value))
		}
		b = append(b, []byte("</Dimensions>")...)
		b = cwQueryAppendf(b, "<Stat>%s</Stat><Configuration><ExcludedTimeRanges>", xmlEscape(d.Stat))
		for _, rg := range d.Configuration.ExcludedTimeRanges {
			b = cwQueryAppendf(b, "<member><StartTime>%s</StartTime><EndTime>%s</EndTime></member>",
				time.Unix(rg.StartTime, 0).UTC().Format(time.RFC3339), time.Unix(rg.EndTime, 0).UTC().Format(time.RFC3339))
		}
		b = append(b, []byte("</ExcludedTimeRanges>")...)
		if d.Configuration.MetricTimezone != "" {
			b = cwQueryAppendf(b, "<MetricTimezone>%s</MetricTimezone>", xmlEscape(d.Configuration.MetricTimezone))
		}
		b = append(b, []byte("</Configuration><MetricCharacteristics>")...)
		if d.PeriodicSpikes != nil {
			b = cwQueryAppendf(b, "<PeriodicSpikes>%t</PeriodicSpikes>", *d.PeriodicSpikes)
		}
		b = cwQueryAppendf(b, "</MetricCharacteristics><StateValue>%s</StateValue>", d.state())
		b = append(b, []byte("</member>")...)
	}
	b = append(b, []byte("</AnomalyDetectors>")...)
	cwQueryResult(w, "DescribeAnomalyDetectors", string(b))
}

func handleCWQueryDeleteAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	d := cwQueryAnomalyIdentity(r)
	key := cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions)
	if _, ok := cwAnomalyDetectors.Get(key); !ok {
		cwQueryError(w, "ResourceNotFoundException", "No anomaly detector found for the given metric.")
		return
	}
	cwAnomalyDetectors.Delete(key)
	cwQueryResult(w, "DeleteAnomalyDetector", "")
}

func handleCWQueryPutInsightRule(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("RuleName")
	def := r.FormValue("RuleDefinition")
	if name == "" {
		cwQueryError(w, "MissingParameter", "The parameter RuleName is required.")
		return
	}
	if def == "" {
		cwQueryError(w, "MissingParameter", "The parameter RuleDefinition is required.")
		return
	}
	if err := cwPutInsightRule(name, r.FormValue("RuleState"), def, r.FormValue("ApplyOnTransformedLogs") == "true", cwQueryTags(r)); err != nil {
		cwQueryError(w, "InvalidParameterValue", err.Error())
		return
	}
	cwQueryResult(w, "PutInsightRule", "")
}

func handleCWQueryDescribeInsightRules(w http.ResponseWriter, r *http.Request) {
	var b []byte
	b = append(b, []byte("<InsightRules>")...)
	for _, ir := range cwListInsightRules() {
		b = append(b, []byte("<member>")...)
		b = cwQueryAppendf(b, "<Name>%s</Name><State>%s</State>", xmlEscape(ir.Name), xmlEscape(ir.State))
		b = cwQueryAppendf(b, "<Schema>%s</Schema><Definition>%s</Definition>", xmlEscape(ir.Schema), xmlEscape(ir.Definition))
		b = cwQueryAppendf(b, "<ManagedRule>false</ManagedRule><ApplyOnTransformedLogs>%t</ApplyOnTransformedLogs>", ir.ApplyOnTransformedLogs)
		b = append(b, []byte("</member>")...)
	}
	b = append(b, []byte("</InsightRules>")...)
	cwQueryResult(w, "DescribeInsightRules", string(b))
}

func handleCWQueryEnableInsightRules(w http.ResponseWriter, r *http.Request) {
	cwSetInsightRulesState(cwQueryStringList(r, "RuleNames"), "ENABLED")
	cwQueryResult(w, "EnableInsightRules", "<Failures/>")
}

func handleCWQueryDisableInsightRules(w http.ResponseWriter, r *http.Request) {
	cwSetInsightRulesState(cwQueryStringList(r, "RuleNames"), "DISABLED")
	cwQueryResult(w, "DisableInsightRules", "<Failures/>")
}

func handleCWQueryDeleteInsightRules(w http.ResponseWriter, r *http.Request) {
	for _, n := range cwQueryStringList(r, "RuleNames") {
		cwInsightRules.Delete(n)
	}
	cwQueryResult(w, "DeleteInsightRules", "<Failures/>")
}
