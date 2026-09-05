package main

import (
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// TestAuthGateAccountsForEveryCatchAllRoute guards the assumption
// publishesAPIMethod rests on: that a mux match means a Google API method
// claims the request. A pattern with no literal first segment matches
// essentially any URI, so a match on one says nothing about whether the
// simulator publishes a method there — such a route must be listed in
// gcpDataPlaneCatchAlls and authenticate inside its own handler, once it knows
// the request is one it serves.
//
// Without this test a newly mounted catch-all would silently change behavior in
// one of two wrong directions: left off the list, every unrouted URI would go
// back to answering 401 instead of 404; added to the list without its own
// credential check, a real cloud surface would become unauthenticated. Both are
// decisions to make deliberately, so an unaccounted catch-all fails here.
func TestAuthGateAccountsForEveryCatchAllRoute(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}

	for _, pattern := range srv.RoutePatterns() {
		path := pattern
		if _, rest, found := strings.Cut(pattern, " "); found {
			path = rest
		}
		if !strings.HasPrefix(path, "/{") {
			continue
		}
		if gcpDataPlaneCatchAlls[path] {
			continue
		}
		t.Errorf("route %q has no literal first segment, so it matches URIs no Google method publishes, "+
			"but it is not in gcpDataPlaneCatchAlls. Either give it a literal prefix, or add it to that "+
			"map and verify the bearer inside its handler once it has decided the request is its own.",
			pattern)
	}

	// The map must not name a route that no longer exists: a stale entry would
	// keep a real API surface out of the credential gate if that path were
	// later mounted by a Google method.
	mounted := map[string]bool{}
	for _, pattern := range srv.RoutePatterns() {
		path := pattern
		if _, rest, found := strings.Cut(pattern, " "); found {
			path = rest
		}
		mounted[path] = true
	}
	for path := range gcpDataPlaneCatchAlls {
		if !mounted[path] {
			t.Errorf("gcpDataPlaneCatchAlls names %q, which no longer mounts — remove the entry", path)
		}
	}
}
