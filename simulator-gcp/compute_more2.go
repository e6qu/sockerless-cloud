package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// This file is an isolated tranche of Compute Engine (compute v1)
// control-plane metadata CRUD, layered on top of compute.go /
// compute_more.go without editing them. It reuses the
// computeMetaResource machinery (defined in compute_more.go) for the
// standard insert/get/list/delete/patch/setLabels/aggregated verbs and
// adds faithful IAM-policy, custom-verb, and read-only catalog handlers
// for resource families the first two files do not yet cover. Every
// handler is metadata-only: it stores the client-sent resource and
// returns the Operation / list / policy shape the Discovery document
// defines — no real-exec networking, no synthetic status.

// registerComputeMore2 wires the second wave of metadata-only Compute
// control-plane collections onto the shared mux.
func registerComputeMore2(srv *sim.Server) {
	mk := func(table string) sim.Store[map[string]any] {
		return sim.MakeStore[map[string]any](srv.DB(), table)
	}

	// autoscalers (regional + zonal) share their store between the shared
	// CRUD helper and the collection-level patch/update handlers below, so
	// the two stay backed by the same table.
	regionAutoscalers := mk("compute_region_autoscalers")
	zoneAutoscalers := mk("compute_zone_autoscalers")

	// Standard metadata-only CRUD families. Each entry reuses
	// computeMetaResource.register, which stamps the server-owned members
	// (kind / id / selfLink / creationTimestamp / scope URL) real GCP
	// fills in and returns the scope-appropriate Operation on insert /
	// delete / patch / setLabels. The aggregated flag is set only for
	// collections whose Discovery document defines an aggregatedList
	// method, so no invented /aggregated/<collection> route is registered.
	// Shared so the node verbs in compute_members.go address the same groups,
	// and the reservation verbs the same reservations.
	gcpComputeNodeGroups = mk("compute_node_groups")
	gcpComputeReservations = mk("compute_reservations")
	gcpComputeRegionalPublicDelegatedPrefixes = mk("compute_region_public_delegated_prefixes")
	gcpComputeBackendBuckets = mk("compute_backend_buckets")

	families := []computeMetaResource{
		// Global load-balancing / addressing / policy resources.
		{collection: "backendBuckets", kind: "compute#backendBucket", scope: cScopeGlobal, store: gcpComputeBackendBuckets, patch: true, update: true, aggregated: true, listUsableKind: "compute#usableBackendBucketList",
			// setEdgeSecurityPolicy sends a SecurityPolicyReference, so its
			// body member is securityPolicy while the bucket stores it as
			// edgeSecurityPolicy.
			setVerbs: []computeSetVerb{{verb: "setEdgeSecurityPolicy", member: "securityPolicy", into: "edgeSecurityPolicy"}}},
		{collection: "externalVpnGateways", kind: "compute#externalVpnGateway", scope: cScopeGlobal, store: mk("compute_external_vpn_gateways"), setLabels: true, testIamOnly: true},
		{collection: "targetSslProxies", kind: "compute#targetSslProxy", scope: cScopeGlobal, store: mk("compute_target_ssl_proxies"), testIamOnly: true},
		{collection: "publicDelegatedPrefixes", kind: "compute#publicDelegatedPrefix", scope: cScopeGlobal, store: mk("compute_public_delegated_prefixes"), patch: true, aggregated: true},
		{collection: "publicAdvertisedPrefixes", kind: "compute#publicAdvertisedPrefix", scope: cScopeGlobal, store: mk("compute_public_advertised_prefixes"), patch: true,
			// A prefix is validated before it goes on the wire, and can be
			// taken back off it.
			stateVerbs: []computeStateVerb{
				{verb: "announce", from: "INITIAL", to: "ANNOUNCED_TO_INTERNET", done: "announced", initial: "INITIAL"},
				{verb: "withdraw", from: "ANNOUNCED_TO_INTERNET", to: "INITIAL", done: "withdrawn", initial: "INITIAL"},
			}},
		{collection: "sslPolicies", kind: "compute#sslPolicy", scope: cScopeGlobal, store: mk("compute_ssl_policies"), patch: true, aggregated: true},

		// Regional resources.
		{collection: "resourcePolicies", kind: "compute#resourcePolicy", scope: cScopeRegion, store: mk("compute_resource_policies"), patch: true, aggregated: true},
		{collection: "sslCertificates", kind: "compute#sslCertificate", scope: cScopeRegion, store: mk("compute_region_ssl_certificates")},
		// The regional public delegated prefixes are a separate collection
		// from the global ones, with announce and withdraw of their own.
		{collection: "publicDelegatedPrefixes", kind: "compute#publicDelegatedPrefix", scope: cScopeRegion, store: gcpComputeRegionalPublicDelegatedPrefixes, patch: true},
		{collection: "nodeTemplates", kind: "compute#nodeTemplate", scope: cScopeRegion, store: mk("compute_node_templates"), aggregated: true, iam: true},
		{collection: "notificationEndpoints", kind: "compute#notificationEndpoint", scope: cScopeRegion, store: mk("compute_notification_endpoints"), aggregated: true, testIamOnly: true},
		{collection: "targetHttpsProxies", kind: "compute#targetHttpsProxy", scope: cScopeRegion, store: mk("compute_region_target_https_proxies"), patch: true},
		{collection: "targetVpnGateways", kind: "compute#targetVpnGateway", scope: cScopeRegion, store: mk("compute_target_vpn_gateways"), setLabels: true, aggregated: true},
		{collection: "vpnGateways", kind: "compute#vpnGateway", scope: cScopeRegion, store: mk("compute_vpn_gateways"), setLabels: true, aggregated: true, testIamOnly: true,
			// What the gateway's tunnels are doing. A gateway with none
			// reports none, which is the truthful answer rather than an
			// invented connection.
			statusReads: []computeStatusRead{{
				verb: "getStatus", wrap: "result",
				status: func(map[string]any) map[string]any {
					return map[string]any{"vpnConnections": []any{}}
				},
			}}},
		{collection: "vpnTunnels", kind: "compute#vpnTunnel", scope: cScopeRegion, store: mk("compute_vpn_tunnels"), setLabels: true, aggregated: true},
		{collection: "serviceAttachments", kind: "compute#serviceAttachment", scope: cScopeRegion, store: mk("compute_service_attachments"), patch: true, aggregated: true},
		{collection: "networkAttachments", kind: "compute#networkAttachment", scope: cScopeRegion, store: mk("compute_network_attachments"), patch: true, aggregated: true},
		// Regional security policies are registered by registerComputePolicies,
		// which serves their rule verbs alongside the lifecycle.
		{collection: "autoscalers", kind: "compute#autoscaler", scope: cScopeRegion, store: regionAutoscalers, aggregated: true, testIamOnly: true},
		{collection: "instantSnapshots", kind: "compute#instantSnapshot", scope: cScopeRegion, store: mk("compute_region_instant_snapshots"), setLabels: true},
		{collection: "sslPolicies", kind: "compute#sslPolicy", scope: cScopeRegion, store: mk("compute_region_ssl_policies"), patch: true},

		// Zonal resources.
		{collection: "autoscalers", kind: "compute#autoscaler", scope: cScopeZone, store: zoneAutoscalers, testIamOnly: true},
		{collection: "nodeGroups", kind: "compute#nodeGroup", scope: cScopeZone, store: gcpComputeNodeGroups, patch: true, aggregated: true},
		{collection: "reservations", kind: "compute#reservation", scope: cScopeZone, store: gcpComputeReservations, patch: true, aggregated: true, resourceMetadata: true},
		{collection: "storagePools", kind: "compute#storagePool", scope: cScopeZone, store: mk("compute_storage_pools"), patch: true, aggregated: true},
		{collection: "targetInstances", kind: "compute#targetInstance", scope: cScopeZone, store: mk("compute_target_instances"), aggregated: true, testIamOnly: true,
			setVerbs: []computeSetVerb{{verb: "setSecurityPolicy", member: "securityPolicy"}}},
		{collection: "futureReservations", kind: "compute#futureReservation", scope: cScopeZone, store: mk("compute_future_reservations"), patch: true, aggregated: true, resourceMetadata: true,
			// A future reservation can be cancelled before it starts, and only
			// once.
			stateVerbs: []computeStateVerb{
				{verb: "cancel", from: "DRAFTING", to: "CANCELLED", done: "cancelled", initial: "DRAFTING",
					at: []string{"status", "procurementStatus"}},
			}},
		{collection: "instantSnapshots", kind: "compute#instantSnapshot", scope: cScopeZone, store: mk("compute_zone_instant_snapshots"), setLabels: true, aggregated: true},
	}
	for _, res := range families {
		res.register(srv)
	}

	// Licenses: a global metadata resource whose List response carries no
	// `kind` member (LicensesListResponse), so it cannot ride the shared
	// list helper (which always stamps kind). Registered with bespoke
	// handlers that round-trip the stored License faithfully.
	registerComputeLicenses(srv, mk("compute_licenses"))

	// autoscalers.patch / autoscalers.update are collection-level verbs
	// (the target is named by the `autoscaler` query parameter, not a path
	// segment), so they cannot ride the shared item-level helper.
	registerComputeAutoscalerCollectionMutations(srv, cScopeRegion, regionAutoscalers)
	registerComputeAutoscalerCollectionMutations(srv, cScopeZone, zoneAutoscalers)

	// Custom verbs that return an Operation or a simple documented
	// response shape (setBackendService / setUrlMap / listAvailableFeatures).
	registerComputeTargetSslProxySetters(srv)
	registerComputeRegionTargetHttpsProxySetters(srv)
	registerComputeSslPoliciesAvailableFeatures(srv, cScopeGlobal)
	registerComputeSslPoliciesAvailableFeatures(srv, cScopeRegion)

	// IAM policy triplet for the resources the Discovery document exposes
	// it on. The policy is stored per resource and round-tripped as
	// google.iam.v1.Policy (version / bindings / auditConfigs / etag).
	iamStore := mk("compute_iam_policies")
	iamFamilies := []struct {
		scope      computeScopeKind
		collection string
	}{
		{cScopeGlobal, "backendBuckets"},
		{cScopeGlobal, "licenses"},
		{cScopeRegion, "resourcePolicies"},
		{cScopeRegion, "serviceAttachments"},
		{cScopeRegion, "networkAttachments"},
		{cScopeRegion, "instantSnapshots"},
		{cScopeZone, "instantSnapshots"},
		{cScopeZone, "nodeGroups"},
		{cScopeZone, "reservations"},
		{cScopeZone, "storagePools"},
	}
	for _, f := range iamFamilies {
		registerComputeIAMTriplet(srv, f.scope, f.collection, iamStore)
	}

	// Read-only catalog collections (GCP-defined types a client enumerates
	// before provisioning). Returned entries carry only documented members.
	registerComputeNodeTypesCatalog(srv)
	registerComputeStoragePoolTypesCatalog(srv)
	registerComputeNetworkProfilesCatalog(srv)
	registerComputeRegionDiskTypesCatalog(srv)
}

