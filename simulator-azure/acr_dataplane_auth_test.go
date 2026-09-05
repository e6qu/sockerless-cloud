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

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Azure Container Registry data plane authenticates every request against
// the registry its Host addresses. These tests drive the wire the way a
// Docker-protocol client does — challenge, token, retry — and assert both that
// a valid credential reaches the registry and that every invalid one is
// refused.

const acrAuthTestARMHost = "127.0.0.1:4571"

func newACRTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new azure sim server: %v", err)
	}
	registerACR(srv)
	return srv
}

func acrServe(t *testing.T, srv *sim.Server, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp, body
}

// acrTestRegistry creates a registry through the real ARM PUT and returns it.
func acrTestRegistry(t *testing.T, srv *sim.Server, name string, properties string) Registry {
	t.Helper()
	armURL := fmt.Sprintf("http://%s/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerRegistry/registries/%s?api-version=2023-07-01",
		acrAuthTestARMHost, name)
	body := fmt.Sprintf(`{"location":"eastus","sku":{"name":"Standard"},"properties":%s}`, properties)
	req := httptest.NewRequest(http.MethodPut, armURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, data := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create registry %s: status %d: %s", name, resp.StatusCode, data)
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("decode registry: %v", err)
	}
	if reg.Properties.LoginServer == "" {
		t.Fatal("created registry advertised no loginServer")
	}
	return reg
}

// acrTestAdminCredentials reads a registry's admin credential through the real
// listCredentials action, the way `az acr credential show` does.
func acrTestAdminCredentials(t *testing.T, srv *sim.Server, reg Registry) (string, []string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s%s/listcredentials?api-version=2023-07-01", acrAuthTestARMHost, reg.ID), nil)
	resp, data := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listCredentials: status %d: %s", resp.StatusCode, data)
	}
	var creds struct {
		Username  string `json:"username"`
		Passwords []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"passwords"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	values := make([]string, 0, len(creds.Passwords))
	for _, p := range creds.Passwords {
		values = append(values, p.Value)
	}
	return creds.Username, values
}

// acrRegenerateCredential rotates one admin password slot through the real
// regenerateCredential action.
func acrRegenerateCredential(t *testing.T, srv *sim.Server, reg Registry, slot string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s%s/regenerateCredential?api-version=2023-07-01", acrAuthTestARMHost, reg.ID),
		strings.NewReader(fmt.Sprintf(`{"name":%q}`, slot)))
	req.Header.Set("Content-Type", "application/json")
	resp, data := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("regenerateCredential: status %d: %s", resp.StatusCode, data)
	}
}

// acrDataPlaneRequest addresses a registry's data plane at its login server,
// which is the coordinate the control plane advertises and a client dials.
func acrDataPlaneRequest(reg Registry, method, path, body string) *http.Request {
	req := httptest.NewRequest(method, "http://"+reg.Properties.LoginServer+path, strings.NewReader(body))
	req.Host = reg.Properties.LoginServer
	return req
}

func acrBasicHeader(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// acrRegistryErrorCode reads the error code out of a Docker Registry v2 error
// envelope.
func acrRegistryErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode registry error envelope %q: %v", body, err)
	}
	if len(envelope.Errors) == 0 {
		t.Fatalf("registry error envelope carried no errors: %s", body)
	}
	return envelope.Errors[0].Code
}

// acrAccessTokenFor runs the Docker Registry v2 token exchange a client runs
// after a challenge: HTTP Basic on the challenge realm, in exchange for the
// scoped access token.
func acrAccessTokenFor(t *testing.T, srv *sim.Server, reg Registry, username, password, scope string) (string, int) {
	t.Helper()
	tokenURL := fmt.Sprintf("/oauth2/token?service=%s&scope=%s",
		url.QueryEscape(reg.Properties.LoginServer), url.QueryEscape(scope))
	req := acrDataPlaneRequest(reg, http.MethodGet, tokenURL, "")
	if username != "" || password != "" {
		req.Header.Set("Authorization", acrBasicHeader(username, password))
	}
	resp, data := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode access token: %v", err)
	}
	return out.AccessToken, resp.StatusCode
}

