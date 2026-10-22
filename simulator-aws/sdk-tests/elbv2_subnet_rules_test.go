package aws_sdk_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// elbv2VPCIpv6Block gives the VPC an Amazon-provided IPv6 CIDR block.
func elbv2VPCIpv6Block(t *testing.T, vpcID string) netip.Prefix {
	t.Helper()
	out, err := ec2Client().AssociateVpcCidrBlock(ctx, &ec2.AssociateVpcCidrBlockInput{
		VpcId: aws.String(vpcID), AmazonProvidedIpv6CidrBlock: aws.Bool(true),
	})
	require.NoError(t, err)
	block, err := netip.ParsePrefix(aws.ToString(out.Ipv6CidrBlockAssociation.Ipv6CidrBlock))
	require.NoError(t, err)
	return block
}

// elbv2Ipv6SubnetRange returns the index'th /64 of a VPC's IPv6 block.
func elbv2Ipv6SubnetRange(block netip.Prefix, index int) string {
	addr := block.Masked().Addr().As16()
	addr[7] += byte(index)
	return netip.PrefixFrom(netip.AddrFrom16(addr), 64).String()
}

func requireELBv2Error(t *testing.T, err error, code, message string) {
	t.Helper()
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "expected an API error, got %v", err)
	assert.Equal(t, code, apiErr.ErrorCode())
	assert.Contains(t, apiErr.ErrorMessage(), message)
}

