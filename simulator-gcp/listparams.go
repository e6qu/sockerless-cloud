package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// GCP list `filter` / `orderBy` query-parameter support.
//
// Real GCP list APIs accept a `filter` expression and an `orderBy` clause; the
// sim previously ignored both, returning the full name-sorted set. gcpApplyListParams
// evaluates them against each resource's JSON representation, so it works for any
// resource type. `filter` is parsed by the full AIP-160 expression grammar in
// filter.go (OR / AND / NOT, parentheses, all comparison operators, nested field
// paths); `orderBy` is `field [asc|desc]`.

// gcpResourceToMap renders a stored resource as a generic map for filter/orderBy
// evaluation. Marshaling an own stored struct to JSON and back into a map cannot
// fail in practice; if it ever does, that's corrupt own-state, so fail loud
// (net/http recovers the panic into a 500) rather than silently evaluating the
// filter against an empty map.
func gcpResourceToMap(it any) map[string]any {
	b, err := json.Marshal(it)
	if err != nil {
		panic("gcp list: marshal resource: " + err.Error())
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		panic("gcp list: unmarshal resource: " + err.Error())
	}
	return m
}

func gcpApplyListParams[T any](items []T, r *http.Request) []T {
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	orderBy := strings.TrimSpace(r.URL.Query().Get("orderBy"))
	if filter == "" && orderBy == "" {
		return items
	}
	type pair struct {
		v T
		m map[string]any
	}
	pairs := make([]pair, 0, len(items))
	for _, it := range items {
		pairs = append(pairs, pair{it, gcpResourceToMap(it)})
	}

	if filter != "" {
		node := gcpParseFilterExpr(filter)
		kept := pairs[:0]
		for _, p := range pairs {
			if node.eval(p.m) {
				kept = append(kept, p)
			}
		}
		pairs = kept
	}
	if orderBy != "" {
		field, desc := gcpParseOrderBy(orderBy)
		sort.SliceStable(pairs, func(i, j int) bool {
			a, b := gcpFieldString(pairs[i].m, field), gcpFieldString(pairs[j].m, field)
			if desc {
				return a > b
			}
			return a < b
		})
	}
	out := make([]T, len(pairs))
	for i, p := range pairs {
		out[i] = p.v
	}
	return out
}

// gcpApplyOrderBy applies only the `orderBy` query param (for handlers that
// already implement their own `filter`).
func gcpApplyOrderBy[T any](items []T, r *http.Request) []T {
	orderBy := strings.TrimSpace(r.URL.Query().Get("orderBy"))
	if orderBy == "" {
		return items
	}
	field, desc := gcpParseOrderBy(orderBy)
	maps := make([]map[string]any, len(items))
	for i, it := range items {
		maps[i] = gcpResourceToMap(it)
	}
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := gcpFieldString(maps[idx[a]], field), gcpFieldString(maps[idx[b]], field)
		if desc {
			return x > y
		}
		return x < y
	})
	out := make([]T, len(items))
	for i, j := range idx {
		out[i] = items[j]
	}
	return out
}

func gcpParseOrderBy(s string) (field string, desc bool) {
	// "field desc" / "field asc" / "field"
	s = strings.TrimSpace(strings.Split(s, ",")[0])
	if strings.HasSuffix(strings.ToLower(s), " desc") {
		return strings.TrimSpace(s[:len(s)-5]), true
	}
	if strings.HasSuffix(strings.ToLower(s), " asc") {
		return strings.TrimSpace(s[:len(s)-4]), false
	}
	return s, false
}

func gcpFieldString(m map[string]any, path string) string {
	var cur any = m
	for _, seg := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[seg]
	}
	switch v := cur.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
