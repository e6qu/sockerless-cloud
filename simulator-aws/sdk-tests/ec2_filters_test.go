package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_DescribeFiltersPositionIndependent covers a vpc-id filter
// must be honored regardless of its position in the Filter.N sequence.
// Previously these handlers only inspected Filter.1, so a vpc-id filter sent
// after another filter was ignored and ALL resources were returned.
func TestEC2_DescribeFiltersPositionIndependent(t *testing.T) {
	client := ec2Client()

	vpc1, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.91.0.0/16")})
	require.NoError(t, err)
	vpc2, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.92.0.0/16")})
	require.NoError(t, err)

	sn1, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc1.Vpc.VpcId, CidrBlock: aws.String("10.91.1.0/24")})
	require.NoError(t, err)
	_, err = client.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc2.Vpc.VpcId, CidrBlock: aws.String("10.92.1.0/24")})
	require.NoError(t, err)

	// vpc-id is the SECOND filter (after an unsupported one) — must still scope.
	subOut, err := client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("vpc-id"), Values: []string{*vpc1.Vpc.VpcId}},
		},
	})
	require.NoError(t, err)
	require.Len(t, subOut.Subnets, 1, "vpc-id filter must apply regardless of position")
	assert.Equal(t, *sn1.Subnet.SubnetId, *subOut.Subnets[0].SubnetId)

	// Same for DescribeNatGateways (a NAT in each VPC).
	mkNat := func(subnetID *string) string {
		eip, err := client.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
		require.NoError(t, err)
		nat, err := client.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{AllocationId: eip.AllocationId, SubnetId: subnetID})
		require.NoError(t, err)
		return *nat.NatGateway.NatGatewayId
	}
	nat1 := mkNat(sn1.Subnet.SubnetId)
	sn2b, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc2.Vpc.VpcId, CidrBlock: aws.String("10.92.2.0/24")})
	require.NoError(t, err)
	_ = mkNat(sn2b.Subnet.SubnetId)

	natOut, err := client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{
		Filter: []types.Filter{
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("vpc-id"), Values: []string{*vpc1.Vpc.VpcId}},
		},
	})
	require.NoError(t, err)
	require.Len(t, natOut.NatGateways, 1, "NAT vpc-id filter must apply regardless of position")
	assert.Equal(t, nat1, *natOut.NatGateways[0].NatGatewayId)
}
