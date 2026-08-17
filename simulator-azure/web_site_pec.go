package main

import (
	"net/http"
	"path"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Private endpoint connections on an App Service site
// (Microsoft.Web/sites/privateEndpointConnections) and the site's private link
// resource catalog. A connection is created when a Microsoft.Network private
// endpoint targets the site (network_privateendpoint.go writes THIS store —
// the endpoint's view and the site owner's view are one object); the site
// owner then approves or rejects it here, and deletes it with the LRO delete
// the swagger declares.

// WebSitePrivateEndpointConnection is the stored representation of a site
// private endpoint connection. Properties is the neutral property bag the
// private-endpoint machinery writes; renderWebSitePEC projects exactly the
// swagger RemotePrivateEndpointConnectionARMResource members onto the wire.
type WebSitePrivateEndpointConnection struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var webSitePECs sim.Store[WebSitePrivateEndpointConnection]

// webSitePECType is the connection's ARM type at the addressed level: the
// production site, one of its deployment slots, or the App Service
// Environment the connection was opened on.
func webSitePECType(id string) string {
	switch {
	case strings.Contains(strings.ToLower(id), "/hostingenvironments/"):
		return aseResourceType + "/privateEndpointConnections"
	case strings.Contains(id, "/slots/"):
		return "Microsoft.Web/sites/slots/privateEndpointConnections"
	}
	return "Microsoft.Web/sites/privateEndpointConnections"
}

func registerWebSitePrivateEndpoints(srv *sim.Server) {
	webSitePECs = sim.MakeStore[WebSitePrivateEndpointConnection](srv.DB(), "web_site_private_endpoint_connections")

	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}

	both("GET", "/privateEndpointConnections", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		prefix := webResourceID(r) + "/privateEndpointConnections/"
		conns := webSitePECs.Filter(func(c WebSitePrivateEndpointConnection) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		sort.Slice(conns, func(i, j int) bool { return conns[i].ID < conns[j].ID })
		out := make([]map[string]any, 0, len(conns))
		for _, c := range conns {
			out = append(out, renderWebSitePEC(c))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	both("GET", "/privateEndpointConnections/{privateEndpointConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		pec, ok := webSitePECs.Get(webResourceID(r) + "/privateEndpointConnections/" + sim.PathParam(r, "privateEndpointConnectionName"))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private endpoint connection '%s' not found.", sim.PathParam(r, "privateEndpointConnectionName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, renderWebSitePEC(pec))
	})

	// PUT — approve or reject the connection. The connection itself is opened
	// by the private endpoint; the site owner only moves its state.
	both("PUT", "/privateEndpointConnections/{privateEndpointConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webResourceID(r) + "/privateEndpointConnections/" + sim.PathParam(r, "privateEndpointConnectionName")
		pec, ok := webSitePECs.Get(id)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private endpoint connection '%s' not found.", sim.PathParam(r, "privateEndpointConnectionName"))
			return
		}
		var req struct {
			Properties struct {
				PrivateLinkServiceConnectionState *PrivateLinkServiceConnectionState `json:"privateLinkServiceConnectionState"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if pec.Properties == nil {
			pec.Properties = map[string]any{}
		}
		if state := req.Properties.PrivateLinkServiceConnectionState; state != nil {
			pec.Properties["privateLinkServiceConnectionState"] = map[string]any{
				"status":          state.Status,
				"description":     state.Description,
				"actionsRequired": state.ActionsRequired,
			}
		}
		pec.Properties["provisioningState"] = "Succeeded"
		webSitePECs.Put(id, pec)
		sim.WriteJSON(w, http.StatusOK, renderWebSitePEC(pec))
	})

	both("DELETE", "/privateEndpointConnections/{privateEndpointConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		id := webResourceID(r) + "/privateEndpointConnections/" + sim.PathParam(r, "privateEndpointConnectionName")
		if !webSitePECs.Delete(id) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private endpoint connection '%s' not found.", sim.PathParam(r, "privateEndpointConnectionName"))
			return
		}
		// The swagger declares a long-running delete; the simulator's removal
		// completes synchronously, which an azcore poller reads as terminal.
		w.WriteHeader(http.StatusOK)
	})

	// privateLinkResources — the site's private link group catalog. App
	// Service publishes one group: "sites" on a production site, and the
	// slot-scoped "sites-<slot>" group on a deployment slot.
	both("GET", "/privateLinkResources", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		group := "sites"
		if slot := sim.PathParam(r, "slot"); slot != "" {
			group = "sites-" + slot
		}
		resID := webResourceID(r)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": []map[string]any{{
				"id":   resID + "/privateLinkResources/" + group,
				"name": group,
				"type": webChildType(r, "privateLinkResources"),
				"properties": map[string]any{
					"groupId":           group,
					"requiredMembers":   []string{group},
					"requiredZoneNames": []string{"privatelink.azurewebsites.net"},
				},
			}},
		})
	})
}

// renderWebSitePEC projects a stored connection onto the exact swagger
// RemotePrivateEndpointConnectionARMResource members.
func renderWebSitePEC(pec WebSitePrivateEndpointConnection) map[string]any {
	props := map[string]any{
		"provisioningState": "Succeeded",
	}
	if v, ok := pec.Properties["provisioningState"].(string); ok && v != "" {
		props["provisioningState"] = v
	}
	if pe, ok := pec.Properties["privateEndpoint"].(map[string]any); ok {
		props["privateEndpoint"] = map[string]any{"id": azureMapString(pe, "id")}
	}
	if state, ok := pec.Properties["privateLinkServiceConnectionState"].(map[string]any); ok {
		props["privateLinkServiceConnectionState"] = map[string]any{
			"status":          azureMapString(state, "status"),
			"description":     azureMapString(state, "description"),
			"actionsRequired": azureMapString(state, "actionsRequired"),
		}
	}
	if ip, ok := pec.Properties["privateIPAddress"].(string); ok && ip != "" {
		props["ipAddresses"] = []string{ip}
	}
	return map[string]any{
		"id":         pec.ID,
		"name":       path.Base(pec.ID),
		"type":       webSitePECType(pec.ID),
		"properties": props,
	}
}
