package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func vnetNameFromSubnetID(subnetID string) string {
	const marker = "/virtualNetworks/"
	i := strings.Index(subnetID, marker)
	if i < 0 {
		return ""
	}
	rest := subnetID[i+len(marker):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

type VirtualNetwork struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties VNetProperties    `json:"properties"`
}

type VNetProperties struct {
	AddressSpace AddressSpace `json:"addressSpace"`
	// Subnets embeds full child documents, as Azure takes and returns them:
	// `az network vnet create --subnet-name` sends the subnet inline.
	Subnets                     []Subnet `json:"subnets,omitempty"`
	PrivateEndpointVNetPolicies string   `json:"privateEndpointVNetPolicies,omitempty"`
	ProvisioningState           string   `json:"provisioningState"`
}

type AddressSpace struct {
	AddressPrefixes []string `json:"addressPrefixes"`
}

type Subnet struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Type       string           `json:"type"`
	Properties SubnetProperties `json:"properties"`
}

type SubnetProperties struct {
	AddressPrefix        string        `json:"addressPrefix,omitempty"`
	AddressPrefixes      []string      `json:"addressPrefixes,omitempty"`
	NetworkSecurityGroup *NSGReference `json:"networkSecurityGroup,omitempty"`
	NatGateway           *SubResource  `json:"natGateway,omitempty"`
	// Its routes override the system routes for the addresses they cover; a
	// next-hop query resolves against them.
	RouteTable                        *SubResource       `json:"routeTable,omitempty"`
	Delegations                       []SubnetDelegation `json:"delegations,omitempty"`
	ProvisioningState                 string             `json:"provisioningState"`
	PrivateEndpointNetworkPolicies    string             `json:"privateEndpointNetworkPolicies,omitempty"`
	PrivateLinkServiceNetworkPolicies string             `json:"privateLinkServiceNetworkPolicies,omitempty"`
	// The policy resource reports the subnets that reference it from here.
	ServiceEndpointPolicies []SubResource `json:"serviceEndpointPolicies,omitempty"`
}

type NSGReference struct {
	ID string `json:"id"`
}

type SubnetDelegation struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Properties struct {
		ServiceName string   `json:"serviceName"`
		Actions     []string `json:"actions,omitempty"`
	} `json:"properties"`
}

type NetworkSecurityGroup struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties NSGProperties     `json:"properties"`
}

type NSGProperties struct {
	SecurityRules     []SecurityRule `json:"securityRules,omitempty"`
	ProvisioningState string         `json:"provisioningState"`
}

type SecurityRule struct {
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type,omitempty"`
	Properties SecurityRuleProperties `json:"properties"`
}

type SecurityRuleProperties struct {
	Protocol                   string   `json:"protocol"`
	SourcePortRange            string   `json:"sourcePortRange,omitempty"`
	DestinationPortRange       string   `json:"destinationPortRange,omitempty"`
	SourceAddressPrefix        string   `json:"sourceAddressPrefix,omitempty"`
	DestinationAddressPrefix   string   `json:"destinationAddressPrefix,omitempty"`
	SourcePortRanges           []string `json:"sourcePortRanges,omitempty"`
	DestinationPortRanges      []string `json:"destinationPortRanges,omitempty"`
	SourceAddressPrefixes      []string `json:"sourceAddressPrefixes,omitempty"`
	DestinationAddressPrefixes []string `json:"destinationAddressPrefixes,omitempty"`
	Access                     string   `json:"access"`
	Priority                   int      `json:"priority"`
	Direction                  string   `json:"direction"`
	ProvisioningState          string   `json:"provisioningState,omitempty"`
	// A source application security group contributes its members' addresses to
	// the match; a destination group scopes the rule to its member interfaces.
	SourceApplicationSecurityGroups      []SubResource `json:"sourceApplicationSecurityGroups,omitempty"`
	DestinationApplicationSecurityGroups []SubResource `json:"destinationApplicationSecurityGroups,omitempty"`
}

type NatGateway struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Sku        *SkuName          `json:"sku,omitempty"`
	Properties NatGatewayProps   `json:"properties"`
}

type NatGatewayProps struct {
	IdleTimeoutInMinutes int           `json:"idleTimeoutInMinutes,omitempty"`
	PublicIPAddresses    []SubResource `json:"publicIpAddresses,omitempty"`
	PublicIPPrefixes     []SubResource `json:"publicIpPrefixes,omitempty"`
	Subnets              []SubResource `json:"subnets,omitempty"`
	ProvisioningState    string        `json:"provisioningState,omitempty"`
}

// SkuName is the name-only SKU envelope.
type SkuName struct {
	Name string `json:"name"`
	// terraform-provider-azurerm reads Tier as sku_tier, a forces-replacement field
	// on public IPs, prefixes and load balancers, so the read-back needs a
	// default.
	Tier string `json:"tier,omitempty"`
}

