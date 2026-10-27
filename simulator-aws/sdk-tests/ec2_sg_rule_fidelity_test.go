package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_SecurityGroupRuleFidelity covers two reads terraform-provider-aws
// checks on every plan:
//   - an all-traffic rule (ip_protocol="-1") has no FromPort/ToPort, not 0,
//     or the provider sees "0 -> null".
//   - a rule referencing another group reports the bare sg- id, and its
//     ReferencedGroupInfo carries no UserId; the provider prefixes the id only
//     when UserId names another account.
func TestEC2_SecurityGroupRuleFidelity(t *testing.T) {
	t.Parallel()
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

	// Amazon EC2 creates a VPC security group with an allow-all egress rule; revoke
	// it so an identical rule can be authorized.
	_, err = c.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
		GroupId: aws.String(sgTasks),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("-1"),
			IpRanges:   []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)

	_, err = c.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: aws.String(sgTasks),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("-1"),
			IpRanges:   []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)

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

	assert.Nil(t, allTraffic.FromPort, "ip_protocol=-1 must omit FromPort")
	assert.Nil(t, allTraffic.ToPort, "ip_protocol=-1 must omit ToPort")
	assert.True(t, aws.ToBool(allTraffic.IsEgress))

	assert.Equal(t, int32(3000), aws.ToInt32(ref.FromPort))
	assert.Equal(t, int32(3000), aws.ToInt32(ref.ToPort))

	require.NotNil(t, ref.ReferencedGroupInfo)
	assert.Equal(t, sgAlb, aws.ToString(ref.ReferencedGroupInfo.GroupId))
	assert.Nil(t, ref.ReferencedGroupInfo.UserId, "same-account reference omits UserId so the provider keeps the bare id")
}
