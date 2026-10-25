package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// iamLROs holds the long-running operations the IAM admin surface returns for
// its mutating verbs (pool/provider/oauth-client create/patch/delete/undelete).
// Each operation's GET endpoint lives under the resource path that produced it
// (e.g. .../workloadIdentityPools/{p}/operations/{op}), so the IAM operations
// are kept in their own store keyed by the full operation resource name.
var iamLROs sim.Store[Operation]

// iamResources is the generic store for every IAM admin resource the sim
// models as a JSON map (workload identity pools + their providers / namespaces
// / managed identities / keys, workforce pools + their providers / subjects /
// scim tenants / tokens / keys, and OAuth clients + credentials). Each row is
// the resource's wire JSON keyed by its fully-qualified resource name; soft-
// deleted resources keep their row (state=DELETED) so undelete can revive them.
var iamResources sim.Store[map[string]any]

// newIAMLRO settles a long-running operation synchronously (done=true) with the
// resource embedded as the protobuf Any response, stores it under
// {opCollection}/operations/{id}, and returns it. opCollection is the resource
// path the operation hangs off (e.g. the pool name), matching real GCP where a
// pool operation's name is .../workloadIdentityPools/{p}/operations/{id}.
func newIAMLRO(r *http.Request, opCollection string, resource map[string]any, typeName string) Operation {
	if iamLROs == nil {
		// The operations store is created in registerIAM; a nil here means a
		// handler ran before registration, which never happens at runtime.
		panic("newIAMLRO: iamLROs store not initialized")
	}
	opID := sim.NewUUID()
	response := map[string]any{"@type": typeName}
	for k, v := range resource {
		response[k] = v
	}
	op := Operation{
		Name:     opCollection + "/operations/" + opID,
		Metadata: iamOperationMetadata(r, opCollection, typeName),
		Done:     true,
		Response: response,
	}
	iamLROs.Put(op.Name, op)
	return op
}

// iamOperationMetadata is the metadata message of an IAM operation. The iam v1
// document declares two: WorkloadIdentityPoolOperationMetadata, which the pool
// methods carry as the google.iam.v1beta protos' operation_info declares and
// which holds no fields, and the standard OperationMetadata for every other
// method. Both live in google.iam.v1, the package of every response type.
func iamOperationMetadata(r *http.Request, target, typeName string) map[string]any {
	if typeName == "type.googleapis.com/google.iam.v1.WorkloadIdentityPool" {
		return map[string]any{"@type": "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolOperationMetadata"}
	}
	now := nowTimestamp()
	return map[string]any{
		"@type":      "type.googleapis.com/google.iam.v1.OperationMetadata",
		"createTime": now,
		"endTime":    now,
		"target":     target,
		"verb":       gcpOperationVerb(r),
	}
}

// iamApplyMask copies the masked fields from src into dst. An empty mask copies
// every field present in src (real GCP treats a missing updateMask on these
// resources as a full update of the provided fields).
func iamApplyMask(dst, src map[string]any, mask string) {
	if strings.TrimSpace(mask) == "" {
		for k, v := range src {
			if k == "name" || k == "state" {
				continue
			}
			dst[k] = v
		}
		return
	}
	for _, f := range strings.Split(mask, ",") {
		f = strings.TrimSpace(f)
		if f == "" || f == "name" || f == "state" {
			continue
		}
		if v, ok := src[f]; ok {
			dst[f] = v
		} else {
			delete(dst, f)
		}
	}
}

