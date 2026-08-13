package main

import (
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_private_access.go serves two site/slot network views:
//
//   - privateAccess/virtualNetworks — the Private Site Access record (which
//     Virtual Networks may reach the site privately), round-tripped exactly
//     per the swagger PrivateAccess contract;
//   - networkFeatures/{view} — the read-only summary of the site's network
//     features, assembled from the REAL connection state the simulator holds:
//     the VNet connection the site actually has (sites_vnet.go) and its two
//     hybrid connection families (web_hybrid.go).

// WebPrivateAccess mirrors the swagger PrivateAccess resource.
type WebPrivateAccess struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Properties WebPrivateAccessProperties `json:"properties"`
}

// WebPrivateAccessProperties mirrors the swagger PrivateAccessProperties.
// virtualNetworks is always present: the contract reads an empty array (not
// null) as "all subnets allowed within this Virtual Network".
type WebPrivateAccessProperties struct {
	Enabled         bool                             `json:"enabled"`
	VirtualNetworks []WebPrivateAccessVirtualNetwork `json:"virtualNetworks"`
}

// WebPrivateAccessVirtualNetwork mirrors the swagger PrivateAccessVirtualNetwork.
type WebPrivateAccessVirtualNetwork struct {
	Name       string                   `json:"name,omitempty"`
	Key        int32                    `json:"key,omitempty"`
	ResourceID string                   `json:"resourceId,omitempty"`
	Subnets    []WebPrivateAccessSubnet `json:"subnets,omitempty"`
}

// WebPrivateAccessSubnet mirrors the swagger PrivateAccessSubnet.
type WebPrivateAccessSubnet struct {
	Name string `json:"name,omitempty"`
	Key  int32  `json:"key,omitempty"`
}

// WebNetworkFeatures mirrors the swagger NetworkFeatures resource.
type WebNetworkFeatures struct {
	ID         string                       `json:"id,omitempty"`
	Name       string                       `json:"name,omitempty"`
	Type       string                       `json:"type,omitempty"`
	Properties WebNetworkFeaturesProperties `json:"properties"`
}

// WebNetworkFeaturesProperties mirrors the swagger NetworkFeaturesProperties.
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
			// A site never configured for private access reads as disabled
			// with no Virtual Networks — the state it genuinely is in.
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
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
			// The summary names the site's VNet integration; the regional
			// (swift) connection wins when both spellings exist.
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

// writePrivateAccess renders a PrivateAccess record at its canonical id with
// the always-present virtualNetworks member.
func writePrivateAccess(w http.ResponseWriter, r *http.Request, pa WebPrivateAccess) {
	pa.ID = webResourceID(r) + "/privateAccess/virtualNetworks"
	pa.Name = "virtualNetworks"
	pa.Type = webChildType(r, "privateAccess")
	if pa.Properties.VirtualNetworks == nil {
		pa.Properties.VirtualNetworks = []WebPrivateAccessVirtualNetwork{}
	}
	sim.WriteJSON(w, http.StatusOK, pa)
}