// TestACRDataPlaneChallengesUnauthenticatedRequests asserts the Docker
// Registry v2 Bearer challenge: the header names this registry's token
// service, the registry it authenticates for, and the scope the client needs,
// and the body is the registry error envelope with UNAUTHORIZED.
func TestACRDataPlaneChallengesUnauthenticatedRequests(t *testing.T) {
	srv := newACRTestServer(t)
	reg := acrTestRegistry(t, srv, "challengeregistry", `{"adminUserEnabled":true}`)

	for _, tc := range []struct {
		name      string
		method    string
		path      string
		wantScope string
	}{
		{"base endpoint", http.MethodGet, "/v2/", "registry:catalog:*"},
		{"manifest pull", http.MethodGet, "/v2/team/app/manifests/v1", "repository:team/app:pull"},
		{"manifest push", http.MethodPut, "/v2/team/app/manifests/v1", "repository:team/app:pull,push"},
		{"blob upload", http.MethodPost, "/v2/team/app/blobs/uploads/", "repository:team/app:pull,push"},
		{"manifest delete", http.MethodDelete, "/v2/team/app/manifests/v1", "repository:team/app:delete"},
		{"tag list", http.MethodGet, "/v2/team/app/tags/list", "repository:team/app:pull"},
		{"acr catalog", http.MethodGet, "/acr/v1/_catalog", "registry:catalog:*"},
		{"acr tags", http.MethodGet, "/acr/v1/team/app/_tags", "repository:team/app:metadata_read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := acrServe(t, srv, acrDataPlaneRequest(reg, tc.method, tc.path, ""))
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", resp.StatusCode, body)
			}
			want := fmt.Sprintf(`Bearer realm="http://%s/oauth2/token",service="%s",scope="%s"`,
				reg.Properties.LoginServer, reg.Properties.LoginServer, tc.wantScope)
			if got := resp.Header.Get("WWW-Authenticate"); got != want {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
			}
			if code := acrRegistryErrorCode(t, body); code != "UNAUTHORIZED" {
				t.Fatalf("error code = %q, want UNAUTHORIZED", code)
			}
		})
	}
}

// TestACRDataPlaneAdminCredential covers the credential `docker login` and the
// console present: the registry's admin username and one of the two passwords
// listCredentials serves.
func TestACRDataPlaneAdminCredential(t *testing.T) {
	srv := newACRTestServer(t)
	reg := acrTestRegistry(t, srv, "adminregistry", `{"adminUserEnabled":true}`)
	username, passwords := acrTestAdminCredentials(t, srv, reg)
	if len(passwords) != 2 {
		t.Fatalf("listCredentials served %d passwords, want 2", len(passwords))
	}

	for i, password := range passwords {
		req := acrDataPlaneRequest(reg, http.MethodGet, "/v2/", "")
		req.Header.Set("Authorization", acrBasicHeader(username, password))
		resp, body := acrServe(t, srv, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("password slot %d: status = %d, want 200: %s", i, resp.StatusCode, body)
		}
	}

	// A wrong password, an unknown username, and a malformed credential are
	// all refused, and the refusal says a credential was presented and
	// rejected rather than absent.
	for _, tc := range []struct{ name, header string }{
		{"wrong password", acrBasicHeader(username, "not-the-password")},
		{"unknown username", acrBasicHeader("someone-else", passwords[0])},
		{"malformed credential", "Basic not-base64!!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := acrDataPlaneRequest(reg, http.MethodGet, "/v2/", "")
			req.Header.Set("Authorization", tc.header)
			resp, body := acrServe(t, srv, req)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", resp.StatusCode, body)
			}
			if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, `error="invalid_token"`) {
				t.Fatalf("challenge %q must report the rejected credential", got)
			}
		})
	}
}

