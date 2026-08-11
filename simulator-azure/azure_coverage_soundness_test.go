package main

import (
	"net/http"
	"strings"
	"testing"
)

// The coverage gate is only worth what its served/unserved discriminator is
// worth, so the discriminator itself is tested rather than trusted.
//
// The failure that matters most here is silent: AzureBearerVerificationMiddleware
// answers 401 ahead of the mux for every /subscriptions/ and /providers/ request
// that arrives without a simulator-minted Azure Resource Manager token, and a
// middleware answering ahead of the mux is classified served — that is what the
// host-addressed data planes need. So if token minting ever breaks, every ARM
// probe would be credited to a middleware rejection and the gate would measure
// nothing while still looking green on the surfaces it does not pin exactly.
// This test fails loudly at that point instead.
func TestServiceConformance_AzureCoverageProbeIsSound(t *testing.T) {
	p := newAzureProber(t)

	// A route the simulator definitely mounts. It must probe served, and it
	// must not do so by way of a 401 — that is the inflation this guards.
	mounted := p.serve(http.MethodGet,
		"/subscriptions/"+azureDefaultSubscriptionID+"/resourceGroups/probe-rg/providers/Microsoft.EventGrid/eventSubscriptions",
		"", "2021-12-01", "")
	if mounted.status == http.StatusUnauthorized {
		t.Fatalf("the probe's Azure Resource Manager token was rejected (401): every ARM probe would be "+
			"credited to the bearer middleware instead of a handler. reason=%q body=%q", mounted.reason, mounted.body)
	}
	if !mounted.served {
		t.Fatalf("a mounted ARM route probed unserved: status=%d pattern=%q reason=%q",
			mounted.status, mounted.pattern, mounted.reason)
	}
	if mounted.pattern == "" {
		t.Fatalf("a mounted ARM route resolved to no route pattern: status=%d reason=%q", mounted.status, mounted.reason)
	}

	// A path nothing mounts must probe unserved via the mux's own miss, not be
	// absorbed by a middleware.
	absent := p.serve(http.MethodGet,
		"/subscriptions/"+azureDefaultSubscriptionID+"/resourceGroups/probe-rg/providers/Microsoft.NotAService/notAResource",
		"", "2021-12-01", "")
	if absent.served {
		t.Fatalf("an unmounted URI probed served: status=%d pattern=%q reason=%q",
			absent.status, absent.pattern, absent.reason)
	}
	if !strings.Contains(absent.reason, "mux miss") {
		t.Fatalf("an unmounted URI was not reported as a mux miss: reason=%q", absent.reason)
	}

	// The 401 check above is load-bearing rather than decorative, and this is
	// the state it exists to catch: with a token the simulator does not accept,
	// the bearer middleware answers even a URI that nothing mounts. No route
	// pattern resolves, so the answer reaches the "middleware answered ahead of
	// the mux" arm — the arm the host-addressed data planes rely on — and an
	// unmounted surface would be counted as covered. Only the probe presenting
	// a token the simulator accepts keeps that arm honest.
	broken := &azureProber{srv: p.srv, token: "not-a-simulator-minted-token"}
	rejected := broken.serve(http.MethodGet,
		"/subscriptions/"+azureDefaultSubscriptionID+"/resourceGroups/probe-rg/providers/Microsoft.NotAService/notAResource",
		"", "2021-12-01", "")
	if rejected.status != http.StatusUnauthorized {
		t.Fatalf("an unaccepted Azure Resource Manager token was not rejected on an unmounted URI: "+
			"status=%d — the 401 guard above no longer proves the probe authenticates", rejected.status)
	}
	if rejected.pattern != "" {
		t.Fatalf("an unmounted URI resolved to route pattern %q", rejected.pattern)
	}

	// A path registered only for other methods is a mux miss too, not a
	// handler declining the verb.
	wrongVerb := p.serve(http.MethodDelete,
		"/subscriptions/"+azureDefaultSubscriptionID+"/resourceGroups/probe-rg/providers/Microsoft.EventGrid/eventSubscriptions",
		"", "2021-12-01", "")
	if wrongVerb.served && wrongVerb.pattern == "" {
		t.Fatalf("an unrouted verb was credited to a middleware: status=%d reason=%q",
			wrongVerb.status, wrongVerb.reason)
	}
}
