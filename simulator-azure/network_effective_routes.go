package main

import (
	"net/netip"
	"strings"
)

// azureEffectiveRoute is one row of a subnet's route table as
// virtual-networks-udr-overview describes it: Azure's default system routes,
// a route per peered address range, and the subnet's user-defined routes,
// which make a default route with the same prefix Invalid.
type azureEffectiveRoute struct {
	name         string
	source       string
	state        string
	prefix       netip.Prefix
	nextHopType  string
	nextHopIP    string
	routeTableID string
}

// The default system routes whose next hop is None.
var azureDefaultNoneRoutes = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
	"157.59.0.0/16", "127.0.0.0/8", "104.147.0.0/16", "104.146.0.0/17",
}

func azureEffectiveRoutesForNIC(nic NetworkInterface) []azureEffectiveRoute {
	subnet, ok := networkWatcherNICSubnet(nic)
	if !ok {
		return nil
	}
	vnet, ok := azureVnets.Get(azureVNetIDOfSubnet(subnet.ID))
	if !ok {
		return nil
	}
	var routes []azureEffectiveRoute
	var vnetPrefixes []netip.Prefix
	for _, cidr := range vnet.Properties.AddressSpace.AddressPrefixes {
		if p, err := netip.ParsePrefix(cidr); err == nil && p.Addr().Is4() {
			vnetPrefixes = append(vnetPrefixes, p.Masked())
			routes = append(routes, azureEffectiveRoute{source: "Default", state: "Active", prefix: p.Masked(), nextHopType: "VnetLocal"})
		}
	}
	// A reserved range the address space names, or one it includes, routes
	// within the virtual network instead of to None.
	for _, cidr := range azureDefaultNoneRoutes {
		reserved := netip.MustParsePrefix(cidr)
		covered := false
		for _, p := range vnetPrefixes {
			if p.Bits() <= reserved.Bits() && p.Contains(reserved.Addr()) {
				covered = true
			}
		}
		if !covered {
			routes = append(routes, azureEffectiveRoute{source: "Default", state: "Active", prefix: reserved, nextHopType: "None"})
		}
	}
	if azureVNetPeerings != nil {
		for _, peering := range azureVNetPeerings.Filter(func(p VirtualNetworkPeering) bool {
			return strings.HasPrefix(strings.ToLower(p.ID), strings.ToLower(vnet.ID)+"/virtualnetworkpeerings/")
		}) {
			if !strings.EqualFold(peering.Properties.PeeringState, "Connected") || peering.Properties.RemoteVirtualNetwork == nil {
				continue
			}
			remote, ok := azureVnets.Get(peering.Properties.RemoteVirtualNetwork.ID)
			if !ok {
				continue
			}
			for _, cidr := range remote.Properties.AddressSpace.AddressPrefixes {
				if p, err := netip.ParsePrefix(cidr); err == nil && p.Addr().Is4() {
					routes = append(routes, azureEffectiveRoute{source: "Default", state: "Active", prefix: p.Masked(), nextHopType: "VNetPeering"})
				}
			}
		}
	}
	routes = append(routes, azureEffectiveRoute{source: "Default", state: "Active", prefix: netip.MustParsePrefix("0.0.0.0/0"), nextHopType: "Internet"})

	if subnet.Properties.RouteTable == nil || azureRouteTables == nil {
		return routes
	}
	table, ok := azureRouteTables.Get(subnet.Properties.RouteTable.ID)
	if !ok {
		return routes
	}
	for _, route := range table.Properties.Routes {
		p, err := netip.ParsePrefix(route.Properties.AddressPrefix)
		if err != nil || !p.Addr().Is4() {
			continue
		}
		p = p.Masked()
		for i := range routes {
			if routes[i].source == "Default" && routes[i].prefix == p {
				routes[i].state = "Invalid"
			}
		}
		routes = append(routes, azureEffectiveRoute{
			name:         route.Name,
			source:       "User",
			state:        "Active",
			prefix:       p,
			nextHopType:  route.Properties.NextHopType,
			nextHopIP:    route.Properties.NextHopIPAddress,
			routeTableID: table.ID,
		})
	}
	return routes
}

// The active route with the longest prefix covering the address.
func azureSelectEffectiveRoute(routes []azureEffectiveRoute, ip netip.Addr) (azureEffectiveRoute, bool) {
	best := -1
	var chosen azureEffectiveRoute
	for _, route := range routes {
		if route.state != "Active" || !route.prefix.Contains(ip) {
			continue
		}
		if route.prefix.Bits() > best {
			best, chosen = route.prefix.Bits(), route
		}
	}
	return chosen, best >= 0
}

func azureEffectiveRouteJSON(route azureEffectiveRoute, disableBGP bool) map[string]any {
	nextHops := []string{}
	if route.nextHopIP != "" {
		nextHops = append(nextHops, route.nextHopIP)
	}
	out := map[string]any{
		"source":                     route.source,
		"state":                      route.state,
		"addressPrefix":              []string{route.prefix.String()},
		"nextHopIpAddress":           nextHops,
		"nextHopType":                route.nextHopType,
		"disableBgpRoutePropagation": disableBGP,
	}
	if route.name != "" {
		out["name"] = route.name
	}
	return out
}

// The interface's own group, then its subnet's, each with its rules and
// Azure's default rules in priority order.
func azureEffectiveSecurityGroupsForNIC(nic NetworkInterface) []map[string]any {
	out := []map[string]any{}
	for _, attached := range azureAttachedSecurityGroups(nic) {
		association := map[string]any{}
		if strings.EqualFold(attached.appliedTo, nic.ID) {
			association["networkInterface"] = map[string]any{"id": nic.ID}
		} else {
			association["subnet"] = map[string]any{"id": attached.appliedTo}
		}
		out = append(out, map[string]any{
			"networkSecurityGroup":   map[string]any{"id": attached.nsg.ID},
			"association":            association,
			"effectiveSecurityRules": azureEffectiveSecurityRules(attached.nsg),
		})
	}
	return out
}
