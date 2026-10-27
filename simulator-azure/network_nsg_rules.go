package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"

	"github.com/e6qu/sockerless-cloud/sim/netrules"
)

// One stage per NSG, the subnet's first: Azure delivers an inbound packet only
// when every group on its path allows it. nil means no group applies.
func azureIngressPacketStages(nic NetworkInterface) ([][]netrules.PacketRule, error) {
	nsgs := azureAttachedNSGs(nic)
	if len(nsgs) == 0 {
		return nil, nil
	}
	stages := make([][]netrules.PacketRule, 0, len(nsgs))
	for _, nsg := range nsgs {
		rules, err := azureNSGIngressRules(nsg, nic)
		if err != nil {
			return nil, fmt.Errorf("network security group %s: %w", nsg.ID, err)
		}
		stages = append(stages, rules)
	}
	return stages, nil
}

// The group's rules in priority order, then the defaults every group carries:
// AllowVnetInBound and AllowAzureLoadBalancerInBound, with DenyAllInBound as
// the final drop.
func azureNSGIngressRules(nsg NetworkSecurityGroup, nic NetworkInterface) ([]netrules.PacketRule, error) {
	securityRules := append([]SecurityRule(nil), nsg.Properties.SecurityRules...)
	sort.SliceStable(securityRules, func(i, j int) bool {
		if securityRules[i].Properties.Priority == securityRules[j].Properties.Priority {
			return securityRules[i].Name < securityRules[j].Name
		}
		return securityRules[i].Properties.Priority < securityRules[j].Properties.Priority
	})
	var rules []netrules.PacketRule
	for _, rule := range securityRules {
		props := rule.Properties
		if !strings.EqualFold(defaultString(props.Direction, "Inbound"), "Inbound") {
			continue
		}
		// A rule scoped to destination application security groups is not part of the
		// filter on interfaces outside them.
		if len(props.DestinationApplicationSecurityGroups) > 0 &&
			!azureNICInApplicationSecurityGroups(nic, props.DestinationApplicationSecurityGroups) {
			continue
		}
		verdict := "drop"
		if strings.EqualFold(props.Access, "Allow") {
			verdict = "accept"
		}
		expanded, err := azurePacketRulesForSecurityRule(props, verdict)
		if err != nil {
			return nil, fmt.Errorf("security rule %s: %w", rule.Name, err)
		}
		rules = append(rules, expanded...)
	}
	for _, cidr := range azureNICVNetCIDRs(nic) {
		rules = append(rules, netrules.PacketRule{Protocol: "*", SourceCIDR: cidr, Action: "accept"})
	}
	rules = append(rules, netrules.PacketRule{Protocol: "*", SourceCIDR: azureLoadBalancerProbeCIDR, Action: "accept"})
	return rules, nil
}

const azureLoadBalancerProbeCIDR = azureResolverIPv4 + "/32"

// The subnet's before the interface's own.
func azureAttachedNSGs(nic NetworkInterface) []NetworkSecurityGroup {
	seen := map[string]bool{}
	var out []NetworkSecurityGroup
	add := func(id string) {
		if id == "" || seen[id] || azureNSGs == nil {
			return
		}
		seen[id] = true
		if nsg, ok := azureNSGs.Get(id); ok {
			out = append(out, nsg)
		}
	}
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Subnet == nil || azureSubnets == nil {
			continue
		}
		if subnet, ok := azureSubnets.Get(ipcfg.Properties.Subnet.ID); ok && subnet.Properties.NetworkSecurityGroup != nil {
			add(subnet.Properties.NetworkSecurityGroup.ID)
		}
	}
	if nic.Properties.NetworkSecurityGroup != nil {
		add(nic.Properties.NetworkSecurityGroup.ID)
	}
	return out
}

func azurePacketRulesForSecurityRule(props SecurityRuleProperties, verdict string) ([]netrules.PacketRule, error) {
	// A rule against source application security groups matches their members
	// only, so an empty group matches nothing rather than "any address".
	var sources []string
	if len(props.SourceApplicationSecurityGroups) > 0 {
		sources = azureApplicationSecurityGroupMemberIPs(props.SourceApplicationSecurityGroups)
	} else {
		sources = azureAddressPrefixes(props.SourceAddressPrefix, props.SourceAddressPrefixes)
	}
	return netrules.ExpandRules(props.Protocol, sources, azurePortRanges(props.DestinationPortRange, props.DestinationPortRanges), verdict)
}

func azureInvalidSecurityRulePort(props SecurityRuleProperties) string {
	for _, port := range azurePortRanges(props.DestinationPortRange, props.DestinationPortRanges) {
		if _, _, err := netrules.PortRange(port); err != nil {
			return port
		}
	}
	return ""
}

func azureWriteInvalidPortRange(w http.ResponseWriter, port string) {
	AzureErrorf(w, "SecurityRuleInvalidPortRange", http.StatusBadRequest,
		"Security rule has invalid Port range. Value provided: %s. Value should be an integer OR integer range with '-' delimiter. Valid range 0-65535.", port)
}

func azureAddressPrefixes(single string, many []string) []string {
	var values []string
	if single != "" {
		values = append(values, single)
	}
	values = append(values, many...)
	if len(values) == 0 {
		return []string{"0.0.0.0/0"}
	}
	var out []string
	for _, value := range values {
		switch {
		case value == "" || value == "*" || strings.EqualFold(value, "Internet"):
			out = append(out, "0.0.0.0/0")
		case strings.EqualFold(value, "VirtualNetwork"):
			out = append(out, azureAllVNetCIDRs()...)
		case strings.EqualFold(value, "AzureLoadBalancer"):
			out = append(out, azureLoadBalancerProbeCIDR)
		default:
			out = append(out, value)
		}
	}
	return out
}

func azurePortRanges(single string, many []string) []string {
	var values []string
	if single != "" {
		values = append(values, single)
	}
	values = append(values, many...)
	if len(values) == 0 {
		return []string{""}
	}
	return values
}

func azureNICVNetCIDRs(nic NetworkInterface) []string {
	seen := map[string]bool{}
	var out []string
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.Subnet == nil || azureSubnets == nil {
			continue
		}
		subnet, ok := azureSubnets.Get(ipcfg.Properties.Subnet.ID)
		if !ok {
			continue
		}
		if azureVnets == nil {
			continue
		}
		vnet, ok := azureVnets.Get(azureVNetIDOfSubnet(subnet.ID))
		if !ok {
			continue
		}
		for _, cidr := range vnet.Properties.AddressSpace.AddressPrefixes {
			if !seen[cidr] {
				seen[cidr] = true
				out = append(out, cidr)
			}
		}
	}
	return out
}

func azureAllVNetCIDRs() []string {
	if azureVnets == nil {
		return []string{"0.0.0.0/0"}
	}
	var out []string
	for _, vnet := range azureVNetsAll.LookupAll(azureVnets, "vnet", func(VirtualNetwork) []string { return []string{"vnet"} }) {
		out = append(out, vnet.Properties.AddressSpace.AddressPrefixes...)
	}
	if len(out) == 0 {
		return []string{"0.0.0.0/0"}
	}
	return out
}

// One key, so the data plane reads the store once per change, not per flow.
var azureVNetsAll sim.GenerationIndex[VirtualNetwork]