type SubResource struct {
	ID string `json:"id"`
}

type RouteTable struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties RouteTableProps   `json:"properties"`
}

type RouteTableProps struct {
	DisableBgpRoutePropagation bool         `json:"disableBgpRoutePropagation,omitempty"`
	Routes                     []RouteEntry `json:"routes,omitempty"`
	ProvisioningState          string       `json:"provisioningState,omitempty"`
}

// A route is addressable inline (routeTable.properties.routes) and as a
// sub-resource through the Routes client.
type RouteEntry struct {
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Type       string          `json:"type,omitempty"`
	Properties RouteEntryProps `json:"properties"`
}

type RouteEntryProps struct {
	AddressPrefix     string `json:"addressPrefix,omitempty"`
	NextHopType       string `json:"nextHopType"`
	NextHopIPAddress  string `json:"nextHopIpAddress,omitempty"`
	ProvisioningState string `json:"provisioningState,omitempty"`
}

type VirtualNetworkPeering struct {
	ID         string                     `json:"id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Type       string                     `json:"type,omitempty"`
	Properties VirtualNetworkPeeringProps `json:"properties"`
}

type VirtualNetworkPeeringProps struct {
	AllowVirtualNetworkAccess bool          `json:"allowVirtualNetworkAccess,omitempty"`
	AllowForwardedTraffic     bool          `json:"allowForwardedTraffic,omitempty"`
	AllowGatewayTransit       bool          `json:"allowGatewayTransit,omitempty"`
	UseRemoteGateways         bool          `json:"useRemoteGateways,omitempty"`
	RemoteVirtualNetwork      *SubResource  `json:"remoteVirtualNetwork,omitempty"`
	RemoteAddressSpace        *AddressSpace `json:"remoteAddressSpace,omitempty"`
	PeeringState              string        `json:"peeringState,omitempty"`
	PeeringSyncLevel          string        `json:"peeringSyncLevel,omitempty"`
	ProvisioningState         string        `json:"provisioningState,omitempty"`
}

type InterfaceTapConfiguration struct {
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Type       string         `json:"type,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

var (
	azureVnets             sim.Store[VirtualNetwork]
	azureSubnets           sim.Store[Subnet]
	azureNSGs              sim.Store[NetworkSecurityGroup]
	azureNatGateways       sim.Store[NatGateway]
	azureRouteTables       sim.Store[RouteTable]
	azureVNetPeerings      sim.Store[VirtualNetworkPeering]
	azureLBInboundNatRules sim.Store[LoadBalancerChild]
	azureNICTapConfigs     sim.Store[InterfaceTapConfiguration]
)

func registerNetwork(srv *sim.Server) {
	vnets := sim.MakeStore[VirtualNetwork](srv.DB(), "network_vnets")
	subnets := sim.MakeStore[Subnet](srv.DB(), "network_subnets")
	nsgs := sim.MakeStore[NetworkSecurityGroup](srv.DB(), "network_nsgs")
	azureVnets = vnets
	azureSubnets = subnets
	azureNSGs = nsgs
	azureVNetPeerings = sim.MakeStore[VirtualNetworkPeering](srv.DB(), "network_vnet_peerings")
	azureLBInboundNatRules = sim.MakeStore[LoadBalancerChild](srv.DB(), "network_lb_inbound_nat_rules")
	azureNICTapConfigs = sim.MakeStore[InterfaceTapConfiguration](srv.DB(), "network_nic_tap_configs")
	// The move hooks read their stores through accessors at request time, so they
	// all register here though the stores are assigned across several register
	// functions.
	registerNetworkResourceMoveHooks()

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"

	registerVirtualNetworks(srv, armBase, vnets, subnets)
	registerNetworkSecurityGroups(srv, armBase, nsgs)
	registerNATGatewaysAndRouteTables(srv, armBase)

	registerNetworkListsAndTags(srv)
	registerVirtualNetworkSubResources(srv)
	registerNSGDefaultRules(srv)
	registerLoadBalancerSubResources(srv)
	registerNetworkInterfaceSubResources(srv)
	registerRouteTableRoutes(srv)
	registerNetworkMoreOps(srv)

	registerNetworkApplicationSecurityGroups(srv)
	registerNetworkPrivateLinkServices(srv)
	registerNetworkPrivateEndpoints(srv)
	registerNetworkProfiles(srv)
	registerNetworkVirtualNetworkTaps(srv)
	registerNetworkServiceEndpointPolicies(srv)
	registerNetworkApplicationGateways(srv)
	registerNetworkManagers(srv)
	registerNetworkWatchers(srv)
}

// The TagsObject body every Microsoft.Network UpdateTags takes.
type azureTagsObject struct {
	Tags map[string]string `json:"tags,omitempty"`
}

func azureNetworkArmBase() string {
	return "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network"
}

func azureNetworkSubBase() string {
	return "/subscriptions/{subscriptionId}/providers/Microsoft.Network"
}

// Emits [] rather than null.
func azureWriteList[T any](w http.ResponseWriter, items []T) {
	if items == nil {
		items = []T{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
}

func normalizeRouteEntries(routeTableID string, routes []RouteEntry) []RouteEntry {
	out := make([]RouteEntry, 0, len(routes))
	for _, rt := range routes {
		out = append(out, normalizeRouteEntry(routeTableID, rt.Name, rt))
	}
	return out
}

func normalizeRouteEntry(routeTableID, name string, route RouteEntry) RouteEntry {
	if name == "" {
		name = route.Name
	}
	route.Name = name
	if name != "" {
		route.ID = routeTableID + "/routes/" + name
	}
	route.Type = "Microsoft.Network/routeTables/routes"
	route.Properties.ProvisioningState = "Succeeded"
	return route
}

// registerNetworkListsAndTags adds ListAll, the resource-group List where the
// per-resource registrar lacks it, and UpdateTags for each top-level type.
func registerNetworkListsAndTags(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	subBase := azureNetworkSubBase()

	rgPrefix := func(r *http.Request, resourceType string) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", resourceType) + "/"
	}

	srv.HandleFunc("GET "+armBase+"/virtualNetworks", func(w http.ResponseWriter, r *http.Request) {
		prefix := rgPrefix(r, "virtualNetworks")
		azureWriteList(w, azureRefreshedVnets(prefix))
	})
	srv.HandleFunc("GET "+subBase+"/virtualNetworks", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		azureWriteList(w, azureRefreshedVnets(prefix))
	})
	srv.HandleFunc("PATCH "+armBase+"/virtualNetworks/{vnetName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "virtualNetworks", sim.PathParam(r, "vnetName"))
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azureVnets.Update(id, func(v *VirtualNetwork) { v.Tags = set })
		}) {
			return
		}
		vnet, _ := azureVnets.Get(id)
		vnet.Properties.Subnets = azureSubnets.Filter(func(s Subnet) bool { return strings.HasPrefix(s.ID, id+"/subnets/") })
		sim.WriteJSON(w, http.StatusOK, vnet)
	})

	srv.HandleFunc("GET "+armBase+"/networkSecurityGroups", func(w http.ResponseWriter, r *http.Request) {
		prefix := rgPrefix(r, "networkSecurityGroups")
		azureWriteList(w, azureNSGs.Filter(func(n NetworkSecurityGroup) bool { return strings.HasPrefix(n.ID, prefix) }))
	})
	srv.HandleFunc("GET "+subBase+"/networkSecurityGroups", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		azureWriteList(w, azureNSGs.Filter(func(n NetworkSecurityGroup) bool { return strings.HasPrefix(n.ID, prefix) }))
	})
	srv.HandleFunc("PATCH "+armBase+"/networkSecurityGroups/{nsgName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkSecurityGroups", sim.PathParam(r, "nsgName"))
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azureNSGs.Update(id, func(n *NetworkSecurityGroup) { n.Tags = set })
		}) {
			return
		}
		nsg, _ := azureNSGs.Get(id)
		sim.WriteJSON(w, http.StatusOK, nsg)
	})

	srv.HandleFunc("GET "+armBase+"/routeTables", func(w http.ResponseWriter, r *http.Request) {
		prefix := rgPrefix(r, "routeTables")
		azureWriteList(w, azureRouteTables.Filter(func(rt RouteTable) bool { return strings.HasPrefix(rt.ID, prefix) }))
	})
	srv.HandleFunc("GET "+subBase+"/routeTables", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		azureWriteList(w, azureRouteTables.Filter(func(rt RouteTable) bool { return strings.HasPrefix(rt.ID, prefix) }))
	})
	srv.HandleFunc("PATCH "+armBase+"/routeTables/{name}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "routeTables", sim.PathParam(r, "name"))
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azureRouteTables.Update(id, func(rt *RouteTable) { rt.Tags = set })
		}) {
			return
		}
		rt, _ := azureRouteTables.Get(id)
		sim.WriteJSON(w, http.StatusOK, rt)
	})

	srv.HandleFunc("GET "+subBase+"/natGateways", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		items := azureNatGateways.Filter(func(gw NatGateway) bool { return strings.HasPrefix(gw.ID, prefix) })
		for i := range items {
			items[i].Properties.Subnets = azureSubnetsForNatGateway(items[i].ID)
		}
		azureWriteList(w, items)
	})
	srv.HandleFunc("PATCH "+armBase+"/natGateways/{name}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "natGateways", sim.PathParam(r, "name"))
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azureNatGateways.Update(id, func(gw *NatGateway) { gw.Tags = set })
		}) {
			return
		}
		gw, _ := azureNatGateways.Get(id)
		gw.Properties.Subnets = azureSubnetsForNatGateway(gw.ID)
		sim.WriteJSON(w, http.StatusOK, gw)
	})

	srv.HandleFunc("GET "+subBase+"/loadBalancers", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		azureWriteList(w, azureLBs.Filter(func(lb LoadBalancer) bool { return strings.HasPrefix(lb.ID, prefix) }))
	})
	srv.HandleFunc("PATCH "+armBase+"/loadBalancers/{loadBalancerName}", func(w http.ResponseWriter, r *http.Request) {
		id := azureLoadBalancerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "loadBalancerName"))
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azureLBs.Update(id, func(lb *LoadBalancer) { lb.Tags = set })
		}) {
			return
		}
		lb, _ := azureLBs.Get(id)
		sim.WriteJSON(w, http.StatusOK, lb)
	})

	srv.HandleFunc("GET "+subBase+"/networkInterfaces", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		azureWriteList(w, azureNICs.Filter(func(n NetworkInterface) bool { return strings.HasPrefix(n.ID, prefix) }))
	})
	srv.HandleFunc("PATCH "+armBase+"/networkInterfaces/{networkInterfaceName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkInterfaces", sim.PathParam(r, "networkInterfaceName"))
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azureNICs.Update(id, func(n *NetworkInterface) { n.Tags = set })
		}) {
			return
		}
		nic, _ := azureNICs.Get(id)
		sim.WriteJSON(w, http.StatusOK, nic)
	})

	srv.HandleFunc("GET "+subBase+"/publicIPAddresses", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		azureWriteList(w, azurePublicIPs.Filter(func(p PublicIPAddress) bool { return strings.HasPrefix(p.ID, prefix) }))
	})
	srv.HandleFunc("PATCH "+armBase+"/publicIPAddresses/{publicIPName}", func(w http.ResponseWriter, r *http.Request) {
		id := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "publicIPAddresses", sim.PathParam(r, "publicIPName"))
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azurePublicIPs.Update(id, func(p *PublicIPAddress) { p.Tags = set })
		}) {
			return
		}
		pip, _ := azurePublicIPs.Get(id)
		sim.WriteJSON(w, http.StatusOK, pip)
	})

	srv.HandleFunc("GET "+subBase+"/publicIPPrefixes", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		azureWriteList(w, azurePublicIPPrefixes.Filter(func(p PublicIPPrefix) bool { return strings.HasPrefix(p.ID, prefix) }))
	})
	srv.HandleFunc("PATCH "+armBase+"/publicIPPrefixes/{publicIPPrefixName}", func(w http.ResponseWriter, r *http.Request) {
		id := azurePublicIPPrefixID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "publicIPPrefixName"))
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azurePublicIPPrefixes.Update(id, func(p *PublicIPPrefix) { p.Tags = set })
		}) {
			return
		}
		prefix, _ := azurePublicIPPrefixes.Get(id)
		sim.WriteJSON(w, http.StatusOK, prefix)
	})
}

