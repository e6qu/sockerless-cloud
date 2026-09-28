package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A revision written before revisions recorded a status is ACTIVE to both
// listings, and a family prefix is read as the families it names, not as a
// prefix of the "<family>:<revision>" key.
func TestECSTaskDefinitionListingsAgreeOnStatusAndPrefix(t *testing.T) {
	AwaitSimulatorBackground()
	ecsTaskDefinitions = sim.MakeStore[ECSTaskDefinition](nil, "ecs_task_definitions")
	for key, td := range map[string]ECSTaskDefinition{
		"web:1":    {TaskDefinitionArn: ecsArn("task-definition", "web:1"), Family: "web", Revision: 1},
		"web:2":    {TaskDefinitionArn: ecsArn("task-definition", "web:2"), Family: "web", Revision: 2, Status: "INACTIVE"},
		"webapp:1": {TaskDefinitionArn: ecsArn("task-definition", "webapp:1"), Family: "webapp", Revision: 1, Status: "ACTIVE"},
		"worker:1": {TaskDefinitionArn: ecsArn("task-definition", "worker:1"), Family: "worker", Revision: 1, Status: "INACTIVE"},
	} {
		ecsTaskDefinitions.Put(key, td)
	}
	call := func(handler http.HandlerFunc, body string) map[string][]string {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, recorder.Code, recorder.Body)
		}
		var out map[string][]string
		if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	families := call(handleECSListTaskDefinitionFamilies, `{"familyPrefix":"web"}`)["families"]
	if strings.Join(families, ",") != "web,webapp" {
		t.Fatalf("active families under web = %v, want web,webapp", families)
	}
	arns := call(handleECSListTaskDefinitions, `{"familyPrefix":"web:"}`)["taskDefinitionArns"]
	if len(arns) != 0 {
		t.Fatalf("a prefix no family begins with listed %v", arns)
	}
	arns = call(handleECSListTaskDefinitions, `{"familyPrefix":"web"}`)["taskDefinitionArns"]
	if len(arns) != 2 {
		t.Fatalf("active revisions under web = %v, want web:1 and webapp:1", arns)
	}
}
