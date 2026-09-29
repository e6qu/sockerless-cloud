package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A Compute Engine insert refuses a resource the Discovery document says it
// cannot create — one without the name it requires, or with a name outside the
// RFC 1035 pattern it declares — with the field error Compute Engine answers,
// and stores nothing.
func TestComputeInsertsRefuseAMissingOrMalformedName(t *testing.T) {
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

	for _, tc := range []struct {
		name, body, reason, message string
	}{
		{
			name:    "missing",
			body:    `{"autoCreateSubnetworks":true}`,
			reason:  "required",
			message: "Required field 'resource.name' not specified",
		},
		{
			name:    "malformed",
			body:    `{"name":"Net_1"}`,
			reason:  "invalid",
			message: "Invalid value for field 'resource.name': 'Net_1'. Must be a match of regex '(?:[a-z](?:[-a-z0-9]{0,61}[a-z0-9])?)'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(http.MethodPost, "/compute/v1/projects/net-validation/global/networks", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			var got struct {
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
					Errors  []struct {
						Domain, Reason, Message string
					} `json:"errors"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode error document: %v: %s", err, rec.Body)
			}
			if got.Error.Code != http.StatusBadRequest || got.Error.Message != tc.message {
				t.Fatalf("error = %d %q, want 400 %q", got.Error.Code, got.Error.Message, tc.message)
			}
			if len(got.Error.Errors) != 1 || got.Error.Errors[0].Reason != tc.reason ||
				got.Error.Errors[0].Domain != "global" || got.Error.Errors[0].Message != tc.message {
				t.Fatalf("errors = %+v, want one global %q item", got.Error.Errors, tc.reason)
			}
		})
	}

	// Every other insert answers the same way through the one helper, and a
	// resource whose name the document gives no pattern is refused only for a
	// missing name.
	for _, tc := range []struct {
		path        string
		patterned   bool
		validSample string
	}{
		{"/compute/v1/projects/net-validation/global/firewalls", true, `{"name":"fw-1","allowed":[{"IPProtocol":"tcp","ports":["22"]}]}`},
		{"/compute/v1/projects/net-validation/global/healthChecks", true, `{"name":"hc-1"}`},
		{"/compute/v1/projects/net-validation/global/instanceTemplates", true, `{"name":"tpl-1"}`},
		{"/compute/v1/projects/net-validation/global/snapshots", true, `{"name":"snap-1"}`},
		{"/compute/v1/projects/net-validation/global/licenses", true, `{"name":"lic-1"}`},
		{"/compute/v1/projects/net-validation/global/securityPolicies", true, `{"name":"armor-1"}`},
		{"/compute/v1/projects/net-validation/regions/us-central1/nodeTemplates", false, `{"name":"Node_Template"}`},
	} {
		missing := do(http.MethodPost, tc.path, `{"description":"no name"}`)
		if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), `"reason":"required"`) ||
			!strings.Contains(missing.Body.String(), "Required field 'resource.name' not specified") {
			t.Errorf("%s without a name answered %d %s", tc.path, missing.Code, missing.Body)
		}
		malformed := do(http.MethodPost, tc.path, `{"name":"Bad_Name"}`)
		if tc.patterned && (malformed.Code != http.StatusBadRequest || !strings.Contains(malformed.Body.String(), `"reason":"invalid"`)) {
			t.Errorf("%s with a malformed name answered %d %s", tc.path, malformed.Code, malformed.Body)
		}
		if !tc.patterned && malformed.Code != http.StatusOK {
			t.Errorf("%s has no declared name pattern, yet refused Bad_Name: %d %s", tc.path, malformed.Code, malformed.Body)
		}
		if tc.patterned {
			if ok := do(http.MethodPost, tc.path, tc.validSample); ok.Code != http.StatusOK {
				t.Errorf("%s refused a well-formed insert: %d %s", tc.path, ok.Code, ok.Body)
			}
		}
	}

	list := do(http.MethodGet, "/compute/v1/projects/net-validation/global/networks", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	var listed struct {
		Items []ComputeNetwork `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Items) != 0 {
		t.Fatalf("a refused insert stored %+v", listed.Items)
	}
}
