package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// This file ratchets up the Compute Engine (compute v1) control-plane
// surface: the metadata CRUD for the resources a real client
// (cloud.google.com/go/compute/apiv1, gcloud compute, terraform's google
// provider) drives. Every handler is metadata-only — it stores the
// resource the client sent, stamps the server-owned members GCP fills in
// (kind / id / selfLink / creationTimestamp / scope URL), and returns the
// zonal/regional/global Operation the client polls to DONE. None of these
// ops need a real-exec networking host, so they work on every platform.

// computeScopeKind distinguishes the three Compute resource scopes. Using a
// named type makes a mistyped scope a compile error.
type computeScopeKind int

const (
	cScopeGlobal computeScopeKind = iota
	cScopeRegion
	cScopeZone
)

// gcpComputeImages backs the user-created images collection. The catalog
// GET handler (registerComputeCatalog) consults it before falling back to
// the public-image catalog shape, so an image inserted via images.insert
// reads back faithfully.
var gcpComputeImages sim.Store[map[string]any]

// computeMetaResource describes one metadata-only Compute collection and
// the verbs to register for it. The store holds the resource as the
// client-sent map plus the server-stamped members, so every echoed member
// is a real Discovery-schema field (no synthetic state in the body).
type computeMetaResource struct {
	collection string // URL collection segment, e.g. "snapshots"
	kind       string // resource kind, e.g. "compute#snapshot"
	scope      computeScopeKind
	store      sim.Store[map[string]any]
	// Verb toggles. insert/get/list/delete are registered unless the
	// corresponding skip flag is set; the rest are opt-in.
	skipGet    bool // image GET is owned by the catalog handler
	skipDelete bool // some collections (e.g. commitments) have no Discovery DELETE
	// skipList suppresses the scoped list for a collection whose document
	// declares only the aggregated one, so the simulator mounts no route
	// Google does not publish.
	skipList bool
	// skipInsert suppresses the create for a collection the service creates
	// some other way — a rollout is produced by the change it rolls out, and
	// the document declares no insert for it.
	skipInsert bool
	// iam registers the getIamPolicy / setIamPolicy / testIamPermissions
	// triple Compute Engine mounts beneath the resource itself, backed by
	// the same policy store every other Google IAM surface reads.
	iam bool
	// testIamOnly registers the permission check alone, for the collections
	// that declare only that one: mounting the policy reader and writer for
	// them would publish routes Google does not.
	testIamOnly bool
	patch       bool
	// update registers the PUT that replaces the resource whole, for the
	// collections whose document declares one beside patch.
	update     bool
	setLabels  bool
	aggregated bool
	// listUsableKind is the `kind` of the listUsable response, set only for the
	// collections whose Discovery document declares the method. It is spelled
	// out rather than derived from res.kind because Google does not derive it:
	// backendServices answers compute#usableBackendServiceList.
	listUsableKind string
	// resourceMetadata stamps the standardized ResourceMetadata the Compute
	// Discovery document declares. It is opt-in per collection because only
	// three schemas carry the member — a reservation, a future reservation and
	// an accelerator type — and emitting it on a schema that does not declare
	// it would be an invented field, which the spec validator reads as the
	// defect it is.
	resourceMetadata bool
	// reconcile, when set, runs after a verb has changed the store, with the
	// resource's key. A collection whose resource does something in the world
	// — a packet mirroring policy really mirroring traffic — brings the world
	// in line with the record here, so the record and the behaviour cannot
	// drift apart.
	reconcile func(key string)
}

// computeScopeSegment renders the scope path segment(s) for a request's
// scope params: "global", "regions/{region}", or "zones/{zone}".
func computeScopeSegment(scope computeScopeKind, r *http.Request) string {
	switch scope {
	case cScopeRegion:
		return "regions/" + sim.PathParam(r, "region")
	case cScopeZone:
		return "zones/" + sim.PathParam(r, "zone")
	default:
		return "global"
	}
}

// computeScopeMux renders the mux path prefix for a collection at a scope,
// e.g. "/compute/v1/projects/{project}/regions/{region}/snapshots".
func computeScopeMux(scope computeScopeKind, collection string) string {
	switch scope {
	case cScopeRegion:
		return "/compute/v1/projects/{project}/regions/{region}/" + collection
	case cScopeZone:
		return "/compute/v1/projects/{project}/zones/{zone}/" + collection
	default:
		return "/compute/v1/projects/{project}/global/" + collection
	}
}