// iamGenericCRUD wires the standard list/get/create/patch/delete/undelete +
// per-resource operations.get surface for one IAM admin collection. It is the
// shared engine behind workload-identity pools (and their nested providers /
// namespaces / managed identities / keys) and workforce pools (and their
// providers / subjects / scim tenants / tokens / keys).
//
//   - parentPat captures the collection's parent in the {parent...} param, so a
//     POST/GET-list resolves the parent name; createParam is the query
//     parameter carrying the new resource ID (e.g. workloadIdentityPoolId).
//   - childField is the list-response array field name (e.g.
//     "workloadIdentityPools").
//   - resType is the protobuf Any @type for this resource's LRO response.
//   - mutatingLRO controls whether create/patch/delete return an Operation
//     (true for pools/providers/identities) or the resource/Empty directly
//     (true for every IAM admin collection here; oauth clients differ and are
//     handled separately).
type iamCollection struct {
	srv         *sim.Server
	collPath    string // e.g. "/v1/projects/{project}/locations/{location}/workloadIdentityPools"
	parentName  func(r *http.Request) string
	childField  string
	createParam string
	resType     string
	stateField  bool // emit state=ACTIVE / DELETED
}

func (c iamCollection) name(r *http.Request, id string) string {
	return c.parentName(r) + "/" + lastCollSegment(c.collPath) + "/" + id
}

// lastCollSegment returns the trailing literal collection segment of a route
// pattern (e.g. "workloadIdentityPools" from ".../workloadIdentityPools").
func lastCollSegment(pat string) string {
	seg := pat[strings.LastIndex(pat, "/")+1:]
	return seg
}

