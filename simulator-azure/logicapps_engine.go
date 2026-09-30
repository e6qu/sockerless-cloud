package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Logic Apps workflow run executes the definition's actions in the order
// their runAfter conditions allow, evaluating Workflow Definition Language
// expressions in their inputs. The engine executes the action types it can
// run for real — Http, Response, Compose, InitializeVariable, SetVariable,
// If (Condition), Foreach, Scope and Terminate — and fails an action of any
// other type, naming the type, rather than reporting work it never did.

// logicActionResult is what one action execution left behind.
type logicActionResult struct {
	Status    string
	Code      string
	Inputs    any
	Outputs   any
	Error     map[string]any
	StartTime time.Time
	EndTime   time.Time
}

// logicRun is the state of one workflow run.
type logicRun struct {
	ctx        context.Context
	parameters map[string]any
	trigger    map[string]any
	results    map[string]*logicActionResult
	order      []string
	variables  map[string]any
	items      []any
	terminated *logicTermination
}

// logicTermination is a Terminate action's verdict on the run.
type logicTermination struct {
	Status string
	Error  map[string]any
}

// logicRunOutcome is the finished run: its status, error, the definition's
// outputs, and every action's result in execution order.
type logicRunOutcome struct {
	Status  string
	Error   map[string]any
	Outputs map[string]any
	Actions []string
	Results map[string]*logicActionResult
}

// logicExecute runs a workflow definition for one trigger firing.
func logicExecute(ctx context.Context, definition, parameters, triggerOutputs map[string]any) logicRunOutcome {
	run := &logicRun{
		ctx:        ctx,
		parameters: logicParameterValues(definition, parameters),
		trigger:    triggerOutputs,
		results:    map[string]*logicActionResult{},
		variables:  map[string]any{},
	}
	actions, _ := definition["actions"].(map[string]any)
	status := run.runActions(actions)
	outcome := logicRunOutcome{Status: status, Actions: run.order, Results: run.results}
	if run.terminated != nil {
		outcome.Status, outcome.Error = run.terminated.Status, run.terminated.Error
	}
	if outcome.Status == "Failed" && outcome.Error == nil {
		outcome.Error = map[string]any{
			"code":    "ActionFailed",
			"message": "An action failed. No dependent actions succeeded.",
		}
	}
	if outputs, ok := definition["outputs"].(map[string]any); ok && len(outputs) > 0 && outcome.Status == "Succeeded" {
		outcome.Outputs = map[string]any{}
		for name, spec := range outputs {
			o, _ := spec.(map[string]any)
			value, err := run.evaluate(o["value"])
			if err != nil {
				outcome.Status = "Failed"
				outcome.Error = map[string]any{"code": "InvalidTemplate", "message": err.Error()}
				break
			}
			outcome.Outputs[name] = map[string]any{"type": o["type"], "value": value}
		}
	}
	return outcome
}

// logicParameterValues resolves each declared parameter to the workflow's
// value for it, or its declared default.
func logicParameterValues(definition, parameters map[string]any) map[string]any {
	values := map[string]any{}
	declared, _ := definition["parameters"].(map[string]any)
	for name, spec := range declared {
		if d, ok := spec.(map[string]any); ok {
			if v, ok := d["defaultValue"]; ok {
				values[name] = v
			}
		}
	}
	for name, spec := range parameters {
		if p, ok := spec.(map[string]any); ok {
			if v, ok := p["value"]; ok {
				values[name] = v
			}
		}
	}
	return values
}

