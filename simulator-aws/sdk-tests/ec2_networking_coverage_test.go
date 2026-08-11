package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests backfill coverage for EC2 networking operations that had no SDK
// or CLI test — the same untested-surface class that hid CloudWatch's broken
// protocol. Each asserts a real round-trip (the mutation is reflected on the
// matching Describe), so future drift fails loudly.

func TestEC2_RouteTableAssociationLifecycle(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.110.0.0/16")})
	require.NoError(t, err)
	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.110.1.0/24")})
	require.NoError(t, err)
	rt, err := c.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err)
	rtID := aws.ToString(rt.RouteTable.RouteTableId)

	assoc, err := c.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
		RouteTableId: aws.String(rtID), SubnetId: sub.Subnet.SubnetId,
	})
	require.NoError(t, err)
	assocID := aws.ToString(assoc.AssociationId)
	require.NotEmpty(t, assocID)

	desc, err := c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{rtID}})
	require.NoError(t, err)
	var found bool
	for _, a := range desc.RouteTables[0].Associations {
		if aws.ToString(a.SubnetId) == aws.ToString(sub.Subnet.SubnetId) {
			found = true
		}
	}
	assert.True(t, found, "association is reflected in DescribeRouteTables")

	_, err = c.DisassociateRouteTable(ctx, &ec2.DisassociateRouteTableInput{AssociationId: aws.String(assocID)})
	require.NoError(t, err)
	desc, err = c.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{rtID}})
	require.NoError(t, err)
	for _, a := range desc.RouteTables[0].Associations {
		assert.NotEqual(t, aws.ToString(sub.Subnet.SubnetId), aws.ToString(a.SubnetId), "association removed after disassociate")
	}

	_, err = c.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{RouteTableId: aws.String(rtID)})
	require.NoError(t, err)
}

func TestEC2_InternetGatewayAttachDetachDelete(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.111.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	igw, err := c.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	require.NoError(t, err)
	igwID := aws.ToString(igw.InternetGateway.InternetGatewayId)

	_, err = c.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{InternetGatewayId: aws.String(igwID), VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	desc, err := c.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{InternetGatewayIds: []string{igwID}})
	require.NoError(t, err)
	require.NotEmpty(t, desc.InternetGateways[0].Attachments)
	assert.Equal(t, vpcID, aws.ToString(desc.InternetGateways[0].Attachments[0].VpcId))

	_, err = c.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{InternetGatewayId: aws.String(igwID), VpcId: aws.String(vpcID)})
	require.NoError(t, err)
	desc, err = c.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{InternetGatewayIds: []string{igwID}})
	require.NoError(t, err)
	assert.Empty(t, desc.InternetGateways[0].Attachments, "attachment removed after detach")

	_, err = c.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{InternetGatewayId: aws.String(igwID)})
	require.NoError(t, err)
}

func TestEC2_VpcAndSubnetAttributes(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.112.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	sub, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: aws.String(vpcID), CidrBlock: aws.String("10.112.1.0/24")})
	require.NoError(t, err)
	subID := aws.ToString(sub.Subnet.SubnetId)

	_, err = c.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId: aws.String(vpcID), EnableDnsHostnames: &ec2types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err)
	attr, err := c.DescribeVpcAttribute(ctx, &ec2.DescribeVpcAttributeInput{VpcId: aws.String(vpcID), Attribute: ec2types.VpcAttributeNameEnableDnsHostnames})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(attr.EnableDnsHostnames.Value), "enableDnsHostnames round-trips")

	_, err = c.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
		SubnetId: aws.String(subID), MapPublicIpOnLaunch: &ec2types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	require.NoError(t, err)
	ds, err := c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{subID}})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(ds.Subnets[0].MapPublicIpOnLaunch), "mapPublicIpOnLaunch round-trips")
}

func TestEC2_RevokeSecurityGroupRules(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.113.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("revoke-cov"), Description: aws.String("r"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	sgID := aws.ToString(sg.GroupId)

	ingress := []ec2types.IpPermission{{
		IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
		IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
	}}
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(sgID), IpPermissions: ingress})
	require.NoError(t, err)
	_, err = c.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: aws.String(sgID), IpPermissions: ingress})
	require.NoError(t, err)

	// Revoking the same rule a second time must fail with InvalidPermission.NotFound.
	_, err = c.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: aws.String(sgID), IpPermissions: ingress})
	require.Error(t, err)
	requireSGErrorCode(t, err, "InvalidPermission.NotFound")

	// VPC security groups are created with a default ALLOW ALL egress rule.
	// Revoke it and confirm it's gone.
	egress := []ec2types.IpPermission{{
		IpProtocol: aws.String("-1"), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
	}}
	_, err = c.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: aws.String(sgID), IpPermissions: egress})
	require.NoError(t, err)

	// Egress re-revoke must also fail with InvalidPermission.NotFound.
	_, err = c.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: aws.String(sgID), IpPermissions: egress})
	require.Error(t, err)
	requireSGErrorCode(t, err, "InvalidPermission.NotFound")

	desc, err := c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{sgID}})
	require.NoError(t, err)
	assert.Empty(t, desc.SecurityGroups[0].IpPermissions, "ingress rule revoked")
}

func TestEC2_RevokeSecurityGroupRulesByID(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.114.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("revoke-by-id"), Description: aws.String("r"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	sgID := aws.ToString(sg.GroupId)

	// Authorize an ingress rule and capture its generated rule ID.
	auth, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443),
			IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)
	require.Len(t, auth.SecurityGroupRules, 1)
	ingressRuleID := aws.ToString(auth.SecurityGroupRules[0].SecurityGroupRuleId)
	require.NotEmpty(t, ingressRuleID)

	// Revoke by rule ID must succeed.
	_, err = c.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
		GroupId: aws.String(sgID), SecurityGroupRuleIds: []string{ingressRuleID},
	})
	require.NoError(t, err)

	// The rule row and legacy permission are gone.
	desc, err := c.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		SecurityGroupRuleIds: []string{ingressRuleID},
	})
	require.NoError(t, err)
	assert.Empty(t, desc.SecurityGroupRules, "rule row deleted after revoke-by-id")

	sgDesc, err := c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{sgID}})
	require.NoError(t, err)
	assert.Empty(t, sgDesc.SecurityGroups[0].IpPermissions, "ingress permission removed after revoke-by-id")

	// Revoking the same rule ID again is idempotent (success).
	_, err = c.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
		GroupId: aws.String(sgID), SecurityGroupRuleIds: []string{ingressRuleID},
	})
	require.NoError(t, err)

	// The default egress rule is visible by ID and can be revoked by ID.
	egressRules, err := c.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-id"), Values: []string{sgID}},
			{Name: aws.String("egress"), Values: []string{"true"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, egressRules.SecurityGroupRules, 1, "default egress rule has a rule row")
	egressRuleID := aws.ToString(egressRules.SecurityGroupRules[0].SecurityGroupRuleId)

	_, err = c.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
		GroupId: aws.String(sgID), SecurityGroupRuleIds: []string{egressRuleID},
	})
	require.NoError(t, err)

	sgDesc, err = c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{sgID}})
	require.NoError(t, err)
	assert.Empty(t, sgDesc.SecurityGroups[0].IpPermissionsEgress, "default egress rule revoked by id")
}
