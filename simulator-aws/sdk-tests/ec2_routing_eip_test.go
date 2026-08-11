package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_ReplaceRouteAndTargetsSDK covers the missing ReplaceRoute operation
// and the full CreateRoute target set (previously only gateway/nat/eni were
// parsed; IPv6, prefix-list, peering, egress-only targets were dropped).
func TestEC2_ReplaceRouteAndTargetsSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.110.0.0/16")})
	require.NoError(t, err)
	rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)
	rtID := aws.ToString(rt.RouteTable.RouteTableId)
	igw, err := c.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	require.NoError(t, err)
	igwID := aws.ToString(igw.InternetGateway.InternetGatewayId)
	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{
		SubnetId: createSubnetFor(t, c, vpc.Vpc.VpcId, "10.110.1.0/24"),
	})
	require.NoError(t, err)
	natID := aws.ToString(nat.NatGateway.NatGatewayId)

	// Create a default route targeting the IGW, then ReplaceRoute → NAT gateway.
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId: aws.String(rtID), DestinationCidrBlock: aws.String("0.0.0.0/0"), GatewayId: aws.String(igwID),
	})
	require.NoError(t, err)
	_, err = c.ReplaceRoute(ctx, &ec2.ReplaceRouteInput{
		RouteTableId: aws.String(rtID), DestinationCidrBlock: aws.String("0.0.0.0/0"), NatGatewayId: aws.String(natID),
	})
	require.NoError(t, err)

	rtOut, err := c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{rtID}})
	require.NoError(t, err)
	require.Len(t, rtOut.RouteTables, 1)
	var defRoute *types.Route
	for i := range rtOut.RouteTables[0].Routes {
		if aws.ToString(rtOut.RouteTables[0].Routes[i].DestinationCidrBlock) == "0.0.0.0/0" {
			defRoute = &rtOut.RouteTables[0].Routes[i]
		}
	}
	require.NotNil(t, defRoute, "default route must exist after replace")
	assert.Equal(t, natID, aws.ToString(defRoute.NatGatewayId), "ReplaceRoute must swap the target to the NAT gateway")
	assert.Empty(t, aws.ToString(defRoute.GatewayId), "old IGW target must be cleared by ReplaceRoute")

	// IPv6 destination + egress-only IGW target round-trip (previously dropped).
	_, err = c.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId:                aws.String(rtID),
		DestinationIpv6CidrBlock:    aws.String("::/0"),
		EgressOnlyInternetGatewayId: aws.String("eigw-0123456789abcdef0"),
	})
	require.NoError(t, err)
	rtOut, err = c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{rtID}})
	require.NoError(t, err)
	var v6 *types.Route
	for i := range rtOut.RouteTables[0].Routes {
		if aws.ToString(rtOut.RouteTables[0].Routes[i].DestinationIpv6CidrBlock) == "::/0" {
			v6 = &rtOut.RouteTables[0].Routes[i]
		}
	}
	require.NotNil(t, v6, "IPv6 route must round-trip")
	assert.Equal(t, "eigw-0123456789abcdef0", aws.ToString(v6.EgressOnlyInternetGatewayId))
}

// TestEC2_ElasticIPAssociationSDK covers the missing AssociateAddress /
// DisassociateAddress operations and the Address response fields (association_id,
// instance, private_ip, public_ipv4_pool) that aws_eip reads back.
func TestEC2_ElasticIPAssociationSDK(t *testing.T) {
	c := ec2Client()
	alloc, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	allocID := aws.ToString(alloc.AllocationId)
	t.Cleanup(func() { _, _ = c.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: aws.String(allocID)}) })
	assert.Equal(t, "amazon", aws.ToString(alloc.PublicIpv4Pool))

	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
	})
	require.NoError(t, err)
	instID := aws.ToString(run.Instances[0].InstanceId)

	assoc, err := c.AssociateAddress(ctx, &ec2.AssociateAddressInput{
		AllocationId: aws.String(allocID), InstanceId: aws.String(instID),
	})
	require.NoError(t, err)
	assocID := aws.ToString(assoc.AssociationId)
	require.NotEmpty(t, assocID)

	desc, err := c.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{AllocationIds: []string{allocID}})
	require.NoError(t, err)
	require.Len(t, desc.Addresses, 1)
	got := desc.Addresses[0]
	assert.Equal(t, assocID, aws.ToString(got.AssociationId), "association_id must round-trip")
	assert.Equal(t, instID, aws.ToString(got.InstanceId), "instance must round-trip")
	assert.NotEmpty(t, aws.ToString(got.PrivateIpAddress), "private_ip must be populated from the instance")

	_, err = c.DisassociateAddress(ctx, &ec2.DisassociateAddressInput{AssociationId: aws.String(assocID)})
	require.NoError(t, err)
	desc, err = c.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{AllocationIds: []string{allocID}})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(desc.Addresses[0].AssociationId), "association must clear on disassociate")
	assert.Empty(t, aws.ToString(desc.Addresses[0].InstanceId))
}

// TestEC2_NatGatewayAddressFieldsSDK covers the NatGatewayAddress fields
// (association_id, network_interface_id) aws_nat_gateway reads back.
func TestEC2_NatGatewayAddressFieldsSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.111.0.0/16")})
	require.NoError(t, err)
	nat, err := c.CreateNatGateway(ctx, &ec2.CreateNatGatewayInput{
		SubnetId: createSubnetFor(t, c, vpc.Vpc.VpcId, "10.111.1.0/24"),
	})
	require.NoError(t, err)
	natID := aws.ToString(nat.NatGateway.NatGatewayId)

	out, err := c.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{natID}})
	require.NoError(t, err)
	require.Len(t, out.NatGateways, 1)
	require.NotEmpty(t, out.NatGateways[0].NatGatewayAddresses)
	addr := out.NatGateways[0].NatGatewayAddresses[0]
	assert.NotEmpty(t, aws.ToString(addr.NetworkInterfaceId), "nat gateway network_interface_id must be reported")
	assert.NotEmpty(t, aws.ToString(addr.AssociationId), "nat gateway address association_id must be reported")
}

// TestEC2_MainRouteTableSDK covers the main route table AWS auto-creates per VPC
// (read by aws_vpc.main_route_table_id / aws_default_route_table via the
// association.main filter).
func TestEC2_MainRouteTableSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.112.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	out, err := c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("association.main"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, out.RouteTables, 1, "exactly one main route table must exist for the VPC")
	require.NotEmpty(t, out.RouteTables[0].Associations)
	assert.True(t, aws.ToBool(out.RouteTables[0].Associations[0].Main), "the association must be flagged main")
	require.NotNil(t, out.RouteTables[0].Associations[0].AssociationState)
}

func createSubnetFor(t *testing.T, c *ec2.Client, vpcID *string, cidr string) *string {
	t.Helper()
	sn, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpcID, CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	return sn.Subnet.SubnetId
}
