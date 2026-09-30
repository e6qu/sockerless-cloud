package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func logicTestDefinition(t *testing.T, actions string) map[string]any {
	t.Helper()
	var def map[string]any
	if err := json.Unmarshal([]byte(`{"parameters":{"greeting":{"type":"String","defaultValue":"hello"}},"actions":`+actions+`}`), &def); err != nil {
		t.Fatalf("definition: %v", err)
	}
	return def
}

// A run executes its actions in runAfter order, evaluating expressions over
// variables, parameters, other actions' outputs and Foreach items, and takes
// the branch its condition selects.
func TestLogicRunExecutesActions(t *testing.T) {
	var received []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append(received, r.Method+" "+r.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":` + r.URL.Query().Get("n") + `}`))
	}))
	defer backend.Close()

	def := logicTestDefinition(t, `{
		"init": {"type": "InitializeVariable", "inputs": {"variables": [{"name": "count", "type": "integer", "value": 0}]}},
		"loop": {"type": "Foreach", "foreach": "@createArray(1, 2, 3)", "runAfter": {"init": ["Succeeded"]},
			"actions": {"bump": {"type": "SetVariable", "inputs": {"name": "count", "value": "@add(variables('count'), item())"}}}},
		"call": {"type": "Http", "runAfter": {"loop": ["Succeeded"]},
			"inputs": {"method": "POST", "uri": "`+backend.URL+`/sum", "queries": {"n": "@{variables('count')}"}, "body": {"greeting": "@parameters('greeting')"}}},
		"check": {"type": "If", "runAfter": {"call": ["Succeeded"]},
			"expression": {"and": [{"equals": ["@body('call')?['total']", 6]}]},
			"actions": {"yes": {"type": "Compose", "inputs": "total @{body('call').total} for @{toUpper(parameters('greeting'))}"}},
			"else": {"actions": {"no": {"type": "Compose", "inputs": "wrong"}}}},
		"onFailure": {"type": "Compose", "inputs": "handled", "runAfter": {"check": ["Failed"]}}
	}`)
	outcome := logicExecute(context.Background(), def, nil, map[string]any{})
	if outcome.Status != "Succeeded" {
		t.Fatalf("run status %s: %+v", outcome.Status, outcome.Error)
	}
	if len(received) != 1 || received[0] != `POST /sum {"greeting":"hello"}` {
		t.Fatalf("the Http action sent %v", received)
	}
	if got := outcome.Results["yes"]; got == nil || got.Outputs != "total 6 for HELLO" {
		t.Fatalf("the true branch composed %+v", got)
	}
	if _, ran := outcome.Results["no"]; ran {
		t.Fatal("the else branch ran")
	}
	if got := outcome.Results["onFailure"]; got == nil || got.Status != "Skipped" {
		t.Fatalf("an action running after a failure that did not happen: %+v", got)
	}
}

// An action type the simulator cannot execute fails the run naming the type,
// and a failure an action after it handles leaves the run Succeeded.
func TestLogicRunFailsAnActionItCannotExecute(t *testing.T) {
	def := logicTestDefinition(t, `{
		"send": {"type": "ApiConnection", "inputs": {"host": {"connection": {"name": "office365"}}}}
	}`)
	outcome := logicExecute(context.Background(), def, nil, map[string]any{})
	send := outcome.Results["send"]
	if outcome.Status != "Failed" || send == nil || send.Status != "Failed" ||
		!strings.Contains(send.Error["message"].(string), "ApiConnection") {
		t.Fatalf("run %s, action %+v; want both Failed naming ApiConnection", outcome.Status, send)
	}

	handled := logicTestDefinition(t, `{
		"bad": {"type": "Compose", "inputs": "@nosuchfunction(1)"},
		"recover": {"type": "Compose", "inputs": "@{actions('bad').status}", "runAfter": {"bad": ["Failed"]}}
	}`)
	outcome = logicExecute(context.Background(), handled, nil, map[string]any{})
	if outcome.Status != "Succeeded" || outcome.Results["recover"].Outputs != "Failed" {
		t.Fatalf("a handled failure: run %s, recover %+v", outcome.Status, outcome.Results["recover"])
	}
	if msg, _ := outcome.Results["bad"].Error["message"].(string); !strings.Contains(msg, "'nosuchfunction' is not defined") {
		t.Fatalf("an unknown function failed with %q", msg)
	}
}

// Terminate ends the run with the status and error it names, and the actions
// it cut off do not run.
func TestLogicRunTerminate(t *testing.T) {
	def := logicTestDefinition(t, `{
		"stop": {"type": "Terminate", "inputs": {"runStatus": "Failed", "runError": {"code": "Stopped", "message": "stopped by design"}}},
		"after": {"type": "Compose", "inputs": "never", "runAfter": {"stop": ["Succeeded"]}}
	}`)
	outcome := logicExecute(context.Background(), def, nil, map[string]any{})
	if outcome.Status != "Failed" || outcome.Error["code"] != "Stopped" {
		t.Fatalf("terminated run: %s %+v", outcome.Status, outcome.Error)
	}
	if after := outcome.Results["after"]; after == nil || after.Status != "Skipped" {
		t.Fatalf("an action after Terminate: %+v", after)
	}
}

func TestLogicExpressions(t *testing.T) {
	run := &logicRun{variables: map[string]any{"name": "World", "list": []any{"a", "b"}}, results: map[string]*logicActionResult{}}
	for expr, want := range map[string]any{
		"@concat('Hello, ', variables('name'))":      "Hello, World",
		"Hi @{variables('name')}!":                   "Hi World!",
		"@@literal":                                  "@literal",
		"@equals(true, 1)":                           true,
		"@contains(variables('list'), 'b')":          true,
		"@startsWith('Azure', 'az')":                 true,
		"@length(join(variables('list'), '-'))":      float64(3),
		"@if(greater(2, 1), 'yes', 'no')":            "yes",
		"@json('{\"a\":{\"b\":[1,2]}}')['a'].b[1]":   float64(2),
		"@coalesce(null, json('{}')?['missing'], 5)": float64(5),
		"@substring('abcdef', 1, 3)":                 "bcd",
		"@div(7, 2)":                                 float64(3),
	} {
		got, err := run.evaluate(expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if !logicEquals(got, want) {
			t.Errorf("%s = %#v, want %#v", expr, got, want)
		}
	}
}
