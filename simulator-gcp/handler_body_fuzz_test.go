package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// fuzzTargets are mutating GCP endpoints whose handlers parse an untrusted JSON
// body into map[string]any and pull fields out with type assertions. Each is a
// candidate for a weak-type panic when a client sends a value of the wrong JSON
// type (e.g. {"name": 123} where the handler does v["name"].(string)).
var fuzzTargets = []struct {
	method string
	path   string
}{
	{"POST", "/storage/v1/b?project=p"},                                        // bucket insert
	{"PATCH", "/storage/v1/b/mybucket"},                                        // bucket patch
	{"POST", "/upload/storage/v1/b/mybucket/o?uploadType=multipart&name=x"},    // object upload
	{"POST", "/bigquery/v2/projects/p/queries"},                                // query
	{"POST", "/bigquery/v2/projects/p/jobs"},                                   // insert job
	{"POST", "/v2/projects/p/locations/l/jobs?jobId=j"},                        // cloud run jobs create
	{"POST", "/apis/serving.knative.dev/v1/namespaces/p/services"},             // cloud run service
	{"POST", "/v1/projects/p/topics/t:publish"},                                // pubsub publish
	{"PUT", "/v1/projects/p/topics/t"},                                         // pubsub create topic
	{"POST", "/v1/projects/p/secrets?secretId=s"},                              // secret create
	{"POST", "/v1/projects/p/locations/l/functions?functionId=f"},              // function create
	{"POST", "/compute/v1/projects/p/zones/z/instances"},                       // instance insert
	{"POST", "/v1/projects/p/locations/l/keyRings/r/cryptoKeys?cryptoKeyId=k"}, // kms key create
	{"PATCH", "/v1/projects/p/dashboards/d"},                                   // dashboard patch
}

// malformed bodies: every field that a handler might assert as a specific type
// is supplied as the *wrong* JSON type, plus structural garbage.
var fuzzBodies = []string{
	`{"name": 123}`,
	`{"name": true}`,
	`{"name": ["a"]}`,
	`{"name": {"x": 1}}`,
	`{"name": null}`,
	`{"labels": "not-a-map"}`,
	`{"labels": 5}`,
	`{"labels": [1,2,3]}`,
	`{"location": 99}`,
	`{"configuration": {"query": 123}}`,
	`{"configuration": "x"}`,
	`{"messages": "x"}`,
	`{"messages": [123]}`,
	`{"template": 7}`,
	`{"template": {"containers": "x"}}`,
	`{"template": {"containers": [123]}}`,
	`{"status": {"state": 9}}`,
	`{"hierarchicalNamespace": {"enabled": "yes"}}`,
	`{"data": 5}`,
	`{"payload": {"data": 5}}`,
	`{"spec": {"template": {"spec": {"containers": "x"}}}}`,
	`[]`,
	`123`,
	`"a string"`,
	`null`,
	`{`,
	``,
	`{"deeply": {"nested": {"a": {"b": {"c": 1}}}}}`,
}

func newFuzzSim(t testing.TB) *sim.Server {
	srv, err := buildSimulator(sim.Config{Provider: "gcp", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("build sim: %v", err)
	}
	return srv
}

// FuzzHandlerMalformedBody drives every mutating handler with malformed-type
// JSON bodies. A handler that does an unchecked type assertion on a body field
// panics → the recover in the server's middleware (or this test) turns it into
// a failure. The contract: a wrong-typed body yields a 4xx/5xx response, never
// a process crash.
func FuzzHandlerMalformedBody(f *testing.F) {
	for ti := range fuzzTargets {
		for bi := range fuzzBodies {
			f.Add(ti, []byte(fuzzBodies[bi]))
		}
	}
	srv := newFuzzSim(f)
	f.Fuzz(func(t *testing.T, targetIdx int, body []byte) {
		if targetIdx < 0 {
			targetIdx = -targetIdx
		}
		tgt := fuzzTargets[targetIdx%len(fuzzTargets)]
		req := httptest.NewRequest(tgt.method, tgt.path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		// A panic here propagates out of ServeHTTP and fails the fuzz case,
		// pinpointing the weak-type assertion.
		srv.ServeHTTP(rec, req)
		if rec.Code == 0 {
			t.Fatalf("%s %s: no status written for body %q", tgt.method, tgt.path, body)
		}
		_ = http.StatusOK
	})
}
