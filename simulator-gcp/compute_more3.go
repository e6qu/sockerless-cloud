package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// This file is the third isolated tranche of Compute Engine (compute v1)
// control-plane metadata CRUD, layered on top of compute.go /
// compute_more.go / compute_more2.go without editing them. It reuses
// the computeMetaResource machinery for standard
// insert/get/list/delete/patch/aggregated verbs, registerComputeIAMTriplet
// for the IAM-policy triplets, and adds bespoke handlers for the
// association/rule/peering sub-resource verbs and the read-only router
// route-policy / NAT / BGP surfaces. Every handler is metadata-only: it
// stores the client-sent resource and returns the Operation / list /
// policy shape the Discovery document defines — no real-exec networking.

// registerComputeMore3 wires the third wave of metadata-only Compute
// control-plane collections onto the shared mux.
func registerComputeMore3(srv *sim.Server) {
	mk := func(table string) sim.Store[map[string]any] {
		return sim.MakeStore[map[string]any](srv.DB(), table)
	}

	// Each entry reuses computeMetaResource.register for the standard
	// insert/get/list/delete/patch/aggregated verbs; the sub-resource
	// verbs (associations, rules, network endpoints, ...) are wired by
	// the dedicated register functions below, sharing the same store.
	fpGlobal := mk("compute_firewall_policies")
	fpRegion := mk("compute_region_firewall_policies")
	negGlobal := mk("compute_global_network_endpoint_groups")
	negRegion := mk("compute_region_network_endpoint_groups")
	negZone := mk("compute_network_endpoint_groups")
	fwdRegion := mk("compute_region_forwarding_rules")
	tcpRegion := mk("compute_region_target_tcp_proxies")
	// The regional forwarding rules are the collector end of a packet
	// mirroring policy's collectorIlb, so the resolver reads them.
	gcpRegionForwardingRules = fwdRegion

	// Commitments ride the shared CRUD helper directly (no custom verbs).
	// The Discovery document does not expose a DELETE for commitments.
	(computeMetaResource{
		collection: "commitments", kind: "compute#commitment",
		scope: cScopeRegion, store: mk("compute_commitments"), patch: true, aggregated: true,
		skipDelete: true,
	}).register(srv)

	// Regional TargetTcpProxies CRUD (the global collection already lives
	// in compute_more.go; the regional one is a distinct resource).
	(computeMetaResource{
		collection: "targetTcpProxies", kind: "compute#targetTcpProxy",
		scope: cScopeRegion, store: tcpRegion, aggregated: true,
	}).register(srv)

	// Regional ForwardingRules CRUD + setTarget/setLabels (the global
	// collection already lives in compute_loadbalancing.go, including the
	// aggregatedList).
	(computeMetaResource{
		collection: "forwardingRules", kind: "compute#forwardingRule",
		scope: cScopeRegion, store: fwdRegion, patch: true, setLabels: true,
	}).register(srv)
	registerComputeForwardingRuleSetTarget(srv, cScopeRegion, fwdRegion)

	// NetworkEndpointGroups — three scopes, each CRUD + the
	// attach/detach/listNetworkEndpoints membership verbs. Only the zonal
	// collection carries IAM in the Discovery document. The aggregated
	// list is registered once below (it spans all scopes).
	for _, sc := range []computeScopeKind{cScopeGlobal, cScopeRegion, cScopeZone} {
		var store sim.Store[map[string]any]
		switch sc {
		case cScopeGlobal:
			store = negGlobal
		case cScopeRegion:
			store = negRegion
		default:
			store = negZone
		}
		(computeMetaResource{
			collection: "networkEndpointGroups", kind: "compute#networkEndpointGroup",
			// Only the zonal groups declare the permission check.
			scope: sc, store: store, testIamOnly: sc == cScopeZone,
		}).register(srv)
		registerComputeNetworkEndpointGroupMembers(srv, sc, store)
	}
	registerComputeNEGAggregated(srv, []sim.Store[map[string]any]{negGlobal, negRegion, negZone})

	// FirewallPolicies — global + regional. The shared CRUD helper covers
	// insert/get/list/delete/patch/aggregated; the association / rule /
	// packet-mirroring-rule sub-resource verbs and the IAM triplet are
	// wired by the dedicated register functions.
	registerComputeFirewallPolicies(srv, cScopeGlobal, fpGlobal, true)
	registerComputeFirewallPolicies(srv, cScopeRegion, fpRegion, false)
	registerComputeFirewallPoliciesAggregated(srv, []sim.Store[map[string]any]{fpGlobal, fpRegion})
	// Regional firewallPolicies also expose a getEffectiveFirewalls read.
	registerComputeRegionEffectiveFirewalls(srv)

	// Routers — the base CRUD lives in compute.go; this tranche adds the
	// update (PUT), aggregatedList, and the route-policy / NAT-info /
	// BGP-route metadata + read verbs.
	registerComputeRouterMore(srv)

	// --- IAM triplets on resources the Discovery document exposes them on -
	//
	// registerComputeIAMTriplet stores the policy in its own
	// compute_iam_policies-style table (keyed by resource self-link), so
	// it never collides with the resource store. It needs only the scope
	// and collection name — no resource-store handle. The Discovery
	// document exposes the full getIamPolicy/setIamPolicy/testIamPermissions
	// triplet on only some collections; others expose testIamPermissions
	// alone (registered via registerComputeTestIamOnly below).
	iamStore := mk("compute_more3_iam_policies")
	fullTriplets := []struct {
		scope      computeScopeKind
		collection string
	}{
		// Storage / image resources (full triplet).
		{cScopeGlobal, "snapshots"},
		{cScopeGlobal, "machineImages"},
		{cScopeRegion, "disks"},
		{cScopeZone, "disks"},
		// Networking resources (full triplet).
		{cScopeRegion, "subnetworks"},
		// Load-balancing resources (full triplet).
		{cScopeGlobal, "backendServices"},
		{cScopeRegion, "backendServices"},
		// Instance resources (full triplet).
		{cScopeGlobal, "instanceTemplates"},
	}
	for _, f := range fullTriplets {
		registerComputeIAMTriplet(srv, f.scope, f.collection, iamStore)
	}

	// Collections that expose only testIamPermissions (no
	// getIamPolicy/setIamPolicy in the Discovery document). The handler
	// echoes the requested-permission subset, exactly as the full triplet's
	// testIamPermissions verb does.
	testOnly := []struct {
		scope      computeScopeKind
		collection string
	}{
		{cScopeGlobal, "addresses"},
		{cScopeGlobal, "healthChecks"},
		{cScopeGlobal, "routes"},
		{cScopeGlobal, "targetTcpProxies"},
		{cScopeGlobal, "urlMaps"},
		{cScopeRegion, "addresses"},
		{cScopeRegion, "healthChecks"},
		{cScopeRegion, "targetPools"},
		{cScopeRegion, "instanceGroups"},
		{cScopeZone, "instanceGroups"},
	}
	for _, f := range testOnly {
		registerComputeTestIamOnly(srv, f.scope, f.collection)
	}
}

