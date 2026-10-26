package main

import (
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/netrules"
)

// Network Watcher's on-demand diagnostics compute every verdict from the
// network the simulator holds: IP flow verify runs the rule evaluation that
// drops packets, next hop the longest-prefix route lookup that routes them,
// and the connectivity check opens a real connection.

func registerNetworkWatcherDiagnostics(srv *sim.Server) {
	base := azureNetworkArmBase() + "/networkWatchers/{networkWatcherName}"

	srv.HandleFunc("POST "+base+"/ipFlowVerify", handleNetworkWatcherVerifyIPFlow)
	srv.HandleFunc("POST "+base+"/nextHop", handleNetworkWatcherNextHop)
	srv.HandleFunc("POST "+base+"/securityGroupView", handleNetworkWatcherSecurityGroupView)
	srv.HandleFunc("POST "+base+"/topology", handleNetworkWatcherTopology)
	srv.HandleFunc("POST "+base+"/connectivityCheck", handleNetworkWatcherConnectivityCheck)
	srv.HandleFunc("POST "+base+"/networkConfigurationDiagnostic", handleNetworkWatcherConfigurationDiagnostic)
	srv.HandleFunc("POST "+base+"/configureFlowLog", handleNetworkWatcherConfigureFlowLog)
	srv.HandleFunc("POST "+base+"/queryFlowLogStatus", handleNetworkWatcherQueryFlowLogStatus)
	srv.HandleFunc("POST "+base+"/troubleshoot", handleNetworkWatcherTroubleshoot)
	srv.HandleFunc("POST "+base+"/queryTroubleshootResult", handleNetworkWatcherQueryTroubleshootResult)
	srv.HandleFunc("POST "+base+"/availableProvidersList", handleNetworkWatcherAvailableProviders)
	srv.HandleFunc("POST "+base+"/azureReachabilityReport", handleNetworkWatcherReachabilityReport)
}

// A target is an interface or a virtual machine, whose every interface is in
// scope unless the request narrows it to one.
func networkWatcherTargetNICs(targetResourceID, targetNICResourceID string) ([]NetworkInterface, bool) {
	if azureNICs == nil {
		return nil, false
	}
	if targetNICResourceID != "" {
		nic, ok := azureNICs.Get(targetNICResourceID)
		if !ok {
			return nil, false
		}
		return []NetworkInterface{nic}, true
	}
	if nic, ok := azureNICs.Get(targetResourceID); ok {
		return []NetworkInterface{nic}, true
	}
	if azureVMs == nil {
		return nil, false
	}
	vm, ok := azureVMs.Get(targetResourceID)
	if !ok {
		return nil, false
	}
	var nics []NetworkInterface
	for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
		if nic, ok := azureNICs.Get(ref.ID); ok {
			nics = append(nics, nic)
		}
	}
	return nics, len(nics) > 0
}

func networkWatcherResolveTarget(w http.ResponseWriter, targetResourceID, targetNICResourceID string) ([]NetworkInterface, bool) {
	nics, ok := networkWatcherTargetNICs(targetResourceID, targetNICResourceID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", targetResourceID)
		return nil, false
	}
	return nics, true
}

func networkWatcherNICSubnet(nic NetworkInterface) (Subnet, bool) {
	if azureSubnets == nil {
		return Subnet{}, false
	}
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Subnet == nil {
			continue
		}
		if subnet, ok := azureSubnets.Get(ipcfg.Properties.Subnet.ID); ok {
			return subnet, true
		}
	}
	return Subnet{}, false
}

func networkWatcherNICAddress(nic NetworkInterface) string {
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.PrivateIPAddress != "" {
			return ipcfg.Properties.PrivateIPAddress
		}
	}
	return ""
}

// In the direction rules are written: source and destination, never local and
// remote.
type azureSecurityFlow struct {
	protocol        string
	sourceIP        string
	sourcePort      string
	destinationIP   string
	destinationPort string
	direction       string
}

type azureSecurityGroupVerdict struct {
	nsg       NetworkSecurityGroup
	appliedTo string
	rule      SecurityRule
	// The collection the deciding rule came from, which prefixes a verify-IP-flow
	// answer's ruleName.
	qualifier string
	access    string
	// Per rule, which parts of the flow it matched, as the network configuration
	// diagnostic reports.
	evaluations []map[string]any
}