// TestACRDataPlaneAdminUserDisabled asserts that the admin credential exists
// only while the registry's admin user is enabled: listCredentials refuses to
// serve it, and the material it would derive does not authenticate.
func TestACRDataPlaneAdminUserDisabled(t *testing.T) {
	srv := newACRTestServer(t)
	enabled := acrTestRegistry(t, srv, "enabledregistry", `{"adminUserEnabled":true}`)
	username, passwords := acrTestAdminCredentials(t, srv, enabled)

	disabled := acrTestRegistry(t, srv, "disabledregistry", `{"adminUserEnabled":false}`)
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s%s/listcredentials?api-version=2023-07-01", acrAuthTestARMHost, disabled.ID), nil)
	resp, _ := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("listCredentials on an admin-disabled registry = %d, want 400", resp.StatusCode)
	}

	// The enabled registry's own credential does not authenticate against the
	// registry that has no admin user.
	dataReq := acrDataPlaneRequest(disabled, http.MethodGet, "/v2/", "")
	dataReq.Header.Set("Authorization", acrBasicHeader(username, passwords[0]))
	resp, body := acrServe(t, srv, dataReq)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", resp.StatusCode, body)
	}

	// Enabling the admin user is what makes the credential work, so the same
	// registry authenticates its own credential once it is enabled.
	patch := httptest.NewRequest(http.MethodPatch,
		fmt.Sprintf("http://%s%s?api-version=2023-07-01", acrAuthTestARMHost, disabled.ID),
		strings.NewReader(`{"properties":{"adminUserEnabled":true}}`))
	patch.Header.Set("Content-Type", "application/json")
	if resp, body := acrServe(t, srv, patch); resp.StatusCode != http.StatusOK {
		t.Fatalf("enable admin user: status %d: %s", resp.StatusCode, body)
	}
	nowUser, nowPasswords := acrTestAdminCredentials(t, srv, disabled)
	dataReq = acrDataPlaneRequest(disabled, http.MethodGet, "/v2/", "")
	dataReq.Header.Set("Authorization", acrBasicHeader(nowUser, nowPasswords[0]))
	if resp, body := acrServe(t, srv, dataReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
}

