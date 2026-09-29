package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// kvMaxResults is the largest page a Key Vault data-plane list serves: every
// list operation in the Key Vault data-plane specification declares
// maxresults with minimum 1 and maximum 25.
const kvMaxResults = 25

// kvPage pages a Key Vault data-plane list by maxresults and $skiptoken,
// 25 to a page by default as the service does, and answers 400 BadParameter
// for a maxresults outside 1..25 or a $skiptoken it never issued.
func kvPage[T any](w http.ResponseWriter, r *http.Request, items []T) ([]T, string, bool) {
	if raw, set := r.URL.Query()["maxresults"]; set {
		if n, err := strconv.Atoi(raw[0]); err != nil || n < 1 || n > kvMaxResults {
			AzureErrorf(w, "BadParameter", http.StatusBadRequest,
				"The value %q of parameter maxresults is not valid; it must be an integer from 1 to %d.", raw[0], kvMaxResults)
			return nil, "", false
		}
	}
	return azurePage(w, r, items, "maxresults", kvMaxResults, "BadParameter")
}

// kvNextLink builds the nextLink URL for a Key Vault data-plane list response.
// Real KV always emits https; we mirror that for fidelity.
func kvNextLink(r *http.Request, skipToken string) string {
	q := r.URL.Query()
	q.Set("$skiptoken", skipToken)
	return fmt.Sprintf("https://%s%s?%s", r.Host, r.URL.Path, q.Encode())
}

// armPage pages an Azure Resource Manager list by $top and $skiptoken, 100
// to a page by default, and answers 400 BadRequest for a $skiptoken it never
// issued.
func armPage[T any](w http.ResponseWriter, r *http.Request, items []T) ([]T, string, bool) {
	return azurePage(w, r, items, "$top", 100, "BadRequest")
}

func azurePage[T any](w http.ResponseWriter, r *http.Request, items []T, sizeParam string, def int, badTokenCode string) ([]T, string, bool) {
	size := 0
	if n, err := strconv.Atoi(r.URL.Query().Get(sizeParam)); err == nil && n > 0 {
		size = n
	}
	token := r.URL.Query().Get("$skiptoken")
	page, next, err := listq.OffsetPage(items, token, size, def, 0)
	if err != nil {
		AzureErrorf(w, badTokenCode, http.StatusBadRequest, "The $skiptoken %q is not valid.", token)
		return nil, "", false
	}
	return page, next, true
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
