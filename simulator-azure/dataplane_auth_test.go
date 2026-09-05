package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// buildAuthTestSim builds the full simulator (including the bearer-verification
// middleware) in API-only mode so the ARM plane can be exercised without a
// container runtime.
func buildAuthTestSim(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)
	return srv
}

const armTestPath = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.App/jobs/j?api-version=2024-03-01"

func armGET(t *testing.T, srv *sim.Server, authz string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, armTestPath, nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func assertInvalidTokenUnauthorized(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if wa := rec.Header().Get("WWW-Authenticate"); !strings.Contains(wa, `error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate = %q, want it to carry error=\"invalid_token\"", wa)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rec.Body.String())
	}
	if body.Error.Code != "invalid_token" {
		t.Fatalf("error.code = %q, want invalid_token", body.Error.Code)
	}
}

func TestARMBearer_MissingTokenRejected(t *testing.T) {
	srv := buildAuthTestSim(t)
	assertInvalidTokenUnauthorized(t, armGET(t, srv, ""))
}

func TestARMBearer_MalformedTokenRejected(t *testing.T) {
	srv := buildAuthTestSim(t)
	assertInvalidTokenUnauthorized(t, armGET(t, srv, "Bearer not-a-jwt"))
}

func TestARMBearer_BadSignatureRejected(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	valid, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	// Corrupt the signature segment so verification fails.
	parts := strings.Split(valid, ".")
	tampered := parts[0] + "." + parts[1] + "." + parts[2] + "AAAA"
	assertInvalidTokenUnauthorized(t, armGET(t, srv, "Bearer "+tampered))
}

func TestARMBearer_ExpiredTokenRejected(t *testing.T) {
	srv := buildAuthTestSim(t)
	past := time.Now().Add(-2 * time.Hour)
	expired, err := mintAzureSimSignedJWT(map[string]any{
		"aud": "https://management.azure.com/",
		"iss": "https://sts.windows.net/" + simTenantID + "/",
		"iat": past.Unix(),
		"nbf": past.Unix(),
		"exp": past.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("mint expired token: %v", err)
	}
	assertInvalidTokenUnauthorized(t, armGET(t, srv, "Bearer "+expired))
}

func TestARMBearer_WrongAudienceRejected(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	// A validly signed, unexpired token minted for the storage audience must not
	// authorize an Azure Resource Manager request.
	storageTok, err := mintAzureSimSignedJWT(map[string]any{
		"aud": "https://storage.azure.com/",
		"iss": "https://sts.windows.net/" + simTenantID + "/",
		"iat": now.Unix(),
		"nbf": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("mint storage token: %v", err)
	}
	assertInvalidTokenUnauthorized(t, armGET(t, srv, "Bearer "+storageTok))
}

func TestARMBearer_ValidManagementTokenPasses(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	tok, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	rec := armGET(t, srv, "Bearer "+tok)
	// The token is accepted; the handler answers (404 for the absent job, or any
	// non-401 status). The security contract is only that it is not rejected as
	// unauthenticated.
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("valid ARM token rejected with 401: %s", rec.Body.String())
	}
}

// TestARMBearer_ExemptPathsNeedNoBearer confirms the surfaces that authenticate
// with their own scheme (or none) are not gated by the ARM bearer verifier.
func TestARMBearer_ExemptPathsNeedNoBearer(t *testing.T) {
	srv := buildAuthTestSim(t)

	get := func(target string, hdr map[string]string) int {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := get("/health", nil); code != http.StatusOK {
		t.Fatalf("/health without bearer = %d, want 200", code)
	}
	if code := get("/metadata/endpoints?api-version=2022-09-01", nil); code != http.StatusOK {
		t.Fatalf("/metadata/endpoints without bearer = %d, want 200", code)
	}
	// IMDS token endpoint is gated by its own contract (the resource query
	// parameter), not an ARM bearer.
	if code := get("/metadata/identity/oauth2/token?resource=https://management.azure.com/", map[string]string{"Metadata": "true"}); code == http.StatusUnauthorized {
		t.Fatalf("IMDS token endpoint returned 401 — it must not require an ARM bearer")
	}

	// The OAuth2 token endpoint itself must mint a token without any inbound
	// bearer; a request there is how a client first obtains one.
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {"https://management.azure.com/.default"},
	}
	req := httptest.NewRequest(http.MethodPost, "/"+simTenantID+"/oauth2/v2.0/token",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("token endpoint without bearer = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
