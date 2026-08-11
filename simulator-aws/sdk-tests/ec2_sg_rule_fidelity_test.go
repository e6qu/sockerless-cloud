package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_SecurityGroupRuleFidelity covers two read-back fidelity gaps that
// drift terraform-provider-aws every idempotency plan:
//   - an all-traffic rule (ip_protocol="-1") must come back with no
//     FromPort/ToPort (not 0), or the provider sees "0 -> null".
//   - a rule referencing another SG must report the bare sg-id with no
//     account prefix; the sim is single-account, so ReferencedGroupInfo
//     omits UserId (the provider only prefixes when UserId != its account).
func TestEC2_SecurityGroupRuleFidelity(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.71.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	mkSG := func(name string) string {
		out, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
			GroupName: aws.String(name), Description: aws.String(name), VpcId: aws.String(vpcID),
		})
		require.NoError(t, err)
		return aws.ToString(out.GroupId)
	}
	sgAlb := mkSG("fidelity-alb")
	sgTasks := mkSG("fidelity-tasks")

	// Real AWS creates VPC security groups with a default ALLOW ALL egress rule.
	// Revoke it first so we can authorize an identical rule and assert its fidelity.
	_, err = c.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
		GroupId: aws.String(sgTasks),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("-1"),
			IpRanges:   []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)

	// All-traffic egress rule — no ports.
	_, err = c.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: aws.String(sgTasks),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("-1"),
			IpRanges:   []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)

	// Ingress rule referencing sgAlb.
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgTasks),
		IpPermissions: []types.IpPermission{{
			IpProtocol:       aws.String("tcp"),
			FromPort:         aws.Int32(3000),
			ToPort:           aws.Int32(3000),
			UserIdGroupPairs: []types.UserIdGroupPair{{GroupId: aws.String(sgAlb)}},
		}},
	})
	require.NoError(t, err)

	out, err := c.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []types.Filter{{Name: aws.String("group-id"), Values: []string{sgTasks}}},
	})
	require.NoError(t, err)
	require.Len(t, out.SecurityGroupRules, 2)

	var allTraffic, ref *types.SecurityGroupRule
	for i := range out.SecurityGroupRules {
		r := &out.SecurityGroupRules[i]
		if aws.ToString(r.IpProtocol) == "-1" {
			allTraffic = r
		} else {
			ref = r
		}
	}
	require.NotNil(t, allTraffic, "all-traffic rule present")
	require.NotNil(t, ref, "referencing rule present")

	// #457: all-traffic rule carries no ports.
	assert.Nil(t, allTraffic.FromPort, "ip_protocol=-1 must omit FromPort")
	assert.Nil(t, allTraffic.ToPort, "ip_protocol=-1 must omit ToPort")
	assert.True(t, aws.ToBool(allTraffic.IsEgress))

	// the tcp rule keeps its ports.
	assert.Equal(t, int32(3000), aws.ToInt32(ref.FromPort))
	assert.Equal(t, int32(3000), aws.ToInt32(ref.ToPort))

	// #458: bare referenced sg-id, no account prefix, no UserId.
	require.NotNil(t, ref.ReferencedGroupInfo)
	assert.Equal(t, sgAlb, aws.ToString(ref.ReferencedGroupInfo.GroupId))
	assert.Nil(t, ref.ReferencedGroupInfo.UserId, "same-account reference omits UserId so the provider keeps the bare id")
}