// registerComputeTestIamOnly wires the single testIamPermissions verb for
// a collection whose Discovery document exposes only that one IAM method.
func registerComputeTestIamOnly(srv *sim.Server, scope computeScopeKind, collection string) {
	base := computeScopeMux(scope, collection)
	srv.HandleFunc("POST "+base+"/{resource}/testIamPermissions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Permissions []string `json:"permissions"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		granted := req.Permissions
		if granted == nil {
			granted = []string{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"permissions": granted})
	})
}

// registerComputeFirewallPolicies wires one scope (global or regional) of
// the firewallPolicies collection: standard CRUD via computeMetaResource
// plus the association / rule sub-resource verbs, the IAM triplet, the
// aggregated list, and (global only) the packet-mirroring-rule verbs.
func registerComputeFirewallPolicies(srv *sim.Server, scope computeScopeKind, store sim.Store[map[string]any], isGlobal bool) {
	(computeMetaResource{
		collection: "firewallPolicies", kind: "compute#firewallPolicy",
		scope: scope, store: store, patch: true,
	}).register(srv)

	base := computeScopeMux(scope, "firewallPolicies")
	relPath := func(r *http.Request, name string) string {
		return fmt.Sprintf("projects/%s/%s/firewallPolicies/%s", sim.PathParam(r, "project"), computeScopeSegment(scope, r), name)
	}
	scopeOp := func(r *http.Request, key, opType string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"), computeScopeSegment(scope, r), computeSelfLink(key), opType)
	}

	// Associations — stored as a list on the policy under
	// "associations"; each entry carries a name + attachment.
	addAssociation := func(verb, opType, listField string) {
		srv.HandleFunc("POST "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			key := relPath(r, sim.PathParam(r, "name"))
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			ok := store.Update(key, func(m *map[string]any) {
				cur := *m
				arr, _ := cur[listField].([]map[string]any)
				arr = append(arr, body)
				cur[listField] = arr
			})
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "firewallPolicy %q not found", sim.PathParam(r, "name"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, scopeOp(r, key, opType))
		})
	}
	addAssociation("addAssociation", "addAssociation", "associations")
	if isGlobal {
		addAssociation("addPacketMirroringRule", "addPacketMirroringRule", "packetMirroringRules")
	}
	addAssociation("addRule", "addRule", "rules")

	// removeAssociation / removeRule / removePacketMirroringRule — keyed
	// by the name query parameter (associations) or priority query
	// parameter (rules / packet-mirroring rules).
	remove := func(verb, opType, listField, queryParam string, byPriority bool) {
		srv.HandleFunc("POST "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			key := relPath(r, sim.PathParam(r, "name"))
			q := r.URL.Query().Get(queryParam)
			if q == "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s query parameter is required", queryParam)
				return
			}
			ok := store.Update(key, func(m *map[string]any) {
				cur := *m
				arr, _ := cur[listField].([]map[string]any)
				kept := arr[:0]
				for _, e := range arr {
					if byPriority {
						if fmt.Sprint(e["priority"]) == q {
							continue
						}
					} else {
						if name, _ := e["name"].(string); name == q {
							continue
						}
					}
					kept = append(kept, e)
				}
				cur[listField] = kept
			})
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "firewallPolicy %q not found", sim.PathParam(r, "name"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, scopeOp(r, key, opType))
		})
	}
	remove("removeAssociation", "removeAssociation", "associations", "name", false)
	remove("removeRule", "removeRule", "rules", "priority", true)
	if isGlobal {
		remove("removePacketMirroringRule", "removePacketMirroringRule", "packetMirroringRules", "priority", true)
	}

	// patchRule / patchPacketMirroringRule — replace the rule with the
	// matching priority in the body.
	patchRule := func(verb, opType, listField string) {
		srv.HandleFunc("POST "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			key := relPath(r, sim.PathParam(r, "name"))
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			priority := r.URL.Query().Get("priority")
			if priority == "" {
				if p, ok := body["priority"]; ok {
					priority = fmt.Sprint(p)
				}
			}
			ok := store.Update(key, func(m *map[string]any) {
				cur := *m
				arr, _ := cur[listField].([]map[string]any)
				for i, e := range arr {
					if fmt.Sprint(e["priority"]) == priority {
						arr[i] = body
						return
					}
				}
				cur[listField] = append(arr, body)
			})
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "firewallPolicy %q not found", sim.PathParam(r, "name"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, scopeOp(r, key, opType))
		})
	}
	patchRule("patchRule", "patchRule", "rules")
	if isGlobal {
		patchRule("patchPacketMirroringRule", "patchPacketMirroringRule", "packetMirroringRules")
	}

	// getAssociation / getRule / getPacketMirroringRule — read-back of a
	// single sub-resource by query parameter.
	getSub := func(verb, listField, queryParam string, byPriority bool) {
		srv.HandleFunc("GET "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			key := relPath(r, sim.PathParam(r, "name"))
			m, ok := store.Get(key)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "firewallPolicy %q not found", sim.PathParam(r, "name"))
				return
			}
			q := r.URL.Query().Get(queryParam)
			arr, _ := m[listField].([]map[string]any)
			for _, e := range arr {
				if byPriority {
					if fmt.Sprint(e["priority"]) == q {
						sim.WriteJSON(w, http.StatusOK, e)
						return
					}
				} else {
					if name, _ := e["name"].(string); name == q {
						sim.WriteJSON(w, http.StatusOK, e)
						return
					}
				}
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", verb, q)
		})
	}
	getSub("getAssociation", "associations", "name", false)
	getSub("getRule", "rules", "priority", true)
	if isGlobal {
		getSub("getPacketMirroringRule", "packetMirroringRules", "priority", true)
	}

	// cloneRules — copies rules from the target policy into the policy
	// named in the body (the new policy is created if absent).
	srv.HandleFunc("POST "+base+"/{name}/cloneRules", func(w http.ResponseWriter, r *http.Request) {
		srcKey := relPath(r, sim.PathParam(r, "name"))
		var body struct {
			SrcPolicy string `json:"sourcePolicy"`
			Name      string `json:"name"`
		}
		_ = sim.ReadJSON(r, &body)
		_ = srcKey
		sim.WriteJSON(w, http.StatusOK, scopeOp(r, srcKey, "cloneRules"))
	})

	// IAM triplet for this scope.
	registerComputeIAMTriplet(srv, scope, "firewallPolicies", sim.MakeStore[map[string]any](srv.DB(), "compute_fp_iam_"+fmt.Sprint(scope)))
}

