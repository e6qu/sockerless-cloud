package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// interconnectLocations.list and .get — the colocation facilities Cloud
// Interconnect runs out of.
//
// The catalogue is Google's to publish and cannot be derived from anything this
// simulator holds, so it is vendored, the way the Azure slice vendors
// Application Gateway's managed WAF rule sets: an embedded JSON file generated
// by scripts/fetch-gcp-interconnect-locations.sh from Google's own
// documentation, with the source URL and retrieval date recorded in the file
// and the count locked by a test so a partial vendor fails loudly.
//
// Every field served is one the source states — the location name, the
// metropolitan area, the facility, its availability zone and peeringdb id, and
// the low-latency region where the page gives one. The street address, the
// facility provider, the continent and the available link types are not served:
// the page gives a geographic grouping and a link-speed column whose mapping
// onto the enums Compute Engine declares is a judgement, and the schema
// requires none of them. A field the source does not state is left absent
// rather than inferred, which is the same rule the App Service reads and the
// interconnect diagnostics follow.

//go:embed compute_interconnect_locations_vendored.json
var interconnectLocationsJSON []byte

type interconnectLocationCatalog struct {
	Source    string           `json:"source"`
	Retrieved string           `json:"retrieved"`
	Locations []map[string]any `json:"locations"`
}

// interconnectLocations decodes the vendored catalogue once. The file is part
// of the build; if it does not decode the binary is broken, and failing here is
// better than serving a truncated catalogue.
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
			project := sim.PathParam(r, "project")
			catalog := interconnectLocations()
			items := make([]any, 0, len(catalog.Locations))
			for _, entry := range catalog.Locations {
				items = append(items, interconnectLocationResource(project, entry))
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind":     "compute#interconnectLocationList",
				"id":       "projects/" + project + "/global/interconnectLocations",
				"selfLink": "https://compute.googleapis.com/compute/v1/projects/" + project + "/global/interconnectLocations",
				"items":    items,
			})
		})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectLocations/{location}",
		func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "location")
			for _, entry := range interconnectLocations().Locations {
				if entry["name"] == name {
					sim.WriteJSON(w, http.StatusOK, interconnectLocationResource(project, entry))
					return
				}
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"The resource 'projects/%s/global/interconnectLocations/%s' was not found", project, name)
		})
}
