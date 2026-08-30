package main

import (
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Compute Engine collections whose whole documented surface is the standard
// resource lifecycle.
//
// Each is a user-created resource: the project's own cross-site networks,
// rollout plans, interconnect attachments, regional health services and
// VM extension policies. Nothing about them needs a handler of its own — they
// insert, read, list, patch and delete against a store, and report the
// scope-appropriate Operation, which is exactly what computeMetaResource
// registers. They were unserved only because they had never been listed.
//
// Deliberately absent: the catalogs Google publishes rather than the caller
// creating them — interconnect locations and remote locations, license codes,
// preview features, reliability risks. An empty list is not what those return,
// and filling one in means inventing Google's own facility and licence data.
func registerComputeMore4(srv *sim.Server) {
	mk := func(table string) sim.Store[map[string]any] {
		return sim.MakeStore[map[string]any](srv.DB(), table)
	}

	families := []computeMetaResource{
		// Cross-site networking: a network spanning two interconnect sites,
		// the wire groups inside it, and the attachments that land it.
		{collection: "crossSiteNetworks", kind: "compute#crossSiteNetwork", scope: cScopeGlobal,
			store: mk("compute_cross_site_networks"), patch: true},
		// The wire groups inside a cross-site network are nested under it,
		// which this registrar does not express; they stay for a handler that
		// can name the parent.
		{collection: "interconnectAttachments", kind: "compute#interconnectAttachment", scope: cScopeRegion,
			store: mk("compute_interconnect_attachments"), patch: true, setLabels: true, aggregated: true},

		// A rollout plan names the stages a change is applied in.
		{collection: "rolloutPlans", kind: "compute#rolloutPlan", scope: cScopeGlobal,
			store: mk("compute_rollout_plans")},

		// Regional health services, which sit beside the health checks already
		// served and are read by the load balancers that reference them.
		{collection: "healthAggregationPolicies", kind: "compute#healthAggregationPolicy", scope: cScopeRegion,
			store: mk("compute_health_aggregation_policies"), patch: true, aggregated: true, testIamOnly: true},
		{collection: "healthCheckServices", kind: "compute#healthCheckService", scope: cScopeRegion,
			store: mk("compute_health_check_services"), patch: true, aggregated: true, testIamOnly: true},

		// The edge security service a backend service points its
		// securityPolicy at. It has no list method in the document, only the
		// aggregated one, so skipList keeps the simulator to what Google
		// declares.
		{collection: "networkEdgeSecurityServices", kind: "compute#networkEdgeSecurityService", scope: cScopeRegion,
			store: mk("compute_network_edge_security_services"), patch: true, aggregated: true, skipList: true},

		// VM extension policies, per zone.
		{collection: "vmExtensionPolicies", kind: "compute#vmExtensionPolicy", scope: cScopeZone,
			store: mk("compute_zone_vm_extension_policies")},

		// Instant snapshot groups, zonal and regional, each carrying the IAM
		// triple the document declares on them.
		{collection: "instantSnapshotGroups", kind: "compute#instantSnapshotGroup", scope: cScopeZone,
			store: mk("compute_zone_instant_snapshot_groups"), iam: true},
		{collection: "instantSnapshotGroups", kind: "compute#instantSnapshotGroup", scope: cScopeRegion,
			store: mk("compute_region_instant_snapshot_groups"), iam: true},
	}
	for _, res := range families {
		res.register(srv)
	}
}