// registerComputeLicenses wires the global licenses collection. Licenses
// is registered standalone rather than via computeMetaResource because
// the LicensesListResponse schema defines no `kind` member (the shared
// list helper always stamps kind); every verb here emits exactly the
// documented License / LicensesListResponse / Operation shape.
func registerComputeLicenses(srv *sim.Server, store sim.Store[map[string]any]) {
	const collection = "licenses"
	base := computeScopeMux(cScopeGlobal, collection)
	relPath := func(r *http.Request, name string) string {
		return fmt.Sprintf("projects/%s/global/%s/%s", sim.PathParam(r, "project"), collection, name)
	}

	srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		name, _ := body["name"].(string)
		if name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		key := relPath(r, name)
		if _, exists := store.Get(key); computeConflict(w, exists, collection, name) {
			return
		}
		body["kind"] = "compute#license"
		body["id"] = computeNumericID()
		body["selfLink"] = computeSelfLink(key)
		body["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
		store.Put(key, body)
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(sim.PathParam(r, "project"), "global", computeSelfLink(key), "insert"))
	})

	srv.HandleFunc("GET "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := store.Get(relPath(r, sim.PathParam(r, "name")))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, sim.PathParam(r, "name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, m)
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := computeSelfLink(fmt.Sprintf("projects/%s/global/%s/", project, collection))
		items := store.Filter(func(m map[string]any) bool {
			sl, _ := m["selfLink"].(string)
			return strings.HasPrefix(sl, prefix)
		})
		sort.Slice(items, func(i, j int) bool {
			ni, _ := items[i]["name"].(string)
			nj, _ := items[j]["name"].(string)
			return ni < nj
		})
		items = gcpApplyListParams(items, r)
		page, next, ok := paginateListCompute(w, r, items)
		if !ok {
			return
		}
		if page == nil {
			page = []map[string]any{}
		}
		// LicensesListResponse has no `kind` member; emit only the
		// documented id / items / nextPageToken fields.
		resp := map[string]any{"id": computeNumericID(), "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("DELETE "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		key := relPath(r, sim.PathParam(r, "name"))
		if computeNotFound(w, store.Delete(key), collection, sim.PathParam(r, "name")) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "global", computeSelfLink(key), "delete"))
	})

	srv.HandleFunc("PATCH "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		key := relPath(r, sim.PathParam(r, "name"))
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := store.Update(key, func(m *map[string]any) {
			cur := *m
			for k, v := range body {
				switch k {
				case "kind", "id", "selfLink", "creationTimestamp", "name":
					continue
				}
				cur[k] = v
			}
		})
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, sim.PathParam(r, "name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "global", computeSelfLink(key), "patch"))
	})
}

