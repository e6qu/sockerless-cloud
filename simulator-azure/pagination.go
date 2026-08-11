package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
)

// kvPage reads maxresults and $skiptoken from r, pages the slice,
// and returns the page and the next offset token (empty = last page).
// Default page size matches real Azure Key Vault (25).
func kvPage[T any](r *http.Request, items []T) ([]T, string) {
	start := 0
	if tok := r.URL.Query().Get("$skiptoken"); tok != "" {
		if n, err := strconv.Atoi(tok); err == nil && n >= 0 {
			start = n
		}
	}
	if start >= len(items) {
		return []T{}, ""
	}
	items = items[start:]
	limit := 25
	if raw := r.URL.Query().Get("maxresults"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit >= len(items) {
		return items, ""
	}
	return items[:limit], strconv.Itoa(start + limit)
}

// kvNextLink builds the nextLink URL for a Key Vault data-plane list response.
// Real KV always emits https; we mirror that for fidelity.
func kvNextLink(r *http.Request, skipToken string) string {
	q := r.URL.Query()
	q.Set("$skiptoken", skipToken)
	return fmt.Sprintf("https://%s%s?%s", r.Host, r.URL.Path, q.Encode())
}

// armPage reads $top and $skiptoken from r, pages the slice,
// and returns the page and the next offset token (empty = last page).
// Default page size matches real Azure ARM list APIs (100).
func armPage[T any](r *http.Request, items []T) ([]T, string) {
	start := 0
	if tok := r.URL.Query().Get("$skiptoken"); tok != "" {
		if n, err := strconv.Atoi(tok); err == nil && n >= 0 {
			start = n
		}
	}
	if start >= len(items) {
		return []T{}, ""
	}
	items = items[start:]
	limit := 100
	if raw := r.URL.Query().Get("$top"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit >= len(items) {
		return items, ""
	}
	return items[:limit], strconv.Itoa(start + limit)
}

// armNextLink builds the nextLink URL for an ARM management-plane list response.
func armNextLink(r *http.Request, skipToken string) string {
	q := r.URL.Query()
	q.Set("$skiptoken", skipToken)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s%s?%s", scheme, r.Host, r.URL.Path, q.Encode())
}

// acrCatalogPage reads n and last from r (Docker Registry v2 pagination).
// n = max count, last = last repo name seen (exclusive lower bound).
// Returns the page and the last name in the page (for the next Link header).
func acrCatalogPage(r *http.Request, repos []string) ([]string, string) {
	sort.Strings(repos)
	start := 0
	if last := r.URL.Query().Get("last"); last != "" {
		for i, name := range repos {
			if name > last {
				start = i
				break
			}
			start = len(repos)
		}
	}
	if start >= len(repos) {
		return []string{}, ""
	}
	repos = repos[start:]
	limit := len(repos)
	if raw := r.URL.Query().Get("n"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < limit {
			limit = n
		}
	}
	if limit >= len(repos) {
		return repos, ""
	}
	last := repos[limit-1]
	return repos[:limit], last
}
