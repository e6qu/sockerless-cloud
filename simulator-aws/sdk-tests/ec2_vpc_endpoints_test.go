package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_VpcEndpointsRoundTrip exercises the VPC-endpoint control plane:
// CreateVpcEndpoint (gateway + interface), DescribeVpcEndpoints (by id and by
// filter), and DeleteVpcEndpoints. It asserts the fields aws_vpc_endpoint reads
// back: type, service name, state, route/subnet/security-group sets, DNS
// entries, and the unsuccessful set on a bad delete.
func TestEC2_VpcEndpointsRoundTrip(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.90.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	rtID := aws.ToString(rt.RouteTable.RouteTableId)

	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: aws.String(vpcID), CidrBlock: aws.String("10.90.1.0/24"),
	})
	require.NoError(t, err)
	subID := aws.ToString(sub.Subnet.SubnetId)

	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("vpce-sg"), Description: aws.String("vpce sg"), VpcId: aws.String(vpcID),
	})
	require.NoError(t, err)
	sgID := aws.ToString(sg.GroupId)

	// Gateway endpoint (e.g. S3) — programmed into route tables, no ENIs.
	gw, err := c.CreateVpcEndpoint(ctx, &ec2.CreateVpcEndpointInput{
		VpcId:           aws.String(vpcID),
		ServiceName:     aws.String("com.amazonaws.us-east-1.s3"),
		VpcEndpointType: types.VpcEndpointTypeGateway,
		RouteTableIds:   []string{rtID},
	})
	require.NoError(t, err)
	require.NotNil(t, gw.VpcEndpoint)
	assert.NotEmpty(t, aws.ToString(gw.VpcEndpoint.VpcEndpointId))
	assert.Equal(t, types.VpcEndpointTypeGateway, gw.VpcEndpoint.VpcEndpointType)
	assert.Equal(t, "com.amazonaws.us-east-1.s3", aws.ToString(gw.VpcEndpoint.ServiceName))
	assert.Equal(t, vpcID, aws.ToString(gw.VpcEndpoint.VpcId))
	assert.Contains(t, gw.VpcEndpoint.RouteTableIds, rtID)
	gwID := aws.ToString(gw.VpcEndpoint.VpcEndpointId)

	// Interface endpoint — gets an ENI per subnet plus a DNS entry.
	iface, err := c.CreateVpcEndpoint(ctx, &ec2.CreateVpcEndpointInput{
		VpcId:             aws.String(vpcID),
		ServiceName:       aws.String("com.amazonaws.us-east-1.ecr.api"),
		VpcEndpointType:   types.VpcEndpointTypeInterface,
		SubnetIds:         []string{subID},
		SecurityGroupIds:  []string{sgID},
		PrivateDnsEnabled: aws.Bool(true),
	})
	require.NoError(t, err)
	ifaceEP := iface.VpcEndpoint
	require.NotNil(t, ifaceEP)
	assert.Equal(t, types.VpcEndpointTypeInterface, ifaceEP.VpcEndpointType)
	assert.True(t, aws.ToBool(ifaceEP.PrivateDnsEnabled))
	assert.Contains(t, ifaceEP.SubnetIds, subID)
	require.Len(t, ifaceEP.Groups, 1)
	assert.Equal(t, sgID, aws.ToString(ifaceEP.Groups[0].GroupId))
	require.NotEmpty(t, ifaceEP.NetworkInterfaceIds)
	require.NotEmpty(t, ifaceEP.DnsEntries)
	assert.NotEmpty(t, aws.ToString(ifaceEP.DnsEntries[0].DnsName))
	ifaceID := aws.ToString(ifaceEP.VpcEndpointId)

	// Describe by id.
	d, err := c.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{
		VpcEndpointIds: []string{gwID},
	})
	require.NoError(t, err)
	require.Len(t, d.VpcEndpoints, 1)
	assert.Equal(t, gwID, aws.ToString(d.VpcEndpoints[0].VpcEndpointId))
	assert.Equal(t, types.StateAvailable, d.VpcEndpoints[0].State)

	// Describe by vpc-id filter returns both.
	df, err := c.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{
		Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, e := range df.VpcEndpoints {
		ids[aws.ToString(e.VpcEndpointId)] = true
	}
	assert.True(t, ids[gwID])
	assert.True(t, ids[ifaceID])

	// Delete both — successful deletes leave an empty unsuccessful set.
	del, err := c.DeleteVpcEndpoints(ctx, &ec2.DeleteVpcEndpointsInput{
		VpcEndpointIds: []string{gwID, ifaceID},
	})
	require.NoError(t, err)
	assert.Empty(t, del.Unsuccessful)

	// They are gone.
	after, err := c.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{
		Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	require.NoError(t, err)
	assert.Empty(t, after.VpcEndpoints)

	// Deleting an unknown endpoint returns it in the unsuccessful set, not a
	// top-level error (matches real AWS).
	bad, err := c.DeleteVpcEndpoints(ctx, &ec2.DeleteVpcEndpointsInput{
		VpcEndpointIds: []string{"vpce-doesnotexist"},
	})
	require.NoError(t, err)
	require.Len(t, bad.Unsuccessful, 1)
	assert.Equal(t, "vpce-doesnotexist", aws.ToString(bad.Unsuccessful[0].ResourceId))
}
