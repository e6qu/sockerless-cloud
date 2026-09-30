package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// logicAsyncRequest sends one ARM request to the in-process simulator and
// returns the whole recorded response, headers included.
func logicAsyncRequest(t *testing.T, srv *sim.Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+moveTestARMHost+path+"?api-version=2019-05-01", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+moveARMToken(t))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func logicAsyncRunStatus(t *testing.T, srv *sim.Server, workflowPath, runName string) string {
	t.Helper()
	rec := logicAsyncRequest(t, srv, http.MethodGet, workflowPath+"/runs/"+runName, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get run: %d %s", rec.Code, rec.Body.String())
	}
	var run LogicWorkflowRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	status, _ := run.Properties["status"].(string)
	return status
}

// A trigger run answers 202 with the run's id before the workflow executes;
// the run reads Running while its actions execute and settles when they end.
// A cancel stops a Running run, and a finished run refuses one.
func TestLogicTriggerRunExecutesInTheBackground(t *testing.T) {
	srv := newMoveTestServer(t)
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(backend.Close)
	t.Cleanup(func() { close(release) })

	workflowPath := fmt.Sprintf("/subscriptions/%s/resourceGroups/logic-async-rg/providers/Microsoft.Logic/workflows/held", moveTestSubscription)
	definition := `{"location":"eastus","properties":{"definition":{
		"$schema":"https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
		"triggers":{"manual":{"type":"Request","kind":"Http"}},
		"actions":{"call":{"type":"Http","inputs":{"method":"GET","uri":"` + backend.URL + `/hold"}}}}}}`
	if rec := logicAsyncRequest(t, srv, http.MethodPut, workflowPath, definition); rec.Code != http.StatusOK {
		t.Fatalf("create workflow: %d %s", rec.Code, rec.Body.String())
	}

	fire := func() string {
		t.Helper()
		rec := logicAsyncRequest(t, srv, http.MethodPost, workflowPath+"/triggers/manual/run", "")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("trigger run: %d %s", rec.Code, rec.Body.String())
		}
		runName := rec.Header().Get("x-ms-workflow-run-id")
		if runName == "" {
			t.Fatal("trigger run answered without x-ms-workflow-run-id")
		}
		<-arrived
		if status := logicAsyncRunStatus(t, srv, workflowPath, runName); status != "Running" {
			t.Fatalf("run reads %q while its Http action waits, want Running", status)
		}
		return runName
	}

	cancelled := fire()
	if rec := logicAsyncRequest(t, srv, http.MethodPost, workflowPath+"/runs/"+cancelled+"/cancel", ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel a Running run: %d %s", rec.Code, rec.Body.String())
	}
	bg.Await()
	if status := logicAsyncRunStatus(t, srv, workflowPath, cancelled); status != "Cancelled" {
		t.Fatalf("cancelled run reads %q after its execution stopped, want Cancelled", status)
	}

	succeeded := fire()
	release <- struct{}{}
	bg.Await()
	if status := logicAsyncRunStatus(t, srv, workflowPath, succeeded); status != "Succeeded" {
		t.Fatalf("run reads %q after its action answered, want Succeeded", status)
	}
	rec := logicAsyncRequest(t, srv, http.MethodPost, workflowPath+"/runs/"+succeeded+"/cancel", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancel a finished run: %d %s, want 409", rec.Code, rec.Body.String())
	}
}