// The interface's own group first, then its subnet's.
func azureAttachedSecurityGroups(nic NetworkInterface) []struct {
	nsg       NetworkSecurityGroup
	appliedTo string
} {
	var out []struct {
		nsg       NetworkSecurityGroup
		appliedTo string
	}
	if azureNSGs == nil {
		return out
	}
	add := func(id, appliedTo string) {
		if id == "" {
			return
		}
		if nsg, ok := azureNSGs.Get(id); ok {
			out = append(out, struct {
				nsg       NetworkSecurityGroup
				appliedTo string
			}{nsg, appliedTo})
		}
	}
	if nic.Properties.NetworkSecurityGroup != nil {
		add(nic.Properties.NetworkSecurityGroup.ID, nic.ID)
	}
	if subnet, ok := networkWatcherNICSubnet(nic); ok && subnet.Properties.NetworkSecurityGroup != nil {
		add(subnet.Properties.NetworkSecurityGroup.ID, subnet.ID)
	}
	return out
}

// Azure evaluates the subnet's and the interface's groups independently and a
// deny by either stops the packet: the first denying group is the verdict, and
// when all allow, the first group's allowing rule is reported.
func azureEvaluateSecurityGroups(nic NetworkInterface, flow azureSecurityFlow) []azureSecurityGroupVerdict {
	var verdicts []azureSecurityGroupVerdict
	for _, attached := range azureAttachedSecurityGroups(nic) {
		verdicts = append(verdicts, azureEvaluateSecurityGroup(attached.nsg, attached.appliedTo, nic, flow))
	}
	return verdicts
}

func azureDecideSecurityGroups(verdicts []azureSecurityGroupVerdict) (azureSecurityGroupVerdict, bool) {
	if len(verdicts) == 0 {
		return azureSecurityGroupVerdict{}, false
	}
	for _, verdict := range verdicts {
		if strings.EqualFold(verdict.access, "Deny") {
			return verdict, true
		}
	}
	return verdicts[0], true
}

// The caller's rules then the platform defaults, in priority order; the first
// rule matching every part of the flow wins.
func azureEvaluateSecurityGroup(nsg NetworkSecurityGroup, appliedTo string, nic NetworkInterface, flow azureSecurityFlow) azureSecurityGroupVerdict {
	verdict := azureSecurityGroupVerdict{nsg: nsg, appliedTo: appliedTo}
	type candidate struct {
		rule      SecurityRule
		qualifier string
	}
	var candidates []candidate
	rules := append([]SecurityRule(nil), nsg.Properties.SecurityRules...)
	sort.SliceStable(rules, func(i, j int) bool {
		return rules[i].Properties.Priority < rules[j].Properties.Priority
	})
	for _, rule := range rules {
		candidates = append(candidates, candidate{rule, "securityRules"})
	}
	for _, rule := range azureDefaultSecurityRules(nsg.ID) {
		candidates = append(candidates, candidate{rule, "defaultSecurityRules"})
	}
	decided := false
	for _, c := range candidates {
		props := c.rule.Properties
		if !strings.EqualFold(defaultString(props.Direction, "Inbound"), flow.direction) {
			continue
		}
		match := azureSecurityRuleMatches(props, nic, flow)
		verdict.evaluations = append(verdict.evaluations, map[string]any{
			"name":                   c.rule.Name,
			"protocolMatched":        match.protocol,
			"sourceMatched":          match.source,
			"sourcePortMatched":      match.sourcePort,
			"destinationMatched":     match.destination,
			"destinationPortMatched": match.destinationPort,
		})
		if decided || !match.all() {
			continue
		}
		verdict.rule = c.rule
		verdict.qualifier = c.qualifier
		verdict.access = props.Access
		decided = true
	}
	return verdict
}

type azureSecurityRuleMatch struct {
	protocol        bool
	source          bool
	sourcePort      bool
	destination     bool
	destinationPort bool
}

func (m azureSecurityRuleMatch) all() bool {
	return m.protocol && m.source && m.sourcePort && m.destination && m.destinationPort
}

// Part by part, so a diagnostic reports which part failed.
func azureSecurityRuleMatches(props SecurityRuleProperties, nic NetworkInterface, flow azureSecurityFlow) azureSecurityRuleMatch {
	sources := azureSecurityRuleEndpoints(props.SourceApplicationSecurityGroups,
		props.SourceAddressPrefix, props.SourceAddressPrefixes)
	destinations := azureSecurityRuleEndpoints(props.DestinationApplicationSecurityGroups,
		props.DestinationAddressPrefix, props.DestinationAddressPrefixes)
	match := azureSecurityRuleMatch{
		protocol:        azureSecurityProtocolMatches(props.Protocol, flow.protocol),
		source:          azureSecurityAddressMatches(sources, flow.sourceIP),
		destination:     azureSecurityAddressMatches(destinations, flow.destinationIP),
		sourcePort:      azureSecurityPortMatches(azurePortRanges(props.SourcePortRange, props.SourcePortRanges), flow.sourcePort),
		destinationPort: azureSecurityPortMatches(azurePortRanges(props.DestinationPortRange, props.DestinationPortRanges), flow.destinationPort),
	}
	// A rule scoped to destination application security groups governs only their
	// member interfaces.
	if len(props.DestinationApplicationSecurityGroups) > 0 && !azureNICInApplicationSecurityGroups(nic, props.DestinationApplicationSecurityGroups) {
		match.destination = false
	}
	return match
}