// runActions executes one scope's actions and returns the scope's status:
// Failed when an action failed and no action ran after the failure, else
// Succeeded. An action whose runAfter statuses its predecessors did not reach
// is Skipped.
func (run *logicRun) runActions(actions map[string]any) string {
	names := make([]string, 0, len(actions))
	for name := range actions {
		names = append(names, name)
	}
	sort.Strings(names)
	done := map[string]bool{}
	for run.terminated == nil {
		progressed := false
		for _, name := range names {
			if done[name] {
				continue
			}
			action, _ := actions[name].(map[string]any)
			ready, skip := run.dependencies(action, done)
			if !ready {
				continue
			}
			done[name], progressed = true, true
			if skip {
				now := time.Now().UTC()
				run.record(name, &logicActionResult{Status: "Skipped", Code: "ActionDependencyFailed", StartTime: now, EndTime: now,
					Error: map[string]any{"code": "ActionConditionFailed", "message": fmt.Sprintf("The execution of template action '%s' is skipped: the 'runAfter' condition for the action was not satisfied.", name)}})
				continue
			}
			run.record(name, run.execute(name, action))
			if run.terminated != nil {
				break
			}
		}
		if !progressed {
			break
		}
	}
	for _, name := range names {
		if !done[name] && run.terminated != nil {
			now := time.Now().UTC()
			run.record(name, &logicActionResult{Status: "Skipped", Code: "ActionSkipped", StartTime: now, EndTime: now})
		}
	}
	failedUnhandled := false
	for _, name := range names {
		result := run.results[name]
		if result == nil || (result.Status != "Failed" && result.Status != "TimedOut") {
			continue
		}
		handled := false
		for _, other := range names {
			after, _ := actions[other].(map[string]any)["runAfter"].(map[string]any)
			if _, ok := after[name]; ok && run.results[other] != nil && run.results[other].Status != "Skipped" {
				handled = true
			}
		}
		if !handled {
			failedUnhandled = true
		}
	}
	if failedUnhandled {
		return "Failed"
	}
	return "Succeeded"
}

func (run *logicRun) record(name string, result *logicActionResult) {
	if _, seen := run.results[name]; !seen {
		run.order = append(run.order, name)
	}
	run.results[name] = result
}

// dependencies reports whether an action's predecessors have all finished,
// and whether it must be skipped because one finished in a status its
// runAfter does not list (Succeeded when it lists none).
func (run *logicRun) dependencies(action map[string]any, done map[string]bool) (ready, skip bool) {
	after, _ := action["runAfter"].(map[string]any)
	for predecessor, statuses := range after {
		if !done[predecessor] {
			return false, false
		}
		result := run.results[predecessor]
		wanted, _ := statuses.([]any)
		if len(wanted) == 0 {
			wanted = []any{"Succeeded"}
		}
		matched := false
		for _, w := range wanted {
			if s, _ := w.(string); result != nil && strings.EqualFold(s, result.Status) {
				matched = true
			}
		}
		if !matched {
			skip = true
		}
	}
	return true, skip
}

