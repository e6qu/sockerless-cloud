package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Proto3 JSON reads an int64 from a number or a string and always writes a
// string, so a worker pool reads back diskSizeGb as a string however the
// request spelled it, and refuses a value that is not an integer.
func TestWorkerPoolDiskSizeGbReadsBackAsAnInt64String(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	const pools = "/v1/projects/cb-pools/locations/us-central1/workerPools"
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://cloudbuild.googleapis.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	diskSize := func(pool string) any {
		t.Helper()
		rec := do(http.MethodGet, pools+"/"+pool, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("get %s: %d %s", pool, rec.Code, rec.Body)
		}
		var got struct {
			PrivatePoolV1Config struct {
				WorkerConfig map[string]any `json:"workerConfig"`
			} `json:"privatePoolV1Config"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %s: %v", pool, err)
		}
		return got.PrivatePoolV1Config.WorkerConfig["diskSizeGb"]
	}

	for pool, body := range map[string]string{
		"from-number": `{"privatePoolV1Config":{"workerConfig":{"machineType":"e2-standard-4","diskSizeGb":100}}}`,
		"from-string": `{"privatePoolV1Config":{"workerConfig":{"machineType":"e2-standard-4","diskSizeGb":"0100"}}}`,
	} {
		if rec := do(http.MethodPost, pools+"?workerPoolId="+pool, body); rec.Code != http.StatusOK {
			t.Fatalf("create %s: %d %s", pool, rec.Code, rec.Body)
		}
		if got := diskSize(pool); got != "100" {
			t.Fatalf("%s: diskSizeGb = %#v, want the string \"100\"", pool, got)
		}
	}

	if rec := do(http.MethodPatch, pools+"/from-number?updateMask=privatePoolV1Config",
		`{"privatePoolV1Config":{"workerConfig":{"diskSizeGb":250}}}`); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	if got := diskSize("from-number"); got != "250" {
		t.Fatalf("after patch: diskSizeGb = %#v, want the string \"250\"", got)
	}

	for _, bad := range []string{`100.5`, `"ten"`, `true`} {
		rec := do(http.MethodPost, pools+"?workerPoolId=bad",
			`{"privatePoolV1Config":{"workerConfig":{"diskSizeGb":`+bad+`}}}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_ARGUMENT") {
			t.Fatalf("diskSizeGb %s: %d %s, want 400 INVALID_ARGUMENT", bad, rec.Code, rec.Body)
		}
	}
}
