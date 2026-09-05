package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// App Service VNet integration. One integration state per site/slot, readable
// and writable through every ARM spelling the service offers:
//
//   - the "swift" regional integration (.../networkConfig/virtualNetwork,
//     SwiftVirtualNetwork) — a site joins a subnet delegated to
//     Microsoft.Web/serverFarms;
//   - the classic virtualNetworkConnections collection
//     (.../virtualNetworkConnections/{vnetName}, VnetInfoResource) — the
//     spelling `az webapp vnet-integration list` reads;
//   - the Site envelope's own properties.virtualNetworkSubnetId — the
//     spelling `az webapp vnet-integration add` writes and terraform's
//     virtual_network_subnet_id manages (applySiteVirtualNetworkSubnetID).
//
// Every spelling drives the same real join: the connection's VNet is realized
// as a Docker user-defined network (dockerNetForVNet) and every container the
// site runs — the persistent site container, a `services:` container, and
// each per-invocation container — attaches to it, so sites on the same VNet
// reach each other by name. A swift PUT surfaces in the classic list (named
// `<vnet>_<subnet>`, isSwift, vnetResourceId = the subnet) and on the site's
// virtualNetworkSubnetId; a classic PUT that references a delegated subnet
// surfaces on the swift GET; deleting through any spelling really disconnects
// the containers.

// SwiftVirtualNetwork mirrors armappservice.SwiftVirtualNetwork (the body of
// PUT .../sites/{name}/networkConfig/virtualNetwork).
type SwiftVirtualNetwork struct {
	ID         string                        `json:"id,omitempty"`
	Name       string                        `json:"name,omitempty"`
	Type       string                        `json:"type,omitempty"`
	Properties SwiftVirtualNetworkProperties `json:"properties"`
}

// SwiftVirtualNetworkProperties mirrors armappservice.SwiftVirtualNetworkProperties.
type SwiftVirtualNetworkProperties struct {
	SubnetResourceID string `json:"subnetResourceId,omitempty"`
	SwiftSupported   bool   `json:"swiftSupported,omitempty"`
}

// WebVnetConnection mirrors the swagger VnetInfoResource — the classic
// spelling of a site/slot VNet connection. Keyed by its own ARM id
// (<site-or-slot id>/virtualNetworkConnections/<name>).
type WebVnetConnection struct {
	ID         string      `json:"id,omitempty"`
	Name       string      `json:"name,omitempty"`
	Type       string      `json:"type,omitempty"`
	Properties WebVnetInfo `json:"properties"`
}

// WebVnetInfo mirrors the swagger VnetInfo contract. certThumbprint, routes
// and resyncRequired are read-only on the wire; the simulator carries them as
// state (the thumbprint is derived from the certBlob the client uploads, the
// routes are the App Service plan's routes for the same connection).
type WebVnetInfo struct {
	VnetResourceID string         `json:"vnetResourceId,omitempty"`
	CertThumbprint string         `json:"certThumbprint,omitempty"`
	CertBlob       string         `json:"certBlob,omitempty"`
	Routes         []WebVnetRoute `json:"routes,omitempty"`
	ResyncRequired bool           `json:"resyncRequired"`
	DNSServers     string         `json:"dnsServers,omitempty"`
	IsSwift        bool           `json:"isSwift"`
}

// WebVnetRoute mirrors the swagger VnetRoute resource.
type WebVnetRoute struct {
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name,omitempty"`
	Type       string                 `json:"type,omitempty"`
	Properties WebVnetRouteProperties `json:"properties"`
}

// WebVnetRouteProperties mirrors the swagger VnetRouteProperties.
type WebVnetRouteProperties struct {
	StartAddress string `json:"startAddress,omitempty"`
	EndAddress   string `json:"endAddress,omitempty"`
	RouteType    string `json:"routeType,omitempty"`
}

// WebVnetGateway mirrors the swagger VnetGateway resource — a pure ARM record
// attached to a VNet connection (site, slot, or App Service plan level).
type WebVnetGateway struct {
	ID         string                   `json:"id,omitempty"`
	Name       string                   `json:"name,omitempty"`
	Type       string                   `json:"type,omitempty"`
	Properties WebVnetGatewayProperties `json:"properties"`
}

