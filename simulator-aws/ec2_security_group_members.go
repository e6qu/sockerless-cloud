package main

import (
	"net/netip"
	"slices"
	"sort"

	"github.com/e6qu/sockerless-cloud/sim/netrules"
)

// One packet rule per source. A permission naming no source means every
// source; a referenced group expands to its members' addresses, since rules
// match prefixes, not group IDs.
func ec2BuildIngressPacketRules(securityGroupIDs []string) []netrules.PacketRule {
	var rules []netrules.PacketRule
	// Read each group's members once per expansion; otherwise every rule naming
	// the group walks the whole ENI, instance and task stores.
	members := map[string][]string{}
	membersOf := func(groupID string) []string {
		if cidrs, ok := members[groupID]; ok {
			return cidrs
		}
		cidrs := ec2SGMemberCIDRs(groupID)
		members[groupID] = cidrs
		return cidrs
	}
	for _, groupID := range securityGroupIDs {
		sg, ok := ec2SecurityGroups.Get(groupID)
		if !ok {
			continue
		}
		for _, perm := range sg.IpPermissions {
			if len(perm.IpRanges) == 0 && len(perm.Ipv6Ranges) == 0 && len(perm.UserIdGroupPairs) == 0 {
				rules = append(rules, netrules.PacketRule{
					Protocol:   perm.IpProtocol,
					SourceCIDR: "0.0.0.0/0",
					FromPort:   perm.FromPort,
					ToPort:     perm.ToPort,
				})
				continue
			}
			for _, ipRange := range perm.IpRanges {
				rules = append(rules, netrules.PacketRule{
					Protocol:   perm.IpProtocol,
					SourceCIDR: ipRange.CidrIp,
					FromPort:   perm.FromPort,
					ToPort:     perm.ToPort,
				})
			}
			for _, ipRange := range perm.Ipv6Ranges {
				rules = append(rules, netrules.PacketRule{
					Protocol:   perm.IpProtocol,
					SourceCIDR: ipRange.CidrIpv6,
					FromPort:   perm.FromPort,
					ToPort:     perm.ToPort,
				})
			}
			for _, gp := range perm.UserIdGroupPairs {
				for _, src := range membersOf(gp.GroupId) {
					rules = append(rules, netrules.PacketRule{
						Protocol:   perm.IpProtocol,
						SourceCIDR: src,
						FromPort:   perm.FromPort,
						ToPort:     perm.ToPort,
					})
				}
			}
		}
	}
	return rules
}

// The /32s of every network interface, instance and live Amazon ECS task in
// the group.
func ec2SGMemberCIDRs(groupID string) []string {
	seen := map[string]bool{}
	add := func(ip string) {
		if ip == "" {
			return
		}
		seen[ip+"/32"] = true
	}
	for _, eni := range ec2NetworkInterfaces.List() {
		for _, id := range eni.SecurityGroupIds {
			if id == groupID {
				add(eni.PrivateIpAddress)
				break
			}
		}
	}
	for _, inst := range ec2Instances.List() {
		for _, id := range inst.SecurityGroupIds {
			if id == groupID {
				add(inst.PrivateIpAddress)
				break
			}
		}
	}
	for _, task := range ecsTasks.List() {
		// A stopped task's address went with its ENI; the store keeps the task an hour
		// for DescribeTasks.
		if task.LastStatus == ECSTaskStatusStopped {
			continue
		}
		if !ecsTaskUsesSecurityGroup(task, groupID) {
			continue
		}
		for _, att := range task.Attachments {
			if att.Type != "ElasticNetworkInterface" {
				continue
			}
			for _, d := range att.Details {
				if d.Name == "privateIPv4Address" {
					add(d.Value)
				}
			}
		}
	}
	// A load balancer's nodes are members of its groups, and a target sees them at
	// the address the simulator forwards from: the network's gateway.
	if elbv2LoadBalancers != nil && ec2Vpcs != nil {
		for _, lb := range elbv2LoadBalancers.List() {
			if !slices.Contains(lb.SecurityGroups, groupID) {
				continue
			}
			if vpc, ok := ec2Vpcs.Get(lb.VpcID); ok {
				if prefix, err := netip.ParsePrefix(vpc.CidrBlock); err == nil {
					add(prefix.Masked().Addr().Next().String())
				}
			}
		}
	}
	var out []string
	for cidr := range seen {
		out = append(out, cidr)
	}
	sort.Strings(out)
	return out
}

// The task's ENI carries the VPC's default group when the run named none.
func ecsTaskUsesSecurityGroup(task ECSTask, groupID string) bool {
	groups, _, ok := ecsTaskENIGroupsOf(task)
	if !ok {
		return false
	}
	for _, id := range groups {
		if id == groupID {
			return true
		}
	}
	return false
}
