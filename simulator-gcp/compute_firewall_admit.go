package main

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/netrules"
)

// gcpLoadBalancerSources are the ranges Google's load balancer proxies and
// health checkers reach a backend from. Cloud Load Balancing documents that a
// backend's firewall rules must allow both, so a flow is admitted only when
// both are.
var gcpLoadBalancerSources = []netip.Addr{
	netip.MustParseAddr("130.211.0.1"),
	netip.MustParseAddr("35.191.0.1"),
}

// gcpPacketRulesAdmit evaluates compiled ingress rules the way Compute Engine
// does: in priority order, the first rule matching the packet decides, and a
// packet no rule matches meets the implied deny-ingress rule.
func gcpPacketRulesAdmit(rules []netrules.PacketRule, protocol string, source netip.Addr, port int) bool {
	for _, rule := range rules {
		if !gcpPacketRuleMatches(rule, protocol, source, port) {
			continue
		}
		return strings.EqualFold(rule.Action, "accept")
	}
	return false
}

func gcpPacketRuleMatches(rule netrules.PacketRule, protocol string, source netip.Addr, port int) bool {
	switch strings.ToLower(rule.Protocol) {
	case "", "all":
	case strings.ToLower(protocol):
	default:
		return false
	}
	if rule.SourceCIDR != "" {
		prefix, err := netip.ParsePrefix(rule.SourceCIDR)
		if err != nil {
			addr, err := netip.ParseAddr(rule.SourceCIDR)
			if err != nil {
				return false
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if !prefix.Contains(source) {
			return false
		}
	}
	if rule.FromPort == 0 && rule.ToPort == 0 {
		return true
	}
	return port >= rule.FromPort && port <= rule.ToPort
}

// gcpBackendAdmit decides whether the firewall rules of a load balancer
// target's interface let Google's proxies and health checkers reach it on
// port, and names the range they refuse when they do not.
func gcpBackendAdmit(target gcpLBTarget, port int) error {
	rules, err := gcpIngressPacketRules(target.Instance, target.Instance.NetworkInterfaces[0])
	if err != nil {
		return err
	}
	for _, source := range gcpLoadBalancerSources {
		if !gcpPacketRulesAdmit(rules, "tcp", source, port) {
			return fmt.Errorf("the firewall rules of %s do not allow tcp:%d from %s", target.Instance.Name, port, source)
		}
	}
	return nil
}
