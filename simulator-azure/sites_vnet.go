package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// App Service VNet integration: one state per site or slot, read and written
// through every spelling the service offers:
//
//   - swift regional integration (.../networkConfig/virtualNetwork) into a
//     subnet delegated to Microsoft.Web/serverFarms;
//   - the classic virtualNetworkConnections collection, which `az webapp
//     vnet-integration list` reads;
//   - the site's properties.virtualNetworkSubnetId, which `az webapp
//     vnet-integration add` and terraform's virtual_network_subnet_id write.
//
// Every spelling drives the same join: every container the site runs attaches
// to the virtual network's engine network (dockerNetForVNet), so sites on one
// VNet reach each other by name. A swift PUT appears in the classic list (as
// `<vnet>_<subnet>`, isSwift) and on virtualNetworkSubnetId; a classic PUT
// naming a delegated subnet appears on the swift GET; a delete through any
// spelling disconnects the containers.

// The body of PUT .../sites/{name}/networkConfig/virtualNetwork.
type SwiftVirtualNetwork struct {
	ID         string                        `json:"id,omitempty"`
	Name       string                        `json:"name,omitempty"`
	Type       string                        `json:"type,omitempty"`
	Properties SwiftVirtualNetworkProperties `json:"properties"`
}

type SwiftVirtualNetworkProperties struct {
	SubnetResourceID string `json:"subnetResourceId,omitempty"`
	SwiftSupported   bool   `json:"swiftSupported,omitempty"`
}

// VnetInfoResource, the classic spelling, keyed by
// <site-or-slot id>/virtualNetworkConnections/<name>.
type WebVnetConnection struct {
	ID         string      `json:"id,omitempty"`
	Name       string      `json:"name,omitempty"`
	Type       string      `json:"type,omitempty"`
	Properties WebVnetInfo `json:"properties"`
}

// certThumbprint, routes and resyncRequired are read-only: the thumbprint
// derives from the uploaded certBlob, and the routes are the plan's for the
// same connection.
type WebVnetInfo struct {
	VnetResourceID string         `json:"vnetResourceId,omitempty"`
	CertThumbprint string         `json:"certThumbprint,omitempty"`
	CertBlob       string         `json:"certBlob,omitempty"`
	Routes         []WebVnetRoute `json:"routes,omitempty"`
	ResyncRequired bool           `json:"resyncRequired"`
	DNSServers     string         `json:"dnsServers,omitempty"`
	IsSwift        bool           `json:"isSwift"`
}

type WebVnetRoute struct {
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name,omitempty"`
	Type       string                 `json:"type,omitempty"`
	Properties WebVnetRouteProperties `json:"properties"`
}

type WebVnetRouteProperties struct {
	StartAddress string `json:"startAddress,omitempty"`
	EndAddress   string `json:"endAddress,omitempty"`
	RouteType    string `json:"routeType,omitempty"`
}

// A pure ARM record on a site, slot or plan VNet connection.
type WebVnetGateway struct {
	ID         string                   `json:"id,omitempty"`
	Name       string                   `json:"name,omitempty"`
	Type       string                   `json:"type,omitempty"`
	Properties WebVnetGatewayProperties `json:"properties"`
}

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

// vpnPackageUri is the contract's one required member.
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

// WebApps_CreateOrUpdate/UpdateSwiftVirtualNetworkConnectionWithCheck and
// their Slot twins.
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
		writeVnetIntegrationError(w, site.Name, err)
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

// WebApps_CreateOrUpdate/UpdateVnetConnection and their Slot twins.
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

	// PATCH without a vnetResourceId updates the connection in place.
	vnetResourceID := req.Properties.VnetResourceID
	existing, hasExisting := webVnetConnections.Get(connID)
	if vnetResourceID == "" && hasExisting {
		vnetResourceID = existing.Properties.VnetResourceID
	}
	// Record a stored subnet by its canonical ID.
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
			// Derived from what the connection references, not a client claim.
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
		writeVnetIntegrationError(w, site.Name, err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, withPlanRoutes(conn, site.Properties.ServerFarmID))
}

// A subnet or the network itself; one the store lacks is the request's error.
func vnetConnectionVNetID(vnetResourceID string) (string, error) {
	if strings.Contains(vnetResourceID, "/subnets/") {
		subnet, ok := azureSubnetByID(vnetResourceID)
		if !ok {
			return "", &azureAddressFault{
				code:    "BadRequest",
				status:  http.StatusBadRequest,
				message: fmt.Sprintf("Subnet %s was not found.", vnetResourceID),
			}
		}
		return azureVNetIDOfSubnet(subnet.ID), nil
	}
	for _, vnet := range azureVnets.List() {
		if strings.EqualFold(vnet.ID, vnetResourceID) {
			return vnet.ID, nil
		}
	}
	return "", &azureAddressFault{
		code:    "BadRequest",
		status:  http.StatusBadRequest,
		message: fmt.Sprintf("Virtual network %s was not found.", vnetResourceID),
	}
}