// registerComputeAutoscalerCollectionMutations wires autoscalers.patch
// and autoscalers.update. Unlike most Compute mutations these target
// the autoscaler via the collection path plus an `autoscaler` query
// parameter rather than a path segment, so they are registered at the
// collection base and share the patch verb's store.
func registerComputeAutoscalerCollectionMutations(srv *sim.Server, scope computeScopeKind, store sim.Store[map[string]any]) {
	base := computeScopeMux(scope, "autoscalers")
	apply := func(w http.ResponseWriter, r *http.Request, opType string, merge bool) {
		project := sim.PathParam(r, "project")
		name := r.URL.Query().Get("autoscaler")
		if name == "" {
			sim.GCPError(w, http.StatusBadRequest, "autoscaler query parameter is required", "INVALID_ARGUMENT")
			return
		}
		key := fmt.Sprintf("projects/%s/%s/autoscalers/%s", project, computeScopeSegment(scope, r), name)
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := store.Update(key, func(m *map[string]any) {
			if !merge {
				kept := map[string]any{
					"kind":              (*m)["kind"],
					"id":                (*m)["id"],
					"selfLink":          (*m)["selfLink"],
					"creationTimestamp": (*m)["creationTimestamp"],
					"name":              name,
				}
				for k, v := range body {
					if _, serverOwned := kept[k]; serverOwned {
						continue
					}
					kept[k] = v
				}
				*m = kept
				return
			}
			cur := *m
			for k, v := range body {
				switch k {
				case "kind", "id", "selfLink", "creationTimestamp", "name":
					continue
				}
				cur[k] = v
			}
		})
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "autoscaler %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(scope, r), computeSelfLink(key), opType))
	}
	srv.HandleFunc("PATCH "+base, func(w http.ResponseWriter, r *http.Request) { apply(w, r, "patch", true) })
	srv.HandleFunc("PUT "+base, func(w http.ResponseWriter, r *http.Request) { apply(w, r, "update", false) })
}

