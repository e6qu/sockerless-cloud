package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Compute Engine verbs that hang off a resource rather than replacing it: the
// named sets a router matches routes against, the signed-URL keys a CDN backend
// signs with, the key a snapshot is re-encrypted under, and moving an address
// between scopes.

// registerComputeRouterNamedSets serves the named sets a router carries. A
// named set is a list of prefixes or communities a route policy matches
// against, addressed by name inside the router that holds it — so it lives in
// the router rather than in a collection of its own, which is why the verbs are
// on the router.
func registerComputeRouterNamedSets(srv *sim.Server) {
	const base = "/compute/v1/projects/{project}/regions/{region}/routers"
	namedSets := sim.MakeStore[map[string]any](srv.DB(), "compute_router_named_sets")

	key := func(r *http.Request, name string) string {
		return fmt.Sprintf("projects/%s/regions/%s/routers/%s/namedSets/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "router"), name)
	}
	prefix := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/regions/%s/routers/%s/namedSets/",
			sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "router"))
	}
	operation := func(r *http.Request, verb string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"),
			"regions/"+sim.PathParam(r, "region"),
			fmt.Sprintf("projects/%s/regions/%s/routers/%s",
				sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "router")),
			verb)
	}
	// The set a request names, which every verb but the list takes as a query
	// parameter rather than a path segment.
	named := func(r *http.Request) string { return r.URL.Query().Get("namedSet") }

	srv.HandleFunc("GET "+base+"/{router}/getNamedSet", func(w http.ResponseWriter, r *http.Request) {
		name := named(r)
		if name == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "getNamedSet needs a namedSet")
			return
		}
		held, ok := namedSets.Get(key(r, name))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "named set %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"resource": held})
	})

	srv.HandleFunc("GET "+base+"/{router}/listNamedSets", func(w http.ResponseWriter, r *http.Request) {
		want := prefix(r)
		items := namedSets.Filter(func(m map[string]any) bool {
			link, _ := m["selfLink"].(string)
			return strings.HasPrefix(link, want)
		})
		sort.Slice(items, func(i, j int) bool {
			a, _ := items[i]["name"].(string)
			b, _ := items[j]["name"].(string)
			return a < b
		})
		result := []any{}
		for _, item := range items {
			result = append(result, item)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#routersListNamedSets", "result": result,
		})
	})

	// patch merges what the client sent; update replaces the set whole. Both
	// create it if it is not there, which is what makes a route policy
	// declarative.
	write := func(verb string, replace bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			name, _ := body["name"].(string)
			if name == "" {
				name = named(r)
			}
			if name == "" {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"%s needs the named set to write, by name", verb)
				return
			}
			k := key(r, name)
			held := map[string]any{}
			if !replace {
				if existing, ok := namedSets.Get(k); ok {
					held = existing
				}
			}
			for field, value := range body {
				held[field] = value
			}
			held["name"] = name
			held["selfLink"] = k
			namedSets.Put(k, held)
			sim.WriteJSON(w, http.StatusOK, operation(r, verb))
		}
	}
	srv.HandleFunc("POST "+base+"/{router}/patchNamedSet", write("patchNamedSet", false))
	srv.HandleFunc("POST "+base+"/{router}/updateNamedSet", write("updateNamedSet", true))

	srv.HandleFunc("POST "+base+"/{router}/deleteNamedSet", func(w http.ResponseWriter, r *http.Request) {
		name := named(r)
		if name == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "deleteNamedSet needs a namedSet")
			return
		}
		if !namedSets.Delete(key(r, name)) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "named set %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, operation(r, "deleteNamedSet"))
	})
}

