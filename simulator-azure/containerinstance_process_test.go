package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// TestACIProcessRuntimeRejectsWorkloadExecution asserts the refusal itself, not
// merely that the create failed. A status code alone would not do: five
// distinct paths through handleACIContainerGroupPut answer 400, and on a host
// with no reachable container engine the deployment would fail with one of them
// even if the runtime guard were deleted — so a status-only assertion passes on
// exactly the hosts where it proves the least. The error code and the reason
// the simulator gives, plus the absence of a stored group, are what separate
// "the process runtime refused to execute a workload" from "the engine was not
// there".
func TestACIProcessRuntimeRejectsWorkloadExecution(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)

	// The ARM plane requires a valid bearer; mint one the way a client acquires
	// it from the token endpoint so the request reaches the container-group
	// handler and exercises the process-runtime rejection under test.
	now := time.Now()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM bearer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPut,
		"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerInstance/containerGroups/workload?api-version=2021-10-01",
		bytes.NewBufferString(`{"location":"westeurope","properties":{"containers":[{"name":"workload","properties":{"image":"alpine:3.20"}}],"osType":"Linux"}}`))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", recorder.Body.String(), err)
	}
	if envelope.Error.Code != "ContainerGroupDeploymentFailed" {
		t.Fatalf("error code = %q, want ContainerGroupDeploymentFailed: %s", envelope.Error.Code, recorder.Body.String())
	}
	if !strings.Contains(envelope.Error.Message, "SIM_RUNTIME=process") {
		t.Fatalf("the refusal must name the runtime that cannot execute a workload; got %q", envelope.Error.Message)
	}

	// A refused deployment leaves nothing behind: the handler rolls the group
	// back, so the resource must not be readable afterwards.
	read := httptest.NewRequest(http.MethodGet,
		"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerInstance/containerGroups/workload?api-version=2021-10-01", nil)
	read.Header.Set("Authorization", "Bearer "+token)
	readRecorder := httptest.NewRecorder()
	srv.ServeHTTP(readRecorder, read)
	if readRecorder.Code != http.StatusNotFound {
		t.Fatalf("a refused container group must not be stored; read status = %d: %s",
			readRecorder.Code, readRecorder.Body.String())
	}
}