func (c iamCollection) register() {
	// List
	c.srv.HandleFunc("GET "+c.collPath, func(w http.ResponseWriter, r *http.Request) {
		prefix := c.parentName(r) + "/" + lastCollSegment(c.collPath) + "/"
		showDeleted := r.URL.Query().Get("showDeleted") == "true"
		rows := iamResources.Filter(func(m map[string]any) bool {
			name, _ := m["name"].(string)
			if !strings.HasPrefix(name, prefix) {
				return false
			}
			// only direct children (no extra "/" after the id)
			if strings.Contains(strings.TrimPrefix(name, prefix), "/") {
				return false
			}
			if !showDeleted && m["state"] == "DELETED" {
				return false
			}
			return true
		})
		sort.Slice(rows, func(i, j int) bool {
			ni, _ := rows[i]["name"].(string)
			nj, _ := rows[j]["name"].(string)
			return ni < nj
		})
		out := make([]map[string]any, 0, len(rows))
		out = append(out, rows...)
		sim.WriteJSON(w, http.StatusOK, map[string]any{c.childField: out})
	})

	// Get / listAttestationRules (colon-verb fan-in on the same path)
	c.srv.HandleFunc("GET "+c.collPath+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		if base, verb, found := strings.Cut(id, ":"); found {
			if verb == "listAttestationRules" {
				name := c.name(r, base)
				if _, ok := iamResources.Get(name); !ok {
					GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
					return
				}
				sim.WriteJSON(w, http.StatusOK, map[string]any{"attestationRules": []any{}})
				return
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported verb %q", verb)
			return
		}
		name := c.name(r, id)
		m, ok := iamResources.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, m)
	})

	// Create (returns an LRO) / colon-verb POSTs (undelete, attestation rules)
	c.srv.HandleFunc("POST "+c.collPath, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get(c.createParam)
		if id == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s is required", c.createParam)
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := c.name(r, id)
		if _, exists := iamResources.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "%s already exists", name)
			return
		}
		res := map[string]any{}
		for k, v := range body {
			res[k] = v
		}
		res["name"] = name
		if c.stateField {
			res["state"] = "ACTIVE"
		}
		iamResources.Put(name, res)
		op := newIAMLRO(r, name, res, c.resType)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Colon-verb POSTs on a specific resource: undelete (revives a soft-
	// deleted resource) and the attestation-rule mutators. All return an LRO.
	c.srv.HandleFunc("POST "+c.collPath+"/{idAction}", func(w http.ResponseWriter, r *http.Request) {
		idAction := sim.PathParam(r, "idAction")
		id, verb, found := strings.Cut(idAction, ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported request %q", idAction)
			return
		}
		name := c.name(r, id)
		switch verb {
		case "undelete":
			res, ok := iamResources.Get(name)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
				return
			}
			if c.stateField {
				res["state"] = "ACTIVE"
				iamResources.Put(name, res)
			}
			op := newIAMLRO(r, name, res, c.resType)
			sim.WriteJSON(w, http.StatusOK, op)
		case "addAttestationRule", "removeAttestationRule", "setAttestationRules":
			res, ok := iamResources.Get(name)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
				return
			}
			op := newIAMLRO(r, name, res, c.resType)
			sim.WriteJSON(w, http.StatusOK, op)
		case "getIamPolicy", "setIamPolicy", "testIamPermissions":
			// The pool/provider is itself an IAM resource; reuse the shared
			// resource-IAM store so its policy round-trips like any other.
			handleResourceIAM(w, r, gcpResourcePolicies, name, verb)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported verb %q", verb)
		}
	})

	// Patch (returns an LRO)
	c.srv.HandleFunc("PATCH "+c.collPath+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		name := c.name(r, sim.PathParam(r, "id"))
		res, ok := iamResources.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		iamApplyMask(res, body, r.URL.Query().Get("updateMask"))
		iamResources.Put(name, res)
		op := newIAMLRO(r, name, res, c.resType)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Delete — soft-delete (state=DELETED) so undelete can revive it. Returns
	// an LRO whose embedded response is the deleted resource.
	c.srv.HandleFunc("DELETE "+c.collPath+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		name := c.name(r, sim.PathParam(r, "id"))
		res, ok := iamResources.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		if c.stateField {
			res["state"] = "DELETED"
			iamResources.Put(name, res)
		} else {
			iamResources.Delete(name)
		}
		op := newIAMLRO(r, name, res, c.resType)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	// Per-resource operations.get
	c.srv.HandleFunc("GET "+c.collPath+"/{id}/operations/{op}", func(w http.ResponseWriter, r *http.Request) {
		opName := c.name(r, sim.PathParam(r, "id")) + "/operations/" + sim.PathParam(r, "op")
		serveIAMOperation(w, opName)
	})
}

// serveIAMOperation returns a stored IAM LRO by name (or a settled, done=true
// operation if the name is well-formed but no record exists — real GCP keeps
// completed operations queryable; the sim settles synchronously so any
// operation it minted is in the store).
func serveIAMOperation(w http.ResponseWriter, opName string) {
	if op, ok := iamLROs.Get(opName); ok {
		sim.WriteJSON(w, http.StatusOK, op)
		return
	}
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", opName)
}

// registerWorkloadIdentityPools mounts the project-scoped Workload Identity
// Federation surface: pools, providers, namespaces, managed identities,
// provider keys, and the operations.get endpoints at every level.
func registerWorkloadIdentityPools(srv *sim.Server) {
	const base = "/v1/projects/{project}/locations/{location}"
	wipParent := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/locations/%s", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
	}

	// Pools.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools",
		parentName:  wipParent,
		childField:  "workloadIdentityPools",
		createParam: "workloadIdentityPoolId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPool",
		stateField:  true,
	}.register()

	// Providers.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools/{pool}/providers",
		parentName: func(r *http.Request) string {
			return wipParent(r) + "/workloadIdentityPools/" + sim.PathParam(r, "pool")
		},
		childField:  "workloadIdentityPoolProviders",
		createParam: "workloadIdentityPoolProviderId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolProvider",
		stateField:  true,
	}.register()

	// Provider keys.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools/{pool}/providers/{provider}/keys",
		parentName: func(r *http.Request) string {
			return wipParent(r) + "/workloadIdentityPools/" + sim.PathParam(r, "pool") + "/providers/" + sim.PathParam(r, "provider")
		},
		childField:  "workloadIdentityPoolProviderKeys",
		createParam: "workloadIdentityPoolProviderKeyId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolProviderKey",
		stateField:  true,
	}.register()

	// Namespaces.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools/{pool}/namespaces",
		parentName: func(r *http.Request) string {
			return wipParent(r) + "/workloadIdentityPools/" + sim.PathParam(r, "pool")
		},
		childField:  "workloadIdentityPoolNamespaces",
		createParam: "workloadIdentityPoolNamespaceId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolNamespace",
		stateField:  true,
	}.register()

	// Managed identities.
	iamCollection{
		srv: srv, collPath: base + "/workloadIdentityPools/{pool}/namespaces/{namespace}/managedIdentities",
		parentName: func(r *http.Request) string {
			return wipParent(r) + "/workloadIdentityPools/" + sim.PathParam(r, "pool") + "/namespaces/" + sim.PathParam(r, "namespace")
		},
		childField:  "workloadIdentityPoolManagedIdentities",
		createParam: "workloadIdentityPoolManagedIdentityId",
		resType:     "type.googleapis.com/google.iam.v1.WorkloadIdentityPoolManagedIdentity",
		stateField:  true,
	}.register()

	// Workload-source operations.get — the only operations endpoint the
	// generic collection doesn't already mint (workloadSources has no CRUD in
	// the doc, just a nested operations.get).
	srv.HandleFunc("GET "+base+"/workloadIdentityPools/{pool}/namespaces/{namespace}/managedIdentities/{mi}/workloadSources/{ws}/operations/{op}", func(w http.ResponseWriter, r *http.Request) {
		serveIAMOperation(w, fmt.Sprintf("projects/%s/locations/%s/workloadIdentityPools/%s/namespaces/%s/managedIdentities/%s/workloadSources/%s/operations/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "pool"), sim.PathParam(r, "namespace"), sim.PathParam(r, "mi"), sim.PathParam(r, "ws"), sim.PathParam(r, "op")))
	})

	// managedIdentities :listAttestationRules — colon-verb fan-in handled by
	// the generic Get handler already (the {id} param captures "id:verb").
}

// registerWorkforcePools mounts the location-scoped (organization-level)
// workforce-pool surface: pools, providers, subjects, scim tenants, scim
// tokens, provider keys, and operations.get at the relevant levels.
func registerWorkforcePools(srv *sim.Server) {
	const base = "/v1/locations/{location}"
	wfpParent := func(r *http.Request) string {
		return "locations/" + sim.PathParam(r, "location")
	}

	// Pools.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools",
		parentName:  wfpParent,
		childField:  "workforcePools",
		createParam: "workforcePoolId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePool",
		stateField:  true,
	}.register()

	// Providers.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools/{pool}/providers",
		parentName: func(r *http.Request) string {
			return wfpParent(r) + "/workforcePools/" + sim.PathParam(r, "pool")
		},
		childField:  "workforcePoolProviders",
		createParam: "workforcePoolProviderId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePoolProvider",
		stateField:  true,
	}.register()

	// Provider keys.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools/{pool}/providers/{provider}/keys",
		parentName: func(r *http.Request) string {
			return wfpParent(r) + "/workforcePools/" + sim.PathParam(r, "pool") + "/providers/" + sim.PathParam(r, "provider")
		},
		childField:  "workforcePoolProviderKeys",
		createParam: "workforcePoolProviderKeyId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePoolProviderKey",
		stateField:  true,
	}.register()

	// SCIM tenants.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools/{pool}/providers/{provider}/scimTenants",
		parentName: func(r *http.Request) string {
			return wfpParent(r) + "/workforcePools/" + sim.PathParam(r, "pool") + "/providers/" + sim.PathParam(r, "provider")
		},
		childField:  "workforcePoolProviderScimTenants",
		createParam: "workforcePoolProviderScimTenantId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePoolProviderScimTenant",
		stateField:  true,
	}.register()

	// SCIM tokens.
	iamCollection{
		srv: srv, collPath: base + "/workforcePools/{pool}/providers/{provider}/scimTenants/{tenant}/tokens",
		parentName: func(r *http.Request) string {
			return wfpParent(r) + "/workforcePools/" + sim.PathParam(r, "pool") + "/providers/" + sim.PathParam(r, "provider") + "/scimTenants/" + sim.PathParam(r, "tenant")
		},
		childField:  "workforcePoolProviderScimTokens",
		createParam: "workforcePoolProviderScimTokenId",
		resType:     "type.googleapis.com/google.iam.v1.WorkforcePoolProviderScimToken",
		stateField:  true,
	}.register()

	// Subjects — only delete (soft) + undelete + operations.get in the doc.
	// A subject is a federated identity, not an API-created resource, so its
	// store row materializes when it is deleted: delete records the DELETED
	// state, undelete flips the same row back, and a subject with no row was
	// never deleted.
	srv.HandleFunc("DELETE "+base+"/workforcePools/{pool}/subjects/{subject}", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("locations/%s/workforcePools/%s/subjects/%s",
			sim.PathParam(r, "location"), sim.PathParam(r, "pool"), sim.PathParam(r, "subject"))
		res := map[string]any{"name": name, "state": "DELETED"}
		iamResources.Put(name, res)
		op := newIAMLRO(r, name, res, "type.googleapis.com/google.iam.v1.WorkforcePoolSubject")
		sim.WriteJSON(w, http.StatusOK, op)
	})
	// subjects.undelete — the collection's one POST custom method. Go's mux
	// captures the "{subject}:undelete" segment whole; the verb resolves
	// before the subject, the way Google's frontend resolves a method before
	// the resource.
	srv.HandleFunc("POST "+base+"/workforcePools/{pool}/subjects/{subjectAction}", func(w http.ResponseWriter, r *http.Request) {
		subject, verb, found := gcpCustomMethod(sim.PathParam(r, "subjectAction"))
		if !found || (verb != "undelete" && verb != "revokeSessions") {
			gcpMethodNotFound(w)
			return
		}
		name := fmt.Sprintf("locations/%s/workforcePools/%s/subjects/%s",
			sim.PathParam(r, "location"), sim.PathParam(r, "pool"), subject)
		if verb == "revokeSessions" {
			var req struct{}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			iamSessionRevocations.Put(workforceSubjectPrincipal(name), time.Now().Unix())
			sim.WriteJSON(w, http.StatusOK, newIAMLRO(r, name, map[string]any{}, "type.googleapis.com/google.protobuf.Empty"))
			return
		}
		res, ok := iamResources.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		if res["state"] != "DELETED" {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION", "%s is not deleted", name)
			return
		}
		res["state"] = "ACTIVE"
		iamResources.Put(name, res)
		op := newIAMLRO(r, name, res, "type.googleapis.com/google.iam.v1.WorkforcePoolSubject")
		sim.WriteJSON(w, http.StatusOK, op)
	})
	srv.HandleFunc("GET "+base+"/workforcePools/{pool}/subjects/{subject}/operations/{op}", func(w http.ResponseWriter, r *http.Request) {
		serveIAMOperation(w, fmt.Sprintf("locations/%s/workforcePools/%s/subjects/%s/operations/%s",
			sim.PathParam(r, "location"), sim.PathParam(r, "pool"), sim.PathParam(r, "subject"), sim.PathParam(r, "op")))
	})
	// Pool-, provider-, and key-level operations.get are minted by the generic
	// collection registrations above (their per-resource operations.get).
}

