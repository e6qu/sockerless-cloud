package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// newOIDCFlowTestServer builds a hermetic sim server carrying the OAuth/OIDC
// middleware plus the Entra/Graph provisioning routes — the surface the
// interactive authorization-code flow spans.
func newOIDCFlowTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new azure sim server: %v", err)
	}
	registerEntra(srv)
	srv.WrapHandler(AzureAuthMiddleware)
	return srv
}

func oidcFlowDo(t *testing.T, srv *sim.Server, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp, data
}

// oidcAuthorize drives GET /{tenant}/oauth2/v2.0/authorize and returns the
// parsed redirect Location. It fails the test on any non-302 response.
func oidcAuthorize(t *testing.T, srv *sim.Server, tenant string, q url.Values) *url.URL {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("http://localhost:4568/%s/oauth2/v2.0/authorize?%s", tenant, q.Encode()), nil)
	resp, body := oidcFlowDo(t, srv, req)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302: %s", resp.StatusCode, body)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect Location: %v", err)
	}
	return loc
}

// oidcAuthorizeCode drives the authorize request and extracts the issued code,
// failing on error redirects.
func oidcAuthorizeCode(t *testing.T, srv *sim.Server, tenant string, q url.Values) string {
	t.Helper()
	loc := oidcAuthorize(t, srv, tenant, q)
	rq := loc.Query()
	if e := rq.Get("error"); e != "" {
		t.Fatalf("authorize redirected with error=%s (%s)", e, rq.Get("error_description"))
	}
	code := rq.Get("code")
	if code == "" {
		t.Fatalf("authorize redirect carries no code: %s", loc)
	}
	return code
}

// oidcToken posts to the token endpoint with optional Basic credentials and
// returns the decoded JSON body plus the HTTP status.
func oidcToken(t *testing.T, srv *sim.Server, tenant string, form url.Values, basicUser, basicSecret string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("http://localhost:4568/%s/oauth2/v2.0/token", tenant),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicUser != "" {
		req.Header.Set("Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(basicUser+":"+basicSecret)))
	}
	resp, data := oidcFlowDo(t, srv, req)
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode token response (%d): %v: %s", resp.StatusCode, err, data)
	}
	return resp.StatusCode, body
}

// oidcJWTClaims decodes the payload segment of a JWT without verification —
// the tests assert claim values, signature verification is covered elsewhere.
func oidcJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal JWT claims: %v", err)
	}
	return claims
}

// oidcCreateGraphUser provisions a user through the standard Graph route and
// returns its object id.
func oidcCreateGraphUser(t *testing.T, srv *sim.Server, displayName, upn string) string {
	t.Helper()
	body := fmt.Sprintf(`{"displayName":%q,"userPrincipalName":%q,"mailNickname":"u","mail":%q,"accountEnabled":true}`,
		displayName, upn, upn)
	req := httptest.NewRequest(http.MethodPost, "http://localhost:4568/v1.0/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, data := oidcFlowDo(t, srv, req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create graph user status = %d, want 201: %s", resp.StatusCode, data)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == "" {
		t.Fatalf("decode created user id: %v: %s", err, data)
	}
	// The Microsoft Graph directory lives in a process-global store, so a user
	// created here outlives the server this test built. Delete it the way a real
	// Graph client would, or a second run in the same process (go test -count=2)
	// collides on the userPrincipalName uniqueness the directory enforces.
	t.Cleanup(func() {
		req := httptest.NewRequest(http.MethodDelete, "http://localhost:4568/v1.0/users/"+created.ID, nil)
		if resp, body := oidcFlowDo(t, srv, req); resp.StatusCode != http.StatusNoContent {
			t.Errorf("delete graph user status = %d, want 204: %s", resp.StatusCode, body)
		}
	})
	return created.ID
}

