package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Without a console the API root is nothing for a non-Cosmos request: a 404,
// not a redirect into a console that does not exist.
func TestBareRootStays404WithoutConsole(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulatorWithUI(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"}, false)
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	// Long-running operations complete in a goroutine; one still running when
	// this test ends would race the next test's store rebuild.
	t.Cleanup(bg.Await)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bare root without a console: got %d, want 404", rec.Code)
	}
}
