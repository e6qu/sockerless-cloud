package main

import (
	"net/http"
	"strings"
	"testing"
)

// The coverage gate is worth only what its served/unserved discriminator is
// worth, so test it. AzureBearerVerificationMiddleware answers 401 ahead of
// the mux for an unauthenticated /subscriptions/ or /providers/ request, and a
// middleware answering ahead of the mux classifies as served (the
// host-addressed data planes need that). If token minting broke, every
// Azure Resource Manager probe would count as served; this test fails then.
func TestServiceConformance_AzureCoverageProbeIsSound(t *testing.T) {
	p := newAzureProber(t)

	// It must probe served, and not by way of a 401.
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

	// With a token the simulator rejects, the bearer middleware answers even a URI
	// nothing mounts, reaching the "middleware answered ahead of the mux" arm and
	// counting as covered. Only a token the simulator accepts keeps that arm
	// honest.
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

	wrongVerb := p.serve(http.MethodDelete,
		"/subscriptions/"+azureDefaultSubscriptionID+"/resourceGroups/probe-rg/providers/Microsoft.EventGrid/eventSubscriptions",
		"", "2021-12-01", "")
	if wrongVerb.served && wrongVerb.pattern == "" {
		t.Fatalf("an unrouted verb was credited to a middleware: status=%d reason=%q",
			wrongVerb.status, wrongVerb.reason)
	}
}