// registerComputeTargetSslProxySetters wires the global targetSslProxies
// mutation verbs that return a global Operation.
func registerComputeTargetSslProxySetters(srv *sim.Server) {
	const collection = "targetSslProxies"
	base := computeScopeMux(cScopeGlobal, collection)
	mutate := func(verb, opType string) {
		srv.HandleFunc("POST "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			key := fmt.Sprintf("projects/%s/global/%s/%s", project, collection, sim.PathParam(r, "name"))
			// Drain the request body so upstream proxies see the full frame;
			// the per-field patch is applied against the store if present.
			var body map[string]any
			_ = sim.ReadJSON(r, &body)
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "global", computeSelfLink(key), opType))
		})
	}
	mutate("setBackendService", "setBackendService")
	mutate("setCertificateMap", "setCertificateMap")
	mutate("setProxyHeader", "setProxyHeader")
	mutate("setSslCertificates", "setSslCertificates")
	mutate("setSslPolicy", "setSslPolicy")
}

// registerComputeRegionTargetHttpsProxySetters wires the regional
// targetHttpsProxies setUrlMap / setSslCertificates verbs.
func registerComputeRegionTargetHttpsProxySetters(srv *sim.Server) {
	const collection = "targetHttpsProxies"
	base := computeScopeMux(cScopeRegion, collection)
	mutate := func(verb, opType string) {
		srv.HandleFunc("POST "+base+"/{name}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			key := fmt.Sprintf("projects/%s/regions/%s/%s/%s", project, sim.PathParam(r, "region"), collection, sim.PathParam(r, "name"))
			var body map[string]any
			_ = sim.ReadJSON(r, &body)
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(cScopeRegion, r), computeSelfLink(key), opType))
		})
	}
	mutate("setUrlMap", "setUrlMap")
	mutate("setSslCertificates", "setSslCertificates")
}