func computeSelfLink(relPath string) string {
	return "https://www.googleapis.com/compute/v1/" + relPath
}

// stampComputeScopeURL sets the zone/region member (full URL) on a stored
// resource map, matching real read-back. Global resources carry neither.
func stampComputeScopeURL(m map[string]any, scope computeScopeKind, project string, r *http.Request) {
	switch scope {
	case cScopeRegion:
		m["region"] = computeSelfLink(fmt.Sprintf("projects/%s/regions/%s", project, sim.PathParam(r, "region")))
	case cScopeZone:
		m["zone"] = computeSelfLink(fmt.Sprintf("projects/%s/zones/%s", project, sim.PathParam(r, "zone")))
	}
}

// register wires the configured verbs onto the mux.
// computeResourceMetadata builds the standardized ResourceMetadata Compute
// Engine returns beside a resource, from that resource's own kind. The
// canonical type name is the AIP-123 form the schema describes — "compute#reservation"
// is "compute.googleapis.com/Reservation".
//
// Only resourceType is stamped. The schema's other member, apiVersion, is the
// version string the resource was retrieved through, and the Discovery document
// gives its shape by example rather than stating what Compute v1 answers with —
// so it is left absent rather than guessed at, which is the difference between
// an incomplete answer and a wrong one.
func computeResourceMetadata(kind string) map[string]any {
	name := strings.TrimPrefix(kind, "compute#")
	if name == "" {
		return nil
	}
	return map[string]any{
		"resourceType": "compute.googleapis.com/" + strings.ToUpper(name[:1]) + name[1:],
	}
}

