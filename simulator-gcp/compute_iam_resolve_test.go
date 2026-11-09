package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Every IAM verb Compute Engine mounts beneath a resource answers a resource
// that was never created with the 404 its collection's get method answers.
func TestCompute_IAMVerbsOnAnAbsentResourceAnswerNotFound(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	wildcard := regexp.MustCompile(`\{[^}]+\}`)
	verbs := []string{"/getIamPolicy", "/setIamPolicy", "/testIamPermissions"}
	paths := map[string]struct{}{
		"GET /compute/v1/projects/absent-project/global/images/absent/getIamPolicy": {},
	}
	for _, pattern := range srv.RoutePatterns() {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok || !strings.HasPrefix(path, "/compute/") {
			continue
		}
		for _, verb := range verbs {
			if strings.HasSuffix(path, verb) {
				paths[method+" "+wildcard.ReplaceAllString(path, "absent")] = struct{}{}
			}
		}
	}
	if len(paths) < 50 {
		t.Fatalf("found %d Compute Engine IAM routes; the route table lost them", len(paths))
	}

	for request := range paths {
		method, path, _ := strings.Cut(request, " ")
		body := ""
		if method == http.MethodPost {
			body = `{"policy":{"bindings":[]},"permissions":["compute.instances.get"]}`
		}
		rec := httptest.NewRecorder()
		srv.Mux().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404: %s", request, rec.Code, rec.Body.String())
			continue
		}
		var answer struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || answer.Error.Code != http.StatusNotFound || answer.Error.Message == "" {
			t.Errorf("%s answered a body that is not a Compute Engine error: %s", request, rec.Body.String())
		}
	}
}