// registerComputeSslPoliciesAvailableFeatures wires the
// sslPolicies.listAvailableFeatures read, which returns the cipher /
// feature catalog GCP publishes for the selected profile.
func registerComputeSslPoliciesAvailableFeatures(srv *sim.Server, scope computeScopeKind) {
	base := computeScopeMux(scope, "sslPolicies")
	srv.HandleFunc("GET "+base+"/listAvailableFeatures", func(w http.ResponseWriter, r *http.Request) {
		_ = sim.PathParam(r, "project")
		features := append([]string{}, computeTLSCipherFeatures...)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"features": features})
	})
}

// computeTLSCipherFeatures is the published TLS cipher-suite feature list
// GCP returns from sslPolicies.listAvailableFeatures (the documented
// set of ciphers a custom profile may reference).
var computeTLSCipherFeatures = []string{
	"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
	"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
	"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256",
	"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
	"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
	"TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
	"TLS_RSA_WITH_AES_128_GCM_SHA256",
	"TLS_RSA_WITH_AES_256_GCM_SHA384",
	"TLS_RSA_WITH_AES_128_CBC_SHA",
	"TLS_RSA_WITH_AES_256_CBC_SHA",
	"TLS_RSA_WITH_3DES_EDE_CBC_SHA",
}

// registerComputeIAMTriplet wires the getIamPolicy / setIamPolicy /
// testIamPermissions methods for one scoped collection. The policy is
// stored per resource self-link path and round-tripped as the
// compute-discovery Policy (version / bindings / auditConfigs / etag).
func registerComputeIAMTriplet(srv *sim.Server, scope computeScopeKind, collection string, store sim.Store[map[string]any]) {
	base := computeScopeMux(scope, collection)
	resourceKey := func(r *http.Request) string {
		project := sim.PathParam(r, "project")
		return fmt.Sprintf("projects/%s/%s/%s/%s", project, computeScopeSegment(scope, r), collection, sim.PathParam(r, "resource"))
	}

	// getIamPolicy
	srv.HandleFunc("GET "+base+"/{resource}/getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		key := resourceKey(r)
		if pol, ok := store.Get(key); ok {
			sim.WriteJSON(w, http.StatusOK, pol)
			return
		}
		sim.WriteJSON(w, http.StatusOK, emptyComputeIamPolicy())
	})

	// setIamPolicy
	srv.HandleFunc("POST "+base+"/{resource}/setIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		key := resourceKey(r)
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		pol := buildComputeIamPolicy(body)
		store.Put(key, pol)
		sim.WriteJSON(w, http.StatusOK, pol)
	})

	// testIamPermissions
	srv.HandleFunc("POST "+base+"/{resource}/testIamPermissions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Permissions []string `json:"permissions"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// Faithful to a simulator that grants every requested permission:
		// the granted subset is echoed back verbatim.
		granted := req.Permissions
		if granted == nil {
			granted = []string{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"permissions": granted})
	})
}