// The members of the application security groups named, or the declared
// prefixes.
func azureSecurityRuleEndpoints(groups []SubResource, single string, many []string) []string {
	if len(groups) > 0 {
		return azureApplicationSecurityGroupMemberIPs(groups)
	}
	var values []string
	if single != "" {
		values = append(values, single)
	}
	values = append(values, many...)
	if len(values) == 0 {
		return []string{"*"}
	}
	return values
}

func azureSecurityProtocolMatches(rule, flow string) bool {
	if rule == "" || rule == "*" || flow == "" || flow == "*" {
		return true
	}
	return strings.EqualFold(rule, flow)
}

// A service tag the simulator cannot resolve matches nothing, so an
// unresolvable rule never widens what a diagnostic reports as allowed.
func azureSecurityAddressMatches(prefixes []string, address string) bool {
	if address == "" || address == "*" {
		return true
	}
	ip := net.ParseIP(address)
	for _, prefix := range prefixes {
		switch {
		case prefix == "" || prefix == "*" || strings.EqualFold(prefix, "Any"):
			return true
		case strings.EqualFold(prefix, "VirtualNetwork"):
			if azureAddressInAnyPrefix(ip, azureAllVNetCIDRs()) {
				return true
			}
		case strings.EqualFold(prefix, "Internet"):
			// Internet is everything outside the virtual network address space.
			if ip != nil && !azureAddressInAnyPrefix(ip, azureAllVNetCIDRs()) {
				return true
			}
		case strings.EqualFold(prefix, "AzureLoadBalancer"):
			if address == "168.63.129.16" {
				return true
			}
		default:
			if azureAddressInAnyPrefix(ip, []string{prefix}) {
				return true
			}
		}
	}
	return false
}