func (run *logicRun) execute(name string, action map[string]any) *logicActionResult {
	result := &logicActionResult{StartTime: time.Now().UTC()}
	finish := func(status, code string, err error) *logicActionResult {
		result.Status, result.Code, result.EndTime = status, code, time.Now().UTC()
		if err != nil {
			errCode := code
			if errCode == "" || errCode == "OK" {
				errCode = "ActionFailed"
			}
			result.Error = map[string]any{"code": errCode, "message": err.Error()}
		}
		return result
	}
	typ, _ := action["type"].(string)
	switch strings.ToLower(typ) {
	case "compose":
		value, err := run.evaluate(action["inputs"])
		if err != nil {
			return finish("Failed", "InvalidTemplate", err)
		}
		result.Inputs, result.Outputs = value, value
		return finish("Succeeded", "OK", nil)

	case "initializevariable":
		inputs, err := run.evaluate(action["inputs"])
		if err != nil {
			return finish("Failed", "InvalidTemplate", err)
		}
		result.Inputs = inputs
		vars, _ := inputs.(map[string]any)["variables"].([]any)
		for _, v := range vars {
			spec, _ := v.(map[string]any)
			varName, _ := spec["name"].(string)
			if _, exists := run.variables[varName]; exists {
				return finish("Failed", "BadRequest", logicErrorf("The variable '%s' has already been initialized.", varName))
			}
			value, err := logicCoerceVariable(spec["type"], spec["value"])
			if err != nil {
				return finish("Failed", "BadRequest", err)
			}
			run.variables[varName] = value
		}
		return finish("Succeeded", "OK", nil)

	case "setvariable":
		inputs, err := run.evaluate(action["inputs"])
		if err != nil {
			return finish("Failed", "InvalidTemplate", err)
		}
		result.Inputs = inputs
		spec, _ := inputs.(map[string]any)
		varName, _ := spec["name"].(string)
		if _, exists := run.variables[varName]; !exists {
			return finish("Failed", "BadRequest", logicErrorf("The variable '%s' has not been initialized.", varName))
		}
		run.variables[varName] = spec["value"]
		result.Outputs = map[string]any{"body": map[string]any{"name": varName, "value": spec["value"]}}
		return finish("Succeeded", "OK", nil)

	case "if":
		verdict, err := run.evaluateCondition(action["expression"])
		if err != nil {
			return finish("Failed", "InvalidTemplate", err)
		}
		branch, _ := action["actions"].(map[string]any)
		if b, _ := verdict.(bool); !b {
			elseBranch, _ := action["else"].(map[string]any)
			branch, _ = elseBranch["actions"].(map[string]any)
		}
		result.Outputs = map[string]any{"expression": verdict}
		if run.runActions(branch) == "Failed" {
			return finish("Failed", "ActionFailed", logicErrorf("An action failed. No dependent actions succeeded."))
		}
		return finish("Succeeded", "OK", nil)

	case "scope":
		inner, _ := action["actions"].(map[string]any)
		if run.runActions(inner) == "Failed" {
			return finish("Failed", "ActionFailed", logicErrorf("An action failed. No dependent actions succeeded."))
		}
		return finish("Succeeded", "OK", nil)

	case "foreach":
		collection, err := run.evaluate(action["foreach"])
		if err != nil {
			return finish("Failed", "InvalidTemplate", err)
		}
		items, ok := collection.([]any)
		if !ok {
			return finish("Failed", "InvalidTemplate", logicErrorf("The execution of template action '%s' failed: the result of the evaluation of 'foreach' expression is of type '%s'. The result must be a valid array.", name, logicTypeName(collection)))
		}
		inner, _ := action["actions"].(map[string]any)
		failed := false
		for _, item := range items {
			run.items = append(run.items, item)
			if run.runActions(inner) == "Failed" {
				failed = true
			}
			run.items = run.items[:len(run.items)-1]
			if run.terminated != nil {
				break
			}
		}
		if failed {
			return finish("Failed", "ActionFailed", logicErrorf("An action failed. No dependent actions succeeded."))
		}
		return finish("Succeeded", "OK", nil)

	case "terminate":
		inputs, err := run.evaluate(action["inputs"])
		if err != nil {
			return finish("Failed", "InvalidTemplate", err)
		}
		spec, _ := inputs.(map[string]any)
		status, _ := spec["runStatus"].(string)
		termination := &logicTermination{Status: status}
		if runError, ok := spec["runError"].(map[string]any); ok {
			termination.Error = runError
		}
		run.terminated = termination
		result.Inputs = inputs
		return finish("Succeeded", "OK", nil)

	case "response":
		inputs, err := run.evaluate(action["inputs"])
		if err != nil {
			return finish("Failed", "InvalidTemplate", err)
		}
		result.Inputs, result.Outputs = inputs, inputs
		return finish("Succeeded", "OK", nil)

	case "http":
		inputs, err := run.evaluate(action["inputs"])
		if err != nil {
			return finish("Failed", "InvalidTemplate", err)
		}
		result.Inputs = inputs
		outputs, code, err := logicHTTPAction(run.ctx, inputs)
		result.Outputs = outputs
		if err != nil {
			return finish("Failed", code, err)
		}
		return finish("Succeeded", code, nil)
	}
	return finish("Failed", "ActionTypeNotSupported",
		logicErrorf("The simulator cannot execute action '%s' of type '%s'.", name, typ))
}

