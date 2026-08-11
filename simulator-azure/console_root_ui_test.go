//go:build !noui

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Azure Cosmos DB owns the simulator's API root: the azcosmos SDK's global
// endpoint manager reads account properties from it, so the console's bare-root
// redirect cannot be mounted on the mux beside it. A visitor who types the bare
// origin into a browser must still reach the console rather than a bare 404,
// and Cosmos must keep the root for its own clients.
func TestBareRootReachesConsoleWhileCosmosKeepsAPIRoot(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	// A browser at the bare origin carries none of Cosmos's data-plane headers.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/ui/" {
		t.Fatalf("bare root: got %d %q, want 307 to /ui/", rec.Code, rec.Header().Get("Location"))
	}

	// A real Cosmos client still gets account properties from the same route.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("x-ms-cosmos-account", "simaccount")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Cosmos account discovery: got %d %q, want 200", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"writableLocations"`, `"readableLocations"`, `"databaseAccountEndpoint"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("Cosmos account properties missing %s: %s", want, rec.Body.String())
		}
	}

	// The console itself is served where it always was.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("console at /ui/: got %d %q", rec.Code, rec.Body.String())
	}
}
