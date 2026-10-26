package main

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A reservation's hosts derive from the machine count of the association
// (reservation, block or sub-block) they belong to, as blocks, sub-blocks and
// slots derive from their parent, so a resize reaches them all.
//
// The association is one Discovery parameter holding a path, declared without
// reserved expansion, so clients send it percent-encoded as one segment.
// ServeMux cannot match that against the other two-segment zone reads, so the
// family mounts the zone subtree as a multi-segment wildcard and routes the
// tail itself, as the Cloud Spanner instance subtree does. Concrete routes
// still win, and a tail it does not own is method-not-found, which the
// coverage probe reads as unserved.

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
			// The reservation reports its blocks READY, so each host under them is
			// ACTIVE. The status beside the state carries Google's datacenter topology
			// and instance placement, which the simulator does not have, so it stays
			// absent.
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
		// Discovery declares the parameter without reserved expansion, so restore
		// the encoded separators as Google does. Read the escaped path so an encoded
		// slash elsewhere (a Cloud Storage object name) stays intact.
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
		// No zone method the simulator serves has this tail; a concrete route would
		// have matched first.
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
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"association %q is not a reservation, reservation block or reservation sub-block", association)
		return "", "", 0, false
	}

	name := parts[1]
	key = "projects/" + project + "/zones/" + zone + "/reservations/" + name
	held, found := reservations.Get(key)
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation %q not found", name)
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation sub-block %q not found", parts[5])
		return "", "", 0, false
	}
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation block %q not found", parts[3])
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
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "host %q not found", wanted)
}

// The document declares getVersion returning an Operation, as the maintenance
// verbs beside it do.
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
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "host %q not found", wanted)
}