func azureAddressInAnyPrefix(ip net.IP, prefixes []string) bool {
	if ip == nil {
		return false
	}
	for _, prefix := range prefixes {
		if !strings.Contains(prefix, "/") {
			if net.ParseIP(prefix).Equal(ip) {
				return true
			}
			continue
		}
		_, network, err := net.ParseCIDR(prefix)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func azureSecurityPortMatches(ranges []string, port string) bool {
	if port == "" || port == "*" {
		return true
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	for _, entry := range ranges {
		entry = strings.TrimSpace(entry)
		if entry == "" || entry == "*" {
			return true
		}
		from, to, err := netrules.PortRange(entry)
		if err != nil {
			continue
		}
		if from == 0 && to == 0 {
			return true
		}
		if value >= from && value <= to {
			return true
		}
	}
	return false
}

func handleNetworkWatcherVerifyIPFlow(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		TargetResourceID    string `json:"targetResourceId"`
		Direction           string `json:"direction"`
		Protocol            string `json:"protocol"`
		LocalPort           string `json:"localPort"`
		RemotePort          string `json:"remotePort"`
		LocalIPAddress      string `json:"localIPAddress"`
		RemoteIPAddress     string `json:"remoteIPAddress"`
		TargetNicResourceID string `json:"targetNicResourceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TargetResourceID == "" || req.Direction == "" || req.Protocol == "" {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: targetResourceId, direction and protocol are required.")
		return
	}
	nics, ok := networkWatcherResolveTarget(w, req.TargetResourceID, req.TargetNicResourceID)
	if !ok {
		return
	}
	nic := nics[0]
	flow := azureSecurityFlow{
		protocol:  req.Protocol,
		direction: req.Direction,
	}
	// The request describes the packet from the machine's side; rules are written
	// from the packet's. Inbound has the remote end as source, outbound the local.
	if strings.EqualFold(req.Direction, "Inbound") {
		flow.sourceIP, flow.sourcePort = req.RemoteIPAddress, req.RemotePort
		flow.destinationIP, flow.destinationPort = req.LocalIPAddress, req.LocalPort
	} else {
		flow.sourceIP, flow.sourcePort = req.LocalIPAddress, req.LocalPort
		flow.destinationIP, flow.destinationPort = req.RemoteIPAddress, req.RemotePort
	}
	verdict, ok := azureDecideSecurityGroups(azureEvaluateSecurityGroups(nic, flow))
	if !ok {
		// An interface no group governs takes every packet.
		sim.WriteJSON(w, http.StatusOK, map[string]any{"access": "Allow"})
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"access":   verdict.access,
		"ruleName": verdict.qualifier + "/" + verdict.rule.Name,
	})
}

func handleNetworkWatcherNextHop(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		TargetResourceID     string `json:"targetResourceId"`
		SourceIPAddress      string `json:"sourceIPAddress"`
		DestinationIPAddress string `json:"destinationIPAddress"`
		TargetNicResourceID  string `json:"targetNicResourceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TargetResourceID == "" || req.DestinationIPAddress == "" {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: targetResourceId and destinationIPAddress are required.")
		return
	}
	nics, ok := networkWatcherResolveTarget(w, req.TargetResourceID, req.TargetNicResourceID)
	if !ok {
		return
	}
	nic := nics[0]
	if req.SourceIPAddress != "" {
		for _, candidate := range nics {
			if networkWatcherNICAddress(candidate) == req.SourceIPAddress {
				nic = candidate
			}
		}
	}
	sim.WriteJSON(w, http.StatusOK, azureNextHopForNIC(nic, req.DestinationIPAddress))
}

// A subnet route table's route wins over platform routing, longest prefix
// first; otherwise an address inside the virtual network stays local and the
// rest leaves through the default route.
func azureNextHopForNIC(nic NetworkInterface, destination string) map[string]any {
	ip := net.ParseIP(destination)
	subnet, hasSubnet := networkWatcherNICSubnet(nic)
	if hasSubnet && subnet.Properties.RouteTable != nil && azureRouteTables != nil {
		if table, ok := azureRouteTables.Get(subnet.Properties.RouteTable.ID); ok {
			if route, ok := azureLongestPrefixRoute(table, ip); ok {
				result := map[string]any{
					"nextHopType":  route.Properties.NextHopType,
					"routeTableId": table.ID,
				}
				if route.Properties.NextHopIPAddress != "" {
					result["nextHopIpAddress"] = route.Properties.NextHopIPAddress
				}
				return result
			}
		}
	}
	// System routes carry no route table ID, which tells them from user routes.
	if azureAddressInAnyPrefix(ip, azureNICVNetCIDRs(nic)) {
		return map[string]any{"nextHopType": "VnetLocal"}
	}
	return map[string]any{"nextHopType": "Internet"}
}

// The covering route with the longest mask, as a router picks.
func azureLongestPrefixRoute(table RouteTable, ip net.IP) (RouteEntry, bool) {
	if ip == nil {
		return RouteEntry{}, false
	}
	best := -1
	var chosen RouteEntry
	for _, route := range table.Properties.Routes {
		_, network, err := net.ParseCIDR(route.Properties.AddressPrefix)
		if err != nil || !network.Contains(ip) {
			continue
		}
		ones, _ := network.Mask.Size()
		if ones > best {
			best, chosen = ones, route
		}
	}
	return chosen, best >= 0
}

func handleNetworkWatcherSecurityGroupView(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		TargetResourceID string `json:"targetResourceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	nics, ok := networkWatcherResolveTarget(w, req.TargetResourceID, "")
	if !ok {
		return
	}
	interfaces := make([]map[string]any, 0, len(nics))
	for _, nic := range nics {
		associations := map[string]any{}
		var effective []map[string]any
		if nic.Properties.NetworkSecurityGroup != nil && azureNSGs != nil {
			if nsg, ok := azureNSGs.Get(nic.Properties.NetworkSecurityGroup.ID); ok {
				associations["networkInterfaceAssociation"] = map[string]any{
					"id":            nic.ID,
					"securityRules": nsg.Properties.SecurityRules,
				}
				effective = append(effective, azureEffectiveSecurityRules(nsg)...)
			}
		}
		if subnet, ok := networkWatcherNICSubnet(nic); ok && subnet.Properties.NetworkSecurityGroup != nil && azureNSGs != nil {
			if nsg, ok := azureNSGs.Get(subnet.Properties.NetworkSecurityGroup.ID); ok {
				associations["subnetAssociation"] = map[string]any{
					"id":            subnet.ID,
					"securityRules": nsg.Properties.SecurityRules,
				}
				effective = append(effective, azureEffectiveSecurityRules(nsg)...)
			}
		}
		defaults := []SecurityRule{}
		for _, attached := range azureAttachedSecurityGroups(nic) {
			defaults = append(defaults, azureDefaultSecurityRules(attached.nsg.ID)...)
		}
		associations["defaultSecurityRules"] = defaults
		if effective == nil {
			effective = []map[string]any{}
		}
		associations["effectiveSecurityRules"] = effective
		interfaces = append(interfaces, map[string]any{
			"id":                       nic.ID,
			"securityRuleAssociations": associations,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"networkInterfaces": interfaces})
}

// The effective-rule shape carries the expanded prefixes the platform matches
// on, not the service tags the rule was written with.
func azureEffectiveSecurityRules(nsg NetworkSecurityGroup) []map[string]any {
	rules := append([]SecurityRule(nil), nsg.Properties.SecurityRules...)
	rules = append(rules, azureDefaultSecurityRules(nsg.ID)...)
	sort.SliceStable(rules, func(i, j int) bool {
		return rules[i].Properties.Priority < rules[j].Properties.Priority
	})
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		props := rule.Properties
		out = append(out, map[string]any{
			"name":                             nsg.Name + "/" + rule.Name,
			"protocol":                         defaultString(props.Protocol, "All"),
			"sourcePortRange":                  props.SourcePortRange,
			"destinationPortRange":             props.DestinationPortRange,
			"sourcePortRanges":                 props.SourcePortRanges,
			"destinationPortRanges":            props.DestinationPortRanges,
			"sourceAddressPrefix":              props.SourceAddressPrefix,
			"destinationAddressPrefix":         props.DestinationAddressPrefix,
			"sourceAddressPrefixes":            props.SourceAddressPrefixes,
			"destinationAddressPrefixes":       props.DestinationAddressPrefixes,
			"expandedSourceAddressPrefix":      azureAddressPrefixes(props.SourceAddressPrefix, props.SourceAddressPrefixes),
			"expandedDestinationAddressPrefix": azureAddressPrefixes(props.DestinationAddressPrefix, props.DestinationAddressPrefixes),
			"access":                           props.Access,
			"priority":                         props.Priority,
			"direction":                        props.Direction,
		})
	}
	return out
}

func handleNetworkWatcherTopology(w http.ResponseWriter, r *http.Request) {
	nw, ok := requireNetworkWatcher(w, r)
	if !ok {
		return
	}
	var req struct {
		TargetResourceGroupName string       `json:"targetResourceGroupName"`
		TargetVirtualNetwork    *SubResource `json:"targetVirtualNetwork"`
		TargetSubnet            *SubResource `json:"targetSubnet"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	subscription := sim.PathParam(r, "subscriptionId")
	var scope string
	switch {
	case req.TargetSubnet != nil && req.TargetSubnet.ID != "":
		scope = req.TargetSubnet.ID
	case req.TargetVirtualNetwork != nil && req.TargetVirtualNetwork.ID != "":
		scope = req.TargetVirtualNetwork.ID
	case req.TargetResourceGroupName != "":
		scope = armResourceGroupID(subscription, req.TargetResourceGroupName) + "/"
	default:
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: a topology request must name a target resource group, virtual network or subnet.")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":              nw.ID + "/topology",
		"createdDateTime": now,
		"lastModified":    now,
		"resources":       azureTopologyResources(scope),
	})
}