// TestACRDataPlaneScopedAccessToken drives the full Docker Registry v2 flow —
// challenge, token, retry — and asserts the token authorizes exactly its
// scope: the repository it was minted for, and no other.
func TestACRDataPlaneScopedAccessToken(t *testing.T) {
	srv := newACRTestServer(t)
	reg := acrTestRegistry(t, srv, "scoperegistry", `{"adminUserEnabled":true}`)
	username, passwords := acrTestAdminCredentials(t, srv, reg)

	token, status := acrAccessTokenFor(t, srv, reg, username, passwords[0], "repository:mine/app:pull,push")
	if status != http.StatusOK {
		t.Fatalf("token request status = %d, want 200", status)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("access token %q is not a JWT", token)
	}

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`

	push := acrDataPlaneRequest(reg, http.MethodPut, "/v2/mine/app/manifests/v1", manifest)
	push.Header.Set("Authorization", "Bearer "+token)
	push.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	if resp, body := acrServe(t, srv, push); resp.StatusCode != http.StatusCreated {
		t.Fatalf("scoped push status = %d, want 201: %s", resp.StatusCode, body)
	}

	// The same token does not reach another repository of the same registry.
	other := acrDataPlaneRequest(reg, http.MethodPut, "/v2/someone/else/manifests/v1", manifest)
	other.Header.Set("Authorization", "Bearer "+token)
	other.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	resp, body := acrServe(t, srv, other)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-repository push status = %d, want 401: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, `error="insufficient_scope"`) ||
		!strings.Contains(got, `scope="repository:someone/else:pull,push"`) {
		t.Fatalf("challenge %q must name the missing scope", got)
	}
	if code := acrRegistryErrorCode(t, body); code != "UNAUTHORIZED" {
		t.Fatalf("error code = %q, want UNAUTHORIZED", code)
	}

	// A pull-only token does not push.
	pullOnly, _ := acrAccessTokenFor(t, srv, reg, username, passwords[0], "repository:mine/app:pull")
	req := acrDataPlaneRequest(reg, http.MethodPut, "/v2/mine/app/manifests/v2", manifest)
	req.Header.Set("Authorization", "Bearer "+pullOnly)
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	if resp, body := acrServe(t, srv, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("pull-only push status = %d, want 401: %s", resp.StatusCode, body)
	}
	// …but it does pull.
	req = acrDataPlaneRequest(reg, http.MethodGet, "/v2/mine/app/manifests/v1", "")
	req.Header.Set("Authorization", "Bearer "+pullOnly)
	if resp, body := acrServe(t, srv, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("pull-only pull status = %d, want 200: %s", resp.StatusCode, body)
	}
}

// TestACRBaseEndpointNeedsAuthenticationOnly asserts the Docker Registry v2
// base endpoint is authenticated but not authorized against an access record:
// a token acquired with an empty scope answers the ping — which is what a
// client that asks its token service for a scope-less token holds — while it
// still reaches no repository.
func TestACRBaseEndpointNeedsAuthenticationOnly(t *testing.T) {
	srv := newACRTestServer(t)
	reg := acrTestRegistry(t, srv, "pingregistry", `{"adminUserEnabled":true}`)
	username, passwords := acrTestAdminCredentials(t, srv, reg)

	token, status := acrAccessTokenFor(t, srv, reg, username, passwords[0], "")
	if status != http.StatusOK {
		t.Fatalf("scope-less token request = %d, want 200", status)
	}

	ping := acrDataPlaneRequest(reg, http.MethodGet, "/v2/", "")
	ping.Header.Set("Authorization", "Bearer "+token)
	if resp, body := acrServe(t, srv, ping); resp.StatusCode != http.StatusOK {
		t.Fatalf("ping with a scope-less token = %d, want 200: %s", resp.StatusCode, body)
	}

	repo := acrDataPlaneRequest(reg, http.MethodGet, "/v2/mine/app/tags/list", "")
	repo.Header.Set("Authorization", "Bearer "+token)
	if resp, body := acrServe(t, srv, repo); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("repository read with a scope-less token = %d, want 401: %s", resp.StatusCode, body)
	}
}

// TestACRDataPlaneTokenIsRegistryBound asserts a token one registry issued
// does not authenticate against another, and that a garbled bearer is refused.
func TestACRDataPlaneTokenIsRegistryBound(t *testing.T) {
	srv := newACRTestServer(t)
	first := acrTestRegistry(t, srv, "firstregistry", `{"adminUserEnabled":true}`)
	second := acrTestRegistry(t, srv, "secondregistry", `{"adminUserEnabled":true}`)
	username, passwords := acrTestAdminCredentials(t, srv, first)

	token, _ := acrAccessTokenFor(t, srv, first, username, passwords[0], "repository:shared/app:pull")
	req := acrDataPlaneRequest(second, http.MethodGet, "/v2/shared/app/manifests/v1", "")
	req.Header.Set("Authorization", "Bearer "+token)
	if resp, body := acrServe(t, srv, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("foreign-registry token status = %d, want 401: %s", resp.StatusCode, body)
	}

	req = acrDataPlaneRequest(first, http.MethodGet, "/v2/shared/app/manifests/v1", "")
	req.Header.Set("Authorization", "Bearer not.a.token")
	if resp, body := acrServe(t, srv, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("garbled bearer status = %d, want 401: %s", resp.StatusCode, body)
	}
}

// TestACRDataPlaneRegenerateCredentialRevokes asserts a regenerated admin
// password stops authenticating, and that the access tokens it had already
// produced stop with it — the rotation contract every key surface of this
// simulator keeps.
func TestACRDataPlaneRegenerateCredentialRevokes(t *testing.T) {
	srv := newACRTestServer(t)
	reg := acrTestRegistry(t, srv, "rotationregistry", `{"adminUserEnabled":true}`)
	username, passwords := acrTestAdminCredentials(t, srv, reg)

	token, _ := acrAccessTokenFor(t, srv, reg, username, passwords[0], "repository:mine/app:pull,push")
	probe := func() int {
		req := acrDataPlaneRequest(reg, http.MethodGet, "/v2/mine/app/tags/list", "")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, _ := acrServe(t, srv, req)
		return resp.StatusCode
	}
	if status := probe(); status != http.StatusOK {
		t.Fatalf("token before rotation = %d, want 200", status)
	}

	acrRegenerateCredential(t, srv, reg, "password")

	if status := probe(); status != http.StatusUnauthorized {
		t.Fatalf("token derived from the rotated password = %d, want 401", status)
	}
	oldBasic := acrDataPlaneRequest(reg, http.MethodGet, "/v2/", "")
	oldBasic.Header.Set("Authorization", acrBasicHeader(username, passwords[0]))
	if resp, _ := acrServe(t, srv, oldBasic); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("rotated password still authenticates: status %d", resp.StatusCode)
	}

	// The other slot is untouched, and the rotated slot authenticates under
	// its new value.
	_, rotated := acrTestAdminCredentials(t, srv, reg)
	if rotated[1] != passwords[1] {
		t.Fatal("regenerating one slot must not change the other")
	}
	for _, password := range rotated {
		req := acrDataPlaneRequest(reg, http.MethodGet, "/v2/", "")
		req.Header.Set("Authorization", acrBasicHeader(username, password))
		if resp, body := acrServe(t, srv, req); resp.StatusCode != http.StatusOK {
			t.Fatalf("current password rejected: status %d: %s", resp.StatusCode, body)
		}
	}
}

// TestACRDataPlaneAnonymousPull asserts the anonymousPullEnabled property is
// what it claims to be: unauthenticated pulls succeed on a registry that sets
// it, unauthenticated writes still do not, and a registry without it refuses
// the same pull.
func TestACRDataPlaneAnonymousPull(t *testing.T) {
	srv := newACRTestServer(t)
	open := acrTestRegistry(t, srv, "anonymousregistry", `{"adminUserEnabled":true,"anonymousPullEnabled":true}`)
	username, passwords := acrTestAdminCredentials(t, srv, open)

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`
	push := acrDataPlaneRequest(open, http.MethodPut, "/v2/public/app/manifests/v1", manifest)
	push.Header.Set("Authorization", acrBasicHeader(username, passwords[0]))
	push.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	if resp, body := acrServe(t, srv, push); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed push status = %d, want 201: %s", resp.StatusCode, body)
	}

	if resp, body := acrServe(t, srv, acrDataPlaneRequest(open, http.MethodGet, "/v2/public/app/manifests/v1", "")); resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous pull status = %d, want 200: %s", resp.StatusCode, body)
	}
	anonPush := acrDataPlaneRequest(open, http.MethodPut, "/v2/public/app/manifests/v2", manifest)
	anonPush.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	if resp, body := acrServe(t, srv, anonPush); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous push status = %d, want 401: %s", resp.StatusCode, body)
	}

	closed := acrTestRegistry(t, srv, "privateregistry", `{"adminUserEnabled":true}`)
	if resp, body := acrServe(t, srv, acrDataPlaneRequest(closed, http.MethodGet, "/v2/public/app/manifests/v1", "")); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous pull from a private registry = %d, want 401: %s", resp.StatusCode, body)
	}
}

