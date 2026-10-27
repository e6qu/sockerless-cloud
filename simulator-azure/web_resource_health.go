package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// `signalAvailability` is read from the site: a running workload produces the
// signal Azure Resource Health reads. `category` comes from Microsoft's RHC
// policy file, which is not vendored and cannot be derived, so it is absent,
// as the document allows.

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

// Order by site so a listing is stable across calls.
func webResourceHealthCollection(sites []Site) map[string]any {
	sort.Slice(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })
	value := make([]any, 0, len(sites))
	for _, site := range sites {
		value = append(value, webResourceHealthMetadata(site))
	}
	return map[string]any{"value": value}
}

func registerWebResourceHealth(srv *sim.Server, both func(string, string, http.HandlerFunc)) {
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

	// A slot's metadata is reached through its own site, so neither listing
	// walks the slot store: the document declares both as listings of sites.
	srv.HandleFunc("GET "+webProvider+"/resourceHealthMetadata", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "sites") + "/"
		sim.WriteJSON(w, http.StatusOK, webResourceHealthCollection(webSitesUnder(prefix)))
	})
	srv.HandleFunc("GET "+webSubscriptionProvider+"/resourceHealthMetadata", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/resourceGroups/"
		sim.WriteJSON(w, http.StatusOK, webResourceHealthCollection(webSitesUnder(prefix)))
	})
}

// A slot's ID extends its site's, so counting it would report the site
// twice.
func webSitesUnder(prefix string) []Site {
	return azfSites.Filter(func(site Site) bool {
		return strings.HasPrefix(site.ID, prefix) && !strings.Contains(site.ID, "/slots/")
	})
}