func (res computeMetaResource) register(srv *sim.Server) {
	base := computeScopeMux(res.scope, res.collection)
	listKind := res.kind + "List"

	relPath := func(r *http.Request, name string) string {
		project := sim.PathParam(r, "project")
		return fmt.Sprintf("projects/%s/%s/%s/%s", project, computeScopeSegment(res.scope, r), res.collection, name)
	}
	listPrefix := func(r *http.Request) string {
		project := sim.PathParam(r, "project")
		return computeSelfLink(fmt.Sprintf("projects/%s/%s/%s/", project, computeScopeSegment(res.scope, r), res.collection))
	}

	// Insert
	if !res.skipInsert {
		srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
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
			if _, exists := res.store.Get(key); computeConflict(w, exists, res.collection, name) {
				return
			}
			body["kind"] = res.kind
			if res.resourceMetadata {
				body["resourceMetadata"] = computeResourceMetadata(res.kind)
			}
			body["id"] = computeNumericID()
			body["selfLink"] = computeSelfLink(key)
			body["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
			if res.setLabels {
				body["labelFingerprint"] = computeFingerprint()
			}
			stampComputeScopeURL(body, res.scope, project, r)
			res.store.Put(key, body)
			if res.reconcile != nil {
				res.reconcile(key)
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(res.scope, r), computeSelfLink(key), "insert"))
		})
	}

	// Get
	if !res.skipGet {
		srv.HandleFunc("GET "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
			key := relPath(r, sim.PathParam(r, "name"))
			m, ok := res.store.Get(key)
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", res.collection, sim.PathParam(r, "name"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, m)
		})
	}

	// List
	if !res.skipList {
		srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
			prefix := listPrefix(r)
			items := res.store.Filter(func(m map[string]any) bool {
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
			resp := map[string]any{"kind": listKind, "items": page}
			if next != "" {
				resp["nextPageToken"] = next
			}
			sim.WriteJSON(w, http.StatusOK, resp)
		})
	}

	// listUsable — the subset of the collection the caller may attach to a
	// load balancer. The caller holds the project, so that is the project's
	// own resources: the same set list returns, under the response kind the
	// Discovery document declares. Registered on the literal segment, which
	// wins over the `{name}` get that would otherwise answer for it.
	if res.listUsableKind != "" {
		srv.HandleFunc("GET "+base+"/listUsable", func(w http.ResponseWriter, r *http.Request) {
			prefix := listPrefix(r)
			items := res.store.Filter(func(m map[string]any) bool {
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
			resp := map[string]any{"kind": res.listUsableKind, "items": page}
			if next != "" {
				resp["nextPageToken"] = next
			}
			sim.WriteJSON(w, http.StatusOK, resp)
		})
	}

	// IAM, where the document declares it: Compute Engine mounts the triple
	// under the resource rather than through the AIP-151 colon verbs, so the
	// routes are named here and the policies live in the shared store.
	if res.iam || res.testIamOnly {
		policyName := func(r *http.Request) string {
			return "compute/" + relPath(r, sim.PathParam(r, "resource"))
		}
		if res.iam {
			srv.HandleFunc("GET "+base+"/{resource}/getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
				handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "getIamPolicy")
			})
			srv.HandleFunc("POST "+base+"/{resource}/setIamPolicy", func(w http.ResponseWriter, r *http.Request) {
				handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "setIamPolicy")
			})
		}
		srv.HandleFunc("POST "+base+"/{resource}/testIamPermissions", func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies, policyName(r), "testIamPermissions")
		})
	}

	// Delete
	if !res.skipDelete {
		srv.HandleFunc("DELETE "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			key := relPath(r, sim.PathParam(r, "name"))
			if computeNotFound(w, res.store.Delete(key), res.collection, sim.PathParam(r, "name")) {
				return
			}
			if res.reconcile != nil {
				res.reconcile(key)
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(res.scope, r), computeSelfLink(key), "delete"))
		})
	}

	// Patch (merge top-level members the client sent).
	if res.patch {
		srv.HandleFunc("PATCH "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			key := relPath(r, sim.PathParam(r, "name"))
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			ok := res.store.Update(key, func(m *map[string]any) {
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
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", res.collection, sim.PathParam(r, "name"))
				return
			}
			if res.reconcile != nil {
				res.reconcile(key)
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(res.scope, r), computeSelfLink(key), "patch"))
		})
	}

	// Update (PUT: replace the resource, keeping only its identity). Compute
	// Engine declares this beside patch for the collections whose resource a
	// client is expected to rewrite whole — a URL map's rules, a health
	// check's probe — and the difference from patch is exactly that a member
	// the client left out is gone afterwards.
	if res.update {
		srv.HandleFunc("PUT "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			name := sim.PathParam(r, "name")
			key := relPath(r, name)
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			ok := res.store.Update(key, func(m *map[string]any) {
				cur := *m
				replaced := map[string]any{}
				for _, identity := range []string{"kind", "id", "selfLink", "creationTimestamp", "name", "region", "zone"} {
					if v, held := cur[identity]; held {
						replaced[identity] = v
					}
				}
				for k, v := range body {
					switch k {
					case "kind", "id", "selfLink", "creationTimestamp", "name":
						continue
					}
					replaced[k] = v
				}
				*m = replaced
			})
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", res.collection, name)
				return
			}
			if res.reconcile != nil {
				res.reconcile(key)
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(res.scope, r), computeSelfLink(key), "update"))
		})
	}

	// setLabels
	if res.setLabels {
		srv.HandleFunc("POST "+base+"/{name}/setLabels", func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			key := relPath(r, sim.PathParam(r, "name"))
			var req struct {
				Labels           map[string]string `json:"labels"`
				LabelFingerprint string            `json:"labelFingerprint"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			ok := res.store.Update(key, func(m *map[string]any) {
				cur := *m
				labels := map[string]any{}
				for k, v := range req.Labels {
					labels[k] = v
				}
				cur["labels"] = labels
				cur["labelFingerprint"] = computeFingerprint()
			})
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", res.collection, sim.PathParam(r, "name"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(res.scope, r), computeSelfLink(key), "setLabels"))
		})
	}

	// Aggregated list — items keyed by scope ("zones/x" / "regions/x" /
	// "global"), each value a scoped list under the collection member.
	if res.aggregated {
		srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/"+res.collection, func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			prefix := computeSelfLink(fmt.Sprintf("projects/%s/", project))
			all := res.store.Filter(func(m map[string]any) bool {
				sl, _ := m["selfLink"].(string)
				return strings.HasPrefix(sl, prefix)
			})
			grouped := map[string]map[string]any{}
			for _, m := range all {
				sl, _ := m["selfLink"].(string)
				rest := strings.TrimPrefix(sl, prefix)
				key := computeScopeKeyFromRest(rest)
				entry, ok := grouped[key]
				if !ok {
					entry = map[string]any{res.collection: []map[string]any{}}
					grouped[key] = entry
				}
				list, _ := entry[res.collection].([]map[string]any)
				entry[res.collection] = append(list, m)
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind":  res.kind + "AggregatedList",
				"items": grouped,
			})
		})
	}
}

// computeScopeKeyFromRest derives the aggregated-list scope key from the
// selfLink remainder after the "projects/{project}/" prefix:
// "zones/us-central1-a/disks/d" -> "zones/us-central1-a";
// "global/images/i" -> "global".
func computeScopeKeyFromRest(rest string) string {
	parts := strings.Split(rest, "/")
	if len(parts) == 0 {
		return "global"
	}
	switch parts[0] {
	case "zones", "regions":
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return "global"
}

func registerComputeMore(srv *sim.Server) {
	mk := func(table string) sim.Store[map[string]any] {
		return sim.MakeStore[map[string]any](srv.DB(), table)
	}

	gcpComputeImages = mk("compute_images")
	// A packet mirroring policy's collectorIlb resolves through the regional
	// backend services to the instances behind it, so the resolver reads them.
	gcpRegionBackendServices = mk("compute_region_backend_services")

	// Shared so the verbs in the files beside this one write the same records
	// the lifecycle here serves.
	gcpComputeTargetPools = mk("compute_target_pools")
	gcpComputeSnapshots = mk("compute_snapshots")
	gcpComputeRegionDisks = mk("compute_region_disks")

	resources := []computeMetaResource{
		// Storage resources.
		{collection: "images", kind: "compute#image", scope: cScopeGlobal, store: gcpComputeImages, skipGet: true, patch: true, setLabels: true},
		{collection: "snapshots", kind: "compute#snapshot", scope: cScopeGlobal, store: gcpComputeSnapshots, setLabels: true},
		{collection: "machineImages", kind: "compute#machineImage", scope: cScopeGlobal, store: mk("compute_machine_images"), setLabels: true},
		{collection: "disks", kind: "compute#disk", scope: cScopeRegion, store: gcpComputeRegionDisks, patch: true, setLabels: true},
		// Addressing / routing.
		{collection: "addresses", kind: "compute#address", scope: cScopeGlobal, store: mk("compute_global_addresses"), setLabels: true},
		{collection: "routes", kind: "compute#route", scope: cScopeGlobal, store: mk("compute_routes")},
		// Load-balancing resources.
		{collection: "targetPools", kind: "compute#targetPool", scope: cScopeRegion, store: gcpComputeTargetPools, aggregated: true},
		{collection: "backendServices", kind: "compute#backendService", scope: cScopeRegion, store: gcpRegionBackendServices, patch: true, listUsableKind: "compute#usableBackendServiceList"},
		{collection: "healthChecks", kind: "compute#healthCheck", scope: cScopeRegion, store: mk("compute_region_health_checks"), patch: true, update: true},
		{collection: "httpHealthChecks", kind: "compute#httpHealthCheck", scope: cScopeGlobal, store: mk("compute_http_health_checks"), patch: true},
		{collection: "httpsHealthChecks", kind: "compute#httpsHealthCheck", scope: cScopeGlobal, store: mk("compute_https_health_checks"), patch: true},
		{collection: "urlMaps", kind: "compute#urlMap", scope: cScopeRegion, store: mk("compute_region_url_maps"), patch: true, update: true},
		{collection: "targetHttpProxies", kind: "compute#targetHttpProxy", scope: cScopeRegion, store: mk("compute_region_target_http_proxies")},
		{collection: "targetHttpsProxies", kind: "compute#targetHttpsProxy", scope: cScopeGlobal, store: mk("compute_target_https_proxies"), aggregated: true},
		{collection: "sslCertificates", kind: "compute#sslCertificate", scope: cScopeGlobal, store: mk("compute_ssl_certificates"), aggregated: true},
		{collection: "targetTcpProxies", kind: "compute#targetTcpProxy", scope: cScopeGlobal, store: mk("compute_target_tcp_proxies")},
		{collection: "targetGrpcProxies", kind: "compute#targetGrpcProxy", scope: cScopeGlobal, store: mk("compute_target_grpc_proxies"), patch: true},
		// Instance templates (regional).
		{collection: "instanceTemplates", kind: "compute#instanceTemplate", scope: cScopeRegion, store: mk("compute_region_instance_templates")},
	}
	for _, res := range resources {
		res.register(srv)
	}

	registerComputeInstanceGroupManagers(srv)
	registerComputeCatalogMore(srv)
	registerComputeOperationsMore(srv)
	registerComputeAggregatedExisting(srv)
	registerComputeInstanceActions(srv)
}

// registerComputeInstanceGroupManagers wires the zonal + regional managed
// instance group surface. A MIG is metadata: name, instanceTemplate,
// targetSize, baseInstanceName. resize updates targetSize;
// setInstanceTemplate swaps the template; listManagedInstances reports the
// instances the group actually manages, which the verbs in
// compute_igm_instances.go create, move between states and remove.
func registerComputeInstanceGroupManagers(srv *sim.Server) {
	store := sim.MakeStore[map[string]any](srv.DB(), "compute_instance_group_managers")
	// A regional instance group is the group a regional manager keeps, so it
	// is derived from this same store rather than held in one of its own.
	registerComputeRegionInstanceGroups(srv, store)

	for _, scope := range []computeScopeKind{cScopeZone, cScopeRegion} {
		scope := scope
		base := computeScopeMux(scope, "instanceGroupManagers")
		// The instances the group manages follow its target size, on every
		// path that can change it — insert, patch, delete, and the resize
		// below.
		reconcile := func(key string) { computeReconcileManagedInstances(store, key) }
		(computeMetaResource{collection: "instanceGroupManagers", kind: "compute#instanceGroupManager", scope: scope, store: store, patch: true, aggregated: scope == cScopeZone, reconcile: reconcile}).register(srv)

		relPath := func(r *http.Request) string {
			return fmt.Sprintf("projects/%s/%s/instanceGroupManagers/%s", sim.PathParam(r, "project"), computeScopeSegment(scope, r), sim.PathParam(r, "name"))
		}

		srv.HandleFunc("POST "+base+"/{name}/resize", func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			key := relPath(r)
			size := r.URL.Query().Get("size")
			ok := store.Update(key, func(m *map[string]any) {
				if size != "" {
					if n, err := parseComputeInt(size); err == nil {
						(*m)["targetSize"] = n
					}
				}
			})
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instanceGroupManager %q not found", sim.PathParam(r, "name"))
				return
			}
			reconcile(key)
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(scope, r), computeSelfLink(key), "resize"))
		})

		srv.HandleFunc("POST "+base+"/{name}/setInstanceTemplate", func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			key := relPath(r)
			var req struct {
				InstanceTemplate string `json:"instanceTemplate"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			ok := store.Update(key, func(m *map[string]any) {
				if req.InstanceTemplate != "" {
					(*m)["instanceTemplate"] = req.InstanceTemplate
				}
			})
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instanceGroupManager %q not found", sim.PathParam(r, "name"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, computeScopeSegment(scope, r), computeSelfLink(key), "setInstanceTemplate"))
		})

		srv.HandleFunc("POST "+base+"/{name}/listManagedInstances", func(w http.ResponseWriter, r *http.Request) {
			key := relPath(r)
			if _, ok := store.Get(key); !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instanceGroupManager %q not found", sim.PathParam(r, "name"))
				return
			}
			computeListManagedInstances(w, key)
		})
	}

	registerComputeInstanceGroupInstances(srv, store)
}

