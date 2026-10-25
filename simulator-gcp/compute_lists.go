package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// computeWriteScopedList answers a list method: the resources of store whose
// selfLink falls under prefix, filtered and ordered by the request's list
// parameters and paged by name, as a list of kind.
func computeWriteScopedList[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], kind, prefix string, selfLink, name func(T) string) {
	all := store.Filter(func(v T) bool { return strings.HasPrefix(selfLink(v), prefix) })
	sort.Slice(all, func(i, j int) bool { return name(all[i]) < name(all[j]) })
	listed, ok := gcpApplyComputeListParams(w, r, all)
	if !ok {
		return
	}
	page, next, ok := paginateListCompute(w, r, listed, gcpAsc(name))
	if !ok {
		return
	}
	resp := map[string]any{"kind": kind, "items": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// computeWriteZonalAggregate answers an aggregatedList method over a zonal
// resource: the project's resources of store grouped under their zone's
// scope key, each group holding them under member.
func computeWriteZonalAggregate[T any](w http.ResponseWriter, store sim.Store[T], project, kind, member string, selfLink func(T) string) {
	prefix := fmt.Sprintf("projects/%s/zones/", project)
	grouped := map[string]map[string]any{}
	for _, v := range store.Filter(func(v T) bool { return strings.HasPrefix(selfLink(v), prefix) }) {
		zone, _, ok := strings.Cut(strings.TrimPrefix(selfLink(v), prefix), "/")
		if !ok {
			continue
		}
		key := "zones/" + zone
		entry, exists := grouped[key]
		if !exists {
			entry = map[string]any{member: []T{}}
			grouped[key] = entry
		}
		list, _ := entry[member].([]T)
		entry[member] = append(list, v)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": kind, "items": grouped})
}

// computeWriteRegionalAggregate answers an aggregatedList method over a
// regional resource: the project's resources of store grouped under their
// region's scope key, each group holding them under member. A store this
// process has not opened holds nothing.
func computeWriteRegionalAggregate[T any](w http.ResponseWriter, store sim.Store[T], project, kind, member string, selfLink func(T) string) {
	prefix := fmt.Sprintf("projects/%s/regions/", project)
	grouped := map[string]map[string]any{}
	if store != nil {
		for _, v := range store.Filter(func(v T) bool { return strings.HasPrefix(selfLink(v), prefix) }) {
			key := computeScopeKeyFromRest(strings.TrimPrefix(selfLink(v), fmt.Sprintf("projects/%s/", project)))
			entry, ok := grouped[key]
			if !ok {
				entry = map[string]any{member: []T{}}
				grouped[key] = entry
			}
			list, _ := entry[member].([]T)
			entry[member] = append(list, v)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": kind, "items": grouped})
}