// logicCoerceVariable checks an initial value against the variable's declared
// type.
func logicCoerceVariable(typ, value any) (any, error) {
	t, _ := typ.(string)
	switch strings.ToLower(t) {
	case "string":
		if value == nil {
			return "", nil
		}
		if _, ok := value.(string); ok {
			return value, nil
		}
	case "integer":
		if value == nil {
			return float64(0), nil
		}
		if f, ok := value.(float64); ok && f == math.Trunc(f) {
			return f, nil
		}
	case "float":
		if value == nil {
			return float64(0), nil
		}
		if _, ok := value.(float64); ok {
			return value, nil
		}
	case "boolean":
		if value == nil {
			return false, nil
		}
		if _, ok := value.(bool); ok {
			return value, nil
		}
	case "array":
		if value == nil {
			return []any{}, nil
		}
		if _, ok := value.([]any); ok {
			return value, nil
		}
	case "object":
		if value == nil {
			return map[string]any{}, nil
		}
		if _, ok := value.(map[string]any); ok {
			return value, nil
		}
	default:
		return nil, logicErrorf("The variable type '%s' is not valid.", t)
	}
	return nil, logicErrorf("The variable value of type '%s' does not match the declared type '%s'.", logicTypeName(value), t)
}

// logicHTTPAction sends an Http action's request and returns its outputs: the
// response's status code, headers and body. A response outside 2xx fails the
// action with the status's name as its code.
func logicHTTPAction(ctx context.Context, inputs any) (map[string]any, string, error) {
	spec, _ := inputs.(map[string]any)
	method, _ := spec["method"].(string)
	uri, _ := spec["uri"].(string)
	if method == "" || uri == "" {
		return nil, "BadRequest", logicErrorf("The Http action requires 'method' and 'uri' inputs.")
	}
	var body io.Reader
	contentType := ""
	switch b := spec["body"].(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		encoded, err := json.Marshal(b)
		if err != nil {
			return nil, "BadRequest", err
		}
		body, contentType = bytes.NewReader(encoded), "application/json"
	}
	// Multitenant Logic Apps fails an outbound request the server has not
	// answered within 120 seconds.
	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, strings.ToUpper(method), uri, body)
	if err != nil {
		return nil, "BadRequest", err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if headers, ok := spec["headers"].(map[string]any); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprint(v))
		}
	}
	if query, ok := spec["queries"].(map[string]any); ok {
		q := req.URL.Query()
		for k, v := range query {
			q.Set(k, fmt.Sprint(v))
		}
		req.URL.RawQuery = q.Encode()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "BadGateway", logicErrorf("Http request failed: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "BadGateway", logicErrorf("Http request failed reading the response: %v", err)
	}
	respHeaders := map[string]any{}
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}
	var respBody any = string(raw)
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			respBody = decoded
		}
	}
	outputs := map[string]any{"statusCode": resp.StatusCode, "headers": respHeaders, "body": respBody}
	code := strings.ReplaceAll(http.StatusText(resp.StatusCode), " ", "")
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return outputs, code, logicErrorf("The Http request returned status %d.", resp.StatusCode)
	}
	return outputs, code, nil
}

// logicFailure is an error whose text is the message Logic Apps reports on
// the action or run, sentence case and all.
type logicFailure string

func (e logicFailure) Error() string { return string(e) }

func logicErrorf(format string, args ...any) error {
	return logicFailure(fmt.Sprintf(format, args...))
}

func logicTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "Null"
	case string:
		return "String"
	case bool:
		return "Boolean"
	case float64, int:
		return "Float"
	case []any:
		return "Array"
	case map[string]any:
		return "Object"
	}
	return fmt.Sprintf("%T", v)
}

// logicActionOutputs is the outputs object an action exposes to outputs()
// and body().
func (run *logicRun) logicActionOutputs(name string) (map[string]any, error) {
	result, ok := run.results[name]
	if !ok {
		return nil, logicErrorf("The template action '%s' is not defined in the template.", name)
	}
	if out, ok := result.Outputs.(map[string]any); ok {
		if _, hasBody := out["body"]; hasBody {
			return out, nil
		}
	}
	return map[string]any{"body": result.Outputs}, nil
}

func logicNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}
