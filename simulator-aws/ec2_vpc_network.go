package main

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ecsVPCNetworkName is the engine network realizing a VPC.
func ecsVPCNetworkName(vpcID string) string { return "sockerless-sim-vpc-" + vpcID }

// ec2VPCResolverAddress is the address of the Amazon Route 53 Resolver inside
// a VPC: the VPC's base plus two, which the VPC reserves for it.
func ec2VPCResolverAddress(cidr netip.Prefix) netip.Addr {
	return cidr.Masked().Addr().Next().Next()
}

// ec2EnsureVPCNetwork creates the engine network realizing a VPC over the
// VPC's own CIDR, at the first workload attached to it, with the sidecar
// that answers the Resolver and the instance metadata service at the VPC's
// reserved address, and returns the network. A live engine network already
// holding an overlapping range fails with sim.ErrAddressRangeInUse.
func ec2EnsureVPCNetwork(ctx context.Context, vpcID string) (awsWorkloadNetwork, error) {
	vpc, ok := ec2Vpcs.Get(vpcID)
	if !ok {
		return awsWorkloadNetwork{}, fmt.Errorf("vpc %s not found", vpcID)
	}
	if vpc.CidrBlock == "" {
		return awsWorkloadNetwork{}, fmt.Errorf("vpc %s has no CIDR block", vpcID)
	}
	cidr, err := netip.ParsePrefix(vpc.CidrBlock)
	if err != nil {
		return awsWorkloadNetwork{}, fmt.Errorf("vpc %s CIDR %q: %w", vpcID, vpc.CidrBlock, err)
	}
	sidecars, err := awsNetworkSidecars()
	if err != nil {
		return awsWorkloadNetwork{}, err
	}
	name := ecsVPCNetworkName(vpcID)
	if _, err := sim.EnsureCloudNetwork(ctx, sim.CloudNetwork{
		Name:   name,
		CIDR:   cidr,
		Labels: map[string]string{"sockerless-sim-vpc": vpcID},
	}); err != nil {
		return awsWorkloadNetwork{}, err
	}
	running, err := sidecars.Ensure(ctx, name, ec2VPCResolverAddress(cidr))
	if err != nil {
		return awsWorkloadNetwork{}, fmt.Errorf("vpc %s resolver: %w", vpcID, err)
	}
	route53RegisterResolverNetwork(name, vpcID)
	if err := awsJoinMicroVMNetwork(ctx, name, cidr); err != nil {
		return awsWorkloadNetwork{}, fmt.Errorf("vpc %s: %w", vpcID, err)
	}
	network := awsWorkloadNetwork{Name: name, Sidecar: running.Address}
	if err := ec2EnsureVPCNatGateways(ctx, vpcID, network); err != nil {
		return awsWorkloadNetwork{}, err
	}
	ec2VPCNetworksUp.Store(vpcID, network)
	return network, nil
}

// ec2VPCNetworksUp holds the engine network of every VPC this process has
// realized, by VPC id.
var ec2VPCNetworksUp sync.Map

// ec2VPCNetworkUp is the VPC's engine network when this process realized
// it: a NAT gateway created afterwards starts its data plane on it at once,
// one created before gets it with the network.
func ec2VPCNetworkUp(vpcID string) (awsWorkloadNetwork, bool) {
	v, ok := ec2VPCNetworksUp.Load(vpcID)
	if !ok {
		return awsWorkloadNetwork{}, false
	}
	network, ok := v.(awsWorkloadNetwork)
	return network, ok
}

// ec2RemoveVPCNetwork removes a deleted VPC's sidecar and engine network.
func ec2RemoveVPCNetwork(ctx context.Context, vpcID string) error {
	name := ecsVPCNetworkName(vpcID)
	for _, natgw := range ec2NatGateways.Filter(func(n EC2NatGateway) bool { return n.VpcId == vpcID }) {
		ec2RemoveNatGatewayEgress(natgw.NatGatewayId)
	}
	if sidecars, err := awsNetworkSidecars(); err == nil {
		sidecars.Remove(name)
	}
	r53ResolverVPCs.Delete(name)
	ec2VPCNetworksUp.Delete(vpcID)
	if err := awsLeaveMicroVMNetwork(ctx, name); err != nil {
		return err
	}
	return sim.RemoveDockerNetworkContext(ctx, name)
}
