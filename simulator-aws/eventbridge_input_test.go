package main

import (
	"encoding/json"
	"testing"
)

func TestEBApplyInput_StaticAndPath(t *testing.T) {
	detail := `{"instance":"i-123","state":{"code":16}}`

	// Static Input wins.
	if got := ebApplyInput(EBTarget{Input: `"hi"`}, "src", "dt", detail, "e1"); got != `"hi"` {
		t.Errorf("static Input: got %q", got)
	}

	// No input transform → the complete EventBridge event envelope.
	got := ebApplyInput(EBTarget{}, "src", "dt", detail, "e1")
	var event map[string]any
	if err := json.Unmarshal([]byte(got), &event); err != nil {
		t.Fatalf("default event JSON: %v", err)
	}
	if event["id"] != "e1" || event["source"] != "src" || event["detail-type"] != "dt" {
		t.Errorf("default event envelope: %#v", event)
	}
	if nested := event["detail"].(map[string]any); nested["instance"] != "i-123" {
		t.Errorf("default event detail: %#v", nested)
	}

	// InputPath extracts a nested value as JSON.
	if got := ebApplyInput(EBTarget{InputPath: "$.detail.instance"}, "src", "dt", detail, "e1"); got != `"i-123"` {
		t.Errorf("InputPath string: got %q", got)
	}
	if got := ebApplyInput(EBTarget{InputPath: "$.detail.state.code"}, "src", "dt", detail, "e1"); got != `16` {
		t.Errorf("InputPath number: got %q", got)
	}
	if got := ebApplyInput(EBTarget{InputPath: "$.detail.missing"}, "src", "dt", detail, "e1"); got != "null" {
		t.Errorf("InputPath missing: got %q", got)
	}
}

func TestEBApplyInput_Transformer(t *testing.T) {
	detail := `{"instance":"i-123","tags":["a","b"]}`
	it, _ := json.Marshal(map[string]any{
		"InputPathsMap": map[string]string{"inst": "$.detail.instance", "first": "$.detail.tags[0]"},
		"InputTemplate": `"Instance <inst> tag <first>"`,
	})
	got := ebApplyInput(EBTarget{InputTransformer: it}, "src", "dt", detail, "e1")
	want := `"Instance i-123 tag a"`
	if got != want {
		t.Errorf("InputTransformer: got %q want %q", got, want)
	}
}
