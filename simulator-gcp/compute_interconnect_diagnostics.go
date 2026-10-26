package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// interconnects.getDiagnostics reports from the interconnect's own record:
// whether the bundle is up and aggregated, the circuit and demarcation IDs of
// each link, and MACsec status, derived by the function getMacsecConfig
// answers with. Readings off the equipment (optical power, LACP state, ARP
// caches) are left out; the schema requires none of them.
func registerComputeInterconnectDiagnostics(srv *sim.Server) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnects/{interconnect}/getDiagnostics",
		func(w http.ResponseWriter, r *http.Request) {
			name := sim.PathParam(r, "interconnect")
			key := "projects/" + sim.PathParam(r, "project") + "/global/interconnects/" + name
			held, ok := gcpComputeInterconnects.Get(key)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "interconnect %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"result": computeInterconnectDiagnostics(key, held),
			})
		})
}

func computeInterconnectDiagnostics(key string, interconnect map[string]any) map[string]any {
	up := interconnect["operationalStatus"] == "OS_ACTIVE"

	bundleStatus := "BUNDLE_OPERATIONAL_STATUS_DOWN"
	linkStatus := "LINK_OPERATIONAL_STATUS_DOWN"
	if up {
		bundleStatus = "BUNDLE_OPERATIONAL_STATUS_UP"
		linkStatus = "LINK_OPERATIONAL_STATUS_UP"
	}

	// A bundle of more than one link is aggregated; a single link is not.
	aggregation := "BUNDLE_AGGREGATION_TYPE_STATIC"
	if computeInterconnectLinkCount(interconnect) > 1 {
		aggregation = "BUNDLE_AGGREGATION_TYPE_LACP"
	}

	macsec := computeInterconnectMacsecStatus(key, interconnect)

	links := []any{}
	circuits, _ := interconnect["circuitInfos"].([]any)
	for _, entry := range circuits {
		circuit, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		link := map[string]any{"operationalStatus": linkStatus}
		if id, ok := circuit["googleCircuitId"].(string); ok && id != "" {
			link["circuitId"] = id
		}
		if demarc, ok := circuit["googleDemarcId"].(string); ok && demarc != "" {
			link["googleDemarc"] = demarc
		}
		if macsec != nil {
			link["macsec"] = macsec
		}
		links = append(links, link)
	}

	result := map[string]any{
		"bundleOperationalStatus": bundleStatus,
		"bundleAggregationType":   aggregation,
		"links":                   links,
	}
	return result
}

// computeInterconnectLinkCount is how many links the bundle has: the number
// provisioned once it is up, and the number asked for before then.
func computeInterconnectLinkCount(interconnect map[string]any) int {
	for _, field := range []string{"provisionedLinkCount", "requestedLinkCount"} {
		switch value := interconnect[field].(type) {
		case float64:
			if int(value) > 0 {
				return int(value)
			}
		case int:
			if value > 0 {
				return value
			}
		}
	}
	return 0
}

// computeInterconnectMacsecStatus reports MACsec on the bundle: whether it is
// operating, and the connectivity association name of the key it is operating
// under — the same CKN getMacsecConfig hands the caller for that key.
func computeInterconnectMacsecStatus(key string, interconnect map[string]any) map[string]any {
	enabled, _ := interconnect["macsecEnabled"].(bool)
	if !enabled {
		return nil
	}
	configuration, _ := interconnect["macsec"].(map[string]any)
	keys, _ := configuration["preSharedKeys"].([]any)
	for _, entry := range keys {
		configured, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		keyName, _ := configured["name"].(string)
		if keyName == "" {
			continue
		}
		return map[string]any{
			"operational": true,
			"ckn":         computeMacsecKey("ckn", key, keyName, 32),
		}
	}
	// Enabled with no key in the chain is not operating.
	return map[string]any{"operational": false}
}
