package aws_sdk_test

import (
	"net/netip"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_SubnetMappings covers what CreateLoadBalancer and SetSubnets do
// with SubnetMappings: a Network Load Balancer holds the Elastic IP address,
// private IPv4 address or IPv6 address each mapping names and lists it under
// the subnet's LoadBalancerAddresses; an Application Load Balancer takes no
// Elastic IP address; a request naming both Subnets and SubnetMappings, an
// unknown allocation, and an address outside the subnet are refused.
func TestELBv2_SubnetMappings(t *testing.T) {
	t.Parallel()
	ec2c := ec2Client()
	elb := elbv2Client()
	vpcID, subnets := elbv2FidVPCSubnets(t, "10.22.0.0/16", "10.22.1.0/24", "10.22.2.0/24")
	ipv6 := elbv2VPCIpv6Block(t, vpcID)
	_, err := ec2c.AssociateSubnetCidrBlock(ctx, &ec2.AssociateSubnetCidrBlockInput{
		SubnetId: aws.String(subnets[0]), Ipv6CidrBlock: aws.String(elbv2Ipv6SubnetRange(ipv6, 1)),
	})
	require.NoError(t, err)
	eip, err := ec2c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: ec2types.DomainTypeVpc})
	require.NoError(t, err)
	allocationID := aws.ToString(eip.AllocationId)

	_, err = elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniqueName("map-both")), Type: elbtypes.LoadBalancerTypeEnumNetwork,
		Subnets:        []string{subnets[0]},
		SubnetMappings: []elbtypes.SubnetMapping{{SubnetId: aws.String(subnets[1])}},
	})
	requireELBv2Error(t, err, "InvalidConfigurationRequest", "but not both")

	_, err = elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniqueName("map-alb")), Type: elbtypes.LoadBalancerTypeEnumApplication,
		SubnetMappings: []elbtypes.SubnetMapping{
			{SubnetId: aws.String(subnets[0]), AllocationId: aws.String(allocationID)},
			{SubnetId: aws.String(subnets[1])},
		},
	})
	requireELBv2Error(t, err, "InvalidConfigurationRequest", "AllocationId")

	_, err = elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniqueName("map-unknown")), Type: elbtypes.LoadBalancerTypeEnumNetwork,
		SubnetMappings: []elbtypes.SubnetMapping{{SubnetId: aws.String(subnets[0]), AllocationId: aws.String("eipalloc-0000000000000000f")}},
	})
	var notFound *elbtypes.AllocationIdNotFoundException
	require.ErrorAs(t, err, &notFound)

	_, err = elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniqueName("map-outside")), Type: elbtypes.LoadBalancerTypeEnumNetwork, Scheme: elbtypes.LoadBalancerSchemeEnumInternal,
		SubnetMappings: []elbtypes.SubnetMapping{{SubnetId: aws.String(subnets[0]), PrivateIPv4Address: aws.String("10.22.2.10")}},
	})
	requireELBv2Error(t, err, "InvalidConfigurationRequest", "PrivateIPv4Address")

	_, err = elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniqueName("map-ipv6-outside")), Type: elbtypes.LoadBalancerTypeEnumNetwork, IpAddressType: elbtypes.IpAddressTypeDualstack,
		SubnetMappings: []elbtypes.SubnetMapping{{SubnetId: aws.String(subnets[0]), IPv6Address: aws.String("2001:db8::10")}},
	})
	requireELBv2Error(t, err, "InvalidConfigurationRequest", "IPv6Address")

	// An internet-facing Network Load Balancer takes one Elastic IP address
	// per subnet and reports its address under the subnet.
	name := uniqueName("map-nlb")
	created, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(name), Type: elbtypes.LoadBalancerTypeEnumNetwork,
		SubnetMappings: []elbtypes.SubnetMapping{{SubnetId: aws.String(subnets[0]), AllocationId: aws.String(allocationID)}},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(created.LoadBalancers[0].LoadBalancerArn)
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	})
	zones := created.LoadBalancers[0].AvailabilityZones
	require.Len(t, zones, 1)
	require.Len(t, zones[0].LoadBalancerAddresses, 1)
	assert.Equal(t, allocationID, aws.ToString(zones[0].LoadBalancerAddresses[0].AllocationId))
	assert.Equal(t, aws.ToString(eip.PublicIp), aws.ToString(zones[0].LoadBalancerAddresses[0].IpAddress))

	// The same settings again answer the same load balancer; the same
	// mapping without its Elastic IP address is a different one.
	again, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(name), Type: elbtypes.LoadBalancerTypeEnumNetwork,
		SubnetMappings: []elbtypes.SubnetMapping{{SubnetId: aws.String(subnets[0]), AllocationId: aws.String(allocationID)}},
	})
	require.NoError(t, err)
	assert.Equal(t, lbArn, aws.ToString(again.LoadBalancers[0].LoadBalancerArn))
	_, err = elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(name), Type: elbtypes.LoadBalancerTypeEnumNetwork,
		SubnetMappings: []elbtypes.SubnetMapping{{SubnetId: aws.String(subnets[0])}},
	})
	var duplicate *elbtypes.DuplicateLoadBalancerNameException
	require.ErrorAs(t, err, &duplicate)

	// SetSubnets replaces the mappings; DescribeLoadBalancers reads them back.
	set, err := elb.SetSubnets(ctx, &elbv2.SetSubnetsInput{
		LoadBalancerArn: aws.String(lbArn), IpAddressType: elbtypes.IpAddressTypeDualstack,
		SubnetMappings: []elbtypes.SubnetMapping{{
			SubnetId: aws.String(subnets[0]), AllocationId: aws.String(allocationID),
			IPv6Address:         aws.String(elbv2Ipv6Address(ipv6, 1, 0x10)),
			SourceNatIpv6Prefix: aws.String("auto_assigned"),
		}},
	})
	require.NoError(t, err)
	require.Len(t, set.AvailabilityZones, 1)
	assert.Equal(t, elbv2Ipv6Address(ipv6, 1, 0x10), aws.ToString(set.AvailabilityZones[0].LoadBalancerAddresses[0].IPv6Address))
	require.Len(t, set.AvailabilityZones[0].SourceNatIpv6Prefixes, 1)
	assert.Regexp(t, `/80$`, set.AvailabilityZones[0].SourceNatIpv6Prefixes[0], "an auto_assigned source NAT prefix is a /80 of the subnet's block")
	described, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	assert.Equal(t, set.AvailabilityZones, described.LoadBalancers[0].AvailabilityZones)
	assert.Equal(t, elbtypes.IpAddressTypeDualstack, described.LoadBalancers[0].IpAddressType)
}

// elbv2Ipv6Address is the host'th address of the index'th /64 of a VPC's IPv6
// block.
func elbv2Ipv6Address(block netip.Prefix, index int, host byte) string {
	addr := block.Masked().Addr().As16()
	addr[7] += byte(index)
	addr[15] = host
	return netip.AddrFrom16(addr).String()
}
