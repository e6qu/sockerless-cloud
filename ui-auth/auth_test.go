package uiauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

func testConfig() Config {
	return Config{
		Issuer: "https://auth.example.test", ClientID: "simulator-test",
		ClientSecret: "client-secret", PublicURL: "https://sim.example.test",
		SessionSecret: "0123456789abcdef0123456789abcdef", CookieName: "sim_session",
		ApplicationName: "Simulator", ReleaseRevision: "0123456789ab", SessionLifetime: time.Hour,
	}
}

func TestConfigRequiresCompleteSecureCoordinates(t *testing.T) {
	if _, err := New(Config{}); err != nil {
		t.Fatalf("disabled authentication: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"missing client secret": func(c *Config) { c.ClientSecret = "" },
		"short session secret":  func(c *Config) { c.SessionSecret = "short" },
		"insecure issuer":       func(c *Config) { c.Issuer = "http://auth.example.test" },
		"issuer user info":      func(c *Config) { c.Issuer = "https://user@auth.example.test" },
		"public path":           func(c *Config) { c.PublicURL += "/sim" },
		"missing release":       func(c *Config) { c.ReleaseRevision = "" },
		"moving release":        func(c *Config) { c.ReleaseRevision = "latest" },
	} {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("invalid OpenID Connect configuration was accepted")
			}
		})
	}
}