// registerComputeCatalogMore adds the read-only catalog collections the
// SDK/gcloud probe before creating resources: regions, acceleratorTypes,
// and the diskTypes/machineTypes list + aggregated variants the existing
// catalog handler doesn't cover.
func registerComputeCatalogMore(srv *sim.Server) {
	regionsList := []string{"us-central1", "us-east1", "europe-west1"}
	regionJSON := func(project, region string) map[string]any {
		return map[string]any{
			"kind":     "compute#region",
			"id":       computeNumericID(),
			"name":     region,
			"status":   "UP",
			"selfLink": computeSelfLink(fmt.Sprintf("projects/%s/regions/%s", project, region)),
		}
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, regionJSON(sim.PathParam(r, "project"), sim.PathParam(r, "region")))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		items := make([]map[string]any, 0, len(regionsList))
		for _, region := range regionsList {
			items = append(items, regionJSON(project, region))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#regionList", "items": items})
	})

	accelJSON := func(project, zone, name string) map[string]any {
		return map[string]any{
			"kind":                    "compute#acceleratorType",
			"id":                      computeNumericID(),
			"name":                    name,
			"description":             "NVIDIA " + name,
			"zone":                    computeSelfLink(fmt.Sprintf("projects/%s/zones/%s", project, zone)),
			"selfLink":                computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/acceleratorTypes/%s", project, zone, name)),
			"maximumCardsPerInstance": 8,
			"resourceMetadata":        computeResourceMetadata("compute#acceleratorType"),
		}
	}
	accelNames := []string{"nvidia-tesla-t4", "nvidia-tesla-v100"}
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes/{acceleratorType}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, accelJSON(sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "acceleratorType")))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/acceleratorTypes", func(w http.ResponseWriter, r *http.Request) {
		project, zone := sim.PathParam(r, "project"), sim.PathParam(r, "zone")
		items := make([]map[string]any, 0, len(accelNames))
		for _, name := range accelNames {
			items = append(items, accelJSON(project, zone, name))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#acceleratorTypeList", "items": items})
	})

	zones := []string{"us-central1-a", "us-central1-b", "europe-west1-b"}
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/acceleratorTypes", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		items := map[string]map[string]any{}
		for _, zone := range zones {
			list := make([]map[string]any, 0, len(accelNames))
			for _, name := range accelNames {
				list = append(list, accelJSON(project, zone, name))
			}
			items["zones/"+zone] = map[string]any{"acceleratorTypes": list}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#acceleratorTypeAggregatedList", "items": items})
	})

	diskTypeJSON := func(project, zone, name string) map[string]any {
		return map[string]any{
			"kind":              "compute#diskType",
			"id":                computeNumericID(),
			"name":              name,
			"zone":              computeSelfLink(fmt.Sprintf("projects/%s/zones/%s", project, zone)),
			"selfLink":          computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/diskTypes/%s", project, zone, name)),
			"defaultDiskSizeGb": "100",
		}
	}
	diskTypeNames := []string{"pd-standard", "pd-balanced", "pd-ssd"}
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/diskTypes", func(w http.ResponseWriter, r *http.Request) {
		project, zone := sim.PathParam(r, "project"), sim.PathParam(r, "zone")
		items := make([]map[string]any, 0, len(diskTypeNames))
		for _, name := range diskTypeNames {
			items = append(items, diskTypeJSON(project, zone, name))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#diskTypeList", "items": items})
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/diskTypes", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		items := map[string]map[string]any{}
		for _, zone := range zones {
			list := make([]map[string]any, 0, len(diskTypeNames))
			for _, name := range diskTypeNames {
				list = append(list, diskTypeJSON(project, zone, name))
			}
			items["zones/"+zone] = map[string]any{"diskTypes": list}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#diskTypeAggregatedList", "items": items})
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/machineTypes", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		names := []string{"e2-micro", "e2-small", "n1-standard-1"}
		items := map[string]map[string]any{}
		for _, zone := range zones {
			list := make([]map[string]any, 0, len(names))
			for _, name := range names {
				list = append(list, map[string]any{
					"kind":      "compute#machineType",
					"id":        computeNumericID(),
					"name":      name,
					"guestCpus": 2,
					"memoryMb":  1024,
					"zone":      computeSelfLink(fmt.Sprintf("projects/%s/zones/%s", project, zone)),
					"selfLink":  computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", project, zone, name)),
				})
			}
			items["zones/"+zone] = map[string]any{"machineTypes": list}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#machineTypeAggregatedList", "items": items})
	})
}

