package main

import (
	"fmt"
	"net/http"

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
	globalVMExtensionPolicies := mk("compute_global_vm_extension_policies")
	// Shared with the handlers that address a collection through its parent,
	// or that carry a verb beyond the lifecycle.
	gcpComputeCrossSiteNetworks = mk("compute_cross_site_networks")
	gcpComputeRegionSnapshots = mk("compute_region_snapshots")

	families := []computeMetaResource{
		// Cross-site networking: a network spanning two interconnect sites,
		// the wire groups inside it, and the attachments that land it.
		{collection: "crossSiteNetworks", kind: "compute#crossSiteNetwork", scope: cScopeGlobal,
			store: gcpComputeCrossSiteNetworks, patch: true},
		// The wire groups inside a cross-site network are nested under it,
		// which this registrar does not express, so they have a handler of
		// their own that names the parent.
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

		// VM extension policies, per zone and for the whole project. The global
		// collection retires a policy through POST .../{name}/delete rather
		// than DELETE, so its delete is named separately below.
		// VmExtensionPolicy declares no zone member, so the zonal collection
		// takes no scope stamp.
		{collection: "vmExtensionPolicies", kind: "compute#vmExtensionPolicy", scope: cScopeZone,
			store: mk("compute_zone_vm_extension_policies"), patch: true, scopeless: true},
		{collection: "vmExtensionPolicies", kind: "compute#globalVmExtensionPolicy", scope: cScopeGlobal,
			store: globalVMExtensionPolicies, patch: true, aggregated: true, skipDelete: true},

		// Instant snapshot groups, zonal and regional, each carrying the IAM
		// triple the document declares on them.
		{collection: "instantSnapshotGroups", kind: "compute#instantSnapshotGroup", scope: cScopeZone,
			store: mk("compute_zone_instant_snapshot_groups"), iam: true},
		{collection: "instantSnapshotGroups", kind: "compute#instantSnapshotGroup", scope: cScopeRegion,
			store: mk("compute_region_instant_snapshot_groups"), iam: true},

		// Regional backend buckets, which carry the same usable-subset read
		// their global counterpart does.
		{collection: "backendBuckets", kind: "compute#backendBucket", scope: cScopeRegion,
			store: mk("compute_region_backend_buckets"), patch: true, iam: true,
			listUsableKind: "compute#usableBackendBucketList"},

		// Regional snapshots, beside the zonal ones already served.
		{collection: "snapshots", kind: "compute#snapshot", scope: cScopeRegion,
			store: gcpComputeRegionSnapshots, setLabels: true, iam: true},

		// The health sources and composite health checks a load balancer
		// aggregates.
		{collection: "healthSources", kind: "compute#healthSource", scope: cScopeRegion,
			store: mk("compute_health_sources"), patch: true, aggregated: true, testIamOnly: true},
		{collection: "compositeHealthChecks", kind: "compute#compositeHealthCheck", scope: cScopeRegion,
			store: mk("compute_composite_health_checks"), patch: true, aggregated: true, testIamOnly: true},

		// Interconnects and the groups that bundle them. Their physical link
		// diagnostics and MACsec configuration are hardware telemetry this
		// simulator has no basis for, and stay unserved rather than answered
		// with numbers nothing measured.
		{collection: "interconnects", kind: "compute#interconnect", scope: cScopeGlobal,
			store: mk("compute_interconnects"), patch: true, setLabels: true},
		// A group's operational status is what its members add up to against
		// what the group was configured for. The members a group has are the
		// ones its own resource names, so the status is derived from the group
		// rather than kept beside it, and a group naming none is degraded
		// rather than healthy — which is what the topology actually is.
		{collection: "interconnectGroups", kind: "compute#InterconnectGroup", scope: cScopeGlobal,
			store: mk("compute_interconnect_groups"), patch: true, iam: true,
			statusReads: []computeStatusRead{{
				verb: "getOperationalStatus", wrap: "result", etag: true,
				status: func(group map[string]any) map[string]any {
					return map[string]any{
						"groupStatus":          computeGroupStatus(group),
						"interconnectStatuses": []any{},
						"configured":           group["intent"],
					}
				},
			}}},
		{collection: "interconnectAttachmentGroups", kind: "compute#interconnectAttachmentGroup", scope: cScopeGlobal,
			store: mk("compute_interconnect_attachment_groups"), patch: true, iam: true,
			statusReads: []computeStatusRead{{
				verb: "getOperationalStatus", wrap: "result", etag: true,
				status: func(group map[string]any) map[string]any {
					return map[string]any{
						"groupStatus":        computeGroupStatus(group),
						"attachmentStatuses": []any{},
						"configured":         group["intent"],
					}
				},
			}}},
	}
	for _, res := range families {
		res.register(srv)
	}

	// The global VM extension policy is retired through a POST rather than a
	// DELETE, which is the one place this collection departs from the standard
	// lifecycle.
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/vmExtensionPolicies/{name}/delete",
		func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			key := fmt.Sprintf("projects/%s/global/vmExtensionPolicies/%s", project, name)
			if computeNotFound(w, globalVMExtensionPolicies.Delete(key), "vmExtensionPolicies", name) {
				return
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "global", computeSelfLink(key), "delete"))
		})
}

// computeGroupStatus reports what an interconnect group adds up to. A group
// with no members cannot carry traffic, so it is degraded rather than fully
// available — reporting otherwise would tell a client its topology is
// redundant when it is empty.
func computeGroupStatus(group map[string]any) string {
	if members, _ := group["interconnects"].(map[string]any); len(members) > 0 {
		return "GROUP_STATUS_FULLY_UP"
	}
	if members, _ := group["attachments"].(map[string]any); len(members) > 0 {
		return "GROUP_STATUS_FULLY_UP"
	}
	return "GROUP_STATUS_DEGRADED"
}