// A virtual network contains its subnets, a subnet the interfaces addressed
// from it, and an interface is associated with its machine, public address and
// security group.
func azureTopologyResources(scope string) []map[string]any {
	inScope := func(id string) bool {
		return strings.HasPrefix(strings.ToLower(id), strings.ToLower(scope))
	}
	resources := make([]map[string]any, 0)
	add := func(id, name, location string, associations []map[string]any) {
		if associations == nil {
			associations = []map[string]any{}
		}
		resources = append(resources, map[string]any{
			"id": id, "name": name, "location": location, "associations": associations,
		})
	}
	association := func(name, resourceID, kind string) map[string]any {
		return map[string]any{"name": name, "resourceId": resourceID, "associationType": kind}
	}

	subnetsOf := map[string][]Subnet{}
	if azureSubnets != nil {
		for _, subnet := range azureSubnets.List() {
			vnetID := strings.Split(subnet.ID, "/subnets/")[0]
			subnetsOf[vnetID] = append(subnetsOf[vnetID], subnet)
		}
	}
	nicsOf := map[string][]NetworkInterface{}
	if azureNICs != nil {
		for _, nic := range azureNICs.List() {
			for _, ipcfg := range nic.Properties.IPConfigurations {
				if ipcfg.Properties.Subnet != nil {
					nicsOf[ipcfg.Properties.Subnet.ID] = append(nicsOf[ipcfg.Properties.Subnet.ID], nic)
				}
			}
		}
	}
	vmOfNIC := map[string]VirtualMachine{}
	if azureVMs != nil {
		for _, vm := range azureVMs.List() {
			for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
				vmOfNIC[strings.ToLower(ref.ID)] = vm
			}
		}
	}

	if azureVnets != nil {
		for _, vnet := range azureVnets.List() {
			if !inScope(vnet.ID) && !strings.HasPrefix(strings.ToLower(scope), strings.ToLower(vnet.ID)) {
				continue
			}
			var associations []map[string]any
			for _, subnet := range subnetsOf[vnet.ID] {
				associations = append(associations, association(subnet.Name, subnet.ID, "Contains"))
			}
			add(vnet.ID, vnet.Name, vnet.Location, associations)
			for _, subnet := range subnetsOf[vnet.ID] {
				if !inScope(subnet.ID) && !strings.HasPrefix(strings.ToLower(scope), strings.ToLower(subnet.ID)) &&
					!strings.HasPrefix(strings.ToLower(subnet.ID), strings.ToLower(scope)) {
					continue
				}
				var subnetAssociations []map[string]any
				for _, nic := range nicsOf[subnet.ID] {
					subnetAssociations = append(subnetAssociations, association(nic.Name, nic.ID, "Contains"))
				}
				add(subnet.ID, subnet.Name, vnet.Location, subnetAssociations)
				for _, nic := range nicsOf[subnet.ID] {
					var nicAssociations []map[string]any
					if vm, ok := vmOfNIC[strings.ToLower(nic.ID)]; ok {
						nicAssociations = append(nicAssociations, association(vm.Name, vm.ID, "Associated"))
					}
					if nic.Properties.NetworkSecurityGroup != nil {
						nicAssociations = append(nicAssociations,
							association(azureResourceLeafName(nic.Properties.NetworkSecurityGroup.ID),
								nic.Properties.NetworkSecurityGroup.ID, "Associated"))
					}
					for _, ipcfg := range nic.Properties.IPConfigurations {
						if ipcfg.Properties.PublicIPAddress != nil {
							nicAssociations = append(nicAssociations,
								association(azureResourceLeafName(ipcfg.Properties.PublicIPAddress.ID),
									ipcfg.Properties.PublicIPAddress.ID, "Associated"))
						}
					}
					add(nic.ID, nic.Name, nic.Location, nicAssociations)
				}
			}
		}
	}
	return resources
}

