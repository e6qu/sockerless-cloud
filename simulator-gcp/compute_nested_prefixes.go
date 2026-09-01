package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Two Compute Engine collections the meta-resource registrar cannot express:
// a wire group, which is nested inside the cross-site network that owns it,
// and a regional public delegated prefix, which carries announce and withdraw
// on top of the lifecycle.

// registerComputeWireGroups serves the wire groups inside a cross-site
// network. A wire group belongs to its network the way a subnetwork belongs to
// a VPC: deleting the network is not modelled as deleting the groups, but a
// group is only ever addressed through the network that owns it, so the store
// is keyed by both.
func registerComputeWireGroups(srv *sim.Server) {
	const base = "/compute/v1/projects/{project}/global/crossSiteNetworks/{crossSiteNetwork}/wireGroups"
	groups := sim.MakeStore[map[string]any](srv.DB(), "compute_wire_groups")

	prefix := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/global/crossSiteNetworks/%s/wireGroups/",
			sim.PathParam(r, "project"), sim.PathParam(r, "crossSiteNetwork"))
	}
	key := func(r *http.Request, name string) string { return prefix(r) + name }
	operation := func(r *http.Request, target, verb string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"), "global", target, verb)
	}
	load := func(w http.ResponseWriter, r *http.Request) (string, map[string]any, bool) {
		k := key(r, sim.PathParam(r, "wireGroup"))
		group, ok := groups.Get(k)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"wire group %q not found", sim.PathParam(r, "wireGroup"))
			return "", nil, false
		}
		return k, group, true
	}

	srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var group map[string]any
		if err := sim.ReadJSON(r, &group); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name, _ := group["name"].(string)
		if name == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "a wire group needs a name")
			return
		}
		// The cross-site network has to exist: a wire group is addressed
		// through it, so one created under a network that is not there could
		// never be read back.
		network := fmt.Sprintf("projects/%s/global/crossSiteNetworks/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "crossSiteNetwork"))
		if _, ok := gcpComputeCrossSiteNetworks.Get(network); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"cross-site network %q not found", sim.PathParam(r, "crossSiteNetwork"))
			return
		}
		k := key(r, name)
		if _, exists := groups.Get(k); computeConflict(w, exists, "wire group", name) {
			return
		}
		group["kind"] = "compute#wireGroup"
		group["id"] = computeNumericID()
		group["selfLink"] = k
		group["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
		groups.Put(k, group)
		sim.WriteJSON(w, http.StatusOK, operation(r, k, "insert"))
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		want := prefix(r)
		items := groups.Filter(func(g map[string]any) bool {
			link, _ := g["selfLink"].(string)
			return strings.HasPrefix(link, want)
		})
		sort.Slice(items, func(i, j int) bool {
			a, _ := items[i]["name"].(string)
			b, _ := items[j]["name"].(string)
			return a < b
		})
		page, next, ok := paginateListCompute(w, r, items)
		if !ok {
			return
		}
		resp := map[string]any{"kind": "compute#wireGroupList", "items": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET "+base+"/{wireGroup}", func(w http.ResponseWriter, r *http.Request) {
		_, group, ok := load(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, group)
	})

	srv.HandleFunc("PATCH "+base+"/{wireGroup}", func(w http.ResponseWriter, r *http.Request) {
		k, group, ok := load(w, r)
		if !ok {
			return
		}
		var patch map[string]any
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		for field, value := range patch {
			switch field {
			case "name", "id", "kind", "selfLink", "creationTimestamp":
				// Identity is not patchable: a rename would strand the key.
			default:
				group[field] = value
			}
		}
		groups.Put(k, group)
		sim.WriteJSON(w, http.StatusOK, operation(r, k, "patch"))
	})

	srv.HandleFunc("DELETE "+base+"/{wireGroup}", func(w http.ResponseWriter, r *http.Request) {
		k, _, ok := load(w, r)
		if !ok {
			return
		}
		groups.Delete(k)
		sim.WriteJSON(w, http.StatusOK, operation(r, k, "delete"))
	})
}

// registerComputeRegionalPublicDelegatedPrefixes serves the regional public
// delegated prefixes and the two verbs that put one on the wire. A prefix is
// delegated before it is announced, so announce and withdraw move a status the
// read beside them reports rather than answering on their own.
func registerComputeRegionalPublicDelegatedPrefixes(srv *sim.Server) {
	const base = "/compute/v1/projects/{project}/regions/{region}/publicDelegatedPrefixes"
	prefixes := gcpComputeRegionalPublicDelegatedPrefixes

	load := func(w http.ResponseWriter, r *http.Request) (string, map[string]any, bool) {
		name := sim.PathParam(r, "publicDelegatedPrefix")
		k := computeScopedKey(r, cScopeRegion, "publicDelegatedPrefixes", name)
		prefix, ok := prefixes.Get(k)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"public delegated prefix %q not found", name)
			return "", nil, false
		}
		return k, prefix, true
	}

	// announce puts a delegated prefix on the wire and withdraw takes it off.
	// Announcing one that is already announced is refused, as is withdrawing
	// one that was never announced — a no-op would hide the mistake.
	for verb, want := range map[string]struct{ from, to, done string }{
		// A delegated prefix's states are ANNOUNCED and INITIALIZING; INITIAL
		// belongs to the advertised prefix, whose enum is a different one.
		"announce": {from: "INITIALIZING", to: "ANNOUNCED", done: "announced"},
		"withdraw": {from: "ANNOUNCED", to: "INITIALIZING", done: "withdrawn"},
	} {
		verb, want := verb, want
		srv.HandleFunc("POST "+base+"/{publicDelegatedPrefix}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			k, prefix, ok := load(w, r)
			if !ok {
				return
			}
			status, _ := prefix["status"].(string)
			if status == "" {
				status = "INITIALIZING"
			}
			if status != want.from {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"a prefix in %s cannot be %s", status, want.done)
				return
			}
			prefix["status"] = want.to
			prefixes.Put(k, prefix)
			sim.WriteJSON(w, http.StatusOK,
				newComputeOpWithType(sim.PathParam(r, "project"),
					"regions/"+sim.PathParam(r, "region"), k, verb))
		})
	}
}
