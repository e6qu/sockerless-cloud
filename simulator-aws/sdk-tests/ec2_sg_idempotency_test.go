package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_SecurityGroupDuplicateNameRejected mirrors the ECS backend's
// idempotent network-create path (backends/ecs/network_cloud.go): the
// FIRST CreateSecurityGroup succeeds; a SECOND create with the same
// name in the same VPC must fail with InvalidGroup.Duplicate so the
// backend takes its reuse-by-name branch. Real AWS rejects same-name
// groups within one VPC; different VPCs may reuse a name.
func TestEC2_SecurityGroupDuplicateNameRejected(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.71.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	const name = "sockerless-net-dup"
	_, err = c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(name),
		Description: aws.String("first"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)

	// Same name + same VPC → InvalidGroup.Duplicate.
	_, err = c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(name),
		Description: aws.String("second"),
		VpcId:       aws.String(vpcID),
	})
	require.Error(t, err, "duplicate group name in the same VPC must be rejected")
	assert.Contains(t, err.Error(), "InvalidGroup.Duplicate")

	// Same name in a DIFFERENT VPC is allowed.
	vpc2, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.72.0.0/16")})
	require.NoError(t, err)
	_, err = c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(name),
		Description: aws.String("other-vpc"),
		VpcId:       vpc2.Vpc.VpcId,
	})
	require.NoError(t, err, "same name in a different VPC is allowed")
}

// TestEC2_AuthorizeIngressDuplicateRejected mirrors the backend's
// self-referencing ingress rule: re-authorizing an identical rule must
// fail with InvalidPermission.Duplicate (the backend swallows exactly
// that code), and DescribeSecurityGroups must NOT accumulate duplicate
// permissions.
func TestEC2_AuthorizeIngressDuplicateRejected(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.73.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("sockerless-selfref"),
		Description: aws.String("self-ref"),
		VpcId:       vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	sgID := aws.ToString(sg.GroupId)

	selfRef := []types.IpPermission{{
		IpProtocol:       aws.String("-1"),
		UserIdGroupPairs: []types.UserIdGroupPair{{GroupId: aws.String(sgID)}},
	}}

	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId:       aws.String(sgID),
		IpPermissions: selfRef,
	})
	require.NoError(t, err)

	// Re-authorize the identical rule → InvalidPermission.Duplicate.
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId:       aws.String(sgID),
		IpPermissions: selfRef,
	})
	require.Error(t, err, "duplicate ingress rule must be rejected")
	assert.Contains(t, err.Error(), "InvalidPermission.Duplicate")

	// The duplicate attempt must not have appended a second permission.
	desc, err := c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{sgID}})
	require.NoError(t, err)
	require.Len(t, desc.SecurityGroups, 1)
	ingress := desc.SecurityGroups[0].IpPermissions
	count := 0
	for _, p := range ingress {
		if aws.ToString(p.IpProtocol) == "-1" {
			for _, pair := range p.UserIdGroupPairs {
				if aws.ToString(pair.GroupId) == sgID {
					count++
				}
			}
		}
	}
	assert.Equal(t, 1, count, "self-referencing rule must appear exactly once, got %d", count)
}