func azureResourceLeafName(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

func handleNetworkWatcherConnectivityCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		Source struct {
			ResourceID string `json:"resourceId"`
			Port       int    `json:"port"`
		} `json:"source"`
		Destination struct {
			ResourceID string `json:"resourceId"`
			Address    string `json:"address"`
			Port       int    `json:"port"`
		} `json:"destination"`
		Protocol string `json:"protocol"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Source.ResourceID == "" {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: source.resourceId is required.")
		return
	}
	sourceNICs, ok := networkWatcherResolveTarget(w, req.Source.ResourceID, "")
	if !ok {
		return
	}
	sourceNIC := sourceNICs[0]
	sourceAddress := networkWatcherNICAddress(sourceNIC)

	destinationAddress := req.Destination.Address
	var destinationNIC *NetworkInterface
	if req.Destination.ResourceID != "" {
		nics, ok := networkWatcherResolveTarget(w, req.Destination.ResourceID, "")
		if !ok {
			return
		}
		destinationNIC = &nics[0]
		if destinationAddress == "" {
			destinationAddress = networkWatcherNICAddress(nics[0])
		}
	}
	if destinationAddress == "" {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: the destination must carry an address or a resource id.")
		return
	}

	protocol := defaultString(req.Protocol, "Tcp")
	port := strconv.Itoa(req.Destination.Port)
	if req.Destination.Port == 0 {
		port = "*"
	}

	var issues []map[string]any
	blocked := false
	// The packet must leave the source and arrive at the destination, so both
	// ends' groups are evaluated.
	if verdict, ok := azureDecideSecurityGroups(azureEvaluateSecurityGroups(sourceNIC, azureSecurityFlow{
		protocol:        protocol,
		sourceIP:        sourceAddress,
		destinationIP:   destinationAddress,
		destinationPort: port,
		direction:       "Outbound",
	})); ok && strings.EqualFold(verdict.access, "Deny") {
		blocked = true
		issues = append(issues, azureConnectivityIssue("Outbound", verdict))
	}
	if destinationNIC != nil {
		if verdict, ok := azureDecideSecurityGroups(azureEvaluateSecurityGroups(*destinationNIC, azureSecurityFlow{
			protocol:        protocol,
			sourceIP:        sourceAddress,
			destinationIP:   destinationAddress,
			destinationPort: port,
			direction:       "Inbound",
		})); ok && strings.EqualFold(verdict.access, "Deny") {
			blocked = true
			issues = append(issues, azureConnectivityIssue("Inbound", verdict))
		}
	}

	status := "Unknown"
	probesSent, probesFailed := 0, 0
	latency := int64(0)
	if blocked {
		status = "Disconnected"
		probesSent, probesFailed = 1, 1
	} else if req.Destination.Port > 0 {
		// Nothing in the configuration blocks the packet, so find out whether the
		// destination answers.
		status, latency, probesSent, probesFailed = azureProbeConnectivity(destinationAddress, req.Destination.Port)
		if status == "Disconnected" {
			issues = append(issues, map[string]any{
				"origin": "Outbound", "severity": "Error", "type": "SocketBind", "context": []map[string]string{},
			})
		}
	}
	if issues == nil {
		issues = []map[string]any{}
	}

	sourceHop := map[string]any{
		"type": "Source", "id": "source", "address": sourceAddress,
		"resourceId": sourceNIC.ID, "nextHopIds": []string{"destination"},
		"previousHopIds": []string{}, "issues": issues,
	}
	destinationHop := map[string]any{
		"type": "Destination", "id": "destination", "address": destinationAddress,
		"nextHopIds": []string{}, "previousHopIds": []string{"source"},
		"issues": []map[string]any{},
	}
	if destinationNIC != nil {
		destinationHop["resourceId"] = destinationNIC.ID
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"hops":             []map[string]any{sourceHop, destinationHop},
		"connectionStatus": status,
		"avgLatencyInMs":   latency,
		"minLatencyInMs":   latency,
		"maxLatencyInMs":   latency,
		"probesSent":       probesSent,
		"probesFailed":     probesFailed,
	})
}

func azureConnectivityIssue(origin string, verdict azureSecurityGroupVerdict) map[string]any {
	return map[string]any{
		"origin":   origin,
		"severity": "Error",
		"type":     "NetworkSecurityRule",
		"context": []map[string]string{{
			"key":   "RuleName",
			"value": verdict.qualifier + "/" + verdict.rule.Name,
		}, {
			"key":   "NetworkSecurityGroupId",
			"value": verdict.nsg.ID,
		}},
	}
}

// Opens a real connection and measures its latency.
func azureProbeConnectivity(address string, port int) (status string, latencyMs int64, probesSent, probesFailed int) {
	const probes = 3
	var total time.Duration
	for i := 0; i < probes; i++ {
		probesSent++
		started := time.Now()
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(address, strconv.Itoa(port)), 2*time.Second)
		if err != nil {
			probesFailed++
			continue
		}
		total += time.Since(started)
		_ = conn.Close()
	}
	switch {
	case probesFailed == probes:
		return "Disconnected", 0, probesSent, probesFailed
	case probesFailed > 0:
		return "Degraded", total.Milliseconds() / int64(probes-probesFailed), probesSent, probesFailed
	default:
		return "Connected", total.Milliseconds() / int64(probes), probesSent, probesFailed
	}
}

func handleNetworkWatcherConfigurationDiagnostic(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		TargetResourceID string `json:"targetResourceId"`
		VerbosityLevel   string `json:"verbosityLevel"`
		Profiles         []struct {
			Direction       string `json:"direction"`
			Protocol        string `json:"protocol"`
			Source          string `json:"source"`
			Destination     string `json:"destination"`
			DestinationPort string `json:"destinationPort"`
		} `json:"profiles"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Profiles) == 0 {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: at least one diagnostic profile is required.")
		return
	}
	nics, ok := networkWatcherResolveTarget(w, req.TargetResourceID, "")
	if !ok {
		return
	}
	nic := nics[0]
	results := make([]map[string]any, 0, len(req.Profiles))
	for _, profile := range req.Profiles {
		verdicts := azureEvaluateSecurityGroups(nic, azureSecurityFlow{
			protocol:        profile.Protocol,
			sourceIP:        profile.Source,
			destinationIP:   profile.Destination,
			destinationPort: profile.DestinationPort,
			direction:       profile.Direction,
		})
		access := "Allow"
		if decided, ok := azureDecideSecurityGroups(verdicts); ok {
			access = decided.access
		}
		evaluated := make([]map[string]any, 0, len(verdicts))
		for _, verdict := range verdicts {
			evaluated = append(evaluated, map[string]any{
				"networkSecurityGroupId": verdict.nsg.ID,
				"appliedTo":              verdict.appliedTo,
				"matchedRule": map[string]any{
					"ruleName": verdict.rule.Name,
					"action":   verdict.access,
				},
				"rulesEvaluationResult": verdict.evaluations,
			})
		}
		results = append(results, map[string]any{
			"profile": map[string]any{
				"direction":       profile.Direction,
				"protocol":        profile.Protocol,
				"source":          profile.Source,
				"destination":     profile.Destination,
				"destinationPort": profile.DestinationPort,
			},
			"networkSecurityGroupResult": map[string]any{
				"securityRuleAccessResult":       access,
				"evaluatedNetworkSecurityGroups": evaluated,
			},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"results": results})
}

