package main

import (
	"net/http"
	"sort"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Insight rules are named, ENABLED or DISABLED.

type CWInsightRule struct {
	Name                   string            `json:"Name"`
	State                  string            `json:"State"`
	Schema                 string            `json:"Schema"`
	Definition             string            `json:"Definition"`
	ApplyOnTransformedLogs bool              `json:"ApplyOnTransformedLogs"`
	Tags                   map[string]string `json:"-"`
	EnabledWindows         []CWInsightWindow `json:"EnabledWindows,omitempty"`
}

var cwInsightRules sim.Store[CWInsightRule]

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
	registerCloudWatchAnomalyJSON(r)
	r.Register("GraniteServiceVersion20100801.PutInsightRule", handleCWJSONPutInsightRule)
	r.Register("GraniteServiceVersion20100801.DescribeInsightRules", handleCWJSONDescribeInsightRules)
	r.Register("GraniteServiceVersion20100801.EnableInsightRules", handleCWJSONEnableInsightRules)
	r.Register("GraniteServiceVersion20100801.DisableInsightRules", handleCWJSONDisableInsightRules)
	r.Register("GraniteServiceVersion20100801.DeleteInsightRules", handleCWJSONDeleteInsightRules)
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
	registerCloudWatchAnomalyCBOR(srv)
	cwCBOR(srv, "PutInsightRule", handleCWCBORPutInsightRule)
	cwCBOR(srv, "DescribeInsightRules", handleCWCBORDescribeInsightRules)
	cwCBOR(srv, "EnableInsightRules", handleCWCBOREnableInsightRules)
	cwCBOR(srv, "DisableInsightRules", handleCWCBORDisableInsightRules)
	cwCBOR(srv, "DeleteInsightRules", handleCWCBORDeleteInsightRules)
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
	registerCloudWatchAnomalyQuery(r)
	r.Register("PutInsightRule", handleCWQueryPutInsightRule)
	r.Register("DescribeInsightRules", handleCWQueryDescribeInsightRules)
	r.Register("EnableInsightRules", handleCWQueryEnableInsightRules)
	r.Register("DisableInsightRules", handleCWQueryDisableInsightRules)
	r.Register("DeleteInsightRules", handleCWQueryDeleteInsightRules)
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