// WebVnetGatewayProperties mirrors the swagger VnetGatewayProperties.
type WebVnetGatewayProperties struct {
	VnetName      string `json:"vnetName,omitempty"`
	VpnPackageURI string `json:"vpnPackageUri,omitempty"`
}

var (
	webVnetConnections sim.Store[WebVnetConnection]
	webVnetGateways    sim.Store[WebVnetGateway]
)

func registerSiteVNetIntegration(srv *sim.Server) {
	webVnetConnections = sim.MakeStore[WebVnetConnection](srv.DB(), "web_vnet_connections")
	webVnetGateways = sim.MakeStore[WebVnetGateway](srv.DB(), "web_vnet_gateways")

	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}

	both("PUT", "/networkConfig/virtualNetwork", handleSwiftVnetPut)
	both("PATCH", "/networkConfig/virtualNetwork", handleSwiftVnetPut)
	both("GET", "/networkConfig/virtualNetwork", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		resID := webResourceID(r)
		resp := SwiftVirtualNetwork{ID: swiftConfigID(resID), Name: "virtualNetwork", Type: webChildType(r, "config")}
		if conn, ok := swiftConnectionFor(resID); ok {
			resp.Properties = SwiftVirtualNetworkProperties{
				SubnetResourceID: conn.Properties.VnetResourceID,
				SwiftSupported:   true,
			}
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	both("DELETE", "/networkConfig/virtualNetwork", func(w http.ResponseWriter, r *http.Request) {
		resID := webResourceID(r)
		if conn, ok := swiftConnectionFor(resID); ok {
			deleteVnetConnection(r, conn)
		}
		w.WriteHeader(http.StatusOK)
	})

	both("GET", "/virtualNetworkConnections", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		site, _ := webResource(r)
		conns := siteVnetConnections(webResourceID(r))
		out := make([]WebVnetConnection, 0, len(conns))
		for _, c := range conns {
			out = append(out, withPlanRoutes(c, site.Properties.ServerFarmID))
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})
	both("GET", "/virtualNetworkConnections/{vnetName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		conn, ok := webVnetConnections.Get(webResourceID(r) + "/virtualNetworkConnections/" + sim.PathParam(r, "vnetName"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network connection '%s' not found.", sim.PathParam(r, "vnetName"))
			return
		}
		site, _ := webResource(r)
		sim.WriteJSON(w, http.StatusOK, withPlanRoutes(conn, site.Properties.ServerFarmID))
	})
	both("PUT", "/virtualNetworkConnections/{vnetName}", handleClassicVnetPut)
	both("PATCH", "/virtualNetworkConnections/{vnetName}", handleClassicVnetPut)
	both("DELETE", "/virtualNetworkConnections/{vnetName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		conn, ok := webVnetConnections.Get(webResourceID(r) + "/virtualNetworkConnections/" + sim.PathParam(r, "vnetName"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network connection '%s' not found.", sim.PathParam(r, "vnetName"))
			return
		}
		deleteVnetConnection(r, conn)
		w.WriteHeader(http.StatusOK)
	})

	both("GET", "/virtualNetworkConnections/{vnetName}/gateways/{gatewayName}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		gw, ok := webVnetGateways.Get(webResourceID(r) + "/virtualNetworkConnections/" + sim.PathParam(r, "vnetName") +
			"/gateways/" + sim.PathParam(r, "gatewayName"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Gateway '%s' not found on virtual network connection '%s'.",
				sim.PathParam(r, "gatewayName"), sim.PathParam(r, "vnetName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, gw)
	})
	putGateway := func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		connID := webResourceID(r) + "/virtualNetworkConnections/" + sim.PathParam(r, "vnetName")
		if _, ok := webVnetConnections.Get(connID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network connection '%s' not found.", sim.PathParam(r, "vnetName"))
			return
		}
		writeVnetGateway(w, r, connID, webChildType(r, "virtualNetworkConnections/gateways"))
	}
	both("PUT", "/virtualNetworkConnections/{vnetName}/gateways/{gatewayName}", putGateway)
	both("PATCH", "/virtualNetworkConnections/{vnetName}/gateways/{gatewayName}", putGateway)
}

