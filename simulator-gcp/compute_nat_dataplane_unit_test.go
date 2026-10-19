package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
)

// A Cloud NAT's sidecar reports with the token the simulator minted for it
// and no Google credential: the bearer check lets the report through to the
// sidecar's own trust, which accepts the minted token and refuses anything
// else.
func TestCloudNATSidecarReportsPassTheBearerCheck(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	srv.WrapHandler(bearerAuthMiddleware(srv))
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, "nat-report-net")
	t.Cleanup(func() { sim.UnregisterSidecarToken(token) })

	report := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, egress.ReportPath, strings.NewReader(`{"gateway":"g","events":[]}`))
		if token != "" {
			req.Header.Set("X-Sockerless-Sidecar-Token", token)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if rec := report(token); rec.Code != http.StatusNoContent {
		t.Fatalf("a report under the minted token = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if rec := report(""); rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "UNAUTHENTICATED") {
		t.Fatalf("a report with no token = %d %s, want the sidecar trust's 401", rec.Code, rec.Body.String())
	}
}