// TestELBv2_SubnetRules covers what CreateLoadBalancer, SetSubnets and
// SetIpAddressType refuse: an Application Load Balancer in fewer than two
// Availability Zones, two subnets in one zone for any type, a subnet that does
// not exist, and a dualstack load balancer on a subnet with no IPv6 CIDR
// block. A Network Load Balancer takes a single zone.
func TestELBv2_SubnetRules(t *testing.T) {
	t.Parallel()
	ec2c := ec2Client()
	elb := elbv2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.164.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	ipv6 := elbv2VPCIpv6Block(t, vpcID)
	subnet := func(cidr, zone, ipv6Range string) string {
		input := &ec2.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String(cidr), AvailabilityZone: aws.String(zone)}
		if ipv6Range != "" {
			input.Ipv6CidrBlock = aws.String(ipv6Range)
		}
		out, err := ec2c.CreateSubnet(ctx, input)
		require.NoError(t, err)
		return aws.ToString(out.Subnet.SubnetId)
	}
	zoneA := subnet("10.164.1.0/24", "us-east-1a", elbv2Ipv6SubnetRange(ipv6, 1))
	zoneAToo := subnet("10.164.2.0/24", "us-east-1a", "")
	zoneB := subnet("10.164.3.0/24", "us-east-1b", "")
	create := func(name string, lbType elbtypes.LoadBalancerTypeEnum, ipType elbtypes.IpAddressType, subnets ...string) (*elbv2.CreateLoadBalancerOutput, error) {
		return elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
			Name: aws.String(uniqueName(name)), Type: lbType, IpAddressType: ipType, Subnets: subnets,
		})
	}

	_, err = create("one-zone-alb", elbtypes.LoadBalancerTypeEnumApplication, "", zoneA)
	requireELBv2Error(t, err, "ValidationError", "At least two subnets in two different Availability Zones must be specified")

	for _, lbType := range []elbtypes.LoadBalancerTypeEnum{elbtypes.LoadBalancerTypeEnumApplication, elbtypes.LoadBalancerTypeEnumNetwork} {
		_, err = create("same-zone-"+string(lbType)[:3], lbType, "", zoneA, zoneAToo, zoneB)
		var sameZone *elbtypes.InvalidConfigurationRequestException
		require.ErrorAs(t, err, &sameZone, "%s on two subnets in one zone", lbType)
		assert.Contains(t, sameZone.ErrorMessage(), "multiple subnets in the same Availability Zone")
	}

	_, err = create("missing-subnet", elbtypes.LoadBalancerTypeEnumApplication, "", zoneA, "subnet-0000000000000dead")
	var missing *elbtypes.SubnetNotFoundException
	require.ErrorAs(t, err, &missing)

	_, err = create("dualstack-ipv4", elbtypes.LoadBalancerTypeEnumApplication, elbtypes.IpAddressTypeDualstack, zoneA, zoneB)
	var noIpv6 *elbtypes.InvalidSubnetException
	require.ErrorAs(t, err, &noIpv6)
	assert.Contains(t, noIpv6.ErrorMessage(), zoneB)

	nlb, err := create("one-zone-nlb", elbtypes.LoadBalancerTypeEnumNetwork, "", zoneAToo)
	require.NoError(t, err, "a Network Load Balancer takes subnets from one or more Availability Zones")
	require.Len(t, nlb.LoadBalancers[0].AvailabilityZones, 1)
	assert.Equal(t, "us-east-1a", aws.ToString(nlb.LoadBalancers[0].AvailabilityZones[0].ZoneName))

	alb, err := create("two-zone-alb", elbtypes.LoadBalancerTypeEnumApplication, "", zoneA, zoneB)
	require.NoError(t, err)
	albArn := aws.ToString(alb.LoadBalancers[0].LoadBalancerArn)
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(albArn)})
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: nlb.LoadBalancers[0].LoadBalancerArn})
	})

	_, err = elb.SetSubnets(ctx, &elbv2.SetSubnetsInput{LoadBalancerArn: aws.String(albArn), Subnets: []string{zoneA}})
	requireELBv2Error(t, err, "ValidationError", "At least two subnets in two different Availability Zones must be specified")
	_, err = elb.SetSubnets(ctx, &elbv2.SetSubnetsInput{LoadBalancerArn: aws.String(albArn), Subnets: []string{zoneA, zoneAToo}})
	var sameZone *elbtypes.InvalidConfigurationRequestException
	require.ErrorAs(t, err, &sameZone)

	_, err = elb.SetIpAddressType(ctx, &elbv2.SetIpAddressTypeInput{LoadBalancerArn: aws.String(albArn), IpAddressType: elbtypes.IpAddressTypeDualstack})
	require.ErrorAs(t, err, &noIpv6, "zone B's subnet has no IPv6 CIDR block yet")

	associated, err := ec2c.AssociateSubnetCidrBlock(ctx, &ec2.AssociateSubnetCidrBlockInput{
		SubnetId: aws.String(zoneB), Ipv6CidrBlock: aws.String(elbv2Ipv6SubnetRange(ipv6, 3)),
	})
	require.NoError(t, err)
	subnets, err := ec2c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{zoneB}})
	require.NoError(t, err)
	require.Len(t, subnets.Subnets[0].Ipv6CidrBlockAssociationSet, 1, "DescribeSubnets reads back the associated IPv6 CIDR block")
	assert.Equal(t, aws.ToString(associated.Ipv6CidrBlockAssociation.AssociationId),
		aws.ToString(subnets.Subnets[0].Ipv6CidrBlockAssociationSet[0].AssociationId))

	set, err := elb.SetIpAddressType(ctx, &elbv2.SetIpAddressTypeInput{LoadBalancerArn: aws.String(albArn), IpAddressType: elbtypes.IpAddressTypeDualstack})
	require.NoError(t, err)
	assert.Equal(t, elbtypes.IpAddressTypeDualstack, set.IpAddressType)
	_, err = elb.SetIpAddressType(ctx, &elbv2.SetIpAddressTypeInput{LoadBalancerArn: aws.String(albArn), IpAddressType: elbtypes.IpAddressTypeIpv4})
	require.NoError(t, err)

	_, err = ec2c.DisassociateSubnetCidrBlock(ctx, &ec2.DisassociateSubnetCidrBlockInput{
		AssociationId: associated.Ipv6CidrBlockAssociation.AssociationId,
	})
	require.NoError(t, err)
	subnets, err = ec2c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{zoneB}})
	require.NoError(t, err)
	assert.Empty(t, subnets.Subnets[0].Ipv6CidrBlockAssociationSet, "a disassociated IPv6 CIDR block leaves the subnet")
}