// TestACRDataPlaneUnknownHost asserts a data-plane request addressed to a host
// that is not a registry login server reaches no registry.
func TestACRDataPlaneUnknownHost(t *testing.T) {
	srv := newACRTestServer(t)
	acrTestRegistry(t, srv, "hostregistry", `{"adminUserEnabled":true}`)

	req := httptest.NewRequest(http.MethodGet, "http://not-a-registry.example/v2/", nil)
	req.Host = "not-a-registry.example"
	resp, body := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, body)
	}
	if code := acrRegistryErrorCode(t, body); code != "NAME_UNKNOWN" {
		t.Fatalf("error code = %q, want NAME_UNKNOWN", code)
	}
}

// TestACRTokenServiceRefusesBadCredentials covers the token service itself:
// the credential it mints from must authenticate, and the service it is asked
// for must be the registry it was reached at.
func TestACRTokenServiceRefusesBadCredentials(t *testing.T) {
	srv := newACRTestServer(t)
	reg := acrTestRegistry(t, srv, "tokenserviceregistry", `{"adminUserEnabled":true}`)
	username, passwords := acrTestAdminCredentials(t, srv, reg)

	if _, status := acrAccessTokenFor(t, srv, reg, username, "wrong", "repository:mine/app:pull"); status != http.StatusUnauthorized {
		t.Fatalf("token for a wrong password = %d, want 401", status)
	}
	if _, status := acrAccessTokenFor(t, srv, reg, "", "", "repository:mine/app:pull"); status != http.StatusUnauthorized {
		t.Fatalf("token with no credential = %d, want 401", status)
	}

	// The `service` the challenge told the client to send must name the
	// registry the request reached.
	req := acrDataPlaneRequest(reg, http.MethodGet, "/oauth2/token?service=other.azurecr.io&scope=repository:mine/app:pull", "")
	req.Header.Set("Authorization", acrBasicHeader(username, passwords[0]))
	if resp, body := acrServe(t, srv, req); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched service = %d, want 400: %s", resp.StatusCode, body)
	}

	// The refresh-token grant verifies the refresh token.
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"service":       {reg.Properties.LoginServer},
		"scope":         {"repository:mine/app:pull"},
		"refresh_token": {"not-a-refresh-token"},
	}
	post := acrDataPlaneRequest(reg, http.MethodPost, "/oauth2/token", form.Encode())
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, body := acrServe(t, srv, post); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bogus refresh token = %d, want 401: %s", resp.StatusCode, body)
	}

	// /oauth2/exchange verifies the Microsoft Entra access token it is handed.
	exchange := url.Values{
		"grant_type":   {"access_token"},
		"service":      {reg.Properties.LoginServer},
		"access_token": {"not-an-entra-token"},
	}
	req = acrDataPlaneRequest(reg, http.MethodPost, "/oauth2/exchange", exchange.Encode())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, body := acrServe(t, srv, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bogus Entra token = %d, want 401: %s", resp.StatusCode, body)
	}
}

// TestACRAnonymousTokenGrantsPullOnly asserts the token service grants an
// anonymous request only what a registry with anonymous pull offers, so a
// token asked for with push actions comes back carrying pull alone.
func TestACRAnonymousTokenGrantsPullOnly(t *testing.T) {
	srv := newACRTestServer(t)
	reg := acrTestRegistry(t, srv, "anonymoustokenregistry", `{"adminUserEnabled":true,"anonymousPullEnabled":true}`)

	form := url.Values{
		"grant_type":    {"password"},
		"service":       {reg.Properties.LoginServer},
		"scope":         {"repository:mine/app:pull,push"},
		"refresh_token": {""},
	}
	req := acrDataPlaneRequest(reg, http.MethodPost, "/oauth2/token", form.Encode())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, data := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous token status = %d, want 200: %s", resp.StatusCode, data)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode access token: %v", err)
	}

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`
	push := acrDataPlaneRequest(reg, http.MethodPut, "/v2/mine/app/manifests/v1", manifest)
	push.Header.Set("Authorization", "Bearer "+out.AccessToken)
	push.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	if resp, body := acrServe(t, srv, push); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous token push = %d, want 401: %s", resp.StatusCode, body)
	}
}
