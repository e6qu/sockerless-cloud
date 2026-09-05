package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cross-resource-group move for Microsoft.Network. The hook table in
// resource_move.go dispatches Resources_MoveResources here.
//
// Networking is the family the repointing pass in resource_move.go exists for.
// A network resource is defined largely by the other resources it names: a
// network interface names a subnet, a network security group and a public IP
// address; a subnet names a network security group, a route table and service
// endpoint policies; a virtual network link names a virtual network; a private
// endpoint names a subnet and the private-link resource it connects to; a load
// balancer names public IP addresses and subnets. Moving one of those without
// re-pointing every referrer would leave the fabric naming addresses nothing
// answers to, so the move hook re-homes the record and the repointing pass
// rewrites every reference held to it from anywhere in the simulator.
//
// No Microsoft.Network resource carries a credential derived from its resource
// ID, so no hook here pins key material.
//
// Which types get a hook is decided by what real Azure Resource Manager
// actually moves, published per type in "Azure resource types for move
// operations" (Microsoft.Network section, "Resource group" column). The types
// that column marks No — applicationgateways, natgateways, networkprofiles,
// privatelinkservices, virtualnetworktaps — get no hook and keep answering
// ResourceMoveNotSupported, and so do the two types the table does not list at
// all, networkmanagers and the network watcher's packet captures. Adding a hook
// for a type Azure refuses would make the simulator less faithful, not more.

// registerNetworkResourceMoveHooks registers the move hook of every
// Microsoft.Network type Azure Resource Manager moves between resource groups.
// It runs from registerNetwork, after the stores those hooks read have been
// assigned.
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
	// A private endpoint is the one Microsoft.Network type whose movability
	// depends on the individual resource rather than on the type.
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

// registerNetworkMoveHook registers one Microsoft.Network type's participation
// in a cross-resource-group move: the record re-keys onto the destination
// group, and the repointing pass carries the child rows stored beneath it and
// every reference any other resource holds to it.
//
// The store is read through an accessor at request time so a rebuild of the
// simulator in the same process moves resources in the build that is serving,
// not in the build that registered the hook.
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

// privateLinkResourcesSupportingMove are the private-link resource types Azure
// publishes as supporting a private endpoint move, in "Move networking
// resources to new resource group or subscription" § Private endpoints: "The
// following private-link resources support move: … All other private-link
// resources don't support move." That is why the move-support table's Resource
// group cell for privateendpoints reads "Yes - for supported private-link
// resources / No - for all other private-link resources" rather than a flat
// Yes, and it is what privateEndpointMoveSupported decides against.
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

// privateEndpointMoveSupported reports whether Azure Resource Manager moves
// this private endpoint, which turns on the type of private-link resource it
// connects to. An endpoint with no connection at all names no private-link
// resource, so there is nothing that supports the move.
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
