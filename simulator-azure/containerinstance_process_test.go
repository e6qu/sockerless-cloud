package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// TestACIProcessRuntimeRejectsWorkloadExecution asserts the refusal's code and
// reason and that no group is stored: five paths answer 400, and on a host
// with no engine a status-only check would pass even without the runtime
// guard.
func TestACIProcessRuntimeRejectsWorkloadExecution(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	// Long-running operations complete in a goroutine; one still running when
	// this test ends would race the next test's store rebuild.
	t.Cleanup(bg.Await)

	// The plane requires a simulator-minted bearer.
	now := time.Now()
	token, err := mintBootstrapSimJWT("https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM bearer: %v", err)
	}

	createTestResourceGroup(t, srv, azureDefaultSubscriptionID, "rg")
	request := httptest.NewRequest(http.MethodPut,
		"/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg/providers/Microsoft.ContainerInstance/containerGroups/workload?api-version=2021-10-01",
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

	// The handler rolls a refused group back.
	read := httptest.NewRequest(http.MethodGet,
		"/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg/providers/Microsoft.ContainerInstance/containerGroups/workload?api-version=2021-10-01", nil)
	read.Header.Set("Authorization", "Bearer "+token)
	readRecorder := httptest.NewRecorder()
	srv.ServeHTTP(readRecorder, read)
	if readRecorder.Code != http.StatusNotFound {
		t.Fatalf("a refused container group must not be stored; read status = %d: %s",
			readRecorder.Code, readRecorder.Body.String())
	}
}
