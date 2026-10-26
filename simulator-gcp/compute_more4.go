package main

import (
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Compute Engine collections whose whole documented surface is the standard
// lifecycle: cross-site networks, rollout plans, interconnect attachments,
// regional health services, VM extension policies and the rest below. Each
// inserts, reads, lists, patches and deletes against a store through
// computeMetaResource.
func registerComputeMore4(srv *sim.Server) {
	mk := func(table string) sim.Store[map[string]any] {
		return sim.MakeStore[map[string]any](srv.DB(), table)
	}
	globalVMExtensionPolicies := mk("compute_global_vm_extension_policies")
	// Shared with the handlers that address a collection through its parent,
	// or that carry a verb beyond the lifecycle.
	gcpComputeCrossSiteNetworks = mk("compute_cross_site_networks")
	gcpComputeRegionSnapshots = mk("compute_region_snapshots")
	gcpComputeInterconnectGroups = mk("compute_interconnect_groups")
	gcpComputeInterconnects = mk("compute_interconnects")
	gcpComputeHealthSources = mk("compute_health_sources")
	gcpComputeCompositeHealthChecks = mk("compute_composite_health_checks")

	families := []computeMetaResource{
		// Cross-site networking: a network spanning two interconnect sites,
		// the wire groups inside it, and the attachments that land it.
		{collection: "crossSiteNetworks", kind: "compute#crossSiteNetwork", scope: cScopeGlobal,
			store: gcpComputeCrossSiteNetworks, patch: true},
		// computeMetaResource cannot express the wire groups nested inside a
		// cross-site network, so they have their own handler.
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

		// The edge security service a backend service's securityPolicy points at. The
		// document declares only its aggregated list, so skipList drops the scoped one.
		{collection: "networkEdgeSecurityServices", kind: "compute#networkEdgeSecurityService", scope: cScopeRegion,
			store: mk("compute_network_edge_security_services"), patch: true, aggregated: true, skipList: true},

		// VM extension policies, per zone and project-wide. The global collection
		// retires a policy through POST .../{name}/delete rather than DELETE, mounted
		// below. VmExtensionPolicy declares no zone member, so the zonal collection
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
			store: gcpComputeHealthSources, patch: true, aggregated: true, testIamOnly: true},
		{collection: "compositeHealthChecks", kind: "compute#compositeHealthCheck", scope: cScopeRegion,
			store: gcpComputeCompositeHealthChecks, patch: true, aggregated: true, testIamOnly: true},

		// Interconnects and their groups; getDiagnostics and getMacsecConfig are
		// served beside them.
		{collection: "interconnects", kind: "compute#interconnect", scope: cScopeGlobal,
			store: gcpComputeInterconnects, patch: true, setLabels: true},
		// A group's operational status derives from the members its resource names
		// against what it was configured for; a group naming none is degraded.
		{collection: "interconnectGroups", kind: "compute#InterconnectGroup", scope: cScopeGlobal,
			store: gcpComputeInterconnectGroups, patch: true, iam: true,
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

// computeGroupStatus reports an interconnect group with no members as
// DEGRADED, since it cannot carry traffic. The declared values are the bare
// FULLY_UP, DEGRADED and FULLY_DOWN, not GROUP_STATUS_-prefixed spellings.
func computeGroupStatus(group map[string]any) string {
	if members, _ := group["interconnects"].(map[string]any); len(members) > 0 {
		return "FULLY_UP"
	}
	if members, _ := group["attachments"].(map[string]any); len(members) > 0 {
		return "FULLY_UP"
	}
	return "DEGRADED"
}
