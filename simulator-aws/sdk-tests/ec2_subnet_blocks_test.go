package aws_sdk_test

import (
	"net/netip"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_SubnetBlocksAndZones covers what a subnet's blocks and zone are held
// to: AmazonProvidedIpv6CidrBlock gives the VPC a /56 DescribeVpcs lists, a
// subnet's IPv4 block is a /16 to /28 within the VPC's blocks overlapping no
// other subnet, its IPv6 block a /64 of the VPC's, and its zone one of the
// Region's; DescribeAvailabilityZones answers by zone name, zone id and its
// declared filters.
func TestEC2_SubnetBlocksAndZones(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	errorCode := func(err error) string {
		t.Helper()
		var apiErr smithy.APIError
		require.ErrorAs(t, err, &apiErr)
		return apiErr.ErrorCode()
	}

	zones, err := c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(zones.AvailabilityZones), 2)
	zoneA, zoneB := zones.AvailabilityZones[0], zones.AvailabilityZones[1]
	byName, err := c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{ZoneNames: []string{aws.ToString(zoneB.ZoneName)}})
	require.NoError(t, err)
	require.Len(t, byName.AvailabilityZones, 1)
	assert.Equal(t, aws.ToString(zoneB.ZoneId), aws.ToString(byName.AvailabilityZones[0].ZoneId))
	byID, err := c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{ZoneIds: []string{aws.ToString(zoneA.ZoneId)}})
	require.NoError(t, err)
	require.Len(t, byID.AvailabilityZones, 1)
	assert.Equal(t, aws.ToString(zoneA.ZoneName), aws.ToString(byID.AvailabilityZones[0].ZoneName))
	filtered, err := c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{Filters: []types.Filter{
		{Name: aws.String("zone-type"), Values: []string{"availability-zone"}},
		{Name: aws.String("state"), Values: []string{"available"}},
		{Name: aws.String("zone-name"), Values: []string{aws.ToString(zoneA.ZoneName), aws.ToString(zoneB.ZoneName)}},
	}})
	require.NoError(t, err)
	assert.Len(t, filtered.AvailabilityZones, 2)
	none, err := c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{Filters: []types.Filter{
		{Name: aws.String("zone-type"), Values: []string{"local-zone"}},
	}})
	require.NoError(t, err)
	assert.Empty(t, none.AvailabilityZones)
	_, err = c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{ZoneNames: []string{"us-east-1z"}})
	assert.Equal(t, "InvalidParameterValue", errorCode(err))

	// "Requests an Amazon-provided IPv6 CIDR block with a /56 prefix length
	// for the VPC."
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.24.0.0/16"), AmazonProvidedIpv6CidrBlock: aws.Bool(true)})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	t.Cleanup(func() { _, _ = c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)}) })
	require.Len(t, vpc.Vpc.Ipv6CidrBlockAssociationSet, 1)
	ipv6 := aws.ToString(vpc.Vpc.Ipv6CidrBlockAssociationSet[0].Ipv6CidrBlock)
	assert.Regexp(t, `/56$`, ipv6)
	described, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	require.Len(t, described.Vpcs[0].Ipv6CidrBlockAssociationSet, 1)
	assert.Equal(t, ipv6, aws.ToString(described.Vpcs[0].Ipv6CidrBlockAssociationSet[0].Ipv6CidrBlock))
	assert.Equal(t, types.VpcCidrBlockStateCodeAssociated, described.Vpcs[0].Ipv6CidrBlockAssociationSet[0].Ipv6CidrBlockState.State)
	block, err := netip.ParsePrefix(ipv6)
	require.NoError(t, err)
	notA64 := netip.PrefixFrom(block.Masked().Addr(), 80).String()

	// A secondary IPv4 block reaches DescribeVpcs and takes subnets.
	secondary, err := c.AssociateVpcCidrBlock(ctx, &ec2.AssociateVpcCidrBlockInput{VpcId: aws.String(vpcID), CidrBlock: aws.String("10.124.0.0/16")})
	require.NoError(t, err)
	described, err = c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	require.Len(t, described.Vpcs[0].CidrBlockAssociationSet, 2)
	assert.Equal(t, aws.ToString(secondary.CidrBlockAssociation.AssociationId), aws.ToString(described.Vpcs[0].CidrBlockAssociationSet[1].AssociationId))

	subnet := func(input ec2.CreateSubnetInput) (*types.Subnet, error) {
		input.VpcId = aws.String(vpcID)
		out, err := c.CreateSubnet(ctx, &input)
		if err != nil {
			return nil, err
		}
		return out.Subnet, nil
	}
	_, err = subnet(ec2.CreateSubnetInput{CidrBlock: aws.String("10.24.1.0/24"), AvailabilityZone: aws.String("us-east-1z")})
	assert.Equal(t, "InvalidParameterValue", errorCode(err), "a zone the Region does not have")
	_, err = subnet(ec2.CreateSubnetInput{CidrBlock: aws.String("10.25.1.0/24")})
	assert.Equal(t, "InvalidSubnet.Range", errorCode(err), "a block outside the VPC's")
	_, err = subnet(ec2.CreateSubnetInput{CidrBlock: aws.String("10.24.1.0/29")})
	assert.Equal(t, "InvalidSubnet.Range", errorCode(err), "smaller than a /28")
	_, err = subnet(ec2.CreateSubnetInput{CidrBlock: aws.String("10.24.1.0/24"), Ipv6CidrBlock: aws.String("2001:db8::/64")})
	assert.Equal(t, "InvalidSubnet.Range", errorCode(err), "an IPv6 block outside the VPC's")
	_, err = subnet(ec2.CreateSubnetInput{CidrBlock: aws.String("10.24.1.0/24"), Ipv6CidrBlock: aws.String(notA64)})
	assert.Equal(t, "InvalidSubnet.Range", errorCode(err), "an IPv6 block that is not a /64")

	first, err := subnet(ec2.CreateSubnetInput{
		CidrBlock: aws.String("10.24.1.7/24"), AvailabilityZoneId: zoneA.ZoneId, Ipv6CidrBlock: aws.String(elbv2Ipv6SubnetRange(block, 1)),
	})
	require.NoError(t, err)
	assert.Equal(t, "10.24.1.0/24", aws.ToString(first.CidrBlock), "the block is kept in canonical form")
	assert.Equal(t, aws.ToString(zoneA.ZoneName), aws.ToString(first.AvailabilityZone), "the zone id names the zone")
	assert.False(t, aws.ToBool(first.DefaultForAz))
	_, err = subnet(ec2.CreateSubnetInput{CidrBlock: aws.String("10.24.1.128/25"), AvailabilityZone: zoneB.ZoneName})
	assert.Equal(t, "InvalidSubnet.Conflict", errorCode(err), "overlaps the first subnet")
	_, err = subnet(ec2.CreateSubnetInput{CidrBlock: aws.String("10.24.2.0/24"), Ipv6CidrBlock: aws.String(elbv2Ipv6SubnetRange(block, 1))})
	assert.Equal(t, "InvalidSubnet.Conflict", errorCode(err), "takes the first subnet's IPv6 block")
	inSecondary, err := subnet(ec2.CreateSubnetInput{CidrBlock: aws.String("10.124.0.0/20"), AvailabilityZone: zoneB.ZoneName})
	require.NoError(t, err, "the secondary block takes subnets")
	assert.Equal(t, aws.ToString(zoneB.ZoneId), aws.ToString(inSecondary.AvailabilityZoneId))

	// Disassociating the IPv6 block answers with the association DescribeVpcs
	// listed.
	assocID := aws.ToString(described.Vpcs[0].Ipv6CidrBlockAssociationSet[0].AssociationId)
	_, err = c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: first.SubnetId})
	require.NoError(t, err)
	gone, err := c.DisassociateVpcCidrBlock(ctx, &ec2.DisassociateVpcCidrBlockInput{AssociationId: aws.String(assocID)})
	require.NoError(t, err)
	require.NotNil(t, gone.Ipv6CidrBlockAssociation)
	assert.Equal(t, ipv6, aws.ToString(gone.Ipv6CidrBlockAssociation.Ipv6CidrBlock))
	described, err = c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
	require.NoError(t, err)
	assert.Empty(t, described.Vpcs[0].Ipv6CidrBlockAssociationSet)
}
