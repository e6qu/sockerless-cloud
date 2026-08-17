package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
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

// Shauth is an ADDITION to the Azure simulator's own authentication, never a
// replacement for it: Azure Resource Manager keeps demanding its bearer, and
// Shauth sits alongside as the portal's OpenID Connect relying party whose
// assertion the portal federates into an Azure Resource Manager token.
//
// The ui-auth package is unit-tested on its own, which proves the package and
// not the wiring. This asserts the Azure simulator actually mounts it, so the
// integration cannot regress unnoticed. simulator-gcp and simulator-aws
// carry the same test; the contract is shared and the three must not drift.

func azureShauthConfig() sim.Config {
	return sim.Config{
		Provider:                   "azure",
		ListenAddr:                 ":0",
		LogLevel:                   "error",
		UIOIDCIssuer:               "https://shauth.example.com",
		UIOIDCClientID:             "sockerless-azure",
		UIOIDCClientSecret:         "test-client-secret",
		UIPublicURL:                "https://azure.example.com",
		UISessionSecret:            "0123456789abcdef0123456789abcdef",
		ApplicationReleaseRevision: "0123456789abcdef0123456789abcdef01234567",
	}
}

func TestShauthIsMountedAlongsideAzureAuth(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(azureShauthConfig())
	if err != nil {
		t.Fatalf("buildSimulator with Shauth configured: %v", err)
	}
	ensureConsoleRegistered(srv)

	get := func(path string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	// "Anything but 404" is too wide to be a mounting contract: a route that
	// 500s, that answers 405, or that is being absorbed by some other subtree's
	// catch-all pattern all satisfy it. Two things are asserted per path
	// instead — that http.ServeMux resolves the request to that exact
	// registered pattern, which is what rules out a catch-all standing in for
	// the relying party, and the answer the endpoint owes an anonymous caller.
	//
	// The login endpoint's 502 is the relying party reaching for the configured
	// issuer's discovery document and finding nothing at https://shauth.example.com;
	// a mounted-but-inert handler would not attempt the round trip at all.
	for _, tc := range []struct {
		path string
		code int
		why  string
	}{
		{uiauth.LoginPath, http.StatusBadGateway, "the login endpoint fetches the configured issuer's discovery document, which this test's issuer does not serve"},
		{uiauth.CallbackPath, http.StatusBadRequest, "a callback with no authorization response is malformed"},
		{uiauth.SessionPath, http.StatusUnauthorized, "an anonymous caller has no session"},
		{uiauth.FederationSubjectPath, http.StatusUnauthorized, "an anonymous caller has no assertion to federate"},
		{uiauth.FrontchannelLogoutPath, http.StatusOK, "the front-channel logout endpoint answers the identity provider's iframe"},
		{uiauth.LogoutCompletePath, http.StatusSeeOther, "logout completion redirects the browser onward"},
		{uiauth.SignedOutPath, http.StatusOK, "the signed-out page is served"},
		{uiauth.ValidationPath, http.StatusSeeOther, "validation redirects the browser onward"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if _, pattern := srv.Mux().Handler(req); pattern != "GET "+tc.path {
			t.Errorf("%s resolved to pattern %q, not its own route — Shauth is not mounted on the azure simulator", tc.path, pattern)
			continue
		}
		if code := get(tc.path); code != tc.code {
			t.Errorf("%s anonymously = %d, want %d (%s)", tc.path, code, tc.code, tc.why)
		}
	}
}

// TestShauthAbsentWhenUnconfiguredAzure pins the opt-in half: with no identity
// provider coordinate the simulator serves no relying party at all.
func TestShauthAbsentWhenUnconfiguredAzure(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	ensureConsoleRegistered(srv)
	req := httptest.NewRequest(http.MethodGet, uiauth.SessionPath, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("%s with no identity provider configured = %d, want 404", uiauth.SessionPath, rec.Code)
	}
}
