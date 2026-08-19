package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Without a console registered the API root is genuinely nothing for a
// non-Cosmos request, and must stay a 404 rather than redirect into a console
// that does not exist.
func TestBareRootStays404WithoutConsole(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulatorWithUI(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"}, false)
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bare root without a console: got %d, want 404", rec.Code)
	}
}
