package main

import (
	"sort"
	"strconv"
)

// awsPage applies numeric-offset pagination to a sorted slice of any type.
// token is the incoming page token (empty = first page).
// maxResults is the caller-requested page size (0 = use defaultMax).
// Returns the page slice and the next token (empty = last page).
//
// The offset cursor is snapshot-stable for the simulator's reality: callers pass
// a deterministically-ordered slice (sort before paginating) and the in-process
// store is not mutated between a client's successive page fetches. The one real
// instability source — an unsorted input slice whose map-iteration order varied
// per page — was eliminated by sorting every paginated caller's input.
func awsPage[T any](all []T, token string, maxResults, defaultMax int) ([]T, string) {
	start := 0
	if token != "" {
		offset, err := strconv.Atoi(token)
		if err != nil || offset < 0 {
			offset = 0
		}
		start = offset
	}
	if start >= len(all) {
		return []T{}, ""
	}
	page := defaultMax
	if maxResults > 0 && maxResults < page {
		page = maxResults
	}
	end := start + page
	if end >= len(all) {
		return all[start:], ""
	}
	return all[start:end], strconv.Itoa(end)
}

// awsPageExplicit paginates a sorted slice only when the caller explicitly
// requested a positive page size. When maxResults is 0 (unset) the full list
// is returned with an empty next token — matching the no-page-size-no-token
// contract clients rely on. token is the incoming page token (empty = first
// page); the returned token is empty on the last page.
func awsPageExplicit[T any](all []T, token string, maxResults int) ([]T, string) {
	start := 0
	if token != "" {
		offset, err := strconv.Atoi(token)
		if err != nil || offset < 0 {
			offset = 0
		}
		start = offset
	}
	if start >= len(all) {
		return []T{}, ""
	}
	if maxResults <= 0 {
		return all[start:], ""
	}
	end := start + maxResults
	if end >= len(all) {
		return all[start:], ""
	}
	return all[start:end], strconv.Itoa(end)
}

// awsMaxResults reads an optional *int32 page-size param, treating nil and
// non-positive values as "no page size requested".
func awsMaxResults(v *int32) int {
	if v == nil || *v <= 0 {
		return 0
	}
	return int(*v)
}

// sortBy is a convenience wrapper that sorts a slice in-place by a string key
// and returns it (for chaining).
func sortBy[T any](s []T, key func(T) string) []T {
	sort.Slice(s, func(i, j int) bool { return key(s[i]) < key(s[j]) })
	return s
}