// It writes the error and returns false for a malformed body or an absent
// resource.
func azureApplyTagsPatch(w http.ResponseWriter, r *http.Request, update func(map[string]string) bool) bool {
	var req azureTagsObject
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if req.Tags == nil {
		req.Tags = map[string]string{}
	}
	if !update(req.Tags) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The resource was not found.")
		return false
	}
	return true
}

// Each with its subnet references refreshed from the subnet store.
func azureRefreshedVnets(prefix string) []VirtualNetwork {
	vnets := azureVnets.Filter(func(v VirtualNetwork) bool { return strings.HasPrefix(v.ID, prefix) })
	for i := range vnets {
		vnets[i].Properties.Subnets = azureSubnets.Filter(func(s Subnet) bool { return strings.HasPrefix(s.ID, vnets[i].ID+"/subnets/") })
	}
	return vnets
}

func registerVirtualNetworkSubResources(srv *sim.Server) {
	armBase := azureNetworkArmBase()

	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/subnets", func(w http.ResponseWriter, r *http.Request) {
		vnetID := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "virtualNetworks", sim.PathParam(r, "vnetName"))
		azureWriteList(w, azureSubnets.Filter(func(s Subnet) bool { return strings.HasPrefix(s.ID, vnetID+"/subnets/") }))
	})

	// POST operations with no resource body; Azure answers a terminal 200.
	policyOp := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	srv.HandleFunc("POST "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}/PrepareNetworkPolicies", policyOp)
	srv.HandleFunc("POST "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}/UnprepareNetworkPolicies", policyOp)

	peeringID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "virtualNetworks", sim.PathParam(r, "vnetName"), "virtualNetworkPeerings", sim.PathParam(r, "peeringName"))
	}
	srv.HandleFunc("PUT "+armBase+"/virtualNetworks/{vnetName}/virtualNetworkPeerings/{peeringName}", func(w http.ResponseWriter, r *http.Request) {
		var req VirtualNetworkPeering
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := peeringID(r)
		peering := VirtualNetworkPeering{
			ID:         id,
			Name:       sim.PathParam(r, "peeringName"),
			Type:       "Microsoft.Network/virtualNetworks/virtualNetworkPeerings",
			Properties: req.Properties,
		}
		peering.Properties.ProvisioningState = "Succeeded"
		if peering.Properties.PeeringState == "" {
			peering.Properties.PeeringState = "Initiated"
		}
		azureVNetPeerings.Put(id, peering)
		sim.WriteJSON(w, http.StatusOK, peering)
	})
	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/virtualNetworkPeerings/{peeringName}", func(w http.ResponseWriter, r *http.Request) {
		peering, ok := azureVNetPeerings.Get(peeringID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Virtual network peering %q not found.", sim.PathParam(r, "peeringName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, peering)
	})
	srv.HandleFunc("DELETE "+armBase+"/virtualNetworks/{vnetName}/virtualNetworkPeerings/{peeringName}", func(w http.ResponseWriter, r *http.Request) {
		azureVNetPeerings.Delete(peeringID(r))
		w.WriteHeader(http.StatusOK)
	})
	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/virtualNetworkPeerings", func(w http.ResponseWriter, r *http.Request) {
		vnetID := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "virtualNetworks", sim.PathParam(r, "vnetName"))
		azureWriteList(w, azureVNetPeerings.Filter(func(p VirtualNetworkPeering) bool { return strings.HasPrefix(p.ID, vnetID+"/virtualNetworkPeerings/") }))
	})

	// Checks the candidate against the virtual network's stored IP
	// configurations.
	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/CheckIPAddressAvailability", func(w http.ResponseWriter, r *http.Request) {
		ip := r.URL.Query().Get("ipAddress")
		available := true
		for _, nic := range azureNICs.List() {
			for _, cfg := range nic.Properties.IPConfigurations {
				if ip != "" && cfg.Properties.PrivateIPAddress == ip {
					available = false
				}
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"available":            available,
			"availableIPAddresses": []string{},
		})
	})

	// Answers an empty usage set.
	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/usages", func(w http.ResponseWriter, r *http.Request) {
		azureWriteList(w, []any{})
	})
}

