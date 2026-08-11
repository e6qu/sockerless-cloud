package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_ModifySecurityGroupRules verifies an in-place update of a security
// group rule's attributes (the path the AWS provider uses to change an existing
// rule rather than revoke + re-authorize).
func TestEC2_ModifySecurityGroupRules(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.71.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("modify-sgr"), Description: aws.String("modify"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sg.GroupId,
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(80), ToPort: aws.Int32(80),
			IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("orig")}},
		}},
	})
	require.NoError(t, err)

	rules, err := c.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []ec2types.Filter{{Name: aws.String("group-id"), Values: []string{aws.ToString(sg.GroupId)}}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, rules.SecurityGroupRules)
	ruleID := aws.ToString(rules.SecurityGroupRules[0].SecurityGroupRuleId)
	require.NotEmpty(t, ruleID)

	_, err = c.ModifySecurityGroupRules(ctx, &ec2.ModifySecurityGroupRulesInput{
		GroupId: sg.GroupId,
		SecurityGroupRules: []ec2types.SecurityGroupRuleUpdate{{
			SecurityGroupRuleId: aws.String(ruleID),
			SecurityGroupRule: &ec2types.SecurityGroupRuleRequest{
				IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443),
				CidrIpv4: aws.String("10.0.0.0/8"), Description: aws.String("updated"),
			},
		}},
	})
	require.NoError(t, err)

	after, err := c.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		SecurityGroupRuleIds: []string{ruleID},
	})
	require.NoError(t, err)
	require.Len(t, after.SecurityGroupRules, 1)
	got := after.SecurityGroupRules[0]
	assert.Equal(t, "updated", aws.ToString(got.Description))
	assert.Equal(t, int32(443), aws.ToInt32(got.FromPort))
	assert.Equal(t, "10.0.0.0/8", aws.ToString(got.CidrIpv4))

	// Unknown rule id is rejected.
	_, err = c.ModifySecurityGroupRules(ctx, &ec2.ModifySecurityGroupRulesInput{
		GroupId: sg.GroupId,
		SecurityGroupRules: []ec2types.SecurityGroupRuleUpdate{{
			SecurityGroupRuleId: aws.String("sgr-doesnotexist"),
			SecurityGroupRule:   &ec2types.SecurityGroupRuleRequest{Description: aws.String("x")},
		}},
	})
	require.Error(t, err)
}
