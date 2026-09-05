package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// interconnectRemoteLocations.list and .get — the third-party facilities Cloud
// Interconnect peers with for Cross-Cloud Interconnect.
//
// Vendored from Google's own documentation by
// scripts/fetch-gcp-interconnect-remote-locations.sh, which reads the four
// "Choose your locations" pages — Amazon Web Services, Microsoft Azure, Oracle
// Cloud Infrastructure and Alibaba Cloud — through a rowspan-aware grid so a
// metropolitan area written once lands on every entry it covers. A row-counting
// parser gets the enumeration right and the associations wrong, which no count
// check reveals.
//
// Every field served is stated by those pages: the remote location's name, the
// facility provider's own id for it, the metropolitan area, and the colocation
// facilities it may connect to. The continent, the street address, the facility
// provider, the remote service, the LAG sizes and the LACP support are not
// stated there and are absent rather than inferred.

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
			project := sim.PathParam(r, "project")
			catalog := interconnectRemoteLocations()
			items := make([]any, 0, len(catalog.RemoteLocations))
			for _, entry := range catalog.RemoteLocations {
				items = append(items, interconnectRemoteLocationResource(project, entry))
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind":     "compute#interconnectRemoteLocationList",
				"id":       "projects/" + project + "/global/interconnectRemoteLocations",
				"selfLink": "https://compute.googleapis.com/compute/v1/projects/" + project + "/global/interconnectRemoteLocations",
				"items":    items,
			})
		})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectRemoteLocations/{location}",
		func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "location")
			for _, entry := range interconnectRemoteLocations().RemoteLocations {
				if entry["name"] == name {
					sim.WriteJSON(w, http.StatusOK, interconnectRemoteLocationResource(project, entry))
					return
				}
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"The resource 'projects/%s/global/interconnectRemoteLocations/%s' was not found", project, name)
		})
}