// The fixed platform default rules Azure applies to every network security
// group.
func registerNSGDefaultRules(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	srv.HandleFunc("GET "+armBase+"/networkSecurityGroups/{nsgName}/defaultSecurityRules", func(w http.ResponseWriter, r *http.Request) {
		nsgID := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkSecurityGroups", sim.PathParam(r, "nsgName"))
		if _, ok := azureNSGs.Get(nsgID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "NSG %q not found.", sim.PathParam(r, "nsgName"))
			return
		}
		azureWriteList(w, azureDefaultSecurityRules(nsgID))
	})
	srv.HandleFunc("GET "+armBase+"/networkSecurityGroups/{nsgName}/defaultSecurityRules/{defaultSecurityRuleName}", func(w http.ResponseWriter, r *http.Request) {
		nsgID := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkSecurityGroups", sim.PathParam(r, "nsgName"))
		want := sim.PathParam(r, "defaultSecurityRuleName")
		for _, rule := range azureDefaultSecurityRules(nsgID) {
			if strings.EqualFold(rule.Name, want) {
				sim.WriteJSON(w, http.StatusOK, rule)
				return
			}
		}
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Default security rule %q not found.", want)
	})
}

func azureDefaultSecurityRules(nsgID string) []SecurityRule {
	defs := []struct {
		name     string
		access   string
		dir      string
		priority int
		src, dst string
	}{
		{"AllowVnetInBound", "Allow", "Inbound", 65000, "VirtualNetwork", "VirtualNetwork"},
		{"AllowAzureLoadBalancerInBound", "Allow", "Inbound", 65001, "AzureLoadBalancer", "*"},
		{"DenyAllInBound", "Deny", "Inbound", 65500, "*", "*"},
		{"AllowVnetOutBound", "Allow", "Outbound", 65000, "VirtualNetwork", "VirtualNetwork"},
		{"AllowInternetOutBound", "Allow", "Outbound", 65001, "*", "Internet"},
		{"DenyAllOutBound", "Deny", "Outbound", 65500, "*", "*"},
	}
	rules := make([]SecurityRule, 0, len(defs))
	for _, d := range defs {
		rules = append(rules, SecurityRule{
			ID:   nsgID + "/defaultSecurityRules/" + d.name,
			Name: d.name,
			Type: "Microsoft.Network/networkSecurityGroups/defaultSecurityRules",
			Properties: SecurityRuleProperties{
				Protocol:                 "*",
				SourcePortRange:          "*",
				DestinationPortRange:     "*",
				SourceAddressPrefix:      d.src,
				DestinationAddressPrefix: d.dst,
				Access:                   d.access,
				Priority:                 d.priority,
				Direction:                d.dir,
				ProvisioningState:        "Succeeded",
			},
		})
	}
	return rules
}

