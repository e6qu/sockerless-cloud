package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServiceFromHostReadsEveryUniverseSpelling(t *testing.T) {
	for host, want := range map[string]string{
		"spanner.googleapis.com":                          "spanner",
		"spanner.googleapis.com:443":                      "spanner",
		"spanner.mtls.googleapis.com":                     "spanner",
		"us-central1-run.googleapis.com":                  "run",
		"spanner.localhost:4567":                          "spanner",
		"spanner.gcp.sockerless.localhost:8443":           "spanner",
		"sqladmin.localhost":                              "sqladmin",
		"localhost:4567":                                  "",
		"gcp.sockerless.localhost:8443":                   "",
		"us-central1-docker.pkg.gcp.sockerless.localhost": "",
		"storage.example.com":                             "",
		"127.0.0.1:4567":                                  "",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		if got := gcpServiceFromHost(r); got != want {
			t.Errorf("%s names %q, want %q", host, got, want)
		}
	}
}

// Spanner and Cloud SQL Admin publish one instances path: Spanner's host
// reaches Spanner, in each of its spellings, and the bare coordinate Cloud
// SQL Admin.
func TestSpannerAndCloudSQLShareTheirPathByHost(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)
	const path = "/v1/projects/test-project/instances"
	for _, host := range []string{"spanner.googleapis.com", "spanner.localhost:4567", "spanner.gcp.sockerless.localhost:8443"} {
		status, body := gcpDo(t, srv, http.MethodGet, host, path, "")
		if status != http.StatusOK || strings.Contains(body, "sql#instancesList") {
			t.Errorf("GET %s%s → %d %s, want Spanner's instances list", host, path, status, body)
		}
	}
	status, body := gcpDo(t, srv, http.MethodGet, "localhost:4567", path, "")
	if status != http.StatusOK || !strings.Contains(body, "sql#instancesList") {
		t.Errorf("GET %s at the bare coordinate → %d %s, want Cloud SQL Admin's instances list", path, status, body)
	}
	if status, _ := gcpDo(t, srv, http.MethodGet, "localhost:4567", "/spanner/v1/projects/test-project/instances", ""); status != http.StatusNotFound {
		t.Errorf("a path spanner.googleapis.com does not have answers %d", status)
	}
}

// The front end answers the preflight and stamps the response as
// specs/cloud-api/gcp/googleapis-cors.http.gz records, and leaves a call to
// the bare coordinate, which no browser sends cross-origin, alone.
func TestCORSFollowsGooglesFrontEnd(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)
	handler := gcpCORSMiddleware(srv)
	const origin = "http://localhost:4567"

	preflight := httptest.NewRequest(http.MethodOptions, "/v1/projects/p/instances", nil)
	preflight.Host = "spanner.localhost:4567"
	preflight.Header.Set("Origin", origin)
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, preflight)
	h := rec.Header()
	if rec.Code != http.StatusOK || h.Get("Access-Control-Allow-Origin") != origin ||
		h.Get("Access-Control-Allow-Methods") != "DELETE,GET,HEAD,OPTIONS,PATCH,POST,PUT" ||
		h.Get("Access-Control-Allow-Headers") != "authorization,content-type" ||
		h.Get("Access-Control-Max-Age") != "3600" || strings.Join(h.Values("Vary"), ",") != "origin,referer,x-origin" {
		t.Errorf("preflight → %d %v", rec.Code, h)
	}

	get := httptest.NewRequest(http.MethodGet, "/v1/projects/p/instances", nil)
	get.Host = "spanner.localhost:4567"
	get.Header.Set("Origin", origin)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	h = rec.Header()
	if h.Get("Access-Control-Allow-Origin") != origin || strings.Join(h.Values("Vary"), ",") != "X-Origin,Referer,Origin" ||
		!strings.HasPrefix(h.Get("Access-Control-Expose-Headers"), "vary,vary,vary,") ||
		!strings.HasSuffix(h.Get("Access-Control-Expose-Headers"), ",content-encoding,date,server,content-length") ||
		h.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("cross-origin GET → %d %v", rec.Code, h)
	}

	bare := httptest.NewRequest(http.MethodGet, "/v1/projects/p/instances", nil)
	bare.Host = "localhost:4567"
	bare.Header.Set("Origin", "http://elsewhere.localhost")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bare)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" || !strings.Contains(rec.Body.String(), "sql#instancesList") {
		t.Errorf("a call to the bare coordinate → Access-Control-Allow-Origin %q, body %s; want none, and Cloud SQL Admin's list", got, rec.Body)
	}
}

// Every spelling of Spanner's host is a Google API method call the bearer
// check covers, not an unrouted path it lets through.
func TestBearerCheckCoversEverySpannerHost(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)
	handler := bearerAuthMiddleware(srv)(srv)
	for _, host := range []string{"spanner.googleapis.com", "spanner.localhost:4567", "spanner.gcp.sockerless.localhost:8443"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/projects/test-project/instances", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("an anonymous GET to %s → %d %s, want 401", host, rec.Code, rec.Body)
		}
	}
}