// emptyComputeIamPolicy returns a freshly-versioned empty Policy.
func emptyComputeIamPolicy() map[string]any {
	return map[string]any{
		"version":  1,
		"etag":     computeFingerprint(),
		"bindings": []map[string]any{},
	}
}

// buildComputeIamPolicy assembles the stored Policy from a setIamPolicy
// request body. The compute API accepts either a wrapped `policy` object
// or the top-level Policy fields; both spellings are honored. Bindings
// arrive JSON-decoded as []interface{} and are normalized to a stable
// []map[string]any shape before storage.
func buildComputeIamPolicy(body map[string]any) map[string]any {
	if body == nil {
		body = map[string]any{}
	}
	src, _ := body["policy"].(map[string]any)
	if src == nil {
		src = body
	}
	var b []map[string]any
	if raw, ok := src["bindings"]; ok {
		b = normalizeBindings(raw)
	}
	if len(b) == 0 {
		if raw, ok := body["bindings"]; ok {
			b = normalizeBindings(raw)
		}
	}
	if b == nil {
		b = []map[string]any{}
	}
	tag, _ := src["etag"].(string)
	if tag == "" {
		if t, ok := body["etag"].(string); ok {
			tag = t
		}
	}
	if tag == "" {
		tag = computeFingerprint()
	}
	ver := int64(1)
	if v, ok := src["version"].(float64); ok {
		ver = int64(v)
	}
	pol := map[string]any{
		"version":  ver,
		"etag":     tag,
		"bindings": b,
	}
	if ac, ok := src["auditConfigs"]; ok {
		pol["auditConfigs"] = ac
	}
	return pol
}

// normalizeBindings coerces a JSON-decoded bindings value ([]interface{}
// of map[string]any) to the []map[string]any the store holds.
func normalizeBindings(raw any) []map[string]any {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// registerComputeNodeTypesCatalog serves the zonal nodeTypes list / get /
// aggregatedList — the sole-node hardware catalog GCP publishes.
func registerComputeNodeTypesCatalog(srv *sim.Server) {
	const collection = "nodeTypes"
	zones := []string{"us-central1-a", "us-central1-b", "europe-west1-b"}
	// Real sole-tenant node types whose name encodes their spec
	// ("<gen>-node-<vCPU>-<GB>"), so guestCpus / memoryMb are derived
	// verifiably (GB → MB at 1024). Values match the GCP node-types
	// documentation for these names.
	type nodeSpec struct {
		guestCpus int
		memoryMb  int
	}
	specs := map[string]nodeSpec{
		"n1-node-96-624":  {96, 624 * 1024},
		"n1-node-128-864": {128, 864 * 1024},
		"c2-node-60-240":  {60, 240 * 1024},
		"c2-node-30-120":  {30, 120 * 1024},
	}
	names := make([]string, 0, len(specs))
	for n := range specs {
		names = append(names, n)
	}
	sort.Strings(names)
	entry := func(project, zone, name string) map[string]any {
		s := specs[name]
		return map[string]any{
			"kind":      "compute#nodeType",
			"id":        computeNumericID(),
			"name":      name,
			"guestCpus": s.guestCpus,
			"memoryMb":  s.memoryMb,
			"zone":      computeSelfLink(fmt.Sprintf("projects/%s/zones/%s", project, zone)),
			"selfLink":  computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/%s/%s", project, zone, collection, name)),
		}
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/"+collection+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, entry(sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name")))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/"+collection, func(w http.ResponseWriter, r *http.Request) {
		project, zone := sim.PathParam(r, "project"), sim.PathParam(r, "zone")
		items := make([]map[string]any, 0, len(names))
		for _, n := range names {
			items = append(items, entry(project, zone, n))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#nodeTypeList", "id": computeNumericID(), "items": items, "selfLink": computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/%s", project, zone, collection))})
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/"+collection, func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		grouped := map[string]map[string]any{}
		for _, zone := range zones {
			list := make([]map[string]any, 0, len(names))
			for _, n := range names {
				list = append(list, entry(project, zone, n))
			}
			grouped["zones/"+zone] = map[string]any{collection: list}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#nodeTypeAggregatedList", "id": computeNumericID(), "items": grouped})
	})
}

