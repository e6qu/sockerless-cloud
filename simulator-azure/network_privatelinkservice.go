package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Private link services (Microsoft.Network/privateLinkServices) are the
// provider half of Azure Private Link: a service owner puts one in front of a
// standard load balancer, and consumers reach it through private endpoints in
// their own virtual networks. Three things carry the resource's behavior, and
// all three are implemented here rather than stored and echoed:
//
//   - Each NAT IP configuration takes a real address out of its subnet, through
//     the same fabric a caller-created network interface uses, and the resulting
//     interfaces are what the read-only networkInterfaces property reports.
//   - The privateEndpointConnections collection is the same set of objects a
//     private endpoint creates when it connects here. Approving or rejecting a
//     connection through this surface is what the consumer's private endpoint
//     reads back, and deleting one disconnects the endpoint.
//   - visibility and autoApproval decide who may see the service and whose
//     connections skip the owner's approval. CheckPrivateLinkServiceVisibility
//     and ListAutoApprovedPrivateLinkServices answer from those two sets, and
//     the connection a private endpoint opens starts Approved or Pending
//     according to them.

// PrivateLinkService mirrors Microsoft.Network/privateLinkServices.
type PrivateLinkService struct {
	azureNetworkResourceHeader
	ExtendedLocation *azureExtendedLocation       `json:"extendedLocation,omitempty"`
	Properties       PrivateLinkServiceProperties `json:"properties"`
}

// azureExtendedLocation is the ARM extended-location envelope (Azure edge zones).
type azureExtendedLocation struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// PrivateLinkServiceProperties holds the service's configuration and the
// read-only state the Microsoft.Network resource provider computes for it.
type PrivateLinkServiceProperties struct {
	LoadBalancerFrontendIPConfigurations []SubResource                       `json:"loadBalancerFrontendIpConfigurations,omitempty"`
	IPConfigurations                     []PrivateLinkServiceIPConfiguration `json:"ipConfigurations,omitempty"`
	DestinationIPAddress                 string                              `json:"destinationIPAddress,omitempty"`
	AccessMode                           string                              `json:"accessMode,omitempty"`
	NetworkInterfaces                    []SubResource                       `json:"networkInterfaces,omitempty"`
	ProvisioningState                    string                              `json:"provisioningState,omitempty"`
	PrivateEndpointConnections           []NetworkPrivateEndpointConnection  `json:"privateEndpointConnections,omitempty"`
	Visibility                           *azureResourceSet                   `json:"visibility,omitempty"`
	AutoApproval                         *azureResourceSet                   `json:"autoApproval,omitempty"`
	Fqdns                                []string                            `json:"fqdns,omitempty"`
	Alias                                string                              `json:"alias,omitempty"`
	EnableProxyProtocol                  bool                                `json:"enableProxyProtocol,omitempty"`
}

// azureResourceSet is the subscription list shared by the visibility and
// auto-approval properties.
type azureResourceSet struct {
	Subscriptions []string `json:"subscriptions,omitempty"`
}

// PrivateLinkServiceIPConfiguration is one NAT IP configuration of a private
// link service.
type PrivateLinkServiceIPConfiguration struct {
	ID         string                                      `json:"id,omitempty"`
	Name       string                                      `json:"name,omitempty"`
	Type       string                                      `json:"type,omitempty"`
	Etag       string                                      `json:"etag,omitempty"`
	Properties PrivateLinkServiceIPConfigurationProperties `json:"properties"`
}

// PrivateLinkServiceIPConfigurationProperties holds one NAT address.
type PrivateLinkServiceIPConfigurationProperties struct {
	PrivateIPAddress          string       `json:"privateIPAddress,omitempty"`
	PrivateIPAllocationMethod string       `json:"privateIPAllocationMethod,omitempty"`
	Subnet                    *SubResource `json:"subnet,omitempty"`
	Primary                   bool         `json:"primary,omitempty"`
	ProvisioningState         string       `json:"provisioningState,omitempty"`
	PrivateIPAddressVersion   string       `json:"privateIPAddressVersion,omitempty"`
}

// NetworkPrivateEndpointConnection is a connection between a private endpoint
// and the private link service it targets, as the service owner sees it.
type NetworkPrivateEndpointConnection struct {
	ID         string                                     `json:"id,omitempty"`
	Name       string                                     `json:"name,omitempty"`
	Type       string                                     `json:"type,omitempty"`
	Etag       string                                     `json:"etag,omitempty"`
	Properties NetworkPrivateEndpointConnectionProperties `json:"properties"`
}

