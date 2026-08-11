package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

func TestACIProcessRuntimeRejectsWorkloadExecution(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}

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
}