func registerLoadBalancerSubResources(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	lbID := func(r *http.Request) string {
		return azureLoadBalancerID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "loadBalancerName"))
	}
	childList := func(collection string, pick func(LoadBalancer) []LoadBalancerChild) {
		srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}/"+collection, func(w http.ResponseWriter, r *http.Request) {
			lb, ok := azureLBs.Get(lbID(r))
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
				return
			}
			azureWriteList(w, pick(lb))
		})
	}
	childList("backendAddressPools", func(lb LoadBalancer) []LoadBalancerChild { return lb.Properties.BackendAddressPools })
	childList("frontendIPConfigurations", func(lb LoadBalancer) []LoadBalancerChild { return lb.Properties.FrontendIPConfigurations })
	childList("loadBalancingRules", func(lb LoadBalancer) []LoadBalancerChild { return lb.Properties.LoadBalancingRules })
	childList("probes", func(lb LoadBalancer) []LoadBalancerChild { return lb.Properties.Probes })

	srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}/frontendIPConfigurations/{frontendIPConfigurationName}", func(w http.ResponseWriter, r *http.Request) {
		lb, ok := azureLBs.Get(lbID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
			return
		}
		want := sim.PathParam(r, "frontendIPConfigurationName")
		for _, child := range lb.Properties.FrontendIPConfigurations {
			if strings.EqualFold(child.Name, want) {
				sim.WriteJSON(w, http.StatusOK, child)
				return
			}
		}
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Frontend IP configuration %q not found.", want)
	})

	inboundID := func(r *http.Request) string {
		return lbID(r) + "/inboundNatRules/" + sim.PathParam(r, "inboundNatRuleName")
	}
	srv.HandleFunc("PUT "+armBase+"/loadBalancers/{loadBalancerName}/inboundNatRules/{inboundNatRuleName}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := azureLBs.Get(lbID(r)); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
			return
		}
		var req LoadBalancerChild
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := inboundID(r)
		rule := normalizeLoadBalancerChild(lbID(r), "inboundNatRules", "Microsoft.Network/loadBalancers/inboundNatRules", sim.PathParam(r, "inboundNatRuleName"), req)
		rule.ID = id
		azureLBInboundNatRules.Put(id, rule)
		sim.WriteJSON(w, http.StatusOK, rule)
	})
	srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}/inboundNatRules/{inboundNatRuleName}", func(w http.ResponseWriter, r *http.Request) {
		rule, ok := azureLBInboundNatRules.Get(inboundID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Inbound NAT rule %q not found.", sim.PathParam(r, "inboundNatRuleName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, rule)
	})
	srv.HandleFunc("DELETE "+armBase+"/loadBalancers/{loadBalancerName}/inboundNatRules/{inboundNatRuleName}", func(w http.ResponseWriter, r *http.Request) {
		azureLBInboundNatRules.Delete(inboundID(r))
		w.WriteHeader(http.StatusOK)
	})
	srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}/inboundNatRules", func(w http.ResponseWriter, r *http.Request) {
		prefix := lbID(r) + "/inboundNatRules/"
		azureWriteList(w, azureLBInboundNatRules.Filter(func(c LoadBalancerChild) bool { return strings.HasPrefix(c.ID, prefix) }))
	})

	// Answers an empty collection: the parent PUT's outboundRules are not
	// captured.
	srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}/outboundRules", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := azureLBs.Get(lbID(r)); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Load balancer %q not found.", sim.PathParam(r, "loadBalancerName"))
			return
		}
		azureWriteList(w, []LoadBalancerChild{})
	})
	srv.HandleFunc("GET "+armBase+"/loadBalancers/{loadBalancerName}/outboundRules/{outboundRuleName}", func(w http.ResponseWriter, r *http.Request) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Outbound rule %q not found.", sim.PathParam(r, "outboundRuleName"))
	})
}