func TestValidationRequiresSessionAndExposesExactContract(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	anonymous := httptest.NewRequest(http.MethodGet, ValidationPath, nil)
	anonymous.Header.Set("Authorization", "Bearer validator-material-must-not-authenticate")
	recorder := httptest.NewRecorder()
	auth.validation(recorder, anonymous)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != SignedOutPath {
		t.Fatalf("anonymous validation = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}

	expires := time.Now().Add(time.Hour).Unix()
	session := browserSession{ID: "validation-session", Subject: "subject", Name: "shauth-validator", Email: "validator@example.test", Role: "developer", Expires: expires}
	value, err := auth.sign(session)
	if err != nil {
		t.Fatal(err)
	}
	auth.store.put(session.ID, sessionRecord{Subject: session.Subject, Expires: expires})
	request := httptest.NewRequest(http.MethodGet, ValidationPath, nil)
	request.AddCookie(&http.Cookie{Name: auth.config.CookieName, Value: value})
	recorder = httptest.NewRecorder()
	auth.validation(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated validation = %d Cache-Control=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	if csp := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://auth.example.test") {
		t.Fatalf("validation Content-Security-Policy omitted the exact Shauth origin: %q", csp)
	}
	for _, expected := range []string{
		`data-testid="validation-username">shauth-validator</dd>`,
		`data-testid="validation-email">validator@example.test</dd>`,
		`data-testid="validation-role">developer</dd>`,
		`data-testid="validation-release">0123456789ab</dd>`,
		`action="/auth/logout"`, `>Sign out</button>`,
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("validation response omitted %q: %s", expected, recorder.Body.String())
		}
	}
}

func TestURLOriginDropsIssuerPath(t *testing.T) {
	if got := urlOrigin("https://auth.example.test/tenant/"); got != "https://auth.example.test" {
		t.Fatalf("OpenID Connect issuer origin = %q", got)
	}
}

func TestConfigAllowsHTTPOnlyForExplicitLoopbackDevelopment(t *testing.T) {
	config := testConfig()
	config.Issuer = "http://localhost:8080"
	config.PublicURL = "http://127.0.0.1:4566"
	config.InsecureCookies = true
	if _, err := New(config); err != nil {
		t.Fatalf("explicit loopback development configuration: %v", err)
	}
	config.Issuer = "http://auth.example.test"
	if _, err := New(config); err == nil {
		t.Fatal("public HTTP issuer was accepted in insecure development mode")
	}
}

func TestConfigPreservesExactIssuer(t *testing.T) {
	config := testConfig()
	config.Issuer = "https://auth.example.test/tenant/"
	auth, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if auth.config.Issuer != config.Issuer {
		t.Fatalf("issuer = %q, want exact %q", auth.config.Issuer, config.Issuer)
	}
}

func TestProtectRedirectsOnlyTheUserInterfaceToLogin(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	protected := auth.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://sim.example.test/ui/tasks?next=1", nil))
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != LoginPath+"?return_to=%2Fui%2Ftasks%3Fnext%3D1" {
		t.Fatalf("redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}

	disabled, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	disabled.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("disabled auth status = %d", recorder.Code)
	}
}

func TestSignedSessionIdentityAndRevocation(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).Unix()
	session := browserSession{ID: "session-1", Subject: "subject", Name: "Ada", Email: "ada@example.test", Role: "admin", Expires: expires}
	value, err := auth.sign(session)
	if err != nil {
		t.Fatal(err)
	}
	auth.store.put(session.ID, sessionRecord{Subject: session.Subject, Expires: expires})

	request := httptest.NewRequest(http.MethodGet, SessionPath, nil)
	request.AddCookie(&http.Cookie{Name: auth.config.CookieName, Value: value})
	recorder := httptest.NewRecorder()
	auth.session(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("session status = %d", recorder.Code)
	}
	var identity map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity["name"] != "Ada" || identity["role"] != "admin" {
		t.Fatalf("identity = %#v", identity)
	}

	auth.store.delete(session.ID)
	recorder = httptest.NewRecorder()
	auth.session(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked status = %d", recorder.Code)
	}
}

func TestBackchannelLogoutRevokesMatchingSessionAndRejectsReplay(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	auth.store.put("target", sessionRecord{Subject: "subject", UpstreamSID: "upstream", Expires: now.Add(time.Hour).Unix()})
	auth.store.put("other", sessionRecord{Subject: "other", UpstreamSID: "other", Expires: now.Add(time.Hour).Unix()})
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: privateKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	signClaims := func(claims map[string]any) string {
		t.Helper()
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		signed, err := signer.Sign(payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := signed.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw := signClaims(map[string]any{
		"iss": auth.config.Issuer, "sub": "subject", "sid": "upstream", "aud": auth.config.ClientID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "jti": "logout-1",
		"events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}},
	})
	verifier := oidc.NewVerifier(auth.config.Issuer, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{privateKey.Public()}}, &oidc.Config{ClientID: auth.config.ClientID, Now: func() time.Time { return now }})
	if err := auth.processBackchannelLogout(context.Background(), raw, verifier, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.store.get("target", now); ok {
		t.Fatal("target session remained active")
	}
	if _, ok := auth.store.get("other", now); !ok {
		t.Fatal("unrelated session was revoked")
	}
	if err := auth.processBackchannelLogout(context.Background(), raw, verifier, now); err == nil {
		t.Fatal("replayed logout token was accepted")
	}
	missingIssuedAt := signClaims(map[string]any{
		"iss": auth.config.Issuer, "sub": "subject", "aud": auth.config.ClientID,
		"exp": now.Add(5 * time.Minute).Unix(), "jti": "logout-2",
		"events": map[string]any{backchannelLogoutEvent: map[string]any{}},
	})
	if err := auth.processBackchannelLogout(context.Background(), missingIssuedAt, verifier, now); err == nil {
		t.Fatal("logout token without iat was accepted")
	}
	missingExpiry := signClaims(map[string]any{
		"iss": auth.config.Issuer, "sub": "subject", "aud": auth.config.ClientID,
		"iat": now.Unix(), "jti": "logout-missing-expiry",
		"events": map[string]any{backchannelLogoutEvent: map[string]any{}},
	})
	if err := auth.processBackchannelLogout(context.Background(), missingExpiry, verifier, now); err == nil {
		t.Fatal("logout token without exp was accepted")
	}
	invalidEvent := signClaims(map[string]any{
		"iss": auth.config.Issuer, "sub": "subject", "aud": auth.config.ClientID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "jti": "logout-3",
		"events": map[string]any{backchannelLogoutEvent: nil},
	})
	if err := auth.processBackchannelLogout(context.Background(), invalidEvent, verifier, now); err == nil {
		t.Fatal("logout token with a null event was accepted")
	}
}

func TestLogoutRejectsCrossOriginBeforeProviderAccess(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, LogoutPath, nil)
	request.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	auth.logout(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin logout status = %d", recorder.Code)
	}
}

func TestLogoutRevokesLocalSessionBeforeProviderFailure(t *testing.T) {
	config := testConfig()
	config.Issuer = "https://127.0.0.1:1"
	auth, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).Unix()
	session := browserSession{ID: "session-1", Subject: "subject", Role: "developer", Expires: expires}
	value, err := auth.sign(session)
	if err != nil {
		t.Fatal(err)
	}
	auth.store.put(session.ID, sessionRecord{Subject: session.Subject, RawIDToken: "id-token", Expires: expires})

	request := httptest.NewRequest(http.MethodPost, config.PublicURL+LogoutPath, nil)
	request.Header.Set("Origin", config.PublicURL)
	request.AddCookie(&http.Cookie{Name: config.CookieName, Value: value})
	recorder := httptest.NewRecorder()
	auth.logout(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("provider failure status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if _, exists := auth.store.get(session.ID, time.Now()); exists {
		t.Fatal("local session remained active after provider failure")
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	cleared := false
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == config.CookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("session cookie was not cleared before provider failure")
	}
}

func TestLogoutRequiresSameOriginBrowserEvidence(t *testing.T) {
	for name, testCase := range map[string]struct {
		configure func(*http.Request)
		want      bool
	}{
		"same origin":                {configure: func(r *http.Request) { r.Header.Set("Origin", "https://sim.example.test") }, want: true},
		"same-origin referer":        {configure: func(r *http.Request) { r.Header.Set("Referer", "https://sim.example.test/ui/") }, want: true},
		"same-origin fetch metadata": {configure: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }, want: true},
		"missing evidence":           {configure: func(*http.Request) {}, want: false},
		"cross-origin referer":       {configure: func(r *http.Request) { r.Header.Set("Referer", "https://attacker.example/") }, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://sim.example.test"+LogoutPath, nil)
			testCase.configure(request)
			if got := sameOrigin(request, testConfig().PublicURL); got != testCase.want {
				t.Fatalf("sameOrigin() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestLogoutURLReturnsToOriginatingSimulator(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	logoutURL, err := auth.logoutURL("https://auth.example.test/oauth2/sessions/logout", "signed-id-token")
	if err != nil {
		t.Fatal(err)
	}
	if logoutURL.Scheme != "https" || logoutURL.Host != "auth.example.test" || logoutURL.Path != "/oauth2/sessions/logout" {
		t.Fatalf("logout endpoint = %q", logoutURL.String())
	}
	query := logoutURL.Query()
	if got := query.Get("client_id"); got != testConfig().ClientID {
		t.Fatalf("client_id = %q", got)
	}
	if got := query.Get("id_token_hint"); got != "signed-id-token" {
		t.Fatalf("id_token_hint = %q", got)
	}
	if got := query.Get("post_logout_redirect_uri"); got != "https://sim.example.test"+LogoutCompletePath {
		t.Fatalf("post_logout_redirect_uri = %q", got)
	}
}

func TestLogoutCompleteUsesOnlyConfiguredIssuer(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://sim.example.test"+LogoutCompletePath+"?next=https%3A%2F%2Fattacker.example&code=secret", nil)
	recorder := httptest.NewRecorder()
	auth.logoutComplete(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "https://auth.example.test/oauth/logout/complete" {
		t.Fatalf("bridge location = %q", location)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("logout completion bridge omitted no-store/no-referrer headers")
	}
}

func TestLogoutURLRejectsAnotherIssuerOrigin(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.logoutURL("https://attacker.example/oauth2/sessions/logout", ""); err == nil {
		t.Fatal("cross-origin logout endpoint was accepted")
	}
}

func TestBackchannelLogoutRequiresFormBody(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for name, testCase := range map[string]struct {
		target, contentType string
		wantStatus          int
	}{
		"missing media type": {target: BackchannelLogoutPath, wantStatus: http.StatusUnsupportedMediaType},
		"JSON media type":    {target: BackchannelLogoutPath, contentType: "application/json", wantStatus: http.StatusUnsupportedMediaType},
		"query token":        {target: BackchannelLogoutPath + "?logout_token=query", contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, testCase.target, strings.NewReader(""))
			if testCase.contentType != "" {
				request.Header.Set("Content-Type", testCase.contentType)
			}
			recorder := httptest.NewRecorder()
			auth.backchannelLogout(recorder, request)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestFrontchannelLogoutRevokesOnlyTrustedIssuerSession(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).Unix()
	auth.store.put("session-1", sessionRecord{Subject: "subject", UpstreamSID: "provider-session", Expires: expires})

	request := httptest.NewRequest(http.MethodGet, FrontchannelLogoutPath+"?iss=https%3A%2F%2Fattacker.example&sid=provider-session", nil)
	recorder := httptest.NewRecorder()
	auth.frontchannelLogout(recorder, request)
	if _, exists := auth.store.get("session-1", time.Now()); !exists {
		t.Fatal("untrusted front-channel issuer revoked a session")
	}

	request = httptest.NewRequest(http.MethodGet, FrontchannelLogoutPath+"?iss=https%3A%2F%2Fauth.example.test&sid=provider-session", nil)
	recorder = httptest.NewRecorder()
	auth.frontchannelLogout(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("front-channel response = %d Cache-Control=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	if _, exists := auth.store.get("session-1", time.Now()); exists {
		t.Fatal("trusted front-channel logout left the session active")
	}
}

func TestBackchannelLogoutEventMustBeJSONObject(t *testing.T) {
	for name, testCase := range map[string]struct {
		raw  string
		want bool
	}{
		"empty object":     {raw: `{}`, want: true},
		"non-empty object": {raw: `{"reason":"admin"}`, want: false},
		"missing":          {raw: ``, want: false},
		"null":             {raw: `null`, want: false},
		"string":           {raw: `"logout"`, want: false},
		"array":            {raw: `[]`, want: false},
		"invalid":          {raw: `{`, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := validLogoutEvent(json.RawMessage(testCase.raw)); got != testCase.want {
				t.Fatalf("validLogoutEvent(%q) = %v, want %v", testCase.raw, got, testCase.want)
			}
		})
	}
}

func TestSignedOutResponseIsNotCached(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	auth.signedOut(recorder, httptest.NewRequest(http.MethodGet, SignedOutPath, nil))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("signed-out response = %d Cache-Control=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `href="/auth/oidc/login"`) || !strings.Contains(body, `>Sign in with Shauth</a>`) {
		t.Fatalf("signed-out response omitted the explicit Shauth sign-in control: %s", body)
	}
}

func TestOperatorAssertionBrokersTheSignedInIDToken(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	// No session: the broker has nothing to federate.
	anonymous := httptest.NewRequest(http.MethodGet, "/auth/cloud-token", nil)
	if _, _, _, ok := auth.OperatorAssertion(anonymous); ok {
		t.Fatal("OperatorAssertion reported a session for an anonymous request")
	}

	// A signed-in operator whose ID token was captured at callback.
	expires := time.Now().Add(time.Hour).Unix()
	session := browserSession{ID: "broker-session", Subject: "operator", Expires: expires}
	value, err := auth.sign(session)
	if err != nil {
		t.Fatal(err)
	}
	auth.store.put(session.ID, sessionRecord{Subject: session.Subject, RawIDToken: "the-shauth-assertion", Expires: expires})

	request := httptest.NewRequest(http.MethodGet, "/auth/cloud-token", nil)
	request.AddCookie(&http.Cookie{Name: auth.config.CookieName, Value: value})
	assertion, issuer, audience, ok := auth.OperatorAssertion(request)
	if !ok {
		t.Fatal("OperatorAssertion did not report the signed-in session")
	}
	if assertion != "the-shauth-assertion" {
		t.Fatalf("assertion = %q, want the captured ID token", assertion)
	}
	if issuer != auth.config.Issuer || audience != auth.config.ClientID {
		t.Fatalf("federation coordinates = %q/%q, want %q/%q", issuer, audience, auth.config.Issuer, auth.config.ClientID)
	}

	// A session record without a captured ID token cannot be federated.
	auth.store.put(session.ID, sessionRecord{Subject: session.Subject, Expires: expires})
	if _, _, _, ok := auth.OperatorAssertion(request); ok {
		t.Fatal("OperatorAssertion federated a session that has no ID token")
	}
}

func TestFederationSubjectExposesTheAssertionToTheConsole(t *testing.T) {
	auth, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	anon := httptest.NewRecorder()
	auth.federationSubject(anon, httptest.NewRequest(http.MethodGet, FederationSubjectPath, nil))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous federation-subject = %d, want 401", anon.Code)
	}

	expires := time.Now().Add(time.Hour).Unix()
	session := browserSession{ID: "fed-session", Subject: "operator", Expires: expires}
	value, err := auth.sign(session)
	if err != nil {
		t.Fatal(err)
	}
	auth.store.put(session.ID, sessionRecord{Subject: session.Subject, RawIDToken: "the-shauth-assertion", Expires: expires})

	request := httptest.NewRequest(http.MethodGet, FederationSubjectPath, nil)
	request.AddCookie(&http.Cookie{Name: auth.config.CookieName, Value: value})
	recorder := httptest.NewRecorder()
	auth.federationSubject(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authenticated federation-subject = %d, want 200", recorder.Code)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("federation-subject must not be cached: %q", recorder.Header().Get("Cache-Control"))
	}
	if !strings.Contains(recorder.Body.String(), `"subject_token":"the-shauth-assertion"`) {
		t.Fatalf("federation-subject omitted the assertion: %s", recorder.Body.String())
	}
}
