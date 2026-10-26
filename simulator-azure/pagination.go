package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// Every Key Vault data-plane list declares maxresults from 1 to 25.
const kvMaxResults = 25

// 25 per page by default, as the service does; 400 BadParameter for a
// maxresults outside 1..25 or a $skiptoken never issued. key must be unique.
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

// Key Vault always emits https.
func kvNextLink(r *http.Request, skipToken string) string {
	q := r.URL.Query()
	q.Set("$skiptoken", skipToken)
	return fmt.Sprintf("https://%s%s?%s", r.Host, r.URL.Path, q.Encode())
}

// 100 per page by default; 400 BadRequest for a $skiptoken never issued. key
// must be unique.
func armPage[T any](w http.ResponseWriter, r *http.Request, items []T, key func(T) string) ([]T, string, bool) {
	return armKeyedPage(w, r, azureKeyAll(items, key))
}

// For a list ordered only by construction, such as a fixed catalogue.
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

// The $skiptoken names the last key returned, so an item created or deleted
// between calls neither repeats nor skips one after the token.
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

// For a list whose names repeat across resource groups.
func azureNameIDKey(name, id string) string {
	return name + "\x00" + id
}

func armNextLink(r *http.Request, skipToken string) string {
	q := r.URL.Query()
	q.Set("$skiptoken", skipToken)
	return fmt.Sprintf("%s://%s%s?%s", azureRequestScheme(r), r.Host, r.URL.Path, q.Encode())
}

// Docker Registry v2 pagination: n is the maximum count, last the exclusive
// lower bound; the second result is the page's last name, for the Link header.
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

// Oldest first by creation time, then by append position, which separates
// versions created within one second.
func kvVersionOrder(created int64, appended int) string {
	return listq.IntKey(created) + listq.IntKey(int64(appended))
}

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