// registerComputeOperationsMore adds the operations list + delete + the
// global wait/aggregated variants the existing per-scope GET handlers
// don't cover. Every operation the sim hands out is recorded with its scope
// and state, so a list reports the operations that scope actually holds and
// delete acknowledges with the 204 real GCP returns.
func registerComputeOperationsMore(srv *sim.Server) {
	listScope := func(w http.ResponseWriter, project, scope string) {
		items := []map[string]any{}
		for _, rec := range computeOpRegistry.List() {
			if rec.Project == project && rec.Scope == scope {
				items = append(items, computeOpJSON(rec))
			}
		}
		sort.Slice(items, func(i, j int) bool {
			return fmt.Sprint(items[i]["name"]) < fmt.Sprint(items[j]["name"])
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#operationList", "items": items})
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/operations", func(w http.ResponseWriter, r *http.Request) {
		listScope(w, sim.PathParam(r, "project"), "zones/"+sim.PathParam(r, "zone"))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/operations", func(w http.ResponseWriter, r *http.Request) {
		listScope(w, sim.PathParam(r, "project"), "regions/"+sim.PathParam(r, "region"))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/operations", func(w http.ResponseWriter, r *http.Request) {
		listScope(w, sim.PathParam(r, "project"), "global")
	})

	delOp := func(w http.ResponseWriter, r *http.Request) {
		if !computeOpKnown(sim.PathParam(r, "name")) {
			sim.GCPErrorf(w, http.StatusNotFound, "notFound", "operation %q not found", sim.PathParam(r, "name"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/zones/{zone}/operations/{name}", delOp)
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/operations/{name}", delOp)
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/operations/{name}", delOp)

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/operations/{name}/wait", func(w http.ResponseWriter, r *http.Request) {
		computeWaitOperation(w, r, sim.PathParam(r, "name"))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/operations", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		grouped := map[string]map[string]any{}
		for _, rec := range computeOpRegistry.List() {
			if rec.Project != project {
				continue
			}
			entry, ok := grouped[rec.Scope]
			if !ok {
				entry = map[string]any{"operations": []map[string]any{}}
				grouped[rec.Scope] = entry
			}
			list, _ := entry["operations"].([]map[string]any)
			entry["operations"] = append(list, computeOpJSON(rec))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#operationAggregatedList", "items": grouped})
	})
}

// registerComputeAggregatedExisting adds aggregated-list endpoints for the
// resources whose per-scope CRUD already lives in compute.go /
// compute_loadbalancing.go (their stores are package globals).
func registerComputeAggregatedExisting(srv *sim.Server) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/subnetworks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/regions/", project)
		grouped := map[string]map[string]any{}
		if gcpSubnetworks != nil {
			for _, s := range gcpSubnetworks.Filter(func(s ComputeSubnetwork) bool { return strings.HasPrefix(s.SelfLink, prefix) }) {
				key := computeScopeKeyFromRest(strings.TrimPrefix(s.SelfLink, fmt.Sprintf("projects/%s/", project)))
				entry, ok := grouped[key]
				if !ok {
					entry = map[string]any{"subnetworks": []ComputeSubnetwork{}}
					grouped[key] = entry
				}
				list, _ := entry["subnetworks"].([]ComputeSubnetwork)
				entry["subnetworks"] = append(list, s)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#subnetworkAggregatedList", "items": grouped})
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/addresses", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/regions/", project)
		grouped := map[string]map[string]any{}
		if gcpAddresses != nil {
			for _, a := range gcpAddresses.Filter(func(a ComputeAddress) bool { return strings.HasPrefix(a.SelfLink, prefix) }) {
				key := computeScopeKeyFromRest(strings.TrimPrefix(a.SelfLink, fmt.Sprintf("projects/%s/", project)))
				entry, ok := grouped[key]
				if !ok {
					entry = map[string]any{"addresses": []ComputeAddress{}}
					grouped[key] = entry
				}
				list, _ := entry["addresses"].([]ComputeAddress)
				entry["addresses"] = append(list, a)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#addressAggregatedList", "items": grouped})
	})

	globalGroup := func(w http.ResponseWriter, collection, kind string, items any) {
		grouped := map[string]map[string]any{"global": {collection: items}}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": kind, "items": grouped})
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/backendServices", func(w http.ResponseWriter, r *http.Request) {
		globalGroup(w, "backendServices", "compute#backendServiceAggregatedList", gcpBackendServices.List())
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/healthChecks", func(w http.ResponseWriter, r *http.Request) {
		globalGroup(w, "healthChecks", "compute#healthChecksAggregatedList", gcpHealthChecks.List())
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/urlMaps", func(w http.ResponseWriter, r *http.Request) {
		globalGroup(w, "urlMaps", "compute#urlMapsAggregatedList", gcpURLMaps.List())
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/targetHttpProxies", func(w http.ResponseWriter, r *http.Request) {
		globalGroup(w, "targetHttpProxies", "compute#targetHttpProxyAggregatedList", gcpTargetHTTPProxies.List())
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/forwardingRules", func(w http.ResponseWriter, r *http.Request) {
		globalGroup(w, "forwardingRules", "compute#forwardingRuleAggregatedList", gcpForwardingRules.List())
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/instanceGroups", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/zones/", project)
		grouped := map[string]map[string]any{}
		if gcpInstanceGroups != nil {
			for _, g := range gcpInstanceGroups.Filter(func(g storedComputeInstanceGroup) bool { return strings.HasPrefix(g.SelfLink, prefix) }) {
				key := computeScopeKeyFromRest(strings.TrimPrefix(g.SelfLink, fmt.Sprintf("projects/%s/", project)))
				entry, ok := grouped[key]
				if !ok {
					entry = map[string]any{"instanceGroups": []ComputeInstanceGroup{}}
					grouped[key] = entry
				}
				list, _ := entry["instanceGroups"].([]ComputeInstanceGroup)
				entry["instanceGroups"] = append(list, wireInstanceGroup(g))
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#instanceGroupAggregatedList", "items": grouped})
	})
}

// registerComputeInstanceActions adds the instance lifecycle + mutation
// methods the existing instances handler (registerComputeInstances)
// doesn't cover: reset, setMachineType, setMetadata, attachDisk,
// detachDisk, and getSerialPortOutput. All mutate the package-global
// gcpInstances store and return a zonal Operation.
func registerComputeInstanceActions(srv *sim.Server) {
	instSelfLink := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/zones/%s/instances/%s", sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "name"))
	}
	zoneOp := func(w http.ResponseWriter, r *http.Request, opType string) {
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(sim.PathParam(r, "project"), sim.PathParam(r, "zone"), computeSelfLink(instSelfLink(r)), opType))
	}
	notFound := func(w http.ResponseWriter, r *http.Request) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", sim.PathParam(r, "name"))
	}
	exists := func(r *http.Request) bool {
		if gcpInstances == nil {
			return false
		}
		_, ok := gcpInstances.Get(instSelfLink(r))
		return ok
	}

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/reset", func(w http.ResponseWriter, r *http.Request) {
		if !exists(r) {
			notFound(w, r)
			return
		}
		zoneOp(w, r, "reset")
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMachineType", func(w http.ResponseWriter, r *http.Request) {
		if !exists(r) {
			notFound(w, r)
			return
		}
		var req struct {
			MachineType string `json:"machineType"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		gcpInstances.Update(instSelfLink(r), func(in *ComputeInstance) {
			if req.MachineType != "" {
				in.MachineType = req.MachineType
			}
		})
		zoneOp(w, r, "setMachineType")
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setMetadata", func(w http.ResponseWriter, r *http.Request) {
		if !exists(r) {
			notFound(w, r)
			return
		}
		var req ComputeInstanceMetadata
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		gcpInstances.Update(instSelfLink(r), func(in *ComputeInstance) {
			md := req
			md.Kind = "compute#metadata"
			md.Fingerprint = computeFingerprint()
			in.Metadata = &md
		})
		zoneOp(w, r, "setMetadata")
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/attachDisk", func(w http.ResponseWriter, r *http.Request) {
		if !exists(r) {
			notFound(w, r)
			return
		}
		var disk ComputeInstanceDisk
		if err := sim.ReadJSON(r, &disk); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		gcpInstances.Update(instSelfLink(r), func(in *ComputeInstance) {
			disk.Kind = "compute#attachedDisk"
			disk.Index = int64(len(in.Disks))
			in.Disks = append(append([]ComputeInstanceDisk{}, in.Disks...), disk)
		})
		zoneOp(w, r, "attachDisk")
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/detachDisk", func(w http.ResponseWriter, r *http.Request) {
		if !exists(r) {
			notFound(w, r)
			return
		}
		deviceName := r.URL.Query().Get("deviceName")
		gcpInstances.Update(instSelfLink(r), func(in *ComputeInstance) {
			var kept []ComputeInstanceDisk
			for _, d := range in.Disks {
				if d.DeviceName != deviceName {
					kept = append(kept, d)
				}
			}
			in.Disks = kept
		})
		zoneOp(w, r, "detachDisk")
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}/serialPort", func(w http.ResponseWriter, r *http.Request) {
		if !exists(r) {
			notFound(w, r)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":     "compute#serialPortOutput",
			"contents": "",
			"start":    "0",
			"next":     "0",
			"selfLink": computeSelfLink(instSelfLink(r) + "/serialPort"),
		})
	})
}

func parseComputeInt(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