// The target-addressed spelling of the flow log resource, on the same record
// FlowLogs_Get returns.
func handleNetworkWatcherConfigureFlowLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		TargetResourceID string `json:"targetResourceId"`
		Properties       struct {
			StorageID                string                       `json:"storageId"`
			EnabledFilteringCriteria string                       `json:"enabledFilteringCriteria"`
			RecordTypes              string                       `json:"recordTypes"`
			Enabled                  *bool                        `json:"enabled"`
			RetentionPolicy          *NetworkWatcherRetention     `json:"retentionPolicy"`
			Format                   *NetworkWatcherFlowLogFormat `json:"format"`
		} `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TargetResourceID == "" || req.Properties.StorageID == "" {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: a flow log configuration requires targetResourceId and storageId.")
		return
	}
	if !networkWatcherFlowLogTargetExists(req.TargetResourceID) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", req.TargetResourceID)
		return
	}
	watcherID := networkWatcherID(r)
	name := azureResourceLeafName(req.TargetResourceID)
	id := armChildID(watcherID, "flowLogs", name)
	if existing, ok := networkWatcherFlowLogForTarget(watcherID, req.TargetResourceID); ok {
		id, name = existing.ID, existing.Name
	}
	flowLog := networkWatcherStoreFlowLog(id, name, NetworkWatcherFlowLog{
		Properties: NetworkWatcherFlowLogProperties{
			TargetResourceID:         req.TargetResourceID,
			StorageID:                req.Properties.StorageID,
			EnabledFilteringCriteria: req.Properties.EnabledFilteringCriteria,
			RecordTypes:              req.Properties.RecordTypes,
			Enabled:                  req.Properties.Enabled,
			RetentionPolicy:          req.Properties.RetentionPolicy,
			Format:                   req.Properties.Format,
		},
	})
	sim.WriteJSON(w, http.StatusOK, networkWatcherFlowLogInformation(flowLog))
}

func handleNetworkWatcherQueryFlowLogStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		TargetResourceID string `json:"targetResourceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	flowLog, ok := networkWatcherFlowLogForTarget(networkWatcherID(r), req.TargetResourceID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"No flow log configuration exists for the resource %q.", req.TargetResourceID)
		return
	}
	sim.WriteJSON(w, http.StatusOK, networkWatcherFlowLogInformation(flowLog))
}