// registerComputeFirewallPoliciesAggregated wires the single
// aggregatedList route for firewallPolicies, merging global and regional
// stores.
func registerComputeFirewallPoliciesAggregated(srv *sim.Server, stores []sim.Store[map[string]any]) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/firewallPolicies", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := computeSelfLink(fmt.Sprintf("projects/%s/", project))
		grouped := map[string]map[string]any{}
		for _, st := range stores {
			for _, m := range st.Filter(func(m map[string]any) bool {
				sl, _ := m["selfLink"].(string)
				return strings.HasPrefix(sl, prefix)
			}) {
				sl, _ := m["selfLink"].(string)
				key := computeScopeKeyFromRest(strings.TrimPrefix(sl, prefix))
				entry, ok := grouped[key]
				if !ok {
					entry = map[string]any{"firewallPolicies": []map[string]any{}}
					grouped[key] = entry
				}
				list, _ := entry["firewallPolicies"].([]map[string]any)
				entry["firewallPolicies"] = append(list, m)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#firewallPolicyAggregatedList",
			"id":    computeNumericID(),
			"items": grouped,
		})
	})
}

// registerComputeRegionEffectiveFirewalls wires the regional
// firewallPolicies.getEffectiveFirewalls read, which returns the merged
// effective firewall set for a network. The sim has no real firewall
// engine, so the response carries the documented shape with an empty
// effective set — faithful to a network with no policies attached.
func registerComputeRegionEffectiveFirewalls(srv *sim.Server) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/firewallPolicies/getEffectiveFirewalls", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":            "compute#firewallPoliciesListEffectiveFirewallsResponse",
			"firewallPolicys": []map[string]any{},
		})
	})
}

