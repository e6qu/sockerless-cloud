package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// interconnectRemoteLocations.list and .get serve the third-party facilities
// Cross-Cloud Interconnect peers with, vendored by
// scripts/fetch-gcp-interconnect-remote-locations.sh from Google's four
// "Choose your locations" pages through a rowspan-aware grid, so a
// metropolitan area written once lands on every entry it spans. Only the
// fields those pages state are served: name, the provider's facility ID,
// metropolitan area and permitted colocation facilities.

//go:embed compute_interconnect_remote_locations_vendored.json
var interconnectRemoteLocationsJSON []byte

type interconnectRemoteLocationCatalog struct {
	Sources         map[string]string `json:"sources"`
	Retrieved       string            `json:"retrieved"`
	RemoteLocations []map[string]any  `json:"remoteLocations"`
}

var interconnectRemoteLocations = sync.OnceValue(func() interconnectRemoteLocationCatalog {
	var catalog interconnectRemoteLocationCatalog
	if err := json.Unmarshal(interconnectRemoteLocationsJSON, &catalog); err != nil {
		panic("vendored interconnect remote-location catalogue is not valid JSON: " + err.Error())
	}
	if len(catalog.RemoteLocations) == 0 {
		panic("vendored interconnect remote-location catalogue is empty")
	}
	return catalog
})

func interconnectRemoteLocationResource(project string, entry map[string]any) map[string]any {
	out := map[string]any{"kind": "compute#interconnectRemoteLocation"}
	for key, value := range entry {
		out[key] = value
	}
	name, _ := entry["name"].(string)
	out["selfLink"] = "https://compute.googleapis.com/compute/v1/projects/" +
		project + "/global/interconnectRemoteLocations/" + name
	return out
}

func registerComputeInterconnectRemoteLocations(srv *sim.Server) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectRemoteLocations",
		func(w http.ResponseWriter, r *http.Request) {
			interconnectCatalogList(w, r, "interconnectRemoteLocations", "compute#interconnectRemoteLocationList", interconnectRemoteLocations().RemoteLocations, interconnectRemoteLocationResource)
		})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectRemoteLocations/{location}",
		func(w http.ResponseWriter, r *http.Request) {
			interconnectCatalogGet(w, r, "interconnectRemoteLocations", interconnectRemoteLocations().RemoteLocations, interconnectRemoteLocationResource)
		})
}
