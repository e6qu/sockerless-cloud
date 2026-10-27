package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Two site and slot network views:
//
//   - privateAccess/virtualNetworks — the Private Site Access record, per the
//     PrivateAccess contract;
//   - networkFeatures/{view} — a read-only summary of the site's VNet connection
//     (sites_vnet.go) and its two hybrid connection families (web_hybrid.go).

type WebPrivateAccess struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Properties WebPrivateAccessProperties `json:"properties"`
}

// virtualNetworks is always present: the contract reads an empty array as "all
// subnets allowed within this Virtual Network".
type WebPrivateAccessProperties struct {
	Enabled         bool                             `json:"enabled"`
	VirtualNetworks []WebPrivateAccessVirtualNetwork `json:"virtualNetworks"`
}

type WebPrivateAccessVirtualNetwork struct {
	Name       string                   `json:"name,omitempty"`
	Key        int32                    `json:"key,omitempty"`
	ResourceID string                   `json:"resourceId,omitempty"`
	Subnets    []WebPrivateAccessSubnet `json:"subnets,omitempty"`
}

type WebPrivateAccessSubnet struct {
	Name string `json:"name,omitempty"`
	Key  int32  `json:"key,omitempty"`
}

type WebNetworkFeatures struct {
	ID         string                       `json:"id,omitempty"`
	Name       string                       `json:"name,omitempty"`
	Type       string                       `json:"type,omitempty"`
	Properties WebNetworkFeaturesProperties `json:"properties"`
}

type WebNetworkFeaturesProperties struct {
	VirtualNetworkName       string                      `json:"virtualNetworkName,omitempty"`
	VirtualNetworkConnection *WebVnetInfo                `json:"virtualNetworkConnection,omitempty"`
	HybridConnections        []WebRelayServiceConnection `json:"hybridConnections"`
	HybridConnectionsV2      []WebHybridConnection       `json:"hybridConnectionsV2"`
}

var webPrivateAccess sim.Store[WebPrivateAccess]

func registerWebPrivateAccess(srv *sim.Server) {
	webPrivateAccess = sim.MakeStore[WebPrivateAccess](srv.DB(), "web_private_access")

	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}

	both("GET", "/privateAccess/virtualNetworks", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		pa, ok := webPrivateAccess.Get(webResourceID(r))
		if !ok {
			// A site never configured reads as disabled with no Virtual Networks.
			pa = WebPrivateAccess{Properties: WebPrivateAccessProperties{}}
		}
		writePrivateAccess(w, r, pa)
	})

	both("PUT", "/privateAccess/virtualNetworks", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		var req WebPrivateAccess
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		webPrivateAccess.Put(webResourceID(r), req)
		writePrivateAccess(w, r, req)
	})

	both("GET", "/networkFeatures/{view}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		resID := webResourceID(r)
		view := sim.PathParam(r, "view")
		props := WebNetworkFeaturesProperties{
			HybridConnections:   siteRelayServiceConnections(resID),
			HybridConnectionsV2: make([]WebHybridConnection, 0),
		}
		for _, hc := range siteHybridConnections(resID) {
			props.HybridConnectionsV2 = append(props.HybridConnectionsV2, stripSendKeyValue(hc))
		}
		if props.HybridConnections == nil {
			props.HybridConnections = []WebRelayServiceConnection{}
		}
		if conns := siteVnetConnections(resID); len(conns) > 0 {
			// The regional (swift) connection wins when both spellings exist.
			conn := conns[0]
			if swift, ok := swiftConnectionFor(resID); ok {
				conn = swift
			}
			site, _ := webResource(r)
			rendered := withPlanRoutes(conn, site.Properties.ServerFarmID)
			props.VirtualNetworkName = vnetNameFromSubnetID(conn.Properties.VnetResourceID)
			props.VirtualNetworkConnection = &rendered.Properties
		}
		sim.WriteJSON(w, http.StatusOK, WebNetworkFeatures{
			ID:         resID + "/networkFeatures/" + view,
			Name:       view,
			Type:       webChildType(r, "networkFeatures"),
			Properties: props,
		})
	})
}

func writePrivateAccess(w http.ResponseWriter, r *http.Request, pa WebPrivateAccess) {
	pa.ID = webResourceID(r) + "/privateAccess/virtualNetworks"
	pa.Name = "virtualNetworks"
	pa.Type = webChildType(r, "privateAccess")
	if pa.Properties.VirtualNetworks == nil {
		pa.Properties.VirtualNetworks = []WebPrivateAccessVirtualNetwork{}
	}
	sim.WriteJSON(w, http.StatusOK, pa)
}