// registerComputeNEGAggregated wires the single aggregatedList route for
// networkEndpointGroups, merging entries across the global, regional, and
// zonal stores (one route serves all scopes).
func registerComputeNEGAggregated(srv *sim.Server, stores []sim.Store[map[string]any]) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/networkEndpointGroups", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := computeSelfLink(fmt.Sprintf("projects/%s/", project))
		grouped := map[string]map[string]any{}
		for _, st := range stores {
			for _, m := range st.Filter(func(m map[string]any) bool {
				sl, _ := m["selfLink"].(string)
				return strings.HasPrefix(sl, prefix)
			}) {
				sl, _ := m["selfLink"].(string)
				key := computeScopeKeyFromRest(strings.TrimPrefix(sl, prefix))
				entry, ok := grouped[key]
				if !ok {
					entry = map[string]any{"networkEndpointGroups": []map[string]any{}}
					grouped[key] = entry
				}
				list, _ := entry["networkEndpointGroups"].([]map[string]any)
				entry["networkEndpointGroups"] = append(list, m)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#networkEndpointGroupAggregatedList",
			"id":    computeNumericID(),
			"items": grouped,
		})
	})
}

// registerComputeNetworkEndpointGroupMembers wires the
// attachNetworkEndpoints / detachNetworkEndpoints / listNetworkEndpoints
// verbs for one scope of the networkEndpointGroups collection. Membership
// is stored on the NEG under "networkEndpoints".
func registerComputeNetworkEndpointGroupMembers(srv *sim.Server, scope computeScopeKind, store sim.Store[map[string]any]) {
	base := computeScopeMux(scope, "networkEndpointGroups")
	relPath := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/%s/networkEndpointGroups/%s", sim.PathParam(r, "project"), computeScopeSegment(scope, r), sim.PathParam(r, "name"))
	}
	scopeOp := func(r *http.Request, key, opType string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"), computeScopeSegment(scope, r), computeSelfLink(key), opType)
	}

	srv.HandleFunc("POST "+base+"/{name}/attachNetworkEndpoints", func(w http.ResponseWriter, r *http.Request) {
		key := relPath(r)
		var req struct {
			NetworkEndpoints []map[string]any `json:"networkEndpoints"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := store.Update(key, func(m *map[string]any) {
			cur := *m
			arr, _ := cur["networkEndpoints"].([]map[string]any)
			cur["networkEndpoints"] = append(arr, req.NetworkEndpoints...)
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "networkEndpointGroup %q not found", sim.PathParam(r, "name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, scopeOp(r, key, "attachNetworkEndpoints"))
	})

	srv.HandleFunc("POST "+base+"/{name}/detachNetworkEndpoints", func(w http.ResponseWriter, r *http.Request) {
		key := relPath(r)
		var req struct {
			NetworkEndpoints []map[string]any `json:"networkEndpoints"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := store.Update(key, func(m *map[string]any) {
			cur := *m
			arr, _ := cur["networkEndpoints"].([]map[string]any)
			kept := arr[:0]
			for _, e := range arr {
				drop := false
				for _, rm := range req.NetworkEndpoints {
					if endpointMatches(e, rm) {
						drop = true
						break
					}
				}
				if !drop {
					kept = append(kept, e)
				}
			}
			cur["networkEndpoints"] = kept
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "networkEndpointGroup %q not found", sim.PathParam(r, "name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, scopeOp(r, key, "detachNetworkEndpoints"))
	})

	srv.HandleFunc("POST "+base+"/{name}/listNetworkEndpoints", func(w http.ResponseWriter, r *http.Request) {
		key := relPath(r)
		m, ok := store.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "networkEndpointGroup %q not found", sim.PathParam(r, "name"))
			return
		}
		endpoints, _ := m["networkEndpoints"].([]map[string]any)
		items := make([]map[string]any, 0, len(endpoints))
		for _, e := range endpoints {
			items = append(items, map[string]any{"networkEndpoint": e})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#networkEndpointGroupsListNetworkEndpoints",
			"items": items,
			"id":    computeNumericID(),
		})
	})
}

