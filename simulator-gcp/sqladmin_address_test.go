package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A Cloud SQL instance lists the addresses it serves on. The simulator runs no
// SQL Server engine, so nothing serves a SQL Server instance, and
// instances.get, its connectSettings and a clone of it all report no address
// rather than one no client could reach.
func TestCloudSQLInstanceWithoutADataPlaneReportsNoAddress(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	do := func(method, path, body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "http://sqladmin.googleapis.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
		return out
	}
	const base = "/v1/projects/sql-address"

	do(http.MethodPost, base+"/instances",
		`{"name":"orders","databaseVersion":"SQLSERVER_2022_STANDARD","region":"us-central1","rootPassword":"s3cret"}`)
	inst := do(http.MethodGet, base+"/instances/orders", "")
	if inst["name"] != "orders" || inst["state"] != "RUNNABLE" {
		t.Fatalf("instance = %v", inst)
	}
	if addresses, present := inst["ipAddresses"]; present {
		t.Fatalf("an instance no data plane serves reports ipAddresses %v", addresses)
	}

	settings := do(http.MethodGet, base+"/instances/orders/connectSettings", "")
	if settings["kind"] != "sql#connectSettings" || settings["databaseVersion"] != "SQLSERVER_2022_STANDARD" {
		t.Fatalf("connectSettings = %v", settings)
	}
	if addresses, present := settings["ipAddresses"]; present {
		t.Fatalf("connectSettings reports ipAddresses %v for an instance nothing serves", addresses)
	}

	do(http.MethodPost, base+"/instances/orders/clone",
		`{"cloneContext":{"destinationInstanceName":"orders-copy"}}`)
	clone := do(http.MethodGet, base+"/instances/orders-copy", "")
	if clone["name"] != "orders-copy" {
		t.Fatalf("clone = %v", clone)
	}
	if addresses, present := clone["ipAddresses"]; present {
		t.Fatalf("the clone reports ipAddresses %v", addresses)
	}
}
