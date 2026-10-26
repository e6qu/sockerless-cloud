package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// TestGCPOperationsAreRecordedComplete pins the invariant CancelOperation
// rests on: these services record operations complete, since their work runs
// inside the request. A service that records one before its work finishes
// fails here, the signal that cancel must stop that work.
func TestGCPOperationsAreRecordedComplete(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	srv.WrapHandler(bearerAuthMiddleware(srv))
	now := time.Now()
	token := signAccessToken("operations-invariant@sockerless.iam.gserviceaccount.com", now, now.Add(time.Hour))

	post := func(host, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code >= 400 {
			t.Fatalf("POST %s: %d %s", path, rec.Code, rec.Body.String())
		}
		return rec
	}

	// Mint an operation into each store through the real create methods; an empty
	// store would pass vacuously.
	post("apigateway.googleapis.com",
		"/v1/projects/test-project/locations/global/apis?apiId=invariant-api",
		`{"displayName":"invariant"}`)
	post("redis.googleapis.com",
		"/v1/projects/test-project/locations/us-central1/instances?instanceId=invariant-redis",
		`{"tier":"BASIC","memorySizeGb":1}`)
	post("logging.googleapis.com",
		"/v2/projects/test-project/locations/us-central1/buckets:createAsync?bucketId=invariant-bucket",
		`{}`)

	for name, store := range map[string]sim.Store[Operation]{
		"crOperations":  crOperations,
		"logOperations": logOperations,
	} {
		if store == nil {
			t.Fatalf("%s: store not initialised; the cancel handler resolves against it", name)
		}
		ops := store.List()
		if len(ops) == 0 {
			t.Fatalf("%s: no operations recorded; the invariant would hold vacuously", name)
		}
		for _, op := range ops {
			if !op.Done {
				t.Errorf("%s: operation %q is recorded before its work finished — cancel must be able to stop that work, not no-op on it",
					name, op.Name)
			}
		}
	}
}
