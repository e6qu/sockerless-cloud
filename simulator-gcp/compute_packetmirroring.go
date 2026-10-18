package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Google Compute Engine packet mirroring. A policy names a set of mirrored
// resources — instances, whole subnetworks, or every instance carrying a
// network tag — and a collector internal load balancer. The policy is the
// control plane of the resource; no traffic is copied to the collector's
// backends.

// gcpRegionForwardingRules and gcpRegionBackendServices are the regional
// collections a collectorIlb names. They are assigned where those collections
// are registered.
var (
	gcpRegionForwardingRules sim.Store[map[string]any]
	gcpRegionBackendServices sim.Store[map[string]any]
)

var gcpPacketMirrorings sim.Store[map[string]any]

func registerComputePacketMirroring(srv *sim.Server) {
	gcpPacketMirrorings = sim.MakeStore[map[string]any](srv.DB(), "compute_packet_mirrorings")
	(computeMetaResource{
		collection: "packetMirrorings", kind: "compute#packetMirroring",
		scope: cScopeRegion, store: gcpPacketMirrorings,
		patch: true, aggregated: true,
	}).register(srv)
	// The Discovery document exposes testIamPermissions on packetMirrorings
	// and neither of the other two IAM verbs.
	registerComputeTestIamOnly(srv, cScopeRegion, "packetMirrorings")
}

// gcpLookupComputeResource finds a map-store resource by any of the URL forms a
// client may send: the full selfLink, the /compute/v1-relative path, or a
// partial URL.
func gcpLookupComputeResource(store sim.Store[map[string]any], url string) (map[string]any, bool) {
	if store == nil {
		return nil, false
	}
	path := gcpComputeResourcePath(url)
	if m, ok := store.Get(path); ok {
		return m, true
	}
	for _, m := range store.List() {
		selfLink, _ := m["selfLink"].(string)
		if selfLink != "" && gcpComputeResourcePath(selfLink) == path {
			return m, true
		}
	}
	return nil, false
}

// gcpComputeResourcePath reduces any of the URL forms Compute accepts to the
// "projects/…" path they share, so a resource referenced as a full selfLink and
// one referenced as a partial URL resolve to the same resource — which is what
// the API does.
func gcpComputeResourcePath(url string) string {
	if i := strings.Index(url, "/compute/v1/"); i >= 0 {
		return url[i+len("/compute/v1/"):]
	}
	if i := strings.Index(url, "/compute/beta/"); i >= 0 {
		return url[i+len("/compute/beta/"):]
	}
	return strings.TrimPrefix(url, "/")
}
