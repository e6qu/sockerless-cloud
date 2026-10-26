package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/e6qu/sockerless-cloud/sim"
)

// withConsoleFederation loads the coordinates as buildSimulator does and
// restores the package state afterwards.
func withConsoleFederation(t *testing.T) {
	t.Helper()
	fed, err := loadConsoleFederation()
	if err != nil {
		t.Fatalf("load console federation: %v", err)
	}
	previous := azureConsoleFederation
	azureConsoleFederation = fed
	t.Cleanup(func() { azureConsoleFederation = previous })
}

func TestLoadConsoleFederationAllOrNothing(t *testing.T) {
	t.Run("unset stays unconfigured", func(t *testing.T) {
		fed, err := loadConsoleFederation()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fed.configured() {
			t.Fatalf("unset environment reported configured: %+v", fed)
		}
	})

	t.Run("client id alone is a deployment error", func(t *testing.T) {
		t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID", "console-app")
		if _, err := loadConsoleFederation(); err == nil {
			t.Fatal("expected an error for a client ID with no tenant")
		}
	})

	t.Run("tenant alone is a deployment error", func(t *testing.T) {
		t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_TENANT", "organizations")
		if _, err := loadConsoleFederation(); err == nil {
			t.Fatal("expected an error for a tenant with no client ID")
		}
	})

	t.Run("client id and tenant is complete without an endpoint", func(t *testing.T) {
		t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID", "console-app")
		t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_TENANT", "organizations")
		fed, err := loadConsoleFederation()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !fed.configured() || fed.endpoint != "" {
			t.Fatalf("federation = %+v, want configured with an empty (co-served) endpoint", fed)
		}
	})
}

// One SOCKERLESS_CONSOLE_FEDERATION_* coordinate without the rest is a startup
// error, not a silently disabled broker.
func TestBuildSimulatorFailsLoudOnIncompleteFederationConfig(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID", "console-app")
	if _, err := buildSimulatorWithUI(sim.Config{Provider: "azure", LogLevel: "disabled"}, false); err == nil {
		t.Fatal("buildSimulator accepted an incomplete federation configuration")
	}
}

func federationTestServer(t *testing.T, configureFederation bool) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	if configureFederation {
		t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID", "console-app")
		t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_TENANT", "organizations")
	}
	withConsoleFederation(t)
	srv, err := sim.NewServer(sim.Config{
		Provider: "azure", LogLevel: "disabled", UIOIDCIssuer: "https://auth.example.test",
		UIOIDCClientID: "azure", UIOIDCClientSecret: "secret", UIPublicURL: "https://azure.example.test",
		UISessionSecret: "0123456789abcdef0123456789abcdef", ApplicationReleaseRevision: "0123456789ab",
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv.RegisterUI(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ui")}}, azureConsoleOptions(srv))
	return srv
}

// /ui/config.json never carries the federation endpoint or client ID, which
// stay inside the broker, but keeps the tenant the portal's CLI samples show.
// It is reachable unauthenticated only when the UI's OpenID Connect layer is
// off (see TestFirstPartyOIDCProtectsOperatorSurfacesOnly).
func TestUIConfigJSONNoLongerExposesFederationCredentialCoordinates(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID", "console-app")
	t.Setenv("SOCKERLESS_CONSOLE_FEDERATION_TENANT", "organizations")
	withConsoleFederation(t)
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv.RegisterUI(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ui")}}, azureConsoleOptions(srv))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/config.json", nil))
	var got map[string]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("/ui/config.json response: %d %q", rec.Code, rec.Body.String())
	}
	if got["federationTenant"] != "organizations" {
		t.Fatalf("federationTenant = %q, want organizations (kept for CLI-usage display)", got["federationTenant"])
	}
	if _, present := got["federationClientId"]; present {
		t.Fatal("federationClientId leaked to the browser — the exchange is server-side now")
	}
	if _, present := got["federationEndpoint"]; present {
		t.Fatal("federationEndpoint leaked to the browser — the exchange is server-side now")
	}
	// No UIOIDCIssuer, so the broker route is not registered and must not be
	// advertised.
	if got["federationTokenEndpoint"] != "" {
		t.Fatalf("federationTokenEndpoint = %q, want empty when the console auth layer is disabled", got["federationTokenEndpoint"])
	}
}

func TestFederationTokenRequiresConfiguredFederation(t *testing.T) {
	srv := federationTestServer(t, false)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, FederationTokenPath+"?scope=arm", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body["error"] != "federation_not_configured" {
		t.Fatalf("error = %v, want federation_not_configured", body["error"])
	}
}

func TestFederationTokenRejectsUnknownScope(t *testing.T) {
	srv := federationTestServer(t, true)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, FederationTokenPath+"?scope=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestFederationTokenRequiresOperatorSession(t *testing.T) {
	srv := federationTestServer(t, true)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, FederationTokenPath+"?scope=arm", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if authenticated, _ := body["authenticated"].(bool); authenticated {
		t.Fatalf("body reported authenticated=true for a request with no session: %v", body)
	}
}

// The co-served exchange (empty SOCKERLESS_CONSOLE_FEDERATION_ENDPOINT) must
// reach the server's final handler chain, not a construction-time snapshot, so
// it sees every WrapHandler layer added later.
func TestExchangeFederationAssertionUsesFullHandlerChainInProcess(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	// Wrap after construction, as main.go does.
	var sawWrapper bool
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawWrapper = true
			next.ServeHTTP(w, r)
		})
	})
	previous := azureConsoleFederation
	azureConsoleFederation = consoleFederation{tenant: "test-tenant", clientID: "console-app"}
	t.Cleanup(func() { azureConsoleFederation = previous })

	var gotForm url.Values
	srv.HandleFunc("POST /test-tenant/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotForm = r.Form
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"access_token": "minted-token", "token_type": "Bearer", "expires_in": float64(3600),
		})
	})

	req := httptest.NewRequest(http.MethodGet, FederationTokenPath+"?scope=arm", nil)
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {"console-app"},
		"scope":                 {"https://management.azure.com/.default"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {"the-shauth-assertion"},
	}
	status, contentType, body, err := exchangeFederationAssertion(srv, req, form)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
	if contentType == "" {
		t.Fatal("exchange returned no content type")
	}
	if !sawWrapper {
		t.Fatal("the in-process exchange bypassed a middleware installed after construction — s.handler was stale")
	}
	if gotForm.Get("client_id") != "console-app" || gotForm.Get("client_assertion") != "the-shauth-assertion" {
		t.Fatalf("token endpoint saw form = %v, want the broker's client_credentials request", gotForm)
	}

	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken != "minted-token" {
		t.Fatalf("unexpected exchange body: %s (decode err=%v)", body, err)
	}
}
