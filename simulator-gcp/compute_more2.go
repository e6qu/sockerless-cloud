package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerComputeMore2(srv *sim.Server) {
	mk := func(table string) sim.Store[map[string]any] {
		return sim.MakeStore[map[string]any](srv.DB(), table)
	}

	// autoscalers (regional + zonal) share their store between the shared
	// CRUD helper and the collection-level patch/update handlers below, so
	// the two stay backed by the same table.
	regionAutoscalers := mk("compute_region_autoscalers")
	zoneAutoscalers := mk("compute_zone_autoscalers")

	// The aggregated flag marks only the collections whose Discovery document
	// declares an aggregatedList. The node verbs in compute_members.go and the
	// reservation verbs share these stores.
	gcpComputeNodeGroups = mk("compute_node_groups")
	gcpComputeReservations = mk("compute_reservations")
	gcpComputeRegionalPublicDelegatedPrefixes = mk("compute_region_public_delegated_prefixes")
	gcpComputeBackendBuckets = mk("compute_backend_buckets")

	families := []computeMetaResource{
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

	registerComputeLicenses(srv, mk("compute_licenses"), mk("compute_license_codes"))

	// autoscalers.patch and .update name the autoscaler in a query parameter
	// rather than a path segment, so they mount at the collection base.
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

// gcpComputeLicenses holds the project licences, keyed by their path.
var gcpComputeLicenses sim.Store[map[string]any]

// registerComputeLicenses serves the global licenses collection on its own:
// LicensesListResponse declares no `kind`, which the shared list helper
// always stamps.
func registerComputeLicenses(srv *sim.Server, store, codes sim.Store[map[string]any]) {
	gcpComputeLicenses = store
	const collection = "licenses"
	base := computeScopeMux(cScopeGlobal, collection)
	relPath := func(r *http.Request, name string) string {
		return fmt.Sprintf("projects/%s/global/%s/%s", sim.PathParam(r, "project"), collection, name)
	}

	srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		name, _ := body["name"].(string)
		if computeRefuseNameOf(w, "compute#license", name) {
			return
		}
		key := relPath(r, name)
		if _, exists := store.Get(key); computeConflict(w, exists, collection, name) {
			return
		}
		body["kind"] = "compute#license"
		body["id"] = computeNumericID()
		// The code the licence is attached to images and disks by. Compute
		// Engine assigns it — it is output-only on the License — and it is
		// what licenseCodes.get is asked for.
		code := computeNumericID()
		body["licenseCode"] = code
		body["selfLink"] = computeSelfLink(key)
		body["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
		store.Put(key, body)
		codes.Put(code, map[string]any{"license": key})
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(sim.PathParam(r, "project"), "global", computeSelfLink(key), "insert"))
	})

	// A licence code reads as its own resource: the code names its licence, and
	// the alias carries that licence's URL and description. The retention and
	// attachment rules Google's published licences set are absent unless the
	// licence declares them.
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/licenseCodes/{licenseCode}",
		func(w http.ResponseWriter, r *http.Request) {
			// The code is its own row holding the licence's key, so a code whose licence
			// is gone resolves to nothing, like one never issued.
			code := sim.PathParam(r, "licenseCode")
			notFound := func() {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"licenseCode %q not found", code)
			}
			row, issued := codes.Get(code)
			if !issued {
				notFound()
				return
			}
			key, _ := row["license"].(string)
			licence, ok := store.Get(key)
			if key == "" || !ok {
				notFound()
				return
			}
			out := map[string]any{
				"kind":     "compute#licenseCode",
				"id":       code,
				"name":     code,
				"selfLink": computeSelfLink("projects/" + sim.PathParam(r, "project") + "/global/licenseCodes/" + code),
				"state":    "ENABLED",
			}
			if v, isSet := licence["creationTimestamp"]; isSet {
				out["creationTimestamp"] = v
			}
			alias := map[string]any{}
			if v, isSet := licence["selfLink"]; isSet {
				alias["selfLink"] = v
			}
			if v, isSet := licence["description"]; isSet {
				out["description"] = v
				alias["description"] = v
			}
			if len(alias) > 0 {
				out["licenseAlias"] = []any{alias}
			}
			// The rules the licence itself states, carried through rather than
			// answered for: each is declared on both resources.
			for _, field := range []string{
				"allowedReplacementLicenses", "appendableToDisk", "incompatibleLicenses",
				"minimumRetention", "multiTenantOnly", "osLicense", "removableFromDisk",
				"requiredCoattachedLicenses", "soleTenantOnly", "transferable",
			} {
				if v, isSet := licence[field]; isSet {
					out[field] = v
				}
			}
			sim.WriteJSON(w, http.StatusOK, out)
		})

	srv.HandleFunc("GET "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := store.Get(relPath(r, sim.PathParam(r, "name")))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, sim.PathParam(r, "name"))
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
		listed, listOK := gcpApplyComputeListParams(w, r, items)
		if !listOK {
			return
		}
		items = listed
		page, next, ok := paginateListCompute(w, r, items, gcpAsc(gcpField("name")), gcpAsc(gcpField("selfLink")))
		if !ok {
			return
		}
		if page == nil {
			page = []map[string]any{}
		}
		// LicensesListResponse has no `kind`; emit only id, items and nextPageToken.
		resp := map[string]any{"id": computeNumericID(), "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("DELETE "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		key := relPath(r, sim.PathParam(r, "name"))
		// Read the code before the licence goes, so the code row goes with it.
		var code string
		if licence, ok := store.Get(key); ok {
			code, _ = licence["licenseCode"].(string)
		}
		if computeNotFound(w, store.Delete(key), collection, sim.PathParam(r, "name")) {
			return
		}
		if code != "" {
			codes.Delete(code)
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "global", computeSelfLink(key), "delete"))
	})

	srv.HandleFunc("PATCH "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		key := relPath(r, sim.PathParam(r, "name"))
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := store.Update(key, func(m *map[string]any) {
			cur := *m
			for k, v := range body {
				switch k {
				case "kind", "id", "licenseCode", "selfLink", "creationTimestamp", "name":
					continue
				}
				cur[k] = v
			}
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, sim.PathParam(r, "name"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "global", computeSelfLink(key), "patch"))
	})
}

