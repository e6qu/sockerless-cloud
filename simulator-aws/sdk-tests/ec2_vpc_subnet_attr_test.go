package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func describeOneSubnet(t *testing.T, c *ec2.Client, id string) types.Subnet {
	t.Helper()
	out, err := c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{id}})
	require.NoError(t, err)
	require.Len(t, out.Subnets, 1)
	return out.Subnets[0]
}

// TestEC2_SubnetAttributeFidelitySDK covers the ModifySubnetAttribute silent
// no-op + Subnet response-field gaps: every attribute set via
// ModifySubnetAttribute must round-trip through DescribeSubnets (previously only
// MapPublicIpOnLaunch was parsed; the rest were silently dropped), and the
// computed fields (availableIpAddressCount, availabilityZoneId, subnetArn) must
// be populated.
func TestEC2_SubnetAttributeFidelitySDK(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.95.0.0/16")})
	require.NoError(t, err)
	sn, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpc.Vpc.VpcId,
		CidrBlock:        aws.String("10.95.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	id := aws.ToString(sn.Subnet.SubnetId)

	// Computed fields populated at create.
	got := describeOneSubnet(t, c, id)
	assert.Equal(t, int32(251), aws.ToInt32(got.AvailableIpAddressCount), "a /24 has 256-5 usable addresses")
	assert.NotEmpty(t, aws.ToString(got.AvailabilityZoneId), "availability_zone_id must be populated")
	assert.Contains(t, aws.ToString(got.SubnetArn), ":subnet/"+id)

	// Each attribute must persist through ModifySubnetAttribute (not silently dropped).
	_, err = c.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
		SubnetId:                    aws.String(id),
		AssignIpv6AddressOnCreation: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err)
	_, err = c.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
		SubnetId:    aws.String(id),
		EnableDns64: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err)
	_, err = c.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
		SubnetId:                             aws.String(id),
		EnableResourceNameDnsARecordOnLaunch: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err)
	_, err = c.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
		SubnetId:                       aws.String(id),
		PrivateDnsHostnameTypeOnLaunch: types.HostnameTypeResourceName,
	})
	require.NoError(t, err)

	got = describeOneSubnet(t, c, id)
	assert.True(t, aws.ToBool(got.AssignIpv6AddressOnCreation), "assign_ipv6_address_on_creation must persist")
	assert.True(t, aws.ToBool(got.EnableDns64), "enable_dns64 must persist")
	require.NotNil(t, got.PrivateDnsNameOptionsOnLaunch)
	assert.True(t, aws.ToBool(got.PrivateDnsNameOptionsOnLaunch.EnableResourceNameDnsARecord),
		"enable_resource_name_dns_a_record_on_launch must persist")
	assert.Equal(t, types.HostnameTypeResourceName, got.PrivateDnsNameOptionsOnLaunch.HostnameType,
		"private_dns_hostname_type_on_launch must persist")
}

// TestEC2_SubnetFiltersSDK covers DescribeSubnets filter coverage: it previously
// handled only a single SubnetId.1 + vpc-id, returning ALL subnets for any other
// filter. tag: / cidr-block / availability-zone must now scope the result.
func TestEC2_SubnetFiltersSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.96.0.0/16")})
	require.NoError(t, err)
	mk := func(cidr, az, env string) string {
		out, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String(cidr), AvailabilityZone: aws.String(az),
			TagSpecifications: []types.TagSpecification{{
				ResourceType: types.ResourceTypeSubnet,
				Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String(env)}},
			}},
		})
		require.NoError(t, err)
		return aws.ToString(out.Subnet.SubnetId)
	}
	a := mk("10.96.1.0/24", "us-east-1a", "prod")
	_ = mk("10.96.2.0/24", "us-east-1b", "dev")

	byTag, err := c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{{Name: aws.String("tag:env"), Values: []string{"prod"}}},
	})
	require.NoError(t, err)
	require.Len(t, byTag.Subnets, 1, "tag:env=prod must return exactly the prod subnet")
	assert.Equal(t, a, aws.ToString(byTag.Subnets[0].SubnetId))

	byCidr, err := c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{{Name: aws.String("cidr-block"), Values: []string{"10.96.2.0/24"}}},
	})
	require.NoError(t, err)
	require.Len(t, byCidr.Subnets, 1, "cidr-block filter must scope")
	assert.Equal(t, "10.96.2.0/24", aws.ToString(byCidr.Subnets[0].CidrBlock))
}

// TestEC2_VpcAttributeFidelitySDK covers CreateVpc instance_tenancy round-trip,
// the dhcp_options_id computed attribute, and the ModifyVpcAttribute
// EnableNetworkAddressUsageMetrics silent no-op.
func TestEC2_VpcAttributeFidelitySDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock:       aws.String("10.97.0.0/16"),
		InstanceTenancy: types.TenancyDefault,
	})
	require.NoError(t, err)
	id := aws.ToString(vpc.Vpc.VpcId)

	desc, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{id}})
	require.NoError(t, err)
	require.Len(t, desc.Vpcs, 1)
	assert.Equal(t, types.TenancyDefault, desc.Vpcs[0].InstanceTenancy)
	assert.Equal(t, "default", aws.ToString(desc.Vpcs[0].DhcpOptionsId), "dhcp_options_id is computed as 'default'")

	_, err = c.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:                            aws.String(id),
		EnableNetworkAddressUsageMetrics: &types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err)

	attr, err := c.DescribeVpcAttribute(ctx, &ec2.DescribeVpcAttributeInput{
		VpcId: aws.String(id), Attribute: types.VpcAttributeNameEnableNetworkAddressUsageMetrics,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(attr.EnableNetworkAddressUsageMetrics.Value),
		"enable_network_address_usage_metrics must persist (was a silent no-op)")
}
