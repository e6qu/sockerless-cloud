//go:build !noui

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
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
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)
	// A browser at the bare origin carries none of Cosmos's data-plane headers.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/ui/" {
		t.Fatalf("bare root: got %d %q, want 307 to /ui/", rec.Code, rec.Header().Get("Location"))
	}

	// A real Cosmos client still gets account properties from the same route.
	// It addresses the account's own hostname and signs the read: the account
	// root is a data-plane resource like any other, so the SDK's endpoint
	// manager sends an authorized request and an unauthorized one is refused.
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

	// The console itself is served where it always was.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("console at /ui/: got %d %q", rec.Code, rec.Body.String())
	}
}
