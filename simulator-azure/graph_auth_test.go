package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func graphCreateApplication(t *testing.T, srv http.Handler, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1.0/applications", strings.NewReader(`{"displayName":"graph-auth"}`))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestGraphRefusesAnUnacceptableBearer(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	armToken, err := mintAzureSimJWT("tenant", defaultAzureTokenAudience, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := mintAzureSimJWT("tenant", "https://graph.microsoft.com", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ authorization, message string }{
		"no token":     {"", "Access token is empty."},
		"not a bearer": {"Basic dXNlcjpwYXNz", "Access token is empty."},
		"malformed":    {"Bearer not-a-jwt", "Access token validation failure."},
		"expired":      {"Bearer " + expired, "Access token validation failure."},
		"ARM audience": {"Bearer " + armToken, "Access token validation failure. Invalid audience."},
	} {
		t.Run(name, func(t *testing.T) {
			rec := graphCreateApplication(t, srv, c.authorization)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body)
			}
			var body struct {
				Error struct {
					Code       string            `json:"code"`
					Message    string            `json:"message"`
					InnerError map[string]string `json:"innerError"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "InvalidAuthenticationToken" || body.Error.Message != c.message || body.Error.InnerError["request-id"] == "" {
				t.Fatalf("error = %+v", body.Error)
			}
			if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, `client_id="00000003-0000-0000-c000-000000000000"`) {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}
}

func TestGraphServesAGraphAudienceBearer(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	for _, audience := range []string{"https://graph.microsoft.com", "00000003-0000-0000-c000-000000000000", "http://example.com"} {
		t.Run(audience, func(t *testing.T) {
			token, err := mintAzureSimJWT("tenant", audience, now, now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if rec := graphCreateApplication(t, srv, "Bearer "+token); rec.Code != http.StatusCreated {
				t.Fatalf("create application with a %s token = %d: %s", audience, rec.Code, rec.Body)
			}
		})
	}
}

func TestGraphLeavesAnUnservedPathToItsOwnAnswer(t *testing.T) {
	srv := buildAuthTestSim(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1.0/noSuchCollection", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("an unserved Graph path answered 401: %s", rec.Body)
	}
}
