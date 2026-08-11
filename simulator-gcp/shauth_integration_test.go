package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	uiauth "github.com/e6qu/sockerless-cloud/ui-auth"
)

// consoleAssets stands in for the embedded single-page application. The
// simulator mounts its Shauth relying party from RegisterUI, which the
// production build reaches through registerUI with the embedded `dist`
// directory — and which the `noui` build compiles away entirely. Handing
// RegisterUI a filesystem directly exercises the mounting contract itself, so
// these tests assert the wiring under every build tag rather than only the one
// that embeds the console.
func consoleAssets() fstest.MapFS {
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>console</title>")}}
}

// consoleConfigPath is the console's configuration document, which RegisterUI
// mounts whether or not an identity provider is configured.
const consoleConfigPath = "/ui/config.json"

// ensureConsoleRegistered mounts the console — and with it the Shauth relying
// party — if the build has not already done so.
//
// The production build embeds the console and registers it during
// buildSimulator; the `noui` build compiles that away entirely, which is how
// `make unit-test` and CI run. Registering unconditionally panics on the first
// of those with a duplicate-pattern conflict, and skipping it entirely tests
// nothing on the second. Probing the mux covers both, and asserts the same
// contract either way.
func ensureConsoleRegistered(srv *sim.Server) {
	// Probe the console config, which RegisterUI mounts unconditionally, and
	// require an EXACT pattern match. A catch-all subtree — Cloud Storage's
	// `/{bucket}/{object...}`, for one — matches this path too, so "some
	// pattern answered" would report the console as registered when it is not
	// and the registration would be skipped.
	// The session path would be the wrong signal: with no identity provider
	// configured the console is still registered while the relying party is
	// not, and re-registering would conflict on the console's own routes.
	probe := httptest.NewRequest(http.MethodGet, consoleConfigPath, nil)
	if _, pattern := srv.Mux().Handler(probe); pattern == "GET "+consoleConfigPath {
		return
	}
	srv.RegisterUI(consoleAssets())
}

// Shauth is an ADDITION to each simulator's cloud authentication, never a
// replacement for it. The cloud data plane keeps demanding the credential its
// real API demands; Shauth sits alongside it as the console's own OpenID
// Connect relying party, and it is what turns an operator's single sign-on
// into the assertion the console federates into cloud credentials.
//
// The ui-auth package has its own unit tests, but those prove the package in
// isolation. Nothing asserted that a simulator actually MOUNTS it, so the
// integration could regress in any of the three simulators without a single
// test noticing — a console would simply stop being able to sign anyone in.
// These tests close that: they build the simulator the way main() does, with
// Shauth configured, and assert both layers are live and independent.
//
// simulator-aws and simulator-azure carry the same test against their own
// servers; the contract is shared, so the three must not drift apart.

// shauthTestConfig is a complete, valid Shauth configuration. The values are
// the shape the validator demands — an HTTPS public URL, a 32-byte session
// secret, and a release revision that identifies an immutable deployed release
// — so a change that tightens validation fails here rather than silently
// disabling authentication.
func shauthTestConfig(provider string) sim.Config {
	return sim.Config{
		Provider:                   provider,
		ListenAddr:                 ":0",
		LogLevel:                   "error",
		UIOIDCIssuer:               "https://shauth.example.com",
		UIOIDCClientID:             "sockerless-" + provider,
		UIOIDCClientSecret:         "test-client-secret",
		UIPublicURL:                "https://" + provider + ".example.com",
		UISessionSecret:            "0123456789abcdef0123456789abcdef",
		ApplicationReleaseRevision: "0123456789abcdef0123456789abcdef01234567",
	}
}

func TestShauthIsMountedAlongsideCloudAuth(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(shauthTestConfig("gcp"))
	if err != nil {
		t.Fatalf("buildSimulator with Shauth configured: %v", err)
	}
	ensureConsoleRegistered(srv)
	srv.WrapHandler(bearerAuthMiddleware(srv))

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// Every endpoint of the Shauth contract is routed. A 404 here means the
	// simulator did not mount the relying party at all, which is the
	// regression these tests exist to catch.
	for _, path := range []string{
		uiauth.LoginPath,
		uiauth.CallbackPath,
		uiauth.SessionPath,
		uiauth.FederationSubjectPath,
		uiauth.FrontchannelLogoutPath,
		uiauth.LogoutCompletePath,
		uiauth.SignedOutPath,
		uiauth.ValidationPath,
	} {
		if code := get(path).Code; code == http.StatusNotFound {
			t.Errorf("%s is not routed — Shauth is not mounted on the gcp simulator", path)
		}
	}

	// The session read is the endpoint a console polls to learn who is signed
	// in. Anonymously it must say "nobody", not "no such endpoint".
	if code := get(uiauth.SessionPath).Code; code != http.StatusUnauthorized {
		t.Errorf("%s anonymously = %d, want 401", uiauth.SessionPath, code)
	}
	// The federation subject is what the console exchanges for cloud
	// credentials; it is equally session-bound.
	if code := get(uiauth.FederationSubjectPath).Code; code != http.StatusUnauthorized {
		t.Errorf("%s anonymously = %d, want 401", uiauth.FederationSubjectPath, code)
	}

	// The console itself is behind the session, so an anonymous browser is
	// sent to sign in rather than handed the application.
	if code := get("/ui/config.json").Code; code != http.StatusFound && code != http.StatusSeeOther {
		t.Errorf("/ui/config.json anonymously = %d, want a redirect to sign-in", code)
	}

	// The addition half: the cloud data plane still enforces the cloud's own
	// credential. If Shauth were replacing cloud auth rather than sitting
	// beside it, this would stop being 401.
	cloud := httptest.NewRequest(http.MethodGet, "/v2/projects/test-project/locations/us-central1/services", nil)
	cloud.Host = "run.googleapis.com"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, cloud)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("cloud data plane without a bearer = %d, want 401 — Shauth must not replace cloud authentication", rec.Code)
	}
}

// TestShauthAbsentWhenUnconfigured is the other half of the contract: Shauth is
// opt-in on a coordinate, so a simulator with no identity provider configured
// serves no relying party — and its cloud surface is unaffected either way.
func TestShauthAbsentWhenUnconfigured(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	ensureConsoleRegistered(srv)
	srv.WrapHandler(bearerAuthMiddleware(srv))

	req := httptest.NewRequest(http.MethodGet, uiauth.SessionPath, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("%s with no identity provider configured = %d, want 404", uiauth.SessionPath, rec.Code)
	}

	cloud := httptest.NewRequest(http.MethodGet, "/v2/projects/test-project/locations/us-central1/services", nil)
	cloud.Host = "run.googleapis.com"
	cloudRec := httptest.NewRecorder()
	srv.ServeHTTP(cloudRec, cloud)
	if cloudRec.Code != http.StatusUnauthorized {
		t.Errorf("cloud data plane without a bearer = %d, want 401 regardless of Shauth", cloudRec.Code)
	}
}
