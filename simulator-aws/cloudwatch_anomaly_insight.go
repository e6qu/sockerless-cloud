package main

import (
	"net/http"
	"sort"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch anomaly detectors and Contributor Insights rules.
//
// Anomaly detectors are identified by (Namespace, MetricName, Stat, Dimensions)
// — there is no name; PutAnomalyDetector creates one, DeleteAnomalyDetector
// removes it, DescribeAnomalyDetectors lists with optional filters. Insight
// rules are named resources with a definition and an ENABLED/DISABLED state.
// Both are served on awsJson1.0, rpc-v2-cbor, and query.

// CWAnomalyDetector holds a single-metric anomaly detector's identity. The sim
// reports PENDING_TRAINING — it stores the detector but does not train a model.
type CWAnomalyDetector struct {
	Namespace  string        `json:"Namespace"`
	MetricName string        `json:"MetricName"`
	Stat       string        `json:"Stat"`
	Dimensions []CWDimension `json:"Dimensions,omitempty"`
}

type CWInsightRule struct {
	Name                   string            `json:"Name"`
	State                  string            `json:"State"`
	Schema                 string            `json:"Schema"`
	Definition             string            `json:"Definition"`
	ApplyOnTransformedLogs bool              `json:"ApplyOnTransformedLogs"`
	Tags                   map[string]string `json:"-"`
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
	for _, n := range names {
		cwInsightRules.Update(n, func(ir *CWInsightRule) { ir.State = state })
	}
}

// ── awsJson1.0 surface ──────────────────────────────────────────────────────

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
	Namespace                   string        `json:"Namespace"`
	MetricName                  string        `json:"MetricName"`
	Stat                        string        `json:"Stat"`
	Dimensions                  []CWDimension `json:"Dimensions"`
	SingleMetricAnomalyDetector *struct {
		Namespace  string        `json:"Namespace"`
		MetricName string        `json:"MetricName"`
		Stat       string        `json:"Stat"`
		Dimensions []CWDimension `json:"Dimensions"`
	} `json:"SingleMetricAnomalyDetector"`
}

// resolve flattens the legacy top-level fields and the SingleMetricAnomalyDetector
// nesting into one identity (newer SDKs send the nested form).
func (req cwAnomalyDetectorReq) resolve() CWAnomalyDetector {
	d := CWAnomalyDetector{Namespace: req.Namespace, MetricName: req.MetricName, Stat: req.Stat, Dimensions: req.Dimensions}
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
	cwAnomalyDetectors.Put(cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions), d)
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
	return map[string]any{
		"Namespace":  d.Namespace,
		"MetricName": d.MetricName,
		"Dimensions": dims,
		"Stat":       d.Stat,
		"StateValue": "PENDING_TRAINING",
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
	cwPutInsightRule(req.RuleName, req.RuleState, req.RuleDefinition, req.ApplyOnTransformedLogs, req.Tags)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func cwPutInsightRule(name, state, definition string, applyOnTransformed bool, tags []cwTagKV) {
	if state == "" {
		state = "ENABLED"
	}
	existing, exists := cwInsightRules.Get(name)
	ir := CWInsightRule{
		Name:                   name,
		State:                  state,
		Schema:                 `{"Name":"CloudWatchLogRule","Version":1}`,
		Definition:             definition,
		ApplyOnTransformedLogs: applyOnTransformed,
	}
	if len(tags) > 0 {
		ir.Tags = map[string]string{}
		for _, t := range tags {
			ir.Tags[t.Key] = t.Value
		}
	} else if exists {
		ir.Tags = existing.Tags
	}
	cwInsightRules.Put(name, ir)
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

// ── rpc-v2-cbor surface (Go SDK) ────────────────────────────────────────────

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
	Namespace                   string        `cbor:"Namespace"`
	MetricName                  string        `cbor:"MetricName"`
	Stat                        string        `cbor:"Stat"`
	Dimensions                  []CWDimension `cbor:"Dimensions"`
	SingleMetricAnomalyDetector *struct {
		Namespace  string        `cbor:"Namespace"`
		MetricName string        `cbor:"MetricName"`
		Stat       string        `cbor:"Stat"`
		Dimensions []CWDimension `cbor:"Dimensions"`
	} `cbor:"SingleMetricAnomalyDetector"`
}

func (req cwAnomalyDetectorCBORReq) resolve() CWAnomalyDetector {
	d := CWAnomalyDetector{Namespace: req.Namespace, MetricName: req.MetricName, Stat: req.Stat, Dimensions: req.Dimensions}
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
	cwAnomalyDetectors.Put(cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions), d)
	cwWriteCBOR(w, map[string]any{})
}

type cborAnomalyDetector struct {
	Namespace                   string                     `cbor:"Namespace"`
	MetricName                  string                     `cbor:"MetricName"`
	Dimensions                  []CWDimension              `cbor:"Dimensions,omitempty"`
	Stat                        string                     `cbor:"Stat"`
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
			StateValue:                  "PENDING_TRAINING",
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
	cwPutInsightRule(req.RuleName, req.RuleState, req.RuleDefinition, req.ApplyOnTransformedLogs, req.Tags)
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

// ── query surface (older aws CLI) ───────────────────────────────────────────

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

// cwQueryAnomalyIdentity reads the anomaly detector identity from the query
// form, honouring both the legacy top-level params and the
// SingleMetricAnomalyDetector nesting.
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
	return d
}

func handleCWQueryPutAnomalyDetector(w http.ResponseWriter, r *http.Request) {
	d := cwQueryAnomalyIdentity(r)
	if d.Namespace == "" || d.MetricName == "" || d.Stat == "" {
		cwQueryError(w, "MissingParameter", "Namespace, MetricName and Stat are required.")
		return
	}
	cwAnomalyDetectors.Put(cwAnomalyKey(d.Namespace, d.MetricName, d.Stat, d.Dimensions), d)
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
		b = cwQueryAppendf(b, "<Stat>%s</Stat><StateValue>PENDING_TRAINING</StateValue>", xmlEscape(d.Stat))
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
	cwPutInsightRule(name, r.FormValue("RuleState"), def, r.FormValue("ApplyOnTransformedLogs") == "true", nil)
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