func registerNetworkInterfaceSubResources(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	nicID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkInterfaces", sim.PathParam(r, "networkInterfaceName"))
	}

	srv.HandleFunc("GET "+armBase+"/networkInterfaces/{networkInterfaceName}/ipConfigurations", func(w http.ResponseWriter, r *http.Request) {
		nic, ok := azureNICs.Get(nicID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Network interface %q not found.", sim.PathParam(r, "networkInterfaceName"))
			return
		}
		azureWriteList(w, nic.Properties.IPConfigurations)
	})
	srv.HandleFunc("GET "+armBase+"/networkInterfaces/{networkInterfaceName}/ipConfigurations/{ipConfigurationName}", func(w http.ResponseWriter, r *http.Request) {
		nic, ok := azureNICs.Get(nicID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Network interface %q not found.", sim.PathParam(r, "networkInterfaceName"))
			return
		}
		want := sim.PathParam(r, "ipConfigurationName")
		for _, cfg := range nic.Properties.IPConfigurations {
			if strings.EqualFold(cfg.Name, want) {
				sim.WriteJSON(w, http.StatusOK, cfg)
				return
			}
		}
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "IP configuration %q not found.", want)
	})

	tapID := func(r *http.Request) string {
		return nicID(r) + "/tapConfigurations/" + sim.PathParam(r, "tapConfigurationName")
	}
	srv.HandleFunc("PUT "+armBase+"/networkInterfaces/{networkInterfaceName}/tapConfigurations/{tapConfigurationName}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := azureNICs.Get(nicID(r)); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Network interface %q not found.", sim.PathParam(r, "networkInterfaceName"))
			return
		}
		var req InterfaceTapConfiguration
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := tapID(r)
		if req.Properties == nil {
			req.Properties = map[string]any{}
		}
		req.Properties["provisioningState"] = "Succeeded"
		cfg := InterfaceTapConfiguration{
			ID:         id,
			Name:       sim.PathParam(r, "tapConfigurationName"),
			Type:       "Microsoft.Network/networkInterfaces/tapConfigurations",
			Properties: req.Properties,
		}
		azureNICTapConfigs.Put(id, cfg)
		sim.WriteJSON(w, http.StatusOK, cfg)
	})
	srv.HandleFunc("GET "+armBase+"/networkInterfaces/{networkInterfaceName}/tapConfigurations/{tapConfigurationName}", func(w http.ResponseWriter, r *http.Request) {
		cfg, ok := azureNICTapConfigs.Get(tapID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Tap configuration %q not found.", sim.PathParam(r, "tapConfigurationName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, cfg)
	})
	srv.HandleFunc("DELETE "+armBase+"/networkInterfaces/{networkInterfaceName}/tapConfigurations/{tapConfigurationName}", func(w http.ResponseWriter, r *http.Request) {
		azureNICTapConfigs.Delete(tapID(r))
		w.WriteHeader(http.StatusOK)
	})
	srv.HandleFunc("GET "+armBase+"/networkInterfaces/{networkInterfaceName}/tapConfigurations", func(w http.ResponseWriter, r *http.Request) {
		prefix := nicID(r) + "/tapConfigurations/"
		azureWriteList(w, azureNICTapConfigs.Filter(func(c InterfaceTapConfiguration) bool { return strings.HasPrefix(c.ID, prefix) }))
	})

	// Answer empty result sets: effective rules are not computed.
	srv.HandleFunc("POST "+armBase+"/networkInterfaces/{networkInterfaceName}/effectiveRouteTable", func(w http.ResponseWriter, r *http.Request) {
		azureWriteList(w, []any{})
	})
	srv.HandleFunc("POST "+armBase+"/networkInterfaces/{networkInterfaceName}/effectiveNetworkSecurityGroups", func(w http.ResponseWriter, r *http.Request) {
		azureWriteList(w, []any{})
	})
}

