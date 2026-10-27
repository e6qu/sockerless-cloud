package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const corsTestOrigin = "https://console.example.test"

// A preflight carries neither the bearer nor the api-version the operation
// needs.
func TestAzureCORS_PreflightToARMPathReturnsCORSHeaders(t *testing.T) {
	srv := buildAuthTestSim(t)

	req := httptest.NewRequest(http.MethodOptions, "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/jobs/j", nil)
	req.Header.Set("Origin", corsTestOrigin)
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != corsTestOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, corsTestOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "PUT") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want it to include PUT", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "authorization") ||
		!strings.Contains(strings.ToLower(got), "content-type") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want it to include authorization and content-type", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got == "" {
		t.Fatal("Access-Control-Max-Age missing from preflight response")
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q, want it to include Origin", got)
	}
}

// Microsoft Graph is the other plane the console calls from the browser.
func TestAzureCORS_PreflightToGraphPathReturnsCORSHeaders(t *testing.T) {
	srv := buildAuthTestSim(t)

	req := httptest.NewRequest(http.MethodOptions, "/v1.0/applications", nil)
	req.Header.Set("Origin", corsTestOrigin)
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("Graph preflight status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != corsTestOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, corsTestOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want it to include GET", got)
	}
}

// TestAzureCORS_SliceDispatchersCarryAllowOrigin: a response a slice's own
// dispatcher writes ahead of the mux — the role definitions list, a scope's
// tags — still carries the headers.
func TestAzureCORS_SliceDispatchersCarryAllowOrigin(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	tok, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	for _, path := range []string{
		"/subscriptions/sub/providers/Microsoft.Authorization/roleDefinitions?api-version=2022-04-01",
		"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/a/providers/Microsoft.Resources/tags/default?api-version=2021-04-01",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Origin", corsTestOrigin)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != corsTestOrigin {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want %q (status %d)", path, got, corsTestOrigin, rec.Code)
		}
	}
}

func TestAzureCORS_PreflightToLogAnalyticsQuery(t *testing.T) {
	srv := buildAuthTestSim(t)
	req := httptest.NewRequest(http.MethodOptions, "/v1/workspaces/00000000-0000-0000-0000-000000000000/query", nil)
	req.Header.Set("Origin", corsTestOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != corsTestOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, corsTestOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want it to include POST", got)
	}
}

func TestAzureCORS_ActualGETCarriesAllowOrigin(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	tok, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, armTestPath, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", corsTestOrigin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != corsTestOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q (response status %d: %s)", got, corsTestOrigin, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "x-ms-request-id") {
		t.Fatalf("Access-Control-Expose-Headers = %q, want it to include x-ms-request-id", got)
	}
}

// Real Azure Resource Manager answers a rejected request CORS-eligible, so the
// browser reads the 401 rather than an opaque CORS failure.
func TestAzureCORS_UnauthenticatedActualGETStillCarriesAllowOrigin(t *testing.T) {
	srv := buildAuthTestSim(t)

	req := httptest.NewRequest(http.MethodGet, armTestPath, nil)
	req.Header.Set("Origin", corsTestOrigin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (unauthenticated ARM request): %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != corsTestOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q even on a 401", got, corsTestOrigin)
	}
}

// Azure serves no CORS for the Microsoft Entra token endpoint, which is why
// the console's server-side federation broker exists.
func TestAzureCORS_TokenEndpointHasNoCORSHeaders(t *testing.T) {
	srv := buildAuthTestSim(t)

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {"https://management.azure.com/.default"},
	}
	req := httptest.NewRequest(http.MethodPost, "/"+simTenantID+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", corsTestOrigin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("token endpoint status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty — real Microsoft Entra serves no CORS on the token endpoint", got)
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/"+simTenantID+"/oauth2/v2.0/token", nil)
	preflight.Header.Set("Origin", corsTestOrigin)
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, preflight)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("token endpoint preflight Access-Control-Allow-Origin = %q, want empty", got)
	}
}

// The console's /auth/* layer is same-origin only, reached with credentials:
// "include".
func TestAzureCORS_AuthLayerHasNoCORSHeaders(t *testing.T) {
	srv := buildAuthTestSim(t)

	req := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	req.Header.Set("Origin", corsTestOrigin)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("/auth/session Access-Control-Allow-Origin = %q, want empty", got)
	}
}