func networkWatcherFlowLogInformation(flowLog NetworkWatcherFlowLog) map[string]any {
	properties := map[string]any{
		"storageId": flowLog.Properties.StorageID,
		"enabled":   flowLog.Properties.Enabled,
	}
	if flowLog.Properties.EnabledFilteringCriteria != "" {
		properties["enabledFilteringCriteria"] = flowLog.Properties.EnabledFilteringCriteria
	}
	if flowLog.Properties.RecordTypes != "" {
		properties["recordTypes"] = flowLog.Properties.RecordTypes
	}
	if flowLog.Properties.RetentionPolicy != nil {
		properties["retentionPolicy"] = flowLog.Properties.RetentionPolicy
	}
	if flowLog.Properties.Format != nil {
		properties["format"] = flowLog.Properties.Format
	}
	return map[string]any{
		"targetResourceId": flowLog.Properties.TargetResourceID,
		"properties":       properties,
	}
}

// Network Watcher troubleshoots virtual network gateways and their
// connections; the simulator holds neither, so every target is not found.
func networkWatcherTroubleshootTarget(w http.ResponseWriter, targetResourceID string) bool {
	if targetResourceID == "" {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: targetResourceId is required.")
		return false
	}
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource %q was not found.", targetResourceID)
	return false
}

func handleNetworkWatcherTroubleshoot(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		TargetResourceID string `json:"targetResourceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	networkWatcherTroubleshootTarget(w, req.TargetResourceID)
}

func handleNetworkWatcherQueryTroubleshootResult(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		TargetResourceID string `json:"targetResourceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	networkWatcherTroubleshootTarget(w, req.TargetResourceID)
}

// The measurements come from Azure's internet-measurement fleet, which the
// simulator has no data from, so it offers no providers.
func handleNetworkWatcherAvailableProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		AzureLocations []string `json:"azureLocations"`
		Country        string   `json:"country"`
		State          string   `json:"state"`
		City           string   `json:"city"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"countries": []map[string]any{}})
}

// The aggregation level follows from how precisely the request located the
// providers; the report is empty, for want of measurement data.
func handleNetworkWatcherReachabilityReport(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireNetworkWatcher(w, r); !ok {
		return
	}
	var req struct {
		ProviderLocation struct {
			Country string `json:"country"`
			State   string `json:"state"`
			City    string `json:"city"`
		} `json:"providerLocation"`
		Providers      []string `json:"providers"`
		AzureLocations []string `json:"azureLocations"`
		StartTime      string   `json:"startTime"`
		EndTime        string   `json:"endTime"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProviderLocation.Country == "" || req.StartTime == "" || req.EndTime == "" {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: providerLocation.country, startTime and endTime are required.")
		return
	}
	aggregation := "Country"
	if req.ProviderLocation.State != "" {
		aggregation = "State"
	}
	if req.ProviderLocation.City != "" {
		aggregation = "City"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"aggregationLevel": aggregation,
		"providerLocation": map[string]any{
			"country": req.ProviderLocation.Country,
			"state":   req.ProviderLocation.State,
			"city":    req.ProviderLocation.City,
		},
		"reachabilityReport": []map[string]any{},
	})
}
