package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Azure Cosmos DB owns the API root — the azcosmos SDK's global endpoint
// manager reads account properties from it — so the console's bare-root
// redirect cannot sit on the mux beside it. A browser at the bare origin must
// still reach the console.
func TestBareRootReachesConsoleWhileCosmosKeepsAPIRoot(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	console := t.TempDir()
	if err := os.WriteFile(filepath.Join(console, "index.html"), []byte("<!doctype html><title>console</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error", ConsoleDir: console})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	// Long-running operations complete in a goroutine; one still running when
	// this test ends would race the next test's store rebuild.
	t.Cleanup(bg.Await)
	// A browser at the bare origin carries none of Cosmos's data-plane headers.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/ui/" {
		t.Fatalf("bare root: got %d %q, want 307 to /ui/", rec.Code, rec.Header().Get("Location"))
	}

	// The SDK's endpoint manager signs its read at the account's own hostname; the
	// account root is a data-plane resource like any other.
	keys := cosmosProvisionAccount(t, srv, "simaccount")
	status, body := cosmosSignedRequest(t, srv, http.MethodGet, "/", "simaccount", keys.PrimaryMasterKey, "")
	if status != http.StatusOK {
		t.Fatalf("Cosmos account discovery: got %d %q, want 200", status, body)
	}
	for _, want := range []string{`"writableLocations"`, `"readableLocations"`, `"databaseAccountEndpoint"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("Cosmos account properties missing %s: %s", want, body)
		}
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("console at /ui/: got %d %q", rec.Code, rec.Body.String())
	}
}
