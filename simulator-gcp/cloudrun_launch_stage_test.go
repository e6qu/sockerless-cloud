package main

import (
	"encoding/json"
	"testing"
)

func TestCloudRunLaunchStageReadsTheProtoNumber(t *testing.T) {
	for raw, want := range map[string]launchStageString{
		`{"launchStage":4}`:      "GA",
		`{"launchStage":3}`:      "BETA",
		`{"launchStage":"GA"}`:   "GA",
		`{"launchStage":"BETA"}`: "BETA",
	} {
		var job struct {
			LaunchStage launchStageString `json:"launchStage"`
		}
		if err := json.Unmarshal([]byte(raw), &job); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if job.LaunchStage != want {
			t.Errorf("%s: launch stage %q, want %q", raw, job.LaunchStage, want)
		}
	}
	var job struct {
		LaunchStage launchStageString `json:"launchStage"`
	}
	if err := json.Unmarshal([]byte(`{"launchStage":99}`), &job); err == nil {
		t.Errorf("an undeclared launch stage number decoded as %q", job.LaunchStage)
	}
}
