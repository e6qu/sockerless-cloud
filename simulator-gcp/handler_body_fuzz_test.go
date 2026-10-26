package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// fuzzTargets are mutating endpoints whose handlers parse an untrusted JSON
// body into map[string]any and type-assert its fields, so a wrong-typed value
// ({"name": 123}) could panic one. TestFuzzTargetsAreMounted holds every entry
// to a mounted handler.
var fuzzTargets = []struct {
	method string
	path   string
}{
	{"POST", "/storage/v1/b?project=test-project"},                             // bucket insert
	{"PATCH", "/storage/v1/b/mybucket"},                                        // bucket patch
	{"POST", "/upload/storage/v1/b/mybucket/o?uploadType=multipart&name=x"},    // object upload
	{"POST", "/bigquery/v2/projects/p/queries"},                                // query
	{"POST", "/bigquery/v2/projects/p/jobs"},                                   // insert job
	{"POST", "/v2/projects/p/locations/l/jobs?jobId=j"},                        // cloud run jobs create
	{"POST", "/apis/serving.knative.dev/v1/namespaces/p/services"},             // cloud run service
	{"POST", "/v1/projects/p/topics/t:publish"},                                // pubsub publish
	{"PUT", "/v1/projects/p/topics/t"},                                         // pubsub create topic
	{"POST", "/v1/projects/p/secrets?secretId=s"},                              // secret create
	{"POST", "/v2/projects/p/locations/l/functions?functionId=f"},              // function create
	{"POST", "/compute/v1/projects/p/zones/z/instances"},                       // instance insert
	{"POST", "/v1/projects/p/locations/l/keyRings/r/cryptoKeys?cryptoKeyId=k"}, // kms key create
	{"POST", "/v1/projects/p/locations/l/triggers?triggerId=t"},                // eventarc trigger create
	{"PATCH", "/compute/v1/projects/p/global/networks/n"},                      // network patch
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
	t.Cleanup(srv.StopBackground)
	gcpTestCreateProject(t, srv, gcpTestProjectID)
	return srv
}

// TestFuzzTargetsAreMounted keeps the fuzz corpus honest: a target no route
// answers gets ServeMux's plain-text "404 page not found", and fuzzing it
// explores the mux rather than a handler. A service 404 is a JSON envelope.
func TestFuzzTargetsAreMounted(t *testing.T) {
	srv := newFuzzSim(t)
	for _, tgt := range fuzzTargets {
		req := httptest.NewRequest(tgt.method, tgt.path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "404 page not found") ||
			strings.Contains(rec.Body.String(), "Method Not Allowed") {
			t.Errorf("%s %s: no handler is mounted (%s)", tgt.method, tgt.path,
				strings.TrimSpace(rec.Body.String()))
		}
	}
}

// FuzzHandlerMalformedBody drives every mutating handler with wrong-typed JSON
// bodies and requires, for every input: no panic; no 5xx, the status a
// recovered panic surfaces as; and every 4xx in Google's
// `{"error":{"code","status","message"}}` envelope with its code matching the
// HTTP status.
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
		srv.ServeHTTP(rec, req)

		if rec.Code < 100 || rec.Code > 599 {
			t.Fatalf("%s %s: status %d is not an HTTP status, for body %q",
				tgt.method, tgt.path, rec.Code, body)
		}
		if rec.Code >= 500 {
			t.Fatalf("%s %s: server error %d for a caller-malformed body %q: %s",
				tgt.method, tgt.path, rec.Code, body, rec.Body.String())
		}
		if rec.Code < 400 {
			return
		}
		var envelope struct {
			Error struct {
				Code    int    `json:"code"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s %s: refusal %d is not JSON for body %q: %v\n%s",
				tgt.method, tgt.path, rec.Code, body, err, rec.Body.String())
		}
		if envelope.Error.Code != rec.Code || envelope.Error.Status == "" || envelope.Error.Message == "" {
			t.Fatalf("%s %s: refusal %d for body %q is not a Google error envelope: %s",
				tgt.method, tgt.path, rec.Code, body, rec.Body.String())
		}
	})
}