// writeVnetGateway parses, validates, stores and echoes a VnetGateway under
// ownerConnID. vpnPackageUri is the contract's one required member.
func writeVnetGateway(w http.ResponseWriter, r *http.Request, ownerConnID, gwType string) {
	var req WebVnetGateway
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Properties.VpnPackageURI == "" {
		AzureError(w, "InvalidRequestContent", "vpnPackageUri is required.", http.StatusBadRequest)
		return
	}
	gwName := sim.PathParam(r, "gatewayName")
	gw := WebVnetGateway{
		ID:   ownerConnID + "/gateways/" + gwName,
		Name: gwName,
		Type: gwType,
		Properties: WebVnetGatewayProperties{
			VnetName:      sim.PathParam(r, "vnetName"),
			VpnPackageURI: req.Properties.VpnPackageURI,
		},
	}
	webVnetGateways.Put(gw.ID, gw)
	sim.WriteJSON(w, http.StatusOK, gw)
}

// handleSwiftVnetPut serves PUT and PATCH of the swift regional VNet
// integration (WebApps_CreateOrUpdate/UpdateSwiftVirtualNetworkConnectionWithCheck
// and their Slot twins).
func handleSwiftVnetPut(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	var req SwiftVirtualNetwork
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	subnetID := req.Properties.SubnetResourceID
	vnetName := vnetNameFromSubnetID(subnetID)
	if vnetName == "" {
		AzureError(w, "InvalidRequestContent", "subnetResourceId is required and must reference a subnet", http.StatusBadRequest)
		return
	}
	site, _ := webResource(r)
	resID := webResourceID(r)

	conn := WebVnetConnection{
		Name: swiftConnectionName(subnetID),
		Type: webChildType(r, "virtualNetworkConnections"),
		Properties: WebVnetInfo{
			VnetResourceID: subnetID,
			IsSwift:        true,
		},
	}
	conn.ID = resID + "/virtualNetworkConnections/" + conn.Name
	if err := upsertVnetConnection(r, site, conn); err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
			"failed to integrate site %q into VNet: %v", site.Name, err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, SwiftVirtualNetwork{
		ID:   swiftConfigID(resID),
		Name: "virtualNetwork",
		Type: webChildType(r, "config"),
		Properties: SwiftVirtualNetworkProperties{
			SubnetResourceID: subnetID,
			SwiftSupported:   true,
		},
	})
}

// handleClassicVnetPut serves PUT and PATCH of a classic virtualNetworkConnections
// entry (WebApps_CreateOrUpdate/UpdateVnetConnection and their Slot twins).
func handleClassicVnetPut(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	var req WebVnetConnection
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	resID := webResourceID(r)
	connName := sim.PathParam(r, "vnetName")
	connID := resID + "/virtualNetworkConnections/" + connName

	// PATCH without a vnetResourceId updates the existing connection in place.
	vnetResourceID := req.Properties.VnetResourceID
	existing, hasExisting := webVnetConnections.Get(connID)
	if vnetResourceID == "" && hasExisting {
		vnetResourceID = existing.Properties.VnetResourceID
	}
	// Resolve the referenced subnet against the real Microsoft.Network store:
	// when the subnet exists, its canonical ARM id (the store key) is what the
	// connection records.
	if sn, ok := azureSubnets.Get(vnetResourceID); ok {
		vnetResourceID = sn.ID
	}
	vnetOfTarget := vnetNameFromSubnetID(vnetResourceID)
	if vnetOfTarget == "" {
		AzureError(w, "InvalidRequestContent",
			"vnetResourceId is required and must reference a virtual network or one of its subnets", http.StatusBadRequest)
		return
	}

	site, _ := webResource(r)
	conn := WebVnetConnection{
		ID:   connID,
		Name: connName,
		Type: webChildType(r, "virtualNetworkConnections"),
		Properties: WebVnetInfo{
			VnetResourceID: vnetResourceID,
			CertBlob:       req.Properties.CertBlob,
			CertThumbprint: existing.Properties.CertThumbprint,
			DNSServers:     req.Properties.DNSServers,
			// VNET injection means a delegated-subnet (regional) integration;
			// derived from what the connection references, not from a client
			// claim.
			IsSwift: strings.Contains(vnetResourceID, "/subnets/"),
		},
	}
	if conn.Properties.DNSServers == "" && hasExisting {
		conn.Properties.DNSServers = existing.Properties.DNSServers
	}
	if conn.Properties.CertBlob == "" && hasExisting {
		conn.Properties.CertBlob = existing.Properties.CertBlob
	}
	if conn.Properties.CertBlob != "" {
		conn.Properties.CertThumbprint = vnetCertThumbprint(conn.Properties.CertBlob)
	}
	if err := upsertVnetConnection(r, site, conn); err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
			"failed to integrate site %q into VNet: %v", site.Name, err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, withPlanRoutes(conn, site.Properties.ServerFarmID))
}

