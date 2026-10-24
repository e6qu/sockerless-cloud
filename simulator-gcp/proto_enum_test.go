package main

import (
	"encoding/json"
	"testing"
)

// A Go REST client sends an enum as its number and gcloud sends its name;
// both read back as the name, and a number the enum does not declare is
// refused.
func TestProtoEnumAcceptsTheNumberAndAnswersTheName(t *testing.T) {
	t.Parallel()
	var config struct {
		Scaling protoEnum[runScalingMode] `json:"scalingMode"`
	}
	for input, want := range map[string]string{`{"scalingMode":2}`: "MANUAL", `{"scalingMode":"MANUAL"}`: "MANUAL", `{"scalingMode":0}`: ""} {
		config.Scaling = ""
		if err := json.Unmarshal([]byte(input), &config); err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if string(config.Scaling) != want {
			t.Errorf("%s reads as %q, want %q", input, config.Scaling, want)
		}
	}
	if err := json.Unmarshal([]byte(`{"scalingMode":9}`), &config); err == nil {
		t.Error("an undeclared enum number was accepted")
	}
	out, err := json.Marshal(struct {
		Scaling protoEnum[runScalingMode] `json:"scalingMode"`
	}{"MANUAL"})
	if err != nil || string(out) != `{"scalingMode":"MANUAL"}` {
		t.Errorf("the enum writes as %s (%v)", out, err)
	}
}