// registerComputeAutoscalerCollectionMutations serves autoscalers.patch and
// .update at the collection base with the `autoscaler` query parameter,
// sharing the patch verb's store.
func registerComputeAutoscalerCollectionMutations(srv *sim.Server, scope computeScopeKind, store sim.Store[map[string]any]) {
	base := computeScopeMux(scope, "autoscalers")
	apply := func(w http.ResponseWriter, r *http.Request, opType string, merge bool) {
		project := sim.PathParam(r, "project")
		name := r.URL.Query().Get("autoscaler")
		if name == "" {
			GCPError(w, http.StatusBadRequest, "autoscaler query parameter is required", "INVALID_ARGUMENT")
			return
		}
		key := fmt.Sprintf("projects/%s/%s/autoscalers/%s", project, computeScopeSegment(scope, r), name)
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "autoscaler %q not found", name)
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

// registerComputeSslPoliciesAvailableFeatures serves
// sslPolicies.listAvailableFeatures from the published cipher catalog.
func registerComputeSslPoliciesAvailableFeatures(srv *sim.Server, scope computeScopeKind) {
	base := computeScopeMux(scope, "sslPolicies")
	srv.HandleFunc("GET "+base+"/listAvailableFeatures", func(w http.ResponseWriter, r *http.Request) {
		_ = sim.PathParam(r, "project")
		features := append([]string{}, computeTLSCipherFeatures...)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"features": features})
	})
}

// computeTLSCipherFeatures is the TLS cipher-suite list Compute Engine
// publishes for a custom SSL policy profile.
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

// registerComputeIAMTriplet serves getIamPolicy, setIamPolicy and
// testIamPermissions for one scoped collection, storing the Policy per
// resource self-link path.
func registerComputeIAMTriplet(srv *sim.Server, scope computeScopeKind, collection string, store sim.Store[map[string]any]) {
	base := computeScopeMux(scope, collection)
	resourceKey := func(r *http.Request) string {
		project := sim.PathParam(r, "project")
		return fmt.Sprintf("projects/%s/%s/%s/%s", project, computeScopeSegment(scope, r), collection, sim.PathParam(r, "resource"))
	}

	srv.HandleFunc("GET "+base+"/{resource}/getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		key := resourceKey(r)
		if pol, ok := store.Get(key); ok {
			sim.WriteJSON(w, http.StatusOK, pol)
			return
		}
		sim.WriteJSON(w, http.StatusOK, emptyComputeIamPolicy())
	})

	srv.HandleFunc("POST "+base+"/{resource}/setIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		key := resourceKey(r)
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		pol := buildComputeIamPolicy(body)
		store.Put(key, pol)
		sim.WriteJSON(w, http.StatusOK, pol)
	})

	srv.HandleFunc("POST "+base+"/{resource}/testIamPermissions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Permissions []string `json:"permissions"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// Echo every requested permission as granted; the stored policy is not
		// consulted.
		granted := req.Permissions
		if granted == nil {
			granted = []string{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"permissions": granted})
	})
}

func emptyComputeIamPolicy() map[string]any {
	return map[string]any{
		"version":  1,
		"etag":     computeFingerprint(),
		"bindings": []map[string]any{},
	}
}

// buildComputeIamPolicy reads a setIamPolicy body, whose Policy may be wrapped
// in `policy` or given top-level, and normalizes its bindings for storage.
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

// registerComputeNodeTypesCatalog serves the zonal nodeTypes list, get and
// aggregatedList.
func registerComputeNodeTypesCatalog(srv *sim.Server) {
	const collection = "nodeTypes"
	zones := []string{"us-central1-a", "us-central1-b", "europe-west1-b"}
	// Sole-tenant node type names encode their spec ("<gen>-node-<vCPU>-<GB>"),
	// so guestCpus and memoryMb derive from the name, matching Google's
	// node-types documentation.
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