// registerOAuthClients mounts the project-scoped OAuth-client surface. Unlike
// pools, oauthClients return the resource directly (not an Operation) on
// create/patch; delete returns the soft-deleted resource and credential delete
// returns Empty. Credentials carry a clientSecret only on the resource itself.
func registerOAuthClients(srv *sim.Server) {
	const base = "/v1/projects/{project}/locations/{location}/oauthClients"
	clientName := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/locations/%s/oauthClients/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "client"))
	}

	// List clients.
	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("projects/%s/locations/%s/oauthClients/", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
		rows := iamResources.Filter(func(m map[string]any) bool {
			name, _ := m["name"].(string)
			return strings.HasPrefix(name, prefix) && !strings.Contains(strings.TrimPrefix(name, prefix), "/")
		})
		sort.Slice(rows, func(i, j int) bool {
			ni, _ := rows[i]["name"].(string)
			nj, _ := rows[j]["name"].(string)
			return ni < nj
		})
		out := make([]map[string]any, 0, len(rows))
		out = append(out, rows...)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"oauthClients": out})
	})

	// Get client.
	srv.HandleFunc("GET "+base+"/{client}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := iamResources.Get(clientName(r))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", clientName(r))
			return
		}
		sim.WriteJSON(w, http.StatusOK, m)
	})

	// Create client — returns the resource directly.
	srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("oauthClientId")
		if id == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "oauthClientId is required")
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/oauthClients/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), id)
		if _, exists := iamResources.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "%s already exists", name)
			return
		}
		res := map[string]any{}
		for k, v := range body {
			res[k] = v
		}
		res["name"] = name
		res["clientId"] = id
		res["state"] = "ACTIVE"
		iamResources.Put(name, res)
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// Undelete client — POST .../oauthClients/{client}:undelete. Returns the
	// revived resource directly (oauthClients are not LRO-based).
	srv.HandleFunc("POST "+base+"/{clientAction}", func(w http.ResponseWriter, r *http.Request) {
		clientAction := sim.PathParam(r, "clientAction")
		id, verb, found := strings.Cut(clientAction, ":")
		if !found || verb != "undelete" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unsupported request %q", clientAction)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/oauthClients/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), id)
		res, ok := iamResources.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", name)
			return
		}
		res["state"] = "ACTIVE"
		iamResources.Put(name, res)
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// Patch client.
	srv.HandleFunc("PATCH "+base+"/{client}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := iamResources.Get(clientName(r))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", clientName(r))
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		iamApplyMask(res, body, r.URL.Query().Get("updateMask"))
		iamResources.Put(clientName(r), res)
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// Delete client — soft-delete, returns the resource.
	srv.HandleFunc("DELETE "+base+"/{client}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := iamResources.Get(clientName(r))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", clientName(r))
			return
		}
		res["state"] = "DELETED"
		iamResources.Put(clientName(r), res)
		sim.WriteJSON(w, http.StatusOK, res)
	})

	// Credentials — list / get / create / patch / delete (Empty).
	credName := func(r *http.Request) string {
		return clientName(r) + "/credentials/" + sim.PathParam(r, "cred")
	}
	srv.HandleFunc("GET "+base+"/{client}/credentials", func(w http.ResponseWriter, r *http.Request) {
		prefix := clientName(r) + "/credentials/"
		rows := iamResources.Filter(func(m map[string]any) bool {
			name, _ := m["name"].(string)
			return strings.HasPrefix(name, prefix)
		})
		sort.Slice(rows, func(i, j int) bool {
			ni, _ := rows[i]["name"].(string)
			nj, _ := rows[j]["name"].(string)
			return ni < nj
		})
		out := make([]map[string]any, 0, len(rows))
		out = append(out, rows...)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"oauthClientCredentials": out})
	})
	srv.HandleFunc("GET "+base+"/{client}/credentials/{cred}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := iamResources.Get(credName(r))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", credName(r))
			return
		}
		sim.WriteJSON(w, http.StatusOK, m)
	})
	srv.HandleFunc("POST "+base+"/{client}/credentials", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("oauthClientCredentialId")
		if id == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "oauthClientCredentialId is required")
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := clientName(r) + "/credentials/" + id
		if _, exists := iamResources.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "%s already exists", name)
			return
		}
		res := map[string]any{}
		for k, v := range body {
			res[k] = v
		}
		res["name"] = name
		res["clientSecret"] = "sim-secret-" + sim.NewUUID()
		iamResources.Put(name, res)
		sim.WriteJSON(w, http.StatusOK, res)
	})
	srv.HandleFunc("PATCH "+base+"/{client}/credentials/{cred}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := iamResources.Get(credName(r))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", credName(r))
			return
		}
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		iamApplyMask(res, body, r.URL.Query().Get("updateMask"))
		iamResources.Put(credName(r), res)
		sim.WriteJSON(w, http.StatusOK, res)
	})
	srv.HandleFunc("DELETE "+base+"/{client}/credentials/{cred}", func(w http.ResponseWriter, r *http.Request) {
		if !iamResources.Delete(credName(r)) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s not found", credName(r))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})
}
