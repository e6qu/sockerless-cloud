package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// The eq/ne form matches its literal as an RE2 expression against the whole
// field, in each spelling the Compute Engine filter parameter documents.
func TestComputeFilterRegexForm(t *testing.T) {
	docs := []listq.Doc{
		{"name": "web-instance", "zone": "us-central1-a", "labels": map[string]any{"tier": "front"}},
		{"name": "db-instance", "zone": "us-east1-b", "labels": map[string]any{"tier": "back"}},
		{"name": "web-instance-2", "zone": "us-central1-a"},
	}
	for filter, want := range map[string][]string{
		"name eq web-instance":                    {"web-instance"},
		"name eq web.*":                           {"web-instance", "web-instance-2"},
		"name ne .*instance":                      {"web-instance-2"},
		"name eq 'db-instance'":                   {"db-instance"},
		`name eq "(web|db)-instance"`:             {"db-instance", "web-instance"},
		"labels.tier eq front":                    {"web-instance"},
		`(zone eq us-central1-a) (name ne ".*2")`: {"web-instance"},
		"(name eq web.*)(zone eq us-.*)":          {"web-instance", "web-instance-2"},
		"name eq instance":                        {},
	} {
		node, err := gcpParseComputeFilter(filter)
		if err != nil {
			t.Fatalf("filter %q: %v", filter, err)
		}
		got := []string{}
		for _, doc := range docs {
			if node.Eval(doc) {
				got = append(got, doc["name"].(string))
			}
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("filter %q selected %v, want %v", filter, got, want)
		}
	}

	// AIP-160 still parses as AIP-160, including a parenthesized value that
	// happens to contain " eq ".
	node, err := gcpParseComputeFilter(`(name = "web-instance") (zone != "x eq y")`)
	if err != nil {
		t.Fatalf("AIP-160 filter: %v", err)
	}
	if !node.Eval(docs[0]) || node.Eval(docs[1]) {
		t.Fatal("an AIP-160 filter must keep its AIP-160 meaning")
	}

	for filter, reason := range map[string]string{
		`(name eq web.*) (zone = "us-central1-a")`: "cannot be combined",
		`(name eq web.*) AND (zone eq us-.*)`:      "cannot be combined",
		`name eq web-(`:                            "invalid regular expression",
	} {
		if _, err := gcpParseComputeFilter(filter); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("filter %q: err = %v, want one naming %q", filter, err, reason)
		}
	}
}

// A Compute Engine list answers the regex form through the list handlers, and
// refuses a filter mixing both languages with INVALID_ARGUMENT.
func TestComputeListAnswersTheRegexFilterForm(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://compute.googleapis.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	const base = "/compute/v1/projects/regex-filter/global/healthChecks"
	for _, name := range []string{"web-check", "web-check-2", "db-check"} {
		if rec := do(http.MethodPost, base, `{"name":"`+name+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("insert %s: %d %s", name, rec.Code, rec.Body)
		}
	}

	rec := do(http.MethodGet, base+"?filter="+url.QueryEscape("name ne web-.*"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var listed struct {
		Items []ComputeHealthCheck `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Name != "db-check" {
		t.Fatalf("`name ne web-.*` listed %+v, want only db-check", listed.Items)
	}

	rec = do(http.MethodGet, base+"?filter="+url.QueryEscape(`(name eq web.*) (type = "HTTP")`), "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_ARGUMENT") ||
		!strings.Contains(rec.Body.String(), "cannot be combined") {
		t.Fatalf("a mixed filter answered %d %s, want 400 INVALID_ARGUMENT", rec.Code, rec.Body)
	}
}
