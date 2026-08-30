package main

import (
	"net/http"
	"sort"
	"strconv"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// A reservation's resize, the maintenance it reports, and the blocks its
// capacity is held in.
//
// The blocks are derived from the reservation's own count rather than stored
// beside it, because that is what they are: a block holds up to sixteen
// machines, so a reservation for forty is three of them. Deriving keeps the two
// from disagreeing after a resize.

// computeScopedKey names a resource in its scope, as the meta-registrar does.
func computeScopedKey(r *http.Request, scope computeScopeKind, collection, name string) string {
	return "projects/" + sim.PathParam(r, "project") + "/" +
		computeScopeSegment(scope, r) + "/" + collection + "/" + name
}

// registerComputeReservationVerbs serves a reservation's resize and the
// maintenance it reports, plus the blocks it is made of.
func registerComputeReservationVerbs(srv *sim.Server, reservations sim.Store[map[string]any]) {
	// Written out rather than composed through computeScopeMux: the generated
	// surface tables read the literal path out of each registration.
	const base = "/compute/v1/projects/{project}/zones/{zone}/reservations"

	load := func(w http.ResponseWriter, r *http.Request) (string, map[string]any, bool) {
		name := sim.PathParam(r, "name")
		key := computeScopedKey(r, cScopeZone, "reservations", name)
		reservation, ok := reservations.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation %q not found", name)
			return "", nil, false
		}
		return key, reservation, true
	}
	operation := func(r *http.Request, key, verb string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"),
			computeScopeSegment(cScopeZone, r), computeSelfLink(key), verb)
	}

	srv.HandleFunc("POST "+base+"/{name}/resize", func(w http.ResponseWriter, r *http.Request) {
		key, reservation, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			// The count is an int64 the document declares as a string, so the
			// wire carries it quoted.
			SpecificSkuCount int64 `json:"specificSkuCount,string"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.SpecificSkuCount <= 0 {
			sim.GCPError(w, http.StatusBadRequest,
				"specificSkuCount must be greater than zero", "INVALID_ARGUMENT")
			return
		}
		reservation["specificReservation"] = mergeInto(reservation["specificReservation"],
			map[string]any{"count": strconv.FormatInt(req.SpecificSkuCount, 10)})
		reservations.Put(key, reservation)
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "resize"))
	})

	srv.HandleFunc("POST "+base+"/{name}/performMaintenance", func(w http.ResponseWriter, r *http.Request) {
		key, reservation, ok := load(w, r)
		if !ok {
			return
		}
		reservation["resourceStatus"] = map[string]any{
			"specificSkuAllocation": map[string]any{"utilizedInstanceCount": 0},
			"maintenance":           "PERFORMED",
		}
		reservations.Put(key, reservation)
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "performMaintenance"))
	})

	// The blocks a reservation is made of. They are derived from the
	// reservation's own count rather than stored separately, because that is
	// what they are: the capacity it holds, reported in the shape the document
	// describes.
	srv.HandleFunc("GET "+base+"/{name}/reservationBlocks", func(w http.ResponseWriter, r *http.Request) {
		_, reservation, ok := load(w, r)
		if !ok {
			return
		}
		blocks := []any{}
		for i, block := range computeReservationBlocks(reservation, sim.PathParam(r, "name")) {
			_ = i
			blocks = append(blocks, block)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#reservationBlocksListResponse", "items": blocks,
		})
	})
	srv.HandleFunc("GET "+base+"/{name}/reservationBlocks/{block}", func(w http.ResponseWriter, r *http.Request) {
		_, reservation, ok := load(w, r)
		if !ok {
			return
		}
		wanted := sim.PathParam(r, "block")
		for _, block := range computeReservationBlocks(reservation, sim.PathParam(r, "name")) {
			if block["name"] == wanted {
				sim.WriteJSON(w, http.StatusOK, map[string]any{"resource": block})
				return
			}
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation block %q not found", wanted)
	})
	srv.HandleFunc("POST "+base+"/{name}/reservationBlocks/{block}/performMaintenance", func(w http.ResponseWriter, r *http.Request) {
		key, _, ok := load(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "performMaintenance"))
	})
}

// computeReservationBlocks reports the blocks a reservation's capacity is held
// in. A block holds up to sixteen machines, which is the shape Compute Engine
// reports for the reservations that expose them.
func computeReservationBlocks(reservation map[string]any, name string) []map[string]any {
	const perBlock = 16
	count := 0
	if specific, ok := reservation["specificReservation"].(map[string]any); ok {
		// count is an int64 the document declares as a string, so the wire
		// carries it quoted and a numeric-only read finds nothing.
		switch value := specific["count"].(type) {
		case string:
			if n, err := strconv.Atoi(value); err == nil {
				count = n
			}
		case float64:
			count = int(value)
		case int64:
			count = int(value)
		case int:
			count = value
		}
	}
	if count <= 0 {
		return nil
	}
	var blocks []map[string]any
	for start := 0; start < count; start += perBlock {
		size := perBlock
		if remaining := count - start; remaining < perBlock {
			size = remaining
		}
		blocks = append(blocks, map[string]any{
			"kind":  "compute#reservationBlock",
			"name":  name + "-block-" + itoa(start/perBlock+1),
			"count": size, "inUseCount": 0, "status": "READY",
		})
	}
	sort.Slice(blocks, func(i, j int) bool {
		left, _ := blocks[i]["name"].(string)
		right, _ := blocks[j]["name"].(string)
		return left < right
	})
	return blocks
}

func mergeInto(existing any, values map[string]any) map[string]any {
	out, _ := existing.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	for field, value := range values {
		out[field] = value
	}
	return out
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