// NetworkPrivateEndpointConnectionProperties holds the connection's state. The
// private endpoint itself is stored as a bare reference and rendered from the
// live endpoint record on read, so the connection can never report an endpoint
// that has since changed or been deleted.
type NetworkPrivateEndpointConnectionProperties struct {
	PrivateEndpoint                   *PrivateEndpoint                   `json:"privateEndpoint,omitempty"`
	PrivateLinkServiceConnectionState *PrivateLinkServiceConnectionState `json:"privateLinkServiceConnectionState,omitempty"`
	ProvisioningState                 string                             `json:"provisioningState,omitempty"`
	LinkIdentifier                    string                             `json:"linkIdentifier,omitempty"`
	PrivateEndpointLocation           string                             `json:"privateEndpointLocation,omitempty"`
}

// PrivateLinkServiceConnectionState is the approval state of a private endpoint
// connection, shared by the consumer and provider surfaces.
type PrivateLinkServiceConnectionState struct {
	Status          string `json:"status,omitempty"`
	Description     string `json:"description,omitempty"`
	ActionsRequired string `json:"actionsRequired,omitempty"`
}

var (
	azurePrivateLinkServices sim.Store[PrivateLinkService]
	// azurePLSConnections holds every private endpoint connection on a private
	// link service, keyed by the connection's ARM id, so the service's own
	// collection and the consumer endpoint's view read one object.
	azurePLSConnections sim.Store[NetworkPrivateEndpointConnection]
)

const azurePLSConnectionType = "Microsoft.Network/privateLinkServices/privateEndpointConnections"

func registerNetworkPrivateLinkServices(srv *sim.Server) {
	azurePrivateLinkServices = sim.MakeStore[PrivateLinkService](srv.DB(), "network_private_link_services")
	azurePLSConnections = sim.MakeStore[NetworkPrivateEndpointConnection](srv.DB(), "network_private_link_service_connections")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[PrivateLinkService]{
		collection:   "privateLinkServices",
		nameParam:    "serviceName",
		resourceType: "Microsoft.Network/privateLinkServices",
		store:        azurePrivateLinkServices,
		noUpdateTags: true,
		header: func(pls *PrivateLinkService) *azureNetworkResourceHeader {
			return &pls.azureNetworkResourceHeader
		},
		validate:    validatePrivateLinkService,
		provision:   provisionPrivateLinkService,
		project:     projectPrivateLinkService,
		afterDelete: deletePrivateLinkServiceResources,
	})

	registerPrivateLinkServiceConnections(srv)
	registerPrivateLinkServiceDiscovery(srv)
}

// validatePrivateLinkService applies the request-validation rules the resource
// provider applies before it provisions anything: a service needs at least one
// NAT IP configuration, and every configuration must name a subnet that exists.
func validatePrivateLinkService(w http.ResponseWriter, _ *http.Request, pls *PrivateLinkService) bool {
	if len(pls.Properties.IPConfigurations) == 0 {
		sim.AzureErrorf(w, "PrivateLinkServiceMustHaveIpConfiguration", http.StatusBadRequest,
			"Private link service must have at least one IP configuration.")
		return false
	}
	for _, ipcfg := range pls.Properties.IPConfigurations {
		subnetID := ""
		if ipcfg.Properties.Subnet != nil {
			subnetID = ipcfg.Properties.Subnet.ID
		}
		if _, ok := azureRequireSubnet(w, subnetID); !ok {
			return false
		}
	}
	return true
}

