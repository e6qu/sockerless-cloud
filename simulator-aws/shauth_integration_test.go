package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/e6qu/sockerless-cloud/sim"
	uiauth "github.com/e6qu/sockerless-cloud/ui-auth"
)

// consoleAssets stands in for the embedded console. RegisterUI mounts the
// Shauth relying party; the `console` build reaches it with the embedded `dist`
// directory and a plain build only through SIM_CONSOLE_DIR, so handing it a
// filesystem directly asserts the wiring under every build tag.
func consoleAssets() fstest.MapFS {
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>console</title>")}}
}

// consoleConfigPath is the console's configuration document, which RegisterUI
// mounts whether or not an identity provider is configured.
const consoleConfigPath = "/ui/config.json"

// ensureConsoleRegistered mounts the console and its Shauth relying party unless
// the build already did. The `console` build registers them in buildSimulator
// and a plain build (unit tests, CI) does not; registering twice panics on a
// duplicate pattern, so probe the mux first.
func ensureConsoleRegistered(srv *sim.Server) {
	// Probe the console config, which RegisterUI always mounts, and require an exact
	// pattern match: a catch-all subtree (Amazon S3's `/{bucket}/{key...}`) matches
	// this path too. The session path would be the wrong signal: without an identity
	// provider the console is registered while the relying party is not.
	probe := httptest.NewRequest(http.MethodGet, consoleConfigPath, nil)
	if _, pattern := srv.Mux().Handler(probe); pattern == "GET "+consoleConfigPath {
		return
	}
	srv.RegisterUI(consoleAssets(), sim.ConsoleOptions{Coordinates: sim.BrowserFederationCoordinates})
}

// Shauth adds to the AWS simulator's authentication and never replaces it: the
// AWS surface still demands SigV4, and Shauth is the console's OpenID Connect
// relying party, whose assertion the console federates through
// AssumeRoleWithWebIdentity. Assert the simulator mounts it; simulator-gcp and
// simulator-azure carry the same test.

func awsShauthConfig() sim.Config {
	return sim.Config{
		Provider:                   "aws",
		ListenAddr:                 ":0",
		LogLevel:                   "error",
		UIOIDCIssuer:               "https://shauth.example.com",
		UIOIDCClientID:             "sockerless-aws",
		UIOIDCClientSecret:         "test-client-secret",
		UIPublicURL:                "https://aws.example.com",
		UISessionSecret:            "0123456789abcdef0123456789abcdef",
		ApplicationReleaseRevision: "0123456789abcdef0123456789abcdef01234567",
	}
}

func TestShauthIsMountedAlongsideAWSAuth(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, _, _, err := buildSimulatorWithOptions(awsShauthConfig(), simulatorBuildOptions{})
	if err != nil {
		t.Fatalf("buildSimulator with Shauth configured: %v", err)
	}
	// Built but never served, so its background workers are this test's to stop.
	t.Cleanup(srv.StopBackground)
	ensureConsoleRegistered(srv)

	get := func(path string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

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
		if code := get(path); code == http.StatusNotFound {
			t.Errorf("%s is not routed — Shauth is not mounted on the aws simulator", path)
		}
	}

	if code := get(uiauth.SessionPath); code != http.StatusUnauthorized {
		t.Errorf("%s anonymously = %d, want 401", uiauth.SessionPath, code)
	}
	if code := get(uiauth.FederationSubjectPath); code != http.StatusUnauthorized {
		t.Errorf("%s anonymously = %d, want 401", uiauth.FederationSubjectPath, code)
	}
}

// TestShauthAbsentWhenUnconfiguredAWS pins the opt-in half: with no identity
// provider coordinate the simulator serves no relying party at all.
func TestShauthAbsentWhenUnconfiguredAWS(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, _, _, err := buildSimulatorWithOptions(
		sim.Config{Provider: "aws", ListenAddr: ":0", LogLevel: "error"}, simulatorBuildOptions{})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	ensureConsoleRegistered(srv)
	req := httptest.NewRequest(http.MethodGet, uiauth.SessionPath, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("%s with no identity provider configured = %d, want 404", uiauth.SessionPath, rec.Code)
	}
}