func TestAzureTokenEndpoint_ClientSecretBasic(t *testing.T) {
	srv := newOIDCFlowTestServer(t)
	const tenant = "basic-tenant"

	t.Run("authorization_code", func(t *testing.T) {
		code := oidcAuthorizeCode(t, srv, tenant, url.Values{
			"client_id":     {"basic-client"},
			"redirect_uri":  {"http://localhost:9400/cb"},
			"response_type": {"code"},
			"scope":         {"openid profile"},
			"state":         {"st-1"},
		})
		status, body := oidcToken(t, srv, tenant, url.Values{
			"grant_type":   {"authorization_code"},
			"code":         {code},
			"redirect_uri": {"http://localhost:9400/cb"},
		}, "basic-client", "s3cret")
		if status != http.StatusOK {
			t.Fatalf("token status = %d, want 200: %v", status, body)
		}
		if body["access_token"] == "" || body["access_token"] == nil {
			t.Fatalf("missing access_token: %v", body)
		}
		idToken, _ := body["id_token"].(string)
		if idToken == "" {
			t.Fatalf("missing id_token: %v", body)
		}
		if aud := oidcJWTClaims(t, idToken)["aud"]; aud != "basic-client" {
			t.Errorf("id_token aud = %v, want basic-client (Basic-supplied client_id)", aud)
		}
	})

	t.Run("authorization_code urlencoded basic components", func(t *testing.T) {
		// RFC 6749 §2.3.1: the Basic components are form-urlencoded before
		// base64; "client+one" must resolve to the client_id "client one".
		code := oidcAuthorizeCode(t, srv, tenant, url.Values{
			"client_id":     {"client one"},
			"redirect_uri":  {"http://localhost:9400/cb"},
			"response_type": {"code"},
			"scope":         {"openid"},
		})
		status, body := oidcToken(t, srv, tenant, url.Values{
			"grant_type":   {"authorization_code"},
			"code":         {code},
			"redirect_uri": {"http://localhost:9400/cb"},
		}, "client+one", "sec%3Aret")
		if status != http.StatusOK {
			t.Fatalf("token status = %d, want 200: %v", status, body)
		}
	})

	t.Run("client_credentials", func(t *testing.T) {
		// The seeded bootstrap application registration (the well-known
		// coordinate every SDK/CLI/Terraform harness authenticates with)
		// stands in for a real confidential client here — real Microsoft
		// Entra rejects an unregistered client_id outright, so this Basic-
		// authorization exercise must present a registered client_id/secret.
		status, body := oidcToken(t, srv, tenant, url.Values{
			"grant_type": {"client_credentials"},
			"scope":      {"https://management.azure.com/.default"},
		}, entraBootstrapClientID, entraBootstrapClientSecret)
		if status != http.StatusOK {
			t.Fatalf("token status = %d, want 200: %v", status, body)
		}
		if tok, _ := body["access_token"].(string); tok == "" {
			t.Fatalf("missing access_token: %v", body)
		}
	})

	t.Run("mismatched basic and form client_id", func(t *testing.T) {
		status, body := oidcToken(t, srv, tenant, url.Values{
			"grant_type": {"client_credentials"},
			"client_id":  {"form-client"},
			"scope":      {"https://management.azure.com/.default"},
		}, "other-client", "s3cret")
		if status != http.StatusBadRequest {
			t.Fatalf("token status = %d, want 400: %v", status, body)
		}
		if body["error"] != "invalid_request" {
			t.Errorf("error = %v, want invalid_request (RFC 6749 §2.3)", body["error"])
		}
	})
}

func TestAzureDiscovery_AdvertisesClientSecretBasic(t *testing.T) {
	srv := newOIDCFlowTestServer(t)
	req := httptest.NewRequest(http.MethodGet,
		"http://localhost:4568/basic-tenant/v2.0/.well-known/openid-configuration", nil)
	resp, data := oidcFlowDo(t, srv, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery status = %d, want 200: %s", resp.StatusCode, data)
	}
	var doc struct {
		Methods []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode discovery document: %v", err)
	}
	want := []string{"client_secret_post", "client_secret_basic"}
	if len(doc.Methods) != len(want) {
		t.Fatalf("token_endpoint_auth_methods_supported = %v, want %v", doc.Methods, want)
	}
	for i, m := range want {
		if doc.Methods[i] != m {
			t.Fatalf("token_endpoint_auth_methods_supported = %v, want %v", doc.Methods, want)
		}
	}
}