// Each route is stored inline on its table, so the table's own Get stays
// consistent.
func registerRouteTableRoutes(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	tableID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "routeTables", sim.PathParam(r, "routeTableName"))
	}
	srv.HandleFunc("PUT "+armBase+"/routeTables/{routeTableName}/routes/{routeName}", func(w http.ResponseWriter, r *http.Request) {
		id := tableID(r)
		routeName := sim.PathParam(r, "routeName")
		var req RouteEntry
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		route := normalizeRouteEntry(id, routeName, req)
		if !azureRouteTables.Update(id, func(rt *RouteTable) {
			found := false
			for i := range rt.Properties.Routes {
				if strings.EqualFold(rt.Properties.Routes[i].Name, routeName) {
					rt.Properties.Routes[i] = route
					found = true
					break
				}
			}
			if !found {
				rt.Properties.Routes = append(rt.Properties.Routes, route)
			}
		}) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Route table %q not found.", sim.PathParam(r, "routeTableName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, route)
	})
	srv.HandleFunc("GET "+armBase+"/routeTables/{routeTableName}/routes/{routeName}", func(w http.ResponseWriter, r *http.Request) {
		rt, ok := azureRouteTables.Get(tableID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Route table %q not found.", sim.PathParam(r, "routeTableName"))
			return
		}
		want := sim.PathParam(r, "routeName")
		for _, route := range rt.Properties.Routes {
			if strings.EqualFold(route.Name, want) {
				sim.WriteJSON(w, http.StatusOK, route)
				return
			}
		}
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Route %q not found.", want)
	})
	srv.HandleFunc("DELETE "+armBase+"/routeTables/{routeTableName}/routes/{routeName}", func(w http.ResponseWriter, r *http.Request) {
		routeName := sim.PathParam(r, "routeName")
		azureRouteTables.Update(tableID(r), func(rt *RouteTable) {
			filtered := rt.Properties.Routes[:0]
			for _, route := range rt.Properties.Routes {
				if !strings.EqualFold(route.Name, routeName) {
					filtered = append(filtered, route)
				}
			}
			rt.Properties.Routes = filtered
		})
		w.WriteHeader(http.StatusOK)
	})
	srv.HandleFunc("GET "+armBase+"/routeTables/{routeTableName}/routes", func(w http.ResponseWriter, r *http.Request) {
		rt, ok := azureRouteTables.Get(tableID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Route table %q not found.", sim.PathParam(r, "routeTableName"))
			return
		}
		azureWriteList(w, rt.Properties.Routes)
	})
}