// upsertVnetConnection records the connection and performs the real join:
// the VNet's Docker network is ensured and the site's containers attach to it.
// A `services:` container (no HTTP function bootstrap, e.g. redis) is never
// invoked, so VNet integration is its run trigger — it starts now on the
// network. An HTTP function site is started by its own invoke (asynchronously);
// the network is recorded and a live container is attached immediately.
// Regional (delegated-subnet) integration is single per site: a new
// subnet-backed connection replaces a previous one under a different name,
// exactly as a swift PUT re-points the integration.
func upsertVnetConnection(r *http.Request, site Site, conn WebVnetConnection) error {
	resID := webResourceID(r)
	if conn.Properties.IsSwift {
		if prev, ok := swiftConnectionFor(resID); ok && prev.ID != conn.ID {
			deleteVnetConnection(r, prev)
		}
	}
	dockerNet := dockerNetForVNet(vnetNameFromSubnetID(conn.Properties.VnetResourceID))
	if _, err := sim.EnsureDockerNetwork(dockerNet); err != nil {
		return err
	}
	webVnetConnections.Put(conn.ID, conn)
	syncSiteVnetSubnetProperty(r)

	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.addNetworkLocked(dockerNet)
	s := site
	if !hasAzureFunctionHTTPBootstrap(&s) && siteContainerImage(&s) != "" {
		return inst.ensureStarted(&s)
	}
	if inst.containerID != "" && sim.ContainerRunning(inst.containerID) {
		_ = sim.ConnectContainerToNetwork(inst.containerID, dockerNet, siteNetAliases(&s))
	}
	return nil
}

// applySiteVirtualNetworkSubnetID realizes the Site.properties.virtualNetworkSubnetId
// spelling of regional VNet integration — the write `az webapp vnet-integration
// add` and terraform's virtual_network_subnet_id perform. A non-empty subnet id
// joins exactly as a swift PUT does; an explicit empty value removes the
// integration, really disconnecting the site's containers.
func applySiteVirtualNetworkSubnetID(r *http.Request, site Site, subnetID string) error {
	if subnetID == "" {
		if conn, ok := swiftConnectionFor(webResourceID(r)); ok {
			deleteVnetConnection(r, conn)
		}
		return nil
	}
	if vnetNameFromSubnetID(subnetID) == "" {
		return nil
	}
	conn := WebVnetConnection{
		Name: swiftConnectionName(subnetID),
		Type: webChildType(r, "virtualNetworkConnections"),
		Properties: WebVnetInfo{
			VnetResourceID: subnetID,
			IsSwift:        true,
		},
	}
	conn.ID = webResourceID(r) + "/virtualNetworkConnections/" + conn.Name
	return upsertVnetConnection(r, site, conn)
}

// syncSiteVnetSubnetProperty reflects the current regional integration onto
// the stored Site row's virtualNetworkSubnetId member, so every read of the
// site (whichever spelling wrote the integration) reports it.
func syncSiteVnetSubnetProperty(r *http.Request) {
	subnet := ""
	if conn, ok := swiftConnectionFor(webResourceID(r)); ok {
		subnet = conn.Properties.VnetResourceID
	}
	webResourceStore(r).Update(webResourceID(r), func(s *Site) {
		s.Properties.VirtualNetworkSubnetID = subnet
	})
}

// deleteVnetConnection removes the connection record, its gateways, and — when
// no other connection of the same site still uses the VNet — really
// disconnects the site's container from the network.
func deleteVnetConnection(r *http.Request, conn WebVnetConnection) {
	webVnetConnections.Delete(conn.ID)
	syncSiteVnetSubnetProperty(r)
	gwPrefix := conn.ID + "/gateways/"
	for _, gw := range webVnetGateways.Filter(func(g WebVnetGateway) bool { return strings.HasPrefix(g.ID, gwPrefix) }) {
		webVnetGateways.Delete(gw.ID)
	}

	dockerNet := dockerNetForVNet(vnetNameFromSubnetID(conn.Properties.VnetResourceID))
	resID := webResourceID(r)
	for _, other := range siteVnetConnections(resID) {
		if dockerNetForVNet(vnetNameFromSubnetID(other.Properties.VnetResourceID)) == dockerNet {
			return // the VNet is still integrated through another connection
		}
	}
	site, ok := webResource(r)
	if !ok {
		return
	}
	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.removeNetworkLocked(dockerNet)
	if inst.containerID != "" {
		_ = sim.DisconnectContainerFromNetwork(inst.containerID, dockerNet)
	}
}

