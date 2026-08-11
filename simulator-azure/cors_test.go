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

// TestAzureCORS_PreflightToARMPathReturnsCORSHeaders proves the sim answers a
// browser's preflight OPTIONS request for an Azure Resource Manager path the
// way real Azure's edge does: without the bearer token or api-version the
// actual operation needs (a preflight carries neither), and with the
// Access-Control-* headers a browser's fetch() requires before it will send
// the real request.
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

// TestAzureCORS_PreflightToGraphPathReturnsCORSHeaders proves the same
// preflight contract holds for Microsoft Graph (/v1.0), the other data plane
// the console's portal calls directly from the browser.
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

// TestAzureCORS_ActualGETCarriesAllowOrigin proves a real (non-preflight)
// cross-origin GET against Azure Resource Manager still carries
// Access-Control-Allow-Origin, so the browser's fetch() can read the
// response — the header a preflight alone would not prove out, since real
// browsers send Origin on the actual request too.
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

// TestAzureCORS_UnauthenticatedActualGETStillCarriesAllowOrigin proves the
// CORS header is present even on a rejected (401) actual request — real ARM
// still answers CORS-eligible so the browser's JavaScript can read the
// unauthenticated-rejection body rather than reporting an opaque CORS
// failure that would hide the real 401.
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

// TestAzureCORS_TokenEndpointHasNoCORSHeaders proves the Microsoft Entra
// token endpoint — the one surface real Azure serves no CORS for, and the
// entire reason the console's server-side federation broker exists — gets no
// Access-Control-Allow-Origin even when the request carries a browser Origin
// header and would otherwise look CORS-eligible.
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

	// A preflight OPTIONS to the token endpoint must likewise get no CORS
	// headers: a browser that tried to call it directly would fail exactly as
	// it does against real Microsoft Entra, which is why the federation
	// broker exists.
	preflight := httptest.NewRequest(http.MethodOptions, "/"+simTenantID+"/oauth2/v2.0/token", nil)
	preflight.Header.Set("Origin", corsTestOrigin)
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, preflight)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("token endpoint preflight Access-Control-Allow-Origin = %q, want empty", got)
	}
}

// TestAzureCORS_AuthLayerHasNoCORSHeaders proves the console's own /auth/*
// auth layer (session, federation broker) is likewise excluded from the ARM
// and Graph CORS surfaces — it is same-origin only, reached with
// credentials: "include", never cross-origin.
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
