package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// gcpResourceToMap renders a stored resource as its JSON object. A stored
// resource that fails to round-trip is corrupt state, so it panics and
// net/http answers 500.
func gcpResourceToMap(it any) map[string]any {
	d, err := listq.ToDoc(it)
	if err != nil {
		panic("gcp: " + err.Error())
	}
	return d
}

// gcpApplyListParams applies the request's `filter` and `orderBy` to items,
// answering INVALID_ARGUMENT for either one the grammar does not admit.
func gcpApplyListParams[T any](w http.ResponseWriter, r *http.Request, items []T) ([]T, bool) {
	return gcpApplyListParamsWith(w, r, items, gcpParseFilterExpr)
}

// gcpApplyComputeListParams is gcpApplyListParams for a Compute Engine list,
// whose filter also admits the eq/ne regular-expression form.
func gcpApplyComputeListParams[T any](w http.ResponseWriter, r *http.Request, items []T) ([]T, bool) {
	return gcpApplyListParamsWith(w, r, items, gcpParseComputeFilter)
}

func gcpApplyListParamsWith[T any](w http.ResponseWriter, r *http.Request, items []T, parse func(string) (listq.Node, error)) ([]T, bool) {
	var node listq.Node
	if filter := strings.TrimSpace(r.URL.Query().Get("filter")); filter != "" {
		parsed, err := parse(filter)
		if err != nil {
			GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
			return nil, false
		}
		node = parsed
	}
	order, err := listq.ParseOrderBy(r.URL.Query().Get("orderBy"), false)
	if err != nil {
		GCPError(w, http.StatusBadRequest, "invalid orderBy: "+err.Error(), "INVALID_ARGUMENT")
		return nil, false
	}
	out, err := listq.ApplyList(items, node, order, ".")
	if err != nil {
		GCPError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return nil, false
	}
	return out, true
}

// gcpWriteList answers a list method: the resources of store whose name falls
// under prefix, filtered and ordered by the request's list parameters and
// paged by name, under member.
func gcpWriteList[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], prefix, member string, name func(T) string) {
	result := store.Filter(func(v T) bool { return strings.HasPrefix(name(v), prefix) })
	listed, ok := gcpApplyListParams(w, r, result)
	if !ok {
		return
	}
	sort.Slice(listed, func(i, j int) bool { return name(listed[i]) < name(listed[j]) })
	page, next, ok := paginateList(w, r, listed, gcpAsc(name))
	if !ok {
		return
	}
	resp := map[string]any{member: page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// gcpWritePage answers a list method: the resources of store whose name falls
// under prefix, ordered by the request's orderBy and paged by name, under
// member.
func gcpWritePage[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], prefix, member string, name func(T) string) {
	page, next, ok := paginateList(w, r, store.Filter(func(v T) bool { return strings.HasPrefix(name(v), prefix) }), gcpAsc(name))
	if !ok {
		return
	}
	resp := map[string]any{member: page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// gcpWriteNamedMaps answers a list of the resources of store, held as JSON
// objects, whose name falls under prefix, in name order under member.
func gcpWriteNamedMaps(w http.ResponseWriter, store sim.Store[map[string]any], prefix, member string) {
	name := func(m map[string]any) string {
		n, _ := m["name"].(string)
		return n
	}
	held := store.Filter(func(m map[string]any) bool { return strings.HasPrefix(name(m), prefix) })
	sort.Slice(held, func(i, j int) bool { return name(held[i]) < name(held[j]) })
	items := make([]any, 0, len(held))
	for _, m := range held {
		items = append(items, m)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{member: items})
}
