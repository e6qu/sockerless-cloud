package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
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
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation %q not found", name)
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.SpecificSkuCount <= 0 {
			GCPError(w, http.StatusBadRequest,
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
		for _, block := range computeReservationBlocks(reservation, sim.PathParam(r, "name")) {
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
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation block %q not found", wanted)
	})
	srv.HandleFunc("POST "+base+"/{name}/reservationBlocks/{block}/performMaintenance", func(w http.ResponseWriter, r *http.Request) {
		key, _, ok := load(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "performMaintenance"))
	})

	// A block's IAM policy, and the sub-blocks it is divided into. A sub-block
	// is the unit maintenance is performed on and a fault is reported against,
	// so it is derived from the block the same way the block is derived from
	// the reservation: two sub-blocks to a block, which is the shape Compute
	// Engine reports for the reservations that expose them.
	blockIAM := func(r *http.Request) string {
		return "compute/" + computeScopedKey(r, cScopeZone, "reservations", sim.PathParam(r, "name")) +
			"/reservationBlocks/" + sim.PathParam(r, "block")
	}
	srv.HandleFunc("GET "+base+"/{name}/reservationBlocks/{block}/getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, blockIAM(r), "getIamPolicy")
	})
	srv.HandleFunc("POST "+base+"/{name}/reservationBlocks/{block}/setIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, blockIAM(r), "setIamPolicy")
	})
	srv.HandleFunc("POST "+base+"/{name}/reservationBlocks/{block}/testIamPermissions", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, blockIAM(r), "testIamPermissions")
	})

	subBlocks := func(w http.ResponseWriter, r *http.Request) ([]map[string]any, bool) {
		_, reservation, ok := load(w, r)
		if !ok {
			return nil, false
		}
		block := sim.PathParam(r, "block")
		for _, held := range computeReservationBlocks(reservation, sim.PathParam(r, "name")) {
			if held["name"] != block {
				continue
			}
			return computeReservationSubBlocks(held), true
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation block %q not found", block)
		return nil, false
	}

	const subBase = base + "/{name}/reservationBlocks/{block}/reservationSubBlocks"
	srv.HandleFunc("GET "+subBase, func(w http.ResponseWriter, r *http.Request) {
		items, ok := subBlocks(w, r)
		if !ok {
			return
		}
		entries := []any{}
		for _, item := range items {
			entries = append(entries, item)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#reservationSubBlocksListResponse", "items": entries,
		})
	})
	findSub := func(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
		items, ok := subBlocks(w, r)
		if !ok {
			return nil, false
		}
		wanted := sim.PathParam(r, "subBlock")
		for _, item := range items {
			if item["name"] == wanted {
				return item, true
			}
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation sub-block %q not found", wanted)
		return nil, false
	}
	srv.HandleFunc("GET "+subBase+"/{subBlock}", func(w http.ResponseWriter, r *http.Request) {
		item, ok := findSub(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"resource": item})
	})
	for _, verb := range []string{"getVersion", "performMaintenance", "reportFaulty"} {
		verb := verb
		srv.HandleFunc("POST "+subBase+"/{subBlock}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			if _, ok := findSub(w, r); !ok {
				return
			}
			key := computeScopedKey(r, cScopeZone, "reservations", sim.PathParam(r, "name"))
			sim.WriteJSON(w, http.StatusOK, operation(r, key, verb))
		})
	}
	subIAM := func(r *http.Request) string {
		return blockIAM(r) + "/reservationSubBlocks/" + sim.PathParam(r, "subBlock")
	}
	srv.HandleFunc("GET "+subBase+"/{subBlock}/getIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, subIAM(r), "getIamPolicy")
	})
	srv.HandleFunc("POST "+subBase+"/{subBlock}/setIamPolicy", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, subIAM(r), "setIamPolicy")
	})
	srv.HandleFunc("POST "+subBase+"/{subBlock}/testIamPermissions", func(w http.ResponseWriter, r *http.Request) {
		handleResourceIAM(w, r, gcpResourcePolicies, subIAM(r), "testIamPermissions")
	})

	// A slot is one machine of a sub-block, and the level at which a
	// reservation reports health and takes an update. Slots come from the
	// sub-block's count for the same reason sub-blocks come from the block's:
	// a resize has to reach all the way down, and a stored copy would not.
	slots := func(w http.ResponseWriter, r *http.Request) ([]map[string]any, bool) {
		sub, ok := findSub(w, r)
		if !ok {
			return nil, false
		}
		return computeReservationSlots(sub), true
	}
	findSlot := func(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
		items, ok := slots(w, r)
		if !ok {
			return nil, false
		}
		wanted := sim.PathParam(r, "slot")
		for _, item := range items {
			if item["name"] == wanted {
				return item, true
			}
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "reservation slot %q not found", wanted)
		return nil, false
	}

	const slotBase = subBase + "/{subBlock}/reservationSlots"
	srv.HandleFunc("GET "+slotBase, func(w http.ResponseWriter, r *http.Request) {
		items, ok := slots(w, r)
		if !ok {
			return
		}
		entries := []any{}
		for _, item := range items {
			entries = append(entries, item)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#reservationSlotsListResponse", "items": entries,
		})
	})
	srv.HandleFunc("GET "+slotBase+"/{slot}", func(w http.ResponseWriter, r *http.Request) {
		item, ok := findSlot(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"resource": item})
	})
	// update, getVersion and getHealth all answer with an Operation, which is
	// what the document declares for each.
	for suffix, verb := range map[string]string{"": "update", "/getVersion": "getVersion", "/getHealth": "getHealth"} {
		verb := verb
		srv.HandleFunc("POST "+slotBase+"/{slot}"+suffix, func(w http.ResponseWriter, r *http.Request) {
			if _, ok := findSlot(w, r); !ok {
				return
			}
			key := computeScopedKey(r, cScopeZone, "reservations", sim.PathParam(r, "name"))
			sim.WriteJSON(w, http.StatusOK, operation(r, key, verb))
		})
	}
}

// computeReservationSlots divides a sub-block into the individual machines it
// holds, which is the level a reservation reports health at.
func computeReservationSlots(sub map[string]any) []map[string]any {
	count, _ := sub["count"].(int)
	name, _ := sub["name"].(string)
	slots := make([]map[string]any, 0, count)
	for i := 1; i <= count; i++ {
		slots = append(slots, map[string]any{
			"kind": "compute#reservationSlot", "name": fmt.Sprintf("%s-slot-%d", name, i),
			"state": "ACTIVE", "status": map[string]any{"runningInstances": []any{}},
		})
	}
	return slots
}

// computeReservationSubBlocks divides a block into the units maintenance is
// performed on. Two to a block, which is what Compute Engine reports for the
// reservations that expose them.
func computeReservationSubBlocks(block map[string]any) []map[string]any {
	count, _ := block["count"].(int)
	if count <= 0 {
		return nil
	}
	name, _ := block["name"].(string)
	half := (count + 1) / 2
	subs := []map[string]any{{
		"kind": "compute#reservationSubBlock", "name": name + "-sub-1",
		"count": half, "inUseCount": 0, "status": "READY",
	}}
	if remaining := count - half; remaining > 0 {
		subs = append(subs, map[string]any{
			"kind": "compute#reservationSubBlock", "name": name + "-sub-2",
			"count": remaining, "inUseCount": 0, "status": "READY",
		})
	}
	return subs
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