func TestAzureAuthorize_LoginHintBindsUser(t *testing.T) {
	srv := newOIDCFlowTestServer(t)
	const tenant = "hint-tenant"
	const upn = "alice.hint@sockerless.test"
	aliceOID := oidcCreateGraphUser(t, srv, "Alice Hint", upn)

	t.Run("login_hint binds tokens to the graph user", func(t *testing.T) {
		code := oidcAuthorizeCode(t, srv, tenant, url.Values{
			"client_id":     {"hint-client"},
			"redirect_uri":  {"http://localhost:9400/cb"},
			"response_type": {"code"},
			"scope":         {"openid profile email"},
			"login_hint":    {upn},
		})
		status, body := oidcToken(t, srv, tenant, url.Values{
			"grant_type":   {"authorization_code"},
			"code":         {code},
			"client_id":    {"hint-client"},
			"redirect_uri": {"http://localhost:9400/cb"},
		}, "", "")
		if status != http.StatusOK {
			t.Fatalf("token status = %d, want 200: %v", status, body)
		}
		idClaims := oidcJWTClaims(t, body["id_token"].(string))
		if idClaims["oid"] != aliceOID {
			t.Errorf("id_token oid = %v, want %s", idClaims["oid"], aliceOID)
		}
		if idClaims["preferred_username"] != upn {
			t.Errorf("id_token preferred_username = %v, want %s", idClaims["preferred_username"], upn)
		}
		atClaims := oidcJWTClaims(t, body["access_token"].(string))
		if atClaims["oid"] != aliceOID {
			t.Errorf("access_token oid = %v, want %s", atClaims["oid"], aliceOID)
		}
	})

	t.Run("without login_hint the default directory identity applies", func(t *testing.T) {
		code := oidcAuthorizeCode(t, srv, tenant, url.Values{
			"client_id":     {"hint-client"},
			"redirect_uri":  {"http://localhost:9400/cb"},
			"response_type": {"code"},
			"scope":         {"openid profile"},
		})
		status, body := oidcToken(t, srv, tenant, url.Values{
			"grant_type":   {"authorization_code"},
			"code":         {code},
			"client_id":    {"hint-client"},
			"redirect_uri": {"http://localhost:9400/cb"},
		}, "", "")
		if status != http.StatusOK {
			t.Fatalf("token status = %d, want 200: %v", status, body)
		}
		idClaims := oidcJWTClaims(t, body["id_token"].(string))
		if want := entraDefaultUser.OID; idClaims["oid"] != want {
			t.Errorf("id_token oid = %v, want default identity %s", idClaims["oid"], want)
		}
	})

	t.Run("unknown login_hint redirects with login_required", func(t *testing.T) {
		loc := oidcAuthorize(t, srv, tenant, url.Values{
			"client_id":     {"hint-client"},
			"redirect_uri":  {"http://localhost:9400/cb"},
			"response_type": {"code"},
			"scope":         {"openid profile"},
			"state":         {"st-unknown"},
			"login_hint":    {"ghost@sockerless.test"},
		})
		rq := loc.Query()
		if rq.Get("error") != "login_required" {
			t.Fatalf("error = %q, want login_required: %s", rq.Get("error"), loc)
		}
		if desc := rq.Get("error_description"); !strings.Contains(desc, "ghost@sockerless.test") {
			t.Errorf("error_description %q does not mention the login_hint", desc)
		}
		if rq.Get("code") != "" {
			t.Errorf("error redirect must not carry an authorization code: %s", loc)
		}
		if rq.Get("state") != "st-unknown" {
			t.Errorf("state = %q, want st-unknown", rq.Get("state"))
		}
	})
}

func TestAzureRefreshToken_KeepsLoginHintBoundUser(t *testing.T) {
	srv := newOIDCFlowTestServer(t)
	const tenant = "refresh-tenant"
	const upn = "bob.refresh@sockerless.test"
	bobOID := oidcCreateGraphUser(t, srv, "Bob Refresh", upn)

	code := oidcAuthorizeCode(t, srv, tenant, url.Values{
		"client_id":     {"refresh-client"},
		"redirect_uri":  {"http://localhost:9400/cb"},
		"response_type": {"code"},
		"scope":         {"openid profile offline_access"},
		"login_hint":    {upn},
	})
	status, body := oidcToken(t, srv, tenant, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"client_id":    {"refresh-client"},
		"redirect_uri": {"http://localhost:9400/cb"},
	}, "", "")
	if status != http.StatusOK {
		t.Fatalf("token status = %d, want 200: %v", status, body)
	}
	refreshToken, _ := body["refresh_token"].(string)
	if refreshToken == "" {
		t.Fatalf("missing refresh_token: %v", body)
	}

	status, body = oidcToken(t, srv, tenant, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {"refresh-client"},
	}, "", "")
	if status != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200: %v", status, body)
	}
	idClaims := oidcJWTClaims(t, body["id_token"].(string))
	if idClaims["oid"] != bobOID {
		t.Errorf("refreshed id_token oid = %v, want %s", idClaims["oid"], bobOID)
	}
	atClaims := oidcJWTClaims(t, body["access_token"].(string))
	if atClaims["oid"] != bobOID {
		t.Errorf("refreshed access_token oid = %v, want %s", atClaims["oid"], bobOID)
	}
}
