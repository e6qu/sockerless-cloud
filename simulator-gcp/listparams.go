package main

import (
	"net/http"
	"strings"

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
	var node listq.Node
	if filter := strings.TrimSpace(r.URL.Query().Get("filter")); filter != "" {
		parsed, err := gcpParseFilterExpr(filter)
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
