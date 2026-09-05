package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Resource Health metadata for App Service sites.
//
// Azure Resource Health watches a site and reports whether it is healthy. The
// metadata beside it says what that watch amounts to for this particular site:
// whether a health signal exists for it at all, and which category the site
// falls into.
//
// Only one of those two is the simulator's to answer. `signalAvailability` is a
// fact about the site — a site with a workload running is producing the signal
// Resource Health reads, and a site with nothing running is producing none — so
// it is read from the site the same way the instance, process and performance
// reads read it. `category` is the classification the site matches in
// Microsoft's own RHC policy file, which is not vendored here and cannot be
// derived from the site: it is left absent, which the document allows, rather
// than filled with a category nothing classified.
//
// The metadata resource itself is real: it is addressed as the site's singleton
// child named "default", and the lists at every scope are the sites that scope
// actually holds.

// webResourceHealthMetadata builds the metadata resource for one site.
func webResourceHealthMetadata(site Site) map[string]any {
	_, running := webSiteInstanceContainer(site)
	return map[string]any{
		"id":   site.ID + "/resourceHealthMetadata/default",
		"name": "default",
		"type": "Microsoft.Web/sites/resourceHealthMetadata",
		"properties": map[string]any{
			"signalAvailability": running,
		},
	}
}

// webResourceHealthCollection orders the metadata by the site each belongs to,
// so a listing is stable across calls.
func webResourceHealthCollection(sites []Site) map[string]any {
	sort.Slice(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })
	value := make([]any, 0, len(sites))
	for _, site := range sites {
		value = append(value, webResourceHealthMetadata(site))
	}
	return map[string]any{"value": value}
}

// registerWebResourceHealth mounts the family at its four scopes: the site and
// its slots through `both`, and the resource group and subscription listings,
// which address no site of their own.
func registerWebResourceHealth(srv *sim.Server, both func(string, string, http.HandlerFunc)) {
	// A site's own metadata, as a one-item collection and as the singleton the
	// collection holds.
	both("GET", "/resourceHealthMetadata", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		sim.WriteJSON(w, http.StatusOK, webResourceHealthCollection([]Site{site}))
	})
	both("GET", "/resourceHealthMetadata/default", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		sim.WriteJSON(w, http.StatusOK, webResourceHealthMetadata(site))
	})

	// The two scope listings. A slot's metadata is reached through its own
	// site, so neither listing walks the slot store: the document declares
	// both as listings of sites.
	srv.HandleFunc("GET "+webProvider+"/resourceHealthMetadata", func(w http.ResponseWriter, r *http.Request) {
		prefix := "/subscriptions/" + sim.PathParam(r, "subscriptionId") +
			"/resourceGroups/" + sim.PathParam(r, "resourceGroupName") + "/providers/Microsoft.Web/sites/"
		sim.WriteJSON(w, http.StatusOK, webResourceHealthCollection(webSitesUnder(prefix)))
	})
	srv.HandleFunc("GET "+webSubscriptionProvider+"/resourceHealthMetadata", func(w http.ResponseWriter, r *http.Request) {
		prefix := "/subscriptions/" + sim.PathParam(r, "subscriptionId") + "/resourceGroups/"
		sim.WriteJSON(w, http.StatusOK, webResourceHealthCollection(webSitesUnder(prefix)))
	})
}

// webSitesUnder returns the production sites whose resource ID sits under a
// scope. A slot's ID extends its site's, so it sits under the same prefix — and
// a listing that counted it would report the site twice, once as itself and
// once as each slot it happens to have.
func webSitesUnder(prefix string) []Site {
	return azfSites.Filter(func(site Site) bool {
		return strings.HasPrefix(site.ID, prefix) && !strings.Contains(site.ID, "/slots/")
	})
}