// computeSignedURLKeys serves the keys a CDN backend signs its URLs with. The
// key material is write-only — Compute Engine reports the names a backend
// holds, never the values — so the resource carries the names and the values
// stay where only the signing side can reach them.
func computeSignedURLKeys(srv *sim.Server, collection string, store sim.Store[map[string]any]) {
	base := "/compute/v1/projects/{project}/global/" + collection
	singular := strings.TrimSuffix(collection, "s")

	operation := func(r *http.Request, verb string) map[string]any {
		project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
		return newComputeOpWithType(project, "global", computeGlobalLink(project, collection, name), verb)
	}
	names := func(m map[string]any) []string {
		held, _ := m["cdnPolicy"].(map[string]any)
		if held == nil {
			return nil
		}
		declared, _ := held["signedUrlKeyNames"].([]any)
		out := make([]string, 0, len(declared))
		for _, entry := range declared {
			if name, ok := entry.(string); ok {
				out = append(out, name)
			}
		}
		return out
	}
	setNames := func(m map[string]any, keys []string) {
		policy, _ := m["cdnPolicy"].(map[string]any)
		if policy == nil {
			policy = map[string]any{}
		}
		held := make([]any, 0, len(keys))
		for _, name := range keys {
			held = append(held, name)
		}
		policy["signedUrlKeyNames"] = held
		m["cdnPolicy"] = policy
	}

	srv.HandleFunc("POST "+base+"/{name}/addSignedUrlKey", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			KeyName  string `json:"keyName"`
			KeyValue string `json:"keyValue"`
		}
		if err := sim.ReadJSON(r, &req); err != nil || req.KeyName == "" || req.KeyValue == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"a signed-URL key needs a keyName and a keyValue")
			return
		}
		project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
		k := computeGlobalLink(project, collection, name)
		refused := false
		if !store.Update(k, func(m *map[string]any) {
			held := names(*m)
			for _, existing := range held {
				if existing == req.KeyName {
					refused = true
					return
				}
			}
			setNames(*m, append(held, req.KeyName))
		}) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", singular, name)
			return
		}
		if refused {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"the %s already has a signed-URL key named %q", singular, req.KeyName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, operation(r, "addSignedUrlKey"))
	})

	srv.HandleFunc("POST "+base+"/{name}/deleteSignedUrlKey", func(w http.ResponseWriter, r *http.Request) {
		wanted := r.URL.Query().Get("keyName")
		if wanted == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"deleteSignedUrlKey needs the keyName to remove")
			return
		}
		project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
		k := computeGlobalLink(project, collection, name)
		found := false
		if !store.Update(k, func(m *map[string]any) {
			kept := []string{}
			for _, existing := range names(*m) {
				if existing == wanted {
					found = true
					continue
				}
				kept = append(kept, existing)
			}
			setNames(*m, kept)
		}) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", singular, name)
			return
		}
		if !found {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"the %s has no signed-URL key named %q", singular, wanted)
			return
		}
		sim.WriteJSON(w, http.StatusOK, operation(r, "deleteSignedUrlKey"))
	})
}

// registerComputeOrganizationOperations serves the operations an
// organization-scoped call produces. They are addressed without a project,
// because the resource they act on has none, and they share the operation store
// every other Compute operation is recorded in.
func registerComputeOrganizationOperations(srv *sim.Server) {
	const base = "/compute/v1/locations/global/operations"

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, _ *http.Request) {
		items := []any{}
		for _, rec := range computeOpRegistry.List() {
			if rec.Scope != "locations/global" {
				continue
			}
			items = append(items, computeOpJSON(rec))
		}
		sort.Slice(items, func(i, j int) bool {
			a, _ := items[i].(map[string]any)["name"].(string)
			b, _ := items[j].(map[string]any)["name"].(string)
			return a < b
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#operationList", "items": items,
		})
	})
	srv.HandleFunc("GET "+base+"/{operation}", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := computeOpRegistry.Get(sim.PathParam(r, "operation"))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"operation %q not found", sim.PathParam(r, "operation"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeOpJSON(rec))
	})
	srv.HandleFunc("DELETE "+base+"/{operation}", func(w http.ResponseWriter, r *http.Request) {
		if !computeOpRegistry.Delete(sim.PathParam(r, "operation")) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"operation %q not found", sim.PathParam(r, "operation"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
