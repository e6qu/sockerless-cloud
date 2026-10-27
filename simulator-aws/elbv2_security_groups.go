package main

import (
	"context"
	"fmt"
	"github.com/e6qu/sockerless-cloud/sim"
	"net/netip"
	"strings"
)

// The load balancer's groups and node subnets, or the client's address when
// the load balancer preserves it.
type elbv2FlowSource struct {
	groups   []string
	prefixes []netip.Prefix
	client   netip.Addr
}

// nil for a target without security groups.
func elbv2TargetAdmission(lb ELBv2LoadBalancer, tg ELBv2TargetGroup, target ELBv2TargetDescription, port int) func(client netip.Addr) error {
	groups := elbv2TargetSecurityGroups(tg, target)
	if len(groups) == 0 {
		return nil
	}
	source := elbv2LoadBalancerSource(lb)
	return func(client netip.Addr) error {
		flow := source
		if len(flow.groups) == 0 && len(flow.prefixes) == 0 {
			flow.client = client
		}
		if ec2SecurityGroupsAdmit(groups, "tcp", port, flow) {
			return nil
		}
		return fmt.Errorf("security groups %s of target %s admit no %s traffic from the load balancer on port %d", strings.Join(groups, ","), target.ID, tg.Protocol, port)
	}
}

// An instance's groups, or those of the interface holding an IP target's
// address in the group's VPC.
func elbv2TargetSecurityGroups(tg ELBv2TargetGroup, target ELBv2TargetDescription) []string {
	if strings.EqualFold(tg.TargetType, "instance") {
		inst, ok := ec2Instances.Get(target.ID)
		if !ok {
			return nil
		}
		return inst.SecurityGroupIds
	}
	if ec2NetworkInterfaces == nil {
		return nil
	}
	for _, eni := range ec2NetworkInterfacesByPrivateIP.LookupAll(ec2NetworkInterfaces, target.ID, func(eni EC2NetworkInterface) []string {
		return []string{eni.PrivateIpAddress}
	}) {
		if tg.VpcID != "" && eni.VpcId != "" && eni.VpcId != tg.VpcID {
			continue
		}
		return eni.SecurityGroupIds
	}
	return nil
}

var ec2NetworkInterfacesByPrivateIP sim.GenerationIndex[EC2NetworkInterface]

// Application Load Balancers, and Network Load Balancers with security groups,
// reach targets from nodes carrying their groups and subnet addresses; one
// without groups preserves the client's address.
func elbv2LoadBalancerSource(lb ELBv2LoadBalancer) elbv2FlowSource {
	if !strings.EqualFold(lb.Type, "application") && len(lb.SecurityGroups) == 0 {
		return elbv2FlowSource{}
	}
	source := elbv2FlowSource{groups: lb.SecurityGroups}
	for _, subnetID := range lb.Subnets {
		subnet, ok := ec2Subnets.Get(subnetID)
		if !ok {
			continue
		}
		if prefix, err := netip.ParsePrefix(subnet.CidrBlock); err == nil {
			source.prefixes = append(source.prefixes, prefix)
		}
	}
	return source
}

// A CIDR permission admits a covered client address or an overlapping subnet;
// a group permission admits a source holding that group.
func ec2SecurityGroupsAdmit(groupIDs []string, protocol string, port int, source elbv2FlowSource) bool {
	for _, groupID := range groupIDs {
		sg, ok := ec2SecurityGroups.Get(groupID)
		if !ok {
			continue
		}
		for _, perm := range sg.IpPermissions {
			if !ec2PermissionCovers(perm, protocol, port) {
				continue
			}
			if len(perm.IpRanges) == 0 && len(perm.Ipv6Ranges) == 0 && len(perm.UserIdGroupPairs) == 0 && len(perm.PrefixListIds) == 0 {
				return true
			}
			for _, ipRange := range perm.IpRanges {
				prefix, err := netip.ParsePrefix(ipRange.CidrIp)
				if err != nil {
					continue
				}
				if source.client.IsValid() && prefix.Contains(source.client) {
					return true
				}
				for _, subnet := range source.prefixes {
					if prefix.Overlaps(subnet) {
						return true
					}
				}
			}
			for _, pair := range perm.UserIdGroupPairs {
				for _, held := range source.groups {
					if held == pair.GroupId {
						return true
					}
				}
			}
		}
	}
	return false
}

func ec2PermissionCovers(perm EC2IpPermission, protocol string, port int) bool {
	switch strings.ToLower(perm.IpProtocol) {
	case "-1", "all":
		return true
	case "tcp", "6":
		if protocol != "tcp" {
			return false
		}
	case "udp", "17":
		if protocol != "udp" {
			return false
		}
	default:
		if !strings.EqualFold(perm.IpProtocol, protocol) {
			return false
		}
	}
	if perm.FromPort == 0 && perm.ToPort == 0 {
		return true
	}
	return perm.FromPort <= port && port <= perm.ToPort
}

// nil when nothing filters the target.
func elbv2AdmitForwardedFlow(listener ELBv2Listener, tg ELBv2TargetGroup, target ELBv2TargetDescription) func(client netip.Addr) error {
	lb, ok := elbv2LoadBalancers.Get(listener.LoadBalancerArn)
	if !ok {
		return nil
	}
	port := target.Port
	if port == 0 {
		port = tg.Port
	}
	return elbv2TargetAdmission(lb, tg, target, port)
}

// A dropped probe times out, as on Elastic Load Balancing.
type errELBv2HealthCheckBlocked struct{ reason error }

func (e *errELBv2HealthCheckBlocked) Error() string {
	return "health check timed out: " + e.reason.Error()
}
func (e *errELBv2HealthCheckBlocked) Is(target error) bool {
	return target == context.DeadlineExceeded
}
func (e *errELBv2HealthCheckBlocked) Unwrap() error { return e.reason }