// siteVnetConnections lists the connections of one site/slot, sorted by id.
func siteVnetConnections(resID string) []WebVnetConnection {
	prefix := resID + "/virtualNetworkConnections/"
	out := webVnetConnections.Filter(func(c WebVnetConnection) bool { return strings.HasPrefix(c.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// swiftConnectionFor finds the site's regional (delegated-subnet) integration —
// the connection the swift spelling reads and deletes.
func swiftConnectionFor(resID string) (WebVnetConnection, bool) {
	for _, c := range siteVnetConnections(resID) {
		if c.Properties.IsSwift && strings.Contains(c.Properties.VnetResourceID, "/subnets/") {
			return c, true
		}
	}
	return WebVnetConnection{}, false
}

// swiftConnectionName is the classic-collection name of a swift integration:
// `<vnet>_<subnet>`, the spelling real App Service lists it under.
func swiftConnectionName(subnetID string) string {
	vnet := vnetNameFromSubnetID(subnetID)
	subnet := subnetID[strings.LastIndex(subnetID, "/")+1:]
	return vnet + "_" + subnet
}

// swiftConfigID is the swift connection's canonical ARM id: the operation path
// is .../networkConfig/virtualNetwork, but the resource's id (and the value
// clients like terraform-provider-azurerm parse from the response) is the
// config sub-resource .../config/virtualNetwork.
func swiftConfigID(resID string) string { return resID + "/config/virtualNetwork" }

// withPlanRoutes renders a connection with the routes member assembled from
// the App Service plan's route records for the same connection name.
func withPlanRoutes(conn WebVnetConnection, serverFarmID string) WebVnetConnection {
	if serverFarmID != "" {
		conn.Properties.Routes = planVnetRoutes(serverFarmID, conn.Name)
	}
	return conn
}

// vnetCertThumbprint is the SHA-1 fingerprint of the connection's certificate
// blob — the thumbprint every Azure certificate surface reports. The blob is
// carried verbatim (base64 when the client sent base64, DER bytes otherwise);
// the fingerprint is computed over the decoded certificate when it decodes.
func vnetCertThumbprint(certBlob string) string {
	raw := []byte(certBlob)
	if decoded, err := base64.StdEncoding.DecodeString(certBlob); err == nil {
		raw = decoded
	}
	sum := sha1.Sum(raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// realizeCNAMEAsSiteDockerAlias makes recordName resolve to the App Service site
// whose DefaultHostName is the CNAME's target, by re-attaching that site's
// container to its VNet networks with the alias — the same mechanism
// realizeCNAMEAsDockerAlias uses for ACA Apps. This is how the sim realizes a
// Private DNS record for an App Service site locally; it is not
// sockerless-specific (any CNAME → a site's default hostname resolves this way).
// No-op when no site matches or the site isn't VNet-integrated yet.
func realizeCNAMEAsSiteDockerAlias(cname string) {
	target := strings.TrimSuffix(cname, ".")
	if target == "" {
		return
	}
	for _, site := range azfSites.List() {
		if !strings.EqualFold(strings.TrimSuffix(site.Properties.DefaultHostName, "."), target) {
			continue
		}
		s := site
		inst := azfInstanceFor(s.Name)
		inst.mu.Lock()
		cid := inst.containerID
		dockerNets := append([]string(nil), inst.dockerNetworks...)
		inst.mu.Unlock()
		if cid == "" || len(dockerNets) == 0 {
			return
		}
		// Docker can't add an alias to a live endpoint, so re-attach with the
		// full set: the site's identity names + every CNAME pointing at it.
		aliases := dedupeStrings(append(siteNetAliases(&s), cnameAliasesForTarget(target)...))
		for _, dockerNet := range dockerNets {
			_ = sim.DisconnectContainerFromNetwork(cid, dockerNet)
			_ = sim.ConnectContainerToNetwork(cid, dockerNet, aliases)
		}
		return
	}
}
