package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cross-resource-group move for Microsoft.Network (dispatched from
// resource_move.go). A network resource is defined largely by what it names —
// an interface names a subnet, an NSG and a public IP; a subnet an NSG, a route
// table and endpoint policies; a load balancer public IPs and subnets — so the
// hook re-homes the record and the repointing pass rewrites every reference to
// it. No Microsoft.Network resource derives a credential from its ID, so
// nothing is pinned.
//
// Only types "Azure resource types for move operations" marks movable get a
// hook. applicationgateways, natgateways, networkprofiles,
// privatelinkservices and virtualnetworktaps (marked No), and networkmanagers
// and packet captures (unlisted), answer ResourceMoveNotSupported.

// registerNetwork calls this after assigning the stores the hooks read.
func registerNetworkResourceMoveHooks() {
	registerNetworkMoveHook("Microsoft.Network/virtualNetworks", func() sim.Store[VirtualNetwork] { return azureVnets },
		func(v *VirtualNetwork) *string { return &v.ID })
	registerNetworkMoveHook("Microsoft.Network/networkSecurityGroups", func() sim.Store[NetworkSecurityGroup] { return azureNSGs },
		func(n *NetworkSecurityGroup) *string { return &n.ID })
	registerNetworkMoveHook("Microsoft.Network/routeTables", func() sim.Store[RouteTable] { return azureRouteTables },
		func(r *RouteTable) *string { return &r.ID })
	registerNetworkMoveHook("Microsoft.Network/publicIPAddresses", func() sim.Store[PublicIPAddress] { return azurePublicIPs },
		func(p *PublicIPAddress) *string { return &p.ID })
	registerNetworkMoveHook("Microsoft.Network/publicIPPrefixes", func() sim.Store[PublicIPPrefix] { return azurePublicIPPrefixes },
		func(p *PublicIPPrefix) *string { return &p.ID })
	registerNetworkMoveHook("Microsoft.Network/loadBalancers", func() sim.Store[LoadBalancer] { return azureLBs },
		func(l *LoadBalancer) *string { return &l.ID })
	registerNetworkMoveHook("Microsoft.Network/networkInterfaces", func() sim.Store[NetworkInterface] { return azureNICs },
		func(n *NetworkInterface) *string { return &n.ID })
	registerNetworkMoveHook("Microsoft.Network/applicationSecurityGroups", func() sim.Store[ApplicationSecurityGroup] { return azureASGs },
		func(a *ApplicationSecurityGroup) *string { return &a.ID })
	registerNetworkMoveHook("Microsoft.Network/privateEndpoints", func() sim.Store[PrivateEndpoint] { return azurePrivateEndpoints },
		func(p *PrivateEndpoint) *string { return &p.ID })
	// A private endpoint's movability depends on the resource, not the type.
	privateEndpoints := resourceMoveHooks["microsoft.network/privateendpoints"]
	privateEndpoints.supported = privateEndpointMoveSupported
	registerResourceMoveHook("Microsoft.Network/privateEndpoints", privateEndpoints)
	registerNetworkMoveHook("Microsoft.Network/serviceEndpointPolicies", func() sim.Store[ServiceEndpointPolicy] { return azureServiceEndpointPolicies },
		func(s *ServiceEndpointPolicy) *string { return &s.ID })
	registerNetworkMoveHook("Microsoft.Network/networkWatchers", func() sim.Store[NetworkWatcher] { return azureNetworkWatchers },
		func(n *NetworkWatcher) *string { return &n.ID })
	registerNetworkMoveHook("Microsoft.Network/dnsZones", func() sim.Store[PublicDnsZone] { return azurePublicDNSZones },
		func(z *PublicDnsZone) *string { return &z.ID })
	registerNetworkMoveHook("Microsoft.Network/privateDnsZones", func() sim.Store[PrivateDnsZone] { return azurePrivateDNSZones },
		func(z *PrivateDnsZone) *string { return &z.ID })
}

// The store is read through an accessor at request time, so a rebuild in the
// same process moves resources in the serving build, not the registering one.
func registerNetworkMoveHook[T any](typeName string, store func() sim.Store[T], id func(*T) *string) {
	registerResourceMoveHook(typeName, resourceMoveHook{
		exists: func(resID string) bool {
			s := store()
			if s == nil {
				return false
			}
			_, ok := s.Get(resID)
			return ok
		},
		move: func(oldID, newID, _ string) {
			s := store()
			if s == nil {
				return
			}
			row, ok := s.Get(oldID)
			if !ok {
				return
			}
			s.Delete(oldID)
			*id(&row) = newID
			s.Put(newID, row)
		},
	})
}

// "Move networking resources to new resource group or subscription" §
// Private endpoints: "The following private-link resources support move: …
// All other private-link resources don't support move." Hence the
// move-support table's "Yes - for supported private-link resources" cell.
var privateLinkResourcesSupportingMove = map[string]bool{
	"microsoft.aadiam/privatelinkforazuread":    true,
	"microsoft.documentdb/databaseaccounts":     true,
	"microsoft.kusto/clusters":                  true,
	"microsoft.signalrservice/signalr":          true,
	"microsoft.signalrservice/webpubsub":        true,
	"microsoft.sql/servers":                     true,
	"microsoft.storagesync/storagesyncservices": true,
	"microsoft.synapse/workspaces":              true,
	"microsoft.synapse/privatelinkhubs":         true,
	"microsoft.hybridcompute/privatelinkscopes": true,
	"microsoft.dbformysql/flexibleservers":      true,
}

// An endpoint with no connection names no private-link resource, so nothing
// supports its move.
func privateEndpointMoveSupported(resID string) (bool, string) {
	endpoint, ok := azurePrivateEndpoints.Get(resID)
	if !ok {
		return false, "the private endpoint was not found"
	}
	connections := append(append([]PrivateLinkServiceConnection(nil),
		endpoint.Properties.PrivateLinkServiceConnections...),
		endpoint.Properties.ManualPrivateLinkServiceConnections...)
	if len(connections) == 0 {
		return false, "the private endpoint connects to no private-link resource"
	}
	for _, connection := range connections {
		target := connection.Properties.PrivateLinkServiceID
		typeKey, ok := azureResourceTypeKeyOfID(target)
		if !ok || !privateLinkResourcesSupportingMove[typeKey] {
			return false, fmt.Sprintf(
				"the private-link resource '%s' it connects to does not support the move operation", target)
		}
	}
	return true, ""
}