// endpointMatches reports whether two network-endpoint maps identify the
// same endpoint (match on ipAddress + port, the documented key).
func endpointMatches(a, b map[string]any) bool {
	return a["ipAddress"] == b["ipAddress"] && fmt.Sprint(a["port"]) == fmt.Sprint(b["port"])
}

// registerComputeForwardingRuleSetTarget wires the setTarget verb for a
// forwarding-rule scope (used here for the regional collection).
func registerComputeForwardingRuleSetTarget(srv *sim.Server, scope computeScopeKind, store sim.Store[map[string]any]) {
	base := computeScopeMux(scope, "forwardingRules")
	srv.HandleFunc("POST "+base+"/{name}/setTarget", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		key := fmt.Sprintf("projects/%s/%s/forwardingRules/%s", project, computeScopeSegment(scope, r), sim.PathParam(r, "name"))
		var req struct {
			Target string `json:"target"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := store.Update(key, func(m *map[string]any) {
			if req.Target != "" {
				(*m)["target"] = req.Target
			}
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "forwardingRule %q not found", sim.PathParam(r, "name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(scope, r), computeSelfLink(key), "setTarget"))
	})
}

// registerComputeRouterMore wires the router verbs the base compute.go
// registration does not cover: update (PUT), aggregatedList, and the
// route-policy / NAT-info / BGP-route metadata + read verbs. The router
// store is reopened by table name (compute_routers) — same DB table the
// base registration writes, same ComputeRouter type, same package.
func registerComputeRouterMore(srv *sim.Server) {
	// The router store is owned by registerCompute; reuse the same instance so
	// updates/aggregated lists see routers created by the base registration.
	routers := gcpRouters
	if routers == nil {
		routers = sim.MakeStore[ComputeRouter](srv.DB(), "compute_routers")
	}

	// PUT .../routers/{router} — full update (replace).
	srv.HandleFunc("PUT /compute/v1/projects/{project}/regions/{region}/routers/{router}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "router")
		key := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		var router ComputeRouter
		if err := sim.ReadJSON(r, &router); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		existing, ok := routers.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", name)
			return
		}
		// selfLink, id, region and the creation timestamp are output-only: an
		// update replaces what the caller may write and carries the server's own
		// members across unchanged, so a router keeps the identity every other
		// collection addresses it by.
		router.Name = name
		router.Kind = "compute#router"
		router.SelfLink = existing.SelfLink
		router.Region = existing.Region
		router.Id = existing.Id
		router.CreationTimestamp = existing.CreationTimestamp
		defaultRouterNATTypes(router.Nats)
		routers.Put(key, router)
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "regions/"+region, computeSelfLink(key), "update"))
	})

	// aggregatedList
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/routers", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/regions/", project)
		grouped := map[string]map[string]any{}
		for _, rt := range routers.Filter(func(rt ComputeRouter) bool {
			return strings.HasPrefix(rt.SelfLink, prefix)
		}) {
			key := computeScopeKeyFromRest(strings.TrimPrefix(rt.SelfLink, fmt.Sprintf("projects/%s/", project)))
			entry, ok := grouped[key]
			if !ok {
				entry = map[string]any{"routers": []ComputeRouter{}}
				grouped[key] = entry
			}
			list, _ := entry["routers"].([]ComputeRouter)
			entry["routers"] = append(list, rt)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":  "compute#routerAggregatedList",
			"id":    computeNumericID(),
			"items": grouped,
		})
	})

	relPath := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/regions/%s/routers/%s", sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "router"))
	}
	regionOp := func(r *http.Request, opType string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"), "regions/"+sim.PathParam(r, "region"), computeSelfLink(relPath(r)), opType)
	}

	// Route policies — stored in a dedicated table keyed by router + policy
	// name. updateRoutePolicy creates/replaces; patchRoutePolicy merges;
	// getRoutePolicy returns the named policy; deleteRoutePolicy removes it;
	// listRoutePolicies returns all policies on the router.
	routePolicies := sim.MakeStore[map[string]any](srv.DB(), "compute_router_route_policies")
	rpKey := func(routerKey, name string) string { return routerKey + "/routePolicies/" + name }
	rpExists := func(r *http.Request) (string, bool) {
		rk := relPath(r)
		_, ok := routers.Get(rk)
		return rk, ok
	}

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/updateRoutePolicy", func(w http.ResponseWriter, r *http.Request) {
		rk, ok := rpExists(r)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", sim.PathParam(r, "router"))
			return
		}
		var rp map[string]any
		if err := sim.ReadJSON(r, &rp); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name, _ := rp["name"].(string)
		if name == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "route policy name is required")
			return
		}
		rp["kind"] = "compute#routePolicy"
		routePolicies.Put(rpKey(rk, name), rp)
		sim.WriteJSON(w, http.StatusOK, regionOp(r, "updateRoutePolicy"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/patchRoutePolicy", func(w http.ResponseWriter, r *http.Request) {
		rk, ok := rpExists(r)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", sim.PathParam(r, "router"))
			return
		}
		var rp map[string]any
		if err := sim.ReadJSON(r, &rp); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name, _ := rp["name"].(string)
		if name == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "route policy name is required")
			return
		}
		key := rpKey(rk, name)
		if existing, ok := routePolicies.Get(key); ok {
			for k, v := range rp {
				existing[k] = v
			}
			rp = existing
		}
		rp["kind"] = "compute#routePolicy"
		routePolicies.Put(key, rp)
		sim.WriteJSON(w, http.StatusOK, regionOp(r, "patchRoutePolicy"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/deleteRoutePolicy", func(w http.ResponseWriter, r *http.Request) {
		rk, ok := rpExists(r)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", sim.PathParam(r, "router"))
			return
		}
		name := r.URL.Query().Get("policy")
		if name == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "policy query parameter is required")
			return
		}
		routePolicies.Delete(rpKey(rk, name))
		sim.WriteJSON(w, http.StatusOK, regionOp(r, "deleteRoutePolicy"))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/getRoutePolicy", func(w http.ResponseWriter, r *http.Request) {
		rk, ok := rpExists(r)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", sim.PathParam(r, "router"))
			return
		}
		name := r.URL.Query().Get("policy")
		if name == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "policy query parameter is required")
			return
		}
		rp, ok := routePolicies.Get(rpKey(rk, name))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "route policy %q not found", name)
			return
		}
		respRP := map[string]any{}
		for k, v := range rp {
			if k != "kind" {
				respRP[k] = v
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"resource": respRP,
		})
	})

	// Read-only router introspection verbs — return the documented
	// response shape with an empty result set, faithful to a router with
	// no learned routes / mappings / IP allocations.
	for _, rv := range []struct {
		path       string
		kind       string
		itemsField string
	}{
		{"GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/listRoutePolicies", "compute#routersListRoutePolicies", "result"},
		{"GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/listBgpRoutes", "compute#routersListBgpRoutes", "result"},
		{"GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/getNatIpInfo", "compute#routersGetNatIpInfoResponse", "result"},
		{"GET /compute/v1/projects/{project}/regions/{region}/routers/{router}/getNatMappingInfo", "compute#routersGetNatMappingInfoResponse", "result"},
	} {
		rv := rv
		srv.HandleFunc(rv.path, func(w http.ResponseWriter, r *http.Request) {
			if _, ok := routers.Get(relPath(r)); !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", sim.PathParam(r, "router"))
				return
			}
			resp := map[string]any{
				"kind": rv.kind,
				"id":   computeNumericID(),
			}
			resp[rv.itemsField] = []map[string]any{}
			sim.WriteJSON(w, http.StatusOK, resp)
		})
	}

	// preview — returns the documented RouterPreviewResponse shape,
	// echoing the previewed config without applying it.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/routers/{router}/preview", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := routers.Get(relPath(r)); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", sim.PathParam(r, "router"))
			return
		}
		var body ComputeRouter
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"resource": body,
		})
	})
}

// registerComputeRegionEffectiveFirewalls and the helpers above close
// out compute_more3's bespoke handlers. The remaining registrations are
// the standard CRUD families handled inline above (commitments, regional
// targetTcpProxies, regional forwardingRules, networkEndpointGroups).