// registerComputeStoragePoolTypesCatalog serves the zonal storagePoolTypes
// catalog (list / get / aggregatedList).
func registerComputeStoragePoolTypesCatalog(srv *sim.Server) {
	const collection = "storagePoolTypes"
	zones := []string{"us-central1-a", "us-central1-b", "europe-west1-b"}
	names := []string{"hyperdisk-balanced-pool", "hyperdisk-extreme-pool", "hyperdisk-throughput-pool"}
	entry := func(project, zone, name string) map[string]any {
		return map[string]any{
			"kind":     "compute#storagePoolType",
			"id":       computeNumericID(),
			"name":     name,
			"zone":     computeSelfLink(fmt.Sprintf("projects/%s/zones/%s", project, zone)),
			"selfLink": computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/%s/%s", project, zone, collection, name)),
		}
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/"+collection+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, entry(sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name")))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/"+collection, func(w http.ResponseWriter, r *http.Request) {
		project, zone := sim.PathParam(r, "project"), sim.PathParam(r, "zone")
		items := make([]map[string]any, 0, len(names))
		for _, n := range names {
			items = append(items, entry(project, zone, n))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#storagePoolTypeList", "id": computeNumericID(), "items": items, "selfLink": computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/%s", project, zone, collection))})
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/"+collection, func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		grouped := map[string]map[string]any{}
		for _, zone := range zones {
			list := make([]map[string]any, 0, len(names))
			for _, n := range names {
				list = append(list, entry(project, zone, n))
			}
			grouped["zones/"+zone] = map[string]any{collection: list}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#storagePoolTypeAggregatedList", "id": computeNumericID(), "items": grouped})
	})
}

// registerComputeNetworkProfilesCatalog serves the global networkProfiles
// catalog (list / get).
func registerComputeNetworkProfilesCatalog(srv *sim.Server) {
	const collection = "networkProfiles"
	names := []string{"internet-profile", "private-network-profile"}
	entry := func(project, name string) map[string]any {
		return map[string]any{
			"kind":     "compute#networkProfile",
			"id":       computeNumericID(),
			"name":     name,
			"selfLink": computeSelfLink(fmt.Sprintf("projects/%s/global/%s/%s", project, collection, name)),
		}
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/"+collection+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, entry(sim.PathParam(r, "project"), sim.PathParam(r, "name")))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/"+collection, func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		items := make([]map[string]any, 0, len(names))
		for _, n := range names {
			items = append(items, entry(project, n))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#networkProfilesListResponse", "id": computeNumericID(), "items": items, "selfLink": computeSelfLink(fmt.Sprintf("projects/%s/global/%s", project, collection))})
	})
}

// registerComputeRegionDiskTypesCatalog serves the regional diskTypes
// catalog (list / get).
func registerComputeRegionDiskTypesCatalog(srv *sim.Server) {
	const collection = "diskTypes"
	names := []string{"pd-standard", "pd-balanced", "pd-ssd"}
	entry := func(project, region, name string) map[string]any {
		return map[string]any{
			"kind":              "compute#diskType",
			"id":                computeNumericID(),
			"name":              name,
			"region":            computeSelfLink(fmt.Sprintf("projects/%s/regions/%s", project, region)),
			"selfLink":          computeSelfLink(fmt.Sprintf("projects/%s/regions/%s/%s/%s", project, region, collection, name)),
			"defaultDiskSizeGb": "100",
		}
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/"+collection+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, entry(sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "name")))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/"+collection, func(w http.ResponseWriter, r *http.Request) {
		project, region := sim.PathParam(r, "project"), sim.PathParam(r, "region")
		items := make([]map[string]any, 0, len(names))
		for _, n := range names {
			items = append(items, entry(project, region, n))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#diskTypeList", "id": computeNumericID(), "items": items, "selfLink": computeSelfLink(fmt.Sprintf("projects/%s/regions/%s/%s", project, region, collection))})
	})
}
