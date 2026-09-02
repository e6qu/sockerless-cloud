package main

import (
	"net/http"
	"regexp"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The hosts a reservation's capacity is held on.
//
// A host is one machine of the capacity, so the hosts of an association are
// derived from the count that association holds — the same way a reservation's
// blocks are derived from the reservation, its sub-blocks from the block, and
// its slots from the sub-block. Deriving keeps them from disagreeing after a
// resize: a reservation resized to forty machines reports forty hosts across
// three blocks without anything being written twice.
//
// Compute Engine addresses a host through the association it belongs to, and
// the document spells that association three ways — a reservation, one of its
// blocks, or one of that block's sub-blocks. It is one Discovery parameter
// holding a whole path, declared without reserved expansion, so a generated
// client percent-encodes the separators inside it and sends
// "reservations%2Fname%2FreservationBlocks%2Fblock" as a single segment.
//
// Go's router matches one segment at a time and cannot express a segment whose
// value is itself a path: a lone wildcard there is ambiguous against every
// other zone collection, because machineTypes/{machineType} occupies the same
// two segments and neither is more specific. Mounting the family on an invented
// reservations/{name}/hosts is not the answer either — that is a path the
// service does not describe, and no client would arrive on it.
//
// So the family takes the zone subtree as a multi-segment mount and routes the
// tail itself, the way the Cloud Spanner instance subtree does. A concrete
// pattern always beats a multi-segment wildcard in Go's router, so every zone
// collection the simulator serves keeps its own route, and a tail this handler
// does not own is answered as a method not found — which the coverage probe
// reads as unserved, so nothing else in the zone subtree counts as covered on
// account of this mount.

// computeReservationHosts names one host per machine the association holds.
func computeReservationHosts(parent string, count int) []map[string]any {
	if count <= 0 {
		return nil
	}
	hosts := make([]map[string]any, 0, count)
	for i := 1; i <= count; i++ {
		hosts = append(hosts, map[string]any{
			"kind": "compute#host",
			"name": parent + "-host-" + itoa(i),
			// A host of a reservation that exists is a machine held for it, and
			// the reservation reports its blocks READY, so the machine under
			// them is active.
			//
			// The status beside the state is left out: it carries the physical
			// topology of Google's own datacenter and the instances placed on
			// this particular machine, and the simulator has neither a
			// datacenter layout nor a placement of instances onto hosts.
			"state": "ACTIVE",
		})
	}
	return hosts
}

// The three method spellings, as Discovery declares them: the association is
// one parameter holding a path, so it is read whole and split afterwards.
var (
	computeHostsList = regexp.MustCompile(
		`\A/compute/v1/projects/([^/]+)/zones/([^/]+)/(.+)/hosts\z`)
	computeHostsGet = regexp.MustCompile(
		`\A/compute/v1/projects/([^/]+)/zones/([^/]+)/(.+)/hosts/([^/]+)\z`)
	computeHostsGetVersion = regexp.MustCompile(
		`\A/compute/v1/projects/([^/]+)/zones/([^/]+)/(.+)/hosts/([^/]+)/getVersion\z`)
)

// registerComputeReservationHosts mounts the zone subtree the host family is
// routed from. The tail is read here rather than by the mux.
func registerComputeReservationHosts(srv *sim.Server, reservations sim.Store[map[string]any]) {
	const base = "/compute/v1/projects/{project}/zones/{zone}"
	route := func(w http.ResponseWriter, r *http.Request) {
		// The separators inside the association arrive encoded, because
		// Discovery declares the parameter without reserved expansion.
		// Restoring them is what the real service does with a simply-expanded
		// multi-segment variable; the escaped path is read rather than the
		// decoded one so a name that genuinely contains an encoded slash — a
		// Cloud Storage object elsewhere in the simulator — is untouched by it.
		path := strings.ReplaceAll(r.URL.EscapedPath(), "%2F", "/")
		path = strings.ReplaceAll(path, "%2f", "/")

		switch r.Method {
		case http.MethodGet:
			if m := computeHostsGet.FindStringSubmatch(path); m != nil {
				computeHostGet(w, reservations, m[1], m[2], m[3], m[4])
				return
			}
			if m := computeHostsList.FindStringSubmatch(path); m != nil {
				computeHostList(w, reservations, m[1], m[2], m[3])
				return
			}
		case http.MethodPost:
			if m := computeHostsGetVersion.FindStringSubmatch(path); m != nil {
				computeHostGetVersion(w, reservations, m[1], m[2], m[3], m[4])
				return
			}
		}
		// This mount routes its own tail, so this is the sub-router's miss: no
		// zone method the simulator serves has this shape. A concrete route
		// would have won before reaching here.
		gcpMethodNotFound(w)
	}
	srv.HandleFunc("GET "+base+"/{rest...}", route)
	srv.HandleFunc("POST "+base+"/{rest...}", route)
}

// computeHostAssociation resolves an association to the name its hosts are
// derived from and the capacity it holds, writing the error the real service
// answers when any part of it is absent.
func computeHostAssociation(
	w http.ResponseWriter, reservations sim.Store[map[string]any],
	project, zone, association string,
) (key, parent string, count int, ok bool) {
	parts := strings.Split(association, "/")
	if len(parts) < 2 || parts[0] != "reservations" ||
		(len(parts) > 2 && (len(parts)%2 != 0 || parts[2] != "reservationBlocks")) ||
		(len(parts) > 4 && parts[4] != "reservationSubBlocks") || len(parts) > 6 {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"association %q is not a reservation, reservation block or reservation sub-block", association)
		return "", "", 0, false
	}

	name := parts[1]
	key = "projects/" + project + "/zones/" + zone + "/reservations/" + name
	held, found := reservations.Get(key)
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation %q not found", name)
		return "", "", 0, false
	}
	blocks := computeReservationBlocks(held, name)

	if len(parts) == 2 {
		total := 0
		for _, block := range blocks {
			size, _ := block["count"].(int)
			total += size
		}
		return key, name, total, true
	}

	for _, block := range blocks {
		if block["name"] != parts[3] {
			continue
		}
		if len(parts) == 4 {
			size, _ := block["count"].(int)
			return key, parts[3], size, true
		}
		for _, sub := range computeReservationSubBlocks(block) {
			if sub["name"] != parts[5] {
				continue
			}
			size, _ := sub["count"].(int)
			return key, parts[5], size, true
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation sub-block %q not found", parts[5])
		return "", "", 0, false
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation block %q not found", parts[3])
	return "", "", 0, false
}

func computeHostList(
	w http.ResponseWriter, reservations sim.Store[map[string]any],
	project, zone, association string,
) {
	_, parent, count, ok := computeHostAssociation(w, reservations, project, zone, association)
	if !ok {
		return
	}
	items := []any{}
	for _, host := range computeReservationHosts(parent, count) {
		items = append(items, host)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind": "compute#hostsListResponse", "items": items,
	})
}

func computeHostGet(
	w http.ResponseWriter, reservations sim.Store[map[string]any],
	project, zone, association, wanted string,
) {
	_, parent, count, ok := computeHostAssociation(w, reservations, project, zone, association)
	if !ok {
		return
	}
	for _, host := range computeReservationHosts(parent, count) {
		if host["name"] == wanted {
			sim.WriteJSON(w, http.StatusOK, host)
			return
		}
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "host %q not found", wanted)
}

// The document declares getVersion returning an Operation rather than a version
// payload, so the answer is the operation the request starts — the same shape
// the maintenance verbs beside it answer with.
func computeHostGetVersion(
	w http.ResponseWriter, reservations sim.Store[map[string]any],
	project, zone, association, wanted string,
) {
	key, parent, count, ok := computeHostAssociation(w, reservations, project, zone, association)
	if !ok {
		return
	}
	for _, host := range computeReservationHosts(parent, count) {
		if host["name"] != wanted {
			continue
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "zones/"+zone,
			computeSelfLink(key), "getVersion"))
		return
	}
	sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "host %q not found", wanted)
}