// provisionPrivateLinkService assigns the service's alias, provisions a network
// interface for every NAT IP configuration, and releases the interfaces an
// update dropped.
func provisionPrivateLinkService(ctx context.Context, pls *PrivateLinkService, previous *PrivateLinkService) error {
	// The alias is minted once and is what a consumer names in a manual
	// connection request, so it must survive every later update.
	if previous != nil && previous.Properties.Alias != "" {
		pls.Properties.Alias = previous.Properties.Alias
	} else {
		pls.Properties.Alias = fmt.Sprintf("%s.%s.%s.azure.privatelinkservice",
			pls.Name, generateUUID(), strings.ToLower(pls.Location))
	}
	if pls.Properties.AccessMode == "" {
		pls.Properties.AccessMode = "Default"
	}
	pls.Properties.ProvisioningState = "Succeeded"
	pls.Properties.NetworkInterfaces = nil

	live := map[string]bool{}
	for i := range pls.Properties.IPConfigurations {
		ipcfg := &pls.Properties.IPConfigurations[i]
		if ipcfg.Name == "" {
			ipcfg.Name = fmt.Sprintf("ipconfig%d", i+1)
		}
		ipcfg.ID = azureNetworkChildID(pls.ID, "ipConfigurations", ipcfg.Name)
		ipcfg.Type = "Microsoft.Network/privateLinkServices/ipConfigurations"
		ipcfg.Etag = azureNetworkEtag()
		if ipcfg.Properties.PrivateIPAllocationMethod == "" {
			ipcfg.Properties.PrivateIPAllocationMethod = "Dynamic"
		}
		if ipcfg.Properties.PrivateIPAddressVersion == "" {
			ipcfg.Properties.PrivateIPAddressVersion = "IPv4"
		}
		if i == 0 {
			ipcfg.Properties.Primary = true
		}
		ipcfg.Properties.ProvisioningState = "Succeeded"
		live[ipcfg.ID] = true

		nicID := privateLinkServiceNICID(pls.ID, ipcfg.Name)
		nic, err := azureCreatePlatformNIC(ctx, azurePlatformNICSpec{
			ID:               nicID,
			Name:             nicID[strings.LastIndex(nicID, "/")+1:],
			Location:         pls.Location,
			SubnetID:         ipcfg.Properties.Subnet.ID,
			IPConfigName:     ipcfg.Name,
			RequestedIP:      ipcfg.Properties.PrivateIPAddress,
			AllocationMethod: ipcfg.Properties.PrivateIPAllocationMethod,
		})
		if err != nil {
			return err
		}
		ipcfg.Properties.PrivateIPAddress = nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress
		pls.Properties.NetworkInterfaces = append(pls.Properties.NetworkInterfaces, SubResource{ID: nicID})
	}
	if previous == nil {
		return nil
	}
	for _, old := range previous.Properties.IPConfigurations {
		if !live[old.ID] {
			if err := azureDeletePlatformNIC(ctx, privateLinkServiceNICID(previous.ID, old.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// projectPrivateLinkService refreshes the read-only properties the resource
// provider recomputes on every read: the NAT addresses the subnet allocated and
// the live connection set.
func projectPrivateLinkService(pls *PrivateLinkService) {
	for i := range pls.Properties.IPConfigurations {
		ipcfg := &pls.Properties.IPConfigurations[i]
		if ip := azurePlatformNICPrivateIP(privateLinkServiceNICID(pls.ID, ipcfg.Name)); ip != "" {
			ipcfg.Properties.PrivateIPAddress = ip
		}
	}
	pls.Properties.PrivateEndpointConnections = privateLinkServiceConnections(pls.ID)
}

// deletePrivateLinkServiceResources releases the interfaces the service owned
// and disconnects every private endpoint that was connected to it.
func deletePrivateLinkServiceResources(ctx context.Context, id string, deleted PrivateLinkService) {
	for _, ipcfg := range deleted.Properties.IPConfigurations {
		_ = azureDeletePlatformNIC(ctx, privateLinkServiceNICID(id, ipcfg.Name))
	}
	for _, conn := range privateLinkServiceConnections(id) {
		azurePLSConnections.Delete(conn.ID)
	}
}

func privateLinkServiceNICID(plsID, ipConfigName string) string {
	// A private link service's interfaces live in the same resource group as
	// the service, under the ordinary network-interface type, named after the
	// service and the NAT configuration they realize.
	rgScope := plsID[:strings.Index(plsID, "/providers/")]
	name := plsID[strings.LastIndex(plsID, "/")+1:]
	return rgScope + "/providers/Microsoft.Network/networkInterfaces/" + name + "." + ipConfigName + ".nic"
}

func privateLinkServiceConnections(plsID string) []NetworkPrivateEndpointConnection {
	prefix := plsID + "/privateEndpointConnections/"
	conns := azurePLSConnections.Filter(func(c NetworkPrivateEndpointConnection) bool {
		return strings.HasPrefix(c.ID, prefix)
	})
	for i := range conns {
		projectNetworkPrivateEndpointConnection(&conns[i])
	}
	return conns
}

// projectNetworkPrivateEndpointConnection renders the stored connection's
// private-endpoint reference from the live endpoint record.
func projectNetworkPrivateEndpointConnection(conn *NetworkPrivateEndpointConnection) {
	if conn.Properties.PrivateEndpoint == nil || conn.Properties.PrivateEndpoint.ID == "" {
		return
	}
	if azurePrivateEndpoints == nil {
		return
	}
	pe, ok := azurePrivateEndpoints.Get(conn.Properties.PrivateEndpoint.ID)
	if !ok {
		return
	}
	projectPrivateEndpoint(&pe)
	conn.Properties.PrivateEndpoint = &pe
	conn.Properties.PrivateEndpointLocation = pe.Location
}

// registerPrivateLinkServiceConnections mounts the provider-side connection
// surface: the owner reads the connections consumers opened, approves or
// rejects them, and deletes the ones it no longer wants.
func registerPrivateLinkServiceConnections(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	base := armBase + "/privateLinkServices/{serviceName}/privateEndpointConnections"

	serviceID := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/privateLinkServices/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "serviceName"))
	}
	connID := func(r *http.Request) string {
		return azureNetworkChildID(serviceID(r), "privateEndpointConnections", sim.PathParam(r, "peConnectionName"))
	}

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := azurePrivateLinkServices.Get(serviceID(r)); !ok {
			azureNetworkResourceNotFound(w, "Microsoft.Network/privateLinkServices",
				sim.PathParam(r, "serviceName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		azureWriteList(w, privateLinkServiceConnections(serviceID(r)))
	})

	srv.HandleFunc("GET "+base+"/{peConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		conn, ok := azurePLSConnections.Get(connID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private endpoint connection %q was not found.", sim.PathParam(r, "peConnectionName"))
			return
		}
		projectNetworkPrivateEndpointConnection(&conn)
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	// UpdatePrivateEndpointConnection is how the service owner approves or
	// rejects a pending connection. Only the connection state is writable; the
	// endpoint reference and the link identifier belong to the consumer side.
	srv.HandleFunc("PUT "+base+"/{peConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		id := connID(r)
		var req NetworkPrivateEndpointConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.PrivateLinkServiceConnectionState == nil {
			sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: privateLinkServiceConnectionState is required.")
			return
		}
		if !azurePLSConnections.Update(id, func(conn *NetworkPrivateEndpointConnection) {
			conn.Properties.PrivateLinkServiceConnectionState = req.Properties.PrivateLinkServiceConnectionState
			conn.Etag = azureNetworkEtag()
		}) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private endpoint connection %q was not found.", sim.PathParam(r, "peConnectionName"))
			return
		}
		conn, _ := azurePLSConnections.Get(id)
		projectNetworkPrivateEndpointConnection(&conn)
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	srv.HandleFunc("DELETE "+base+"/{peConnectionName}", func(w http.ResponseWriter, r *http.Request) {
		if !azurePLSConnections.Delete(connID(r)) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// registerPrivateLinkServiceDiscovery mounts the two consumer-facing discovery
// operations. Both answer from the visibility and auto-approval sets the
// service owner configured, evaluated against the subscription making the call.
func registerPrivateLinkServiceDiscovery(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	subBase := azureNetworkSubBase()

	checkVisibility := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PrivateLinkServiceAlias string `json:"privateLinkServiceAlias"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		sub := sim.PathParam(r, "subscriptionId")
		visible := false
		for _, pls := range azurePrivateLinkServices.List() {
			if strings.EqualFold(pls.Properties.Alias, req.PrivateLinkServiceAlias) {
				visible = privateLinkServiceVisibleTo(pls, sub)
				break
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"visible": visible})
	}
	srv.HandleFunc("POST "+subBase+"/locations/{location}/checkPrivateLinkServiceVisibility", checkVisibility)
	srv.HandleFunc("POST "+armBase+"/locations/{location}/checkPrivateLinkServiceVisibility", checkVisibility)

	autoApproved := func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		location := sim.PathParam(r, "location")
		out := []map[string]any{}
		for _, pls := range azurePrivateLinkServices.List() {
			if !strings.EqualFold(pls.Location, location) {
				continue
			}
			if !privateLinkServiceAutoApprovesFor(pls, sub) {
				continue
			}
			out = append(out, map[string]any{"privateLinkService": pls.ID})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	}
	srv.HandleFunc("GET "+subBase+"/locations/{location}/autoApprovedPrivateLinkServices", autoApproved)
	srv.HandleFunc("GET "+armBase+"/locations/{location}/autoApprovedPrivateLinkServices", autoApproved)
}

// privateLinkServiceVisibleTo reports whether a subscription may see the
// service. A service in the default access mode is visible to everyone; a
// restricted one only to the subscriptions on its visibility list, where "*"
// stands for all of them.
func privateLinkServiceVisibleTo(pls PrivateLinkService, subscription string) bool {
	if strings.EqualFold(pls.Properties.AccessMode, "Restricted") {
		return azureResourceSetContains(pls.Properties.Visibility, subscription)
	}
	if pls.Properties.Visibility == nil || len(pls.Properties.Visibility.Subscriptions) == 0 {
		return true
	}
	return azureResourceSetContains(pls.Properties.Visibility, subscription)
}

// privateLinkServiceAutoApprovesFor reports whether a connection opened from
// the subscription skips the owner's approval.
func privateLinkServiceAutoApprovesFor(pls PrivateLinkService, subscription string) bool {
	return azureResourceSetContains(pls.Properties.AutoApproval, subscription)
}

func azureResourceSetContains(set *azureResourceSet, subscription string) bool {
	if set == nil {
		return false
	}
	for _, s := range set.Subscriptions {
		if s == "*" || strings.EqualFold(s, subscription) {
			return true
		}
	}
	return false
}
