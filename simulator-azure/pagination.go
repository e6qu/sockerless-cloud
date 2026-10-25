package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// kvMaxResults is the largest page a Key Vault data-plane list serves: every
// list operation in the Key Vault data-plane specification declares
// maxresults with minimum 1 and maximum 25.
const kvMaxResults = 25

// kvPage pages a Key Vault data-plane list by maxresults and $skiptoken,
// 25 to a page by default as the service does, and answers 400 BadParameter
// for a maxresults outside 1..25 or a $skiptoken it never issued. key orders
// the list and must be unique within it.
func kvPage[T any](w http.ResponseWriter, r *http.Request, items []T, key func(T) string) ([]T, string, bool) {
	if raw, set := r.URL.Query()["maxresults"]; set {
		if n, err := strconv.Atoi(raw[0]); err != nil || n < 1 || n > kvMaxResults {
			AzureErrorf(w, "BadParameter", http.StatusBadRequest,
				"The value %q of parameter maxresults is not valid; it must be an integer from 1 to %d.", raw[0], kvMaxResults)
			return nil, "", false
		}
	}
	return azurePage(w, r, azureKeyAll(items, key), "maxresults", kvMaxResults, "BadParameter")
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
// issued. key orders the list and must be unique within it.
func armPage[T any](w http.ResponseWriter, r *http.Request, items []T, key func(T) string) ([]T, string, bool) {
	return armKeyedPage(w, r, azureKeyAll(items, key))
}

// armSeqPage pages a list whose order is only the order it was built in,
// such as a fixed catalogue, keying each item by its position.
func armSeqPage[T any](w http.ResponseWriter, r *http.Request, items []T) ([]T, string, bool) {
	keyed := make([]azureKeyed[T], len(items))
	for i, item := range items {
		keyed[i] = azureKeyed[T]{listq.IntKey(int64(i)), item}
	}
	return armKeyedPage(w, r, keyed)
}

func armKeyedPage[T any](w http.ResponseWriter, r *http.Request, keyed []azureKeyed[T]) ([]T, string, bool) {
	return azurePage(w, r, keyed, "$top", 100, "BadRequest")
}

// azureKeyed is a list item with the key it pages by, computed once.
type azureKeyed[T any] struct {
	key  string
	item T
}

func azureKeyAll[T any](items []T, key func(T) string) []azureKeyed[T] {
	keyed := make([]azureKeyed[T], len(items))
	for i, item := range items {
		keyed[i] = azureKeyed[T]{key(item), item}
	}
	return keyed
}

// azurePage sorts keyed by key and pages it with a $skiptoken that names the
// last key returned, so an item created or deleted between two calls neither
// repeats nor skips one after the token.
func azurePage[T any](w http.ResponseWriter, r *http.Request, keyed []azureKeyed[T], sizeParam string, def int, badTokenCode string) ([]T, string, bool) {
	size := 0
	if n, err := strconv.Atoi(r.URL.Query().Get(sizeParam)); err == nil && n > 0 {
		size = n
	}
	token := r.URL.Query().Get("$skiptoken")
	sort.Slice(keyed, func(i, j int) bool { return keyed[i].key < keyed[j].key })
	page, next, err := listq.KeyPage(keyed, func(k azureKeyed[T]) string { return k.key }, token, size, def, 0)
	if err != nil {
		AzureErrorf(w, badTokenCode, http.StatusBadRequest, "The $skiptoken %q is not valid.", token)
		return nil, "", false
	}
	out := make([]T, len(page))
	for i, k := range page {
		out[i] = k.item
	}
	return out, next, true
}

// azureNameIDKey orders by name, then by resource ID, for a list whose names
// repeat across the resource groups it spans.
func azureNameIDKey(name, id string) string {
	return name + "\x00" + id
}

// armNextLink builds the nextLink URL for an ARM management-plane list response.
func armNextLink(r *http.Request, skipToken string) string {
	q := r.URL.Query()
	q.Set("$skiptoken", skipToken)
	return fmt.Sprintf("%s://%s%s?%s", azureRequestScheme(r), r.Host, r.URL.Path, q.Encode())
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

// kvVersionOrder orders a Key Vault object's versions oldest first: by
// creation time, then by the position the version was appended at, which
// separates versions created within the same second.
func kvVersionOrder(created int64, appended int) string {
	return listq.IntKey(created) + listq.IntKey(int64(appended))
}

// kvWriteDeletedPage answers a page of the items deleted reports as deleted,
// each rendered as its deleted bundle.
func kvWriteDeletedPage[T any](w http.ResponseWriter, r *http.Request, items []T, deleted func(T) bool, name func(T) string, bundle func(T) map[string]any) {
	matching := []T{}
	for _, item := range items {
		if deleted(item) {
			matching = append(matching, item)
		}
	}
	recs, next, pageOK := kvPage(w, r, matching, name)
	if !pageOK {
		return
	}
	page := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		page = append(page, bundle(rec))
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = kvNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
