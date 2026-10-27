package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// interconnectLocations.list and .get serve Google's catalogue of Cloud
// Interconnect colocation facilities, vendored by
// scripts/fetch-gcp-interconnect-locations.sh with its source URL and
// retrieval date in the file and its count locked by a test. Only the fields
// the source states are served: name, metropolitan area, facility,
// availability zone, peeringdb id and, where given, the low-latency region.

//go:embed compute_interconnect_locations_vendored.json
var interconnectLocationsJSON []byte

type interconnectLocationCatalog struct {
	Source    string           `json:"source"`
	Retrieved string           `json:"retrieved"`
	Locations []map[string]any `json:"locations"`
}

// interconnectLocations decodes the vendored catalogue once; a file that does
// not decode is a broken build, so panic rather than serve a truncated list.
var interconnectLocations = sync.OnceValue(func() interconnectLocationCatalog {
	var catalog interconnectLocationCatalog
	if err := json.Unmarshal(interconnectLocationsJSON, &catalog); err != nil {
		panic("vendored interconnect location catalogue is not valid JSON: " + err.Error())
	}
	if len(catalog.Locations) == 0 {
		panic("vendored interconnect location catalogue is empty")
	}
	return catalog
})

// interconnectLocationResource renders one entry with the fields the API adds
// around the vendored ones.
func interconnectLocationResource(project string, entry map[string]any) map[string]any {
	out := map[string]any{"kind": "compute#interconnectLocation"}
	for key, value := range entry {
		out[key] = value
	}
	name, _ := entry["name"].(string)
	out["selfLink"] = "https://compute.googleapis.com/compute/v1/projects/" +
		project + "/global/interconnectLocations/" + name
	return out
}

func registerComputeInterconnectLocations(srv *sim.Server) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectLocations",
		func(w http.ResponseWriter, r *http.Request) {
			interconnectCatalogList(w, r, "interconnectLocations", "compute#interconnectLocationList", interconnectLocations().Locations, interconnectLocationResource)
		})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectLocations/{location}",
		func(w http.ResponseWriter, r *http.Request) {
			interconnectCatalogGet(w, r, "interconnectLocations", interconnectLocations().Locations, interconnectLocationResource)
		})
}

// interconnectCatalogList answers the list of a published Interconnect
// catalog, each entry rendered for the project by resource.
func interconnectCatalogList(w http.ResponseWriter, r *http.Request, collection, kind string, entries []map[string]any, resource func(project string, entry map[string]any) map[string]any) {
	project := sim.PathParam(r, "project")
	items := make([]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, resource(project, entry))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"kind":     kind,
		"id":       "projects/" + project + "/global/" + collection,
		"selfLink": "https://compute.googleapis.com/compute/v1/projects/" + project + "/global/" + collection,
		"items":    items,
	})
}

// interconnectCatalogGet answers the read of one entry of a published
// Interconnect catalog by the name its location path segment carries.
func interconnectCatalogGet(w http.ResponseWriter, r *http.Request, collection string, entries []map[string]any, resource func(project string, entry map[string]any) map[string]any) {
	project, name := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	for _, entry := range entries {
		if entry["name"] == name {
			sim.WriteJSON(w, http.StatusOK, resource(project, entry))
			return
		}
	}
	GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
		"The resource 'projects/%s/global/%s/%s' was not found", project, collection, name)
}