// The request's own error when the network does not exist, the platform's
// otherwise.
func writeVnetIntegrationError(w http.ResponseWriter, siteName string, err error) {
	var fault *azureAddressFault
	if errors.As(err, &fault) {
		AzureError(w, fault.code, fault.message, fault.status)
		return
	}
	AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
		"failed to integrate site %q into VNet: %v", siteName, err)
}

// Record the connection and attach the site's containers to the virtual
// network's engine network: an Always On site's container is on it when this
// returns; any other attaches when a request starts it. Regional integration
// is single per site, so a new subnet connection replaces the old one, as a
// swift PUT re-points it.
func upsertVnetConnection(r *http.Request, site Site, conn WebVnetConnection) error {
	vnetID, err := vnetConnectionVNetID(conn.Properties.VnetResourceID)
	if err != nil {
		return err
	}
	resID := webResourceID(r)
	if conn.Properties.IsSwift {
		if prev, ok := swiftConnectionFor(resID); ok && prev.ID != conn.ID {
			deleteVnetConnection(r, prev)
		}
	}
	webVnetConnections.Put(conn.ID, conn)
	syncSiteVnetSubnetProperty(r)

	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.addNetworkLocked(vnetID)
	s := site
	// A changed integration restarts the container on its new network, as App
	// Service restarts the app: Always On at once, otherwise on the next request.
	if inst.containerID != "" && !inst.siteNetworkCurrentLocked() {
		inst.teardownLocked()
	}
	if siteAlwaysOn(&s) && siteRunsContainer(&s) {
		return inst.ensureStarted(&s)
	}
	return nil
}

// A non-empty subnet ID joins as a swift PUT does; an explicit empty value
// removes the integration and disconnects the containers.
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

// Reflect the regional integration onto the stored site, so every read reports
// it whichever spelling wrote it.
func syncSiteVnetSubnetProperty(r *http.Request) {
	subnet := ""
	if conn, ok := swiftConnectionFor(webResourceID(r)); ok {
		subnet = conn.Properties.VnetResourceID
	}
	webResourceStore(r).Update(webResourceID(r), func(s *Site) {
		s.Properties.VirtualNetworkSubnetID = subnet
	})
}

// Restart the container off the network unless another connection still uses
// the VNet.
func deleteVnetConnection(r *http.Request, conn WebVnetConnection) {
	webVnetConnections.Delete(conn.ID)
	syncSiteVnetSubnetProperty(r)
	gwPrefix := conn.ID + "/gateways/"
	for _, gw := range webVnetGateways.Filter(func(g WebVnetGateway) bool { return strings.HasPrefix(g.ID, gwPrefix) }) {
		webVnetGateways.Delete(gw.ID)
	}

	vnetID := strings.ToLower(azureVNetIDOfSubnet(conn.Properties.VnetResourceID))
	resID := webResourceID(r)
	for _, other := range siteVnetConnections(resID) {
		if strings.ToLower(azureVNetIDOfSubnet(other.Properties.VnetResourceID)) == vnetID {
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
	inst.removeNetworkLocked(vnetID)
	if inst.containerID != "" && !inst.siteNetworkCurrentLocked() {
		inst.teardownLocked()
		startAlwaysOnSite(site)
	}
}

func siteVnetConnections(resID string) []WebVnetConnection {
	prefix := resID + "/virtualNetworkConnections/"
	out := webVnetConnections.Filter(func(c WebVnetConnection) bool { return strings.HasPrefix(c.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func swiftConnectionFor(resID string) (WebVnetConnection, bool) {
	for _, c := range siteVnetConnections(resID) {
		if c.Properties.IsSwift && strings.Contains(c.Properties.VnetResourceID, "/subnets/") {
			return c, true
		}
	}
	return WebVnetConnection{}, false
}

// `<vnet>_<subnet>`, the name App Service lists it under.
func swiftConnectionName(subnetID string) string {
	vnet := vnetNameFromSubnetID(subnetID)
	subnet := subnetID[strings.LastIndex(subnetID, "/")+1:]
	return vnet + "_" + subnet
}

// The operation path is .../networkConfig/virtualNetwork, but the resource's ID,
// which terraform-provider-azurerm parses, is .../config/virtualNetwork.
func swiftConfigID(resID string) string { return armChildID(resID, "config", "virtualNetwork") }

// The routes member comes from the plan's routes for the same connection name.
func withPlanRoutes(conn WebVnetConnection, serverFarmID string) WebVnetConnection {
	if serverFarmID != "" {
		conn.Properties.Routes = planVnetRoutes(serverFarmID, conn.Name)
	}
	return conn
}

// The SHA-1 thumbprint every Azure certificate surface reports. The blob is
// carried verbatim (base64 or DER); the fingerprint is over the decoded
// certificate when it decodes.
func vnetCertThumbprint(certBlob string) string {
	raw := []byte(certBlob)
	if decoded, err := base64.StdEncoding.DecodeString(certBlob); err == nil {
		raw = decoded
	}
	sum := sha1.Sum(raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
