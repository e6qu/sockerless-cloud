package main

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"

	"github.com/e6qu/sockerless-cloud/sim/netrules"
)

// A flow the simulator mediates — a load balancer's or application gateway's
// forward — reaches the target interface only when every NSG on its inbound
// path admits it, the subnet's first, each ending in DenyAllInBound. Traffic
// between containers on one network is not mediated.

// A target no stored interface holds is admitted: nothing of the simulator's
// sits in front of it.
func azureAdmitIngress(protocol string, source netip.Addr, target string, port int) error {
	nic, ok := azureNICByPrivateAddress(target)
	if !ok {
		return nil
	}
	stages, err := azureIngressPacketStages(nic)
	if err != nil {
		return err
	}
	for _, stage := range stages {
		if !azurePacketRulesAccept(stage, protocol, source, port) {
			return fmt.Errorf("the network security groups of %s deny %s/%d from %s", nic.ID, strings.ToLower(protocol), port, source)
		}
	}
	return nil
}

// In priority order the first matching rule decides; an unmatched packet meets
// DenyAllInBound.
func azurePacketRulesAccept(rules []netrules.PacketRule, protocol string, source netip.Addr, port int) bool {
	for _, rule := range rules {
		if azurePacketRuleMatches(rule, protocol, source, port) {
			return strings.EqualFold(rule.Action, "accept")
		}
	}
	return false
}

func azurePacketRuleMatches(rule netrules.PacketRule, protocol string, source netip.Addr, port int) bool {
	if rule.Protocol != "*" && !strings.EqualFold(rule.Protocol, protocol) {
		return false
	}
	if (rule.FromPort != 0 || rule.ToPort != 0) && (port < rule.FromPort || port > rule.ToPort) {
		return false
	}
	prefix, err := netip.ParsePrefix(rule.SourceCIDR)
	if err != nil {
		addr, err := netip.ParseAddr(rule.SourceCIDR)
		if err != nil {
			return false
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	return prefix.Contains(source.Unmap())
}

// target is "host" or "host:port".
func azureNICByPrivateAddress(target string) (NetworkInterface, bool) {
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	if azureNICs == nil || host == "" {
		return NetworkInterface{}, false
	}
	return azureNICsByPrivateAddress.Lookup(azureNICs, host, func(nic NetworkInterface) []string {
		keys := make([]string, 0, len(nic.Properties.IPConfigurations))
		for _, ipcfg := range nic.Properties.IPConfigurations {
			keys = append(keys, ipcfg.Properties.PrivateIPAddress)
		}
		return keys
	})
}

var azureNICsByPrivateAddress sim.GenerationIndex[NetworkInterface]
