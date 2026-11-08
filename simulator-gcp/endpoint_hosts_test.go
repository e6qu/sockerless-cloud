package main

import (
	"encoding/json"
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
	if h.Get("Access-Control-Allow-Origin") != origin || strings.Join(h.Values("Vary"), ",") != "X-Origin,Referer,Origin,Accept-Encoding" ||
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

// Cloud Storage refuses a JSON API call without a credential in the JSON
// API's own envelope, which storage-json-status-codes.md.gz shows: reason
// required, located at the Authorization header, and no google.rpc status.
func TestBearerCheckAnswersCloudStorageInItsOwnEnvelope(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)
	handler := bearerAuthMiddleware(srv)(srv)
	req := httptest.NewRequest(http.MethodGet, "/storage/v1/b?project=test-project", nil)
	req.Host = "storage.googleapis.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var refusal struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Errors  []struct {
				Domain       string `json:"domain"`
				Reason       string `json:"reason"`
				LocationType string `json:"locationType"`
				Location     string `json:"location"`
			} `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("refusal is not JSON: %v %s", err, rec.Body)
	}
	e := refusal.Error
	if rec.Code != http.StatusUnauthorized || e.Code != 401 || e.Status != "" || e.Message != "Login Required" ||
		len(e.Errors) != 1 || e.Errors[0].Reason != "required" || e.Errors[0].Domain != "global" ||
		e.Errors[0].LocationType != "header" || e.Errors[0].Location != "Authorization" {
		t.Fatalf("an anonymous Cloud Storage call → %d %s", rec.Code, rec.Body)
	}
}

// The XML API refuses an object read without a credential with its own
// AuthenticationRequired, which storage-xml-status-codes.md.gz lists under 401.
func TestXMLObjectReadWithoutCredentialIsAuthenticationRequired(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)
	if status, body := gcpDo(t, srv, http.MethodPost, "storage.googleapis.com",
		"/storage/v1/b?project="+gcpTestProjectID, `{"name":"xml-anonymous-read"}`); status != http.StatusOK {
		t.Fatalf("create bucket: %d %s", status, body)
	}
	status, body := gcpDo(t, srv, http.MethodGet, "storage.googleapis.com", "/xml-anonymous-read/object.txt", "")
	if status != http.StatusUnauthorized || !strings.Contains(body, "<Code>AuthenticationRequired</Code>") {
		t.Fatalf("an anonymous XML read → %d %s", status, body)
	}
}