func azureSubnetsForNatGateway(natGatewayID string) []SubResource {
	subnets := azureSubnets.Filter(func(sn Subnet) bool {
		return sn.Properties.NatGateway != nil && strings.EqualFold(sn.Properties.NatGateway.ID, natGatewayID)
	})
	if len(subnets) == 0 {
		return nil
	}
	refs := make([]SubResource, 0, len(subnets))
	for _, sn := range subnets {
		refs = append(refs, SubResource{ID: sn.ID})
	}
	return refs
}

func azureBuildSubnet(resourceID, subnetName string, req Subnet) Subnet {
	privateEndpointPolicies := req.Properties.PrivateEndpointNetworkPolicies
	if privateEndpointPolicies == "" {
		privateEndpointPolicies = "Disabled"
	}
	privateLinkPolicies := req.Properties.PrivateLinkServiceNetworkPolicies
	if privateLinkPolicies == "" {
		privateLinkPolicies = "Enabled"
	}
	return Subnet{
		ID:   resourceID,
		Name: subnetName,
		Type: "Microsoft.Network/virtualNetworks/subnets",
		Properties: SubnetProperties{
			AddressPrefix:                     req.Properties.AddressPrefix,
			AddressPrefixes:                   req.Properties.AddressPrefixes,
			NetworkSecurityGroup:              req.Properties.NetworkSecurityGroup,
			NatGateway:                        req.Properties.NatGateway,
			RouteTable:                        req.Properties.RouteTable,
			Delegations:                       req.Properties.Delegations,
			ProvisioningState:                 "Succeeded",
			PrivateEndpointNetworkPolicies:    privateEndpointPolicies,
			PrivateLinkServiceNetworkPolicies: privateLinkPolicies,
			ServiceEndpointPolicies:           req.Properties.ServiceEndpointPolicies,
		},
	}
}

// azureMaterializeSubnet validates and stores a subnet the same way from its
// own PUT or inline on its virtual network's, and returns the error triple to
// write, or an empty code.
func azureMaterializeSubnet(sn Subnet) (string, string, int) {
	if sn.Properties.AddressPrefix == "" && len(sn.Properties.AddressPrefixes) == 0 {
		return "NoAddressPrefixOrPoolProvided",
			fmt.Sprintf("Either address prefix or IPAM pool must be provided for subnet %s.", sn.ID), http.StatusBadRequest
	}
	for _, prefix := range append([]string{sn.Properties.AddressPrefix}, sn.Properties.AddressPrefixes...) {
		if _, _, err := net.ParseCIDR(prefix); prefix != "" && err != nil {
			return "InvalidAddressPrefixFormat",
				fmt.Sprintf("Address prefix %s of resource %s is not formatted correctly.", prefix, sn.ID), http.StatusBadRequest
		}
	}
	// The virtual network's engine network spans its address space and nothing
	// outside it.
	if vnet, ok := azureVnets.Get(azureVNetIDOfSubnet(sn.ID)); ok {
		if outside, inside := azureSubnetInsideVNet(sn, vnet); !inside {
			return "InvalidParameter",
				fmt.Sprintf("Subnet %s is not valid in virtual network %s: its address prefix %s is outside the virtual network's address space %s.",
					sn.ID, vnet.ID, outside, strings.Join(vnet.Properties.AddressSpace.AddressPrefixes, ", ")), http.StatusBadRequest
		}
	}
	azureSubnets.Put(sn.ID, sn)
	azureReapplyIngress()
	return "", "", 0
}
