package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_NatGatewayConnectivityType verifies ConnectivityType round-trips
// through DescribeNatGateways. connectivity_type is ForceNew in the provider;
// an absent value vs the configured "public" forces destroy+create every plan.
// An omitted connectivity type defaults to "public" (matching real AWS).
func TestEC2_NatGatewayConnectivityType(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.72.0.0/16")})
	require.NoError(t, err)
	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.72.1.0/24"),
	})
	require.NoError(t, err)
	subID := aws.ToString(sub.Subnet.SubnetId)

	mkNat := func(connType types.ConnectivityType) *types.NatGateway {
		eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
		require.NoError(t, err)
		in := &ec2.CreateNatGatewayInput{SubnetId: aws.String(subID), AllocationId: eip.AllocationId}
		if connType != "" {
			in.ConnectivityType = connType
		}
		out, err := c.CreateNatGateway(ctx, in)
		require.NoError(t, err)
		return out.NatGateway
	}

	pub := mkNat(types.ConnectivityTypePublic)
	assert.Equal(t, types.ConnectivityTypePublic, pub.ConnectivityType, "Create echoes connectivity type")

	desc, err := c.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{
		NatGatewayIds: []string{aws.ToString(pub.NatGatewayId)},
	})
	require.NoError(t, err)
	require.Len(t, desc.NatGateways, 1)
	assert.Equal(t, types.ConnectivityTypePublic, desc.NatGateways[0].ConnectivityType)

	// Omitted connectivity type defaults to public.
	def := mkNat("")
	descDef, err := c.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{
		NatGatewayIds: []string{aws.ToString(def.NatGatewayId)},
	})
	require.NoError(t, err)
	require.Len(t, descDef.NatGateways, 1)
	assert.Equal(t, types.ConnectivityTypePublic, descDef.NatGateways[0].ConnectivityType)
}
