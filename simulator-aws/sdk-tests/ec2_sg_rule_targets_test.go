package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sgRulesFor(t *testing.T, c *ec2.Client, groupID string) []types.SecurityGroupRule {
	t.Helper()
	out, err := c.DescribeSecurityGroupRules(ctx, &ec2.DescribeSecurityGroupRulesInput{
		Filters: []types.Filter{{Name: aws.String("group-id"), Values: []string{groupID}}},
	})
	require.NoError(t, err)
	return out.SecurityGroupRules
}

func ingressRules(rules []types.SecurityGroupRule) []types.SecurityGroupRule {
	var out []types.SecurityGroupRule
	for _, r := range rules {
		if !aws.ToBool(r.IsEgress) {
			out = append(out, r)
		}
	}
	return out
}

// TestEC2_SecurityGroupRuleTargetsSDK covers the P1 gap where Authorize* with
// IPv6 ranges or prefix lists produced NO SecurityGroupRule row (so a standalone
// aws_vpc_security_group_*_rule with cidr_ipv6/prefix_list_id drifted/recreated
// every plan), plus the securityGroupRuleArn field.
func TestEC2_SecurityGroupRuleTargetsSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.120.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("sgr-targets"), Description: aws.String("t"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	gid := aws.ToString(sg.GroupId)

	// IPv6 egress rule — must produce a rule row carrying CidrIpv6.
	_, err = c.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: aws.String(gid),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("-1"),
			Ipv6Ranges: []types.Ipv6Range{{CidrIpv6: aws.String("::/0")}},
		}},
	})
	require.NoError(t, err)

	// Prefix-list ingress rule — must produce a rule row carrying PrefixListId.
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(gid),
		IpPermissions: []types.IpPermission{{
			IpProtocol:    aws.String("tcp"),
			FromPort:      aws.Int32(443),
			ToPort:        aws.Int32(443),
			PrefixListIds: []types.PrefixListId{{PrefixListId: aws.String("pl-0123456789abcdef0")}},
		}},
	})
	require.NoError(t, err)

	rules := sgRulesFor(t, c, gid)
	var v6, pl *types.SecurityGroupRule
	for i := range rules {
		switch {
		case aws.ToString(rules[i].CidrIpv6) == "::/0":
			v6 = &rules[i]
		case aws.ToString(rules[i].PrefixListId) == "pl-0123456789abcdef0":
			pl = &rules[i]
		}
	}
	require.NotNil(t, v6, "IPv6 egress rule must have a SecurityGroupRule row")
	assert.True(t, aws.ToBool(v6.IsEgress))
	assert.NotEmpty(t, aws.ToString(v6.SecurityGroupRuleArn), "rule must carry securityGroupRuleArn")
	require.NotNil(t, pl, "prefix-list ingress rule must have a SecurityGroupRule row")
	assert.NotEmpty(t, aws.ToString(pl.SecurityGroupRuleId))
}

// TestEC2_RevokeRemovesOnlyMatchingRuleSDK covers the revoke gaps: revoking one
// of several rules sharing a port range must remove only the matching rule (not
// all of them) and must delete its SecurityGroupRule row (no orphan).
func TestEC2_RevokeRemovesOnlyMatchingRuleSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.121.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("sgr-revoke"), Description: aws.String("t"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	gid := aws.ToString(sg.GroupId)

	perm := func(cidr string) types.IpPermission {
		return types.IpPermission{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443),
			IpRanges: []types.IpRange{{CidrIp: aws.String(cidr)}},
		}
	}
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(gid), IpPermissions: []types.IpPermission{perm("10.121.1.0/24")},
	})
	require.NoError(t, err)
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(gid), IpPermissions: []types.IpPermission{perm("10.121.2.0/24")},
	})
	require.NoError(t, err)
	require.Len(t, ingressRules(sgRulesFor(t, c, gid)), 2, "two ingress rules expected before revoke")

	// Revoke only the first CIDR — the second must survive (was: both removed).
	_, err = c.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
		GroupId: aws.String(gid), IpPermissions: []types.IpPermission{perm("10.121.1.0/24")},
	})
	require.NoError(t, err)

	rules := ingressRules(sgRulesFor(t, c, gid))
	require.Len(t, rules, 1, "revoking one rule must leave exactly the other (no orphan, no over-delete)")
	assert.Equal(t, "10.121.2.0/24", aws.ToString(rules[0].CidrIpv4))
}

// TestEC2_UpdateSecurityGroupRuleDescriptionsSDK covers the
// UpdateSecurityGroupRuleDescriptions{Ingress,Egress} operations (legacy
// aws_security_group inline-block description path).
func TestEC2_UpdateSecurityGroupRuleDescriptionsSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.124.0.0/16")})
	require.NoError(t, err)
	sg, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("sgr-desc"), Description: aws.String("t"), VpcId: vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	gid := aws.ToString(sg.GroupId)

	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(gid),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
			IpRanges: []types.IpRange{{CidrIp: aws.String("10.124.5.0/24")}},
		}},
	})
	require.NoError(t, err)

	_, err = c.UpdateSecurityGroupRuleDescriptionsIngress(ctx, &ec2.UpdateSecurityGroupRuleDescriptionsIngressInput{
		GroupId: aws.String(gid),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
			IpRanges: []types.IpRange{{CidrIp: aws.String("10.124.5.0/24"), Description: aws.String("ssh from office")}},
		}},
	})
	require.NoError(t, err)

	rules := ingressRules(sgRulesFor(t, c, gid))
	require.Len(t, rules, 1)
	assert.Equal(t, "ssh from office", aws.ToString(rules[0].Description), "rule description must update in place")

	// Egress variant.
	_, err = c.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: aws.String(gid),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(80), ToPort: aws.Int32(80),
			IpRanges: []types.IpRange{{CidrIp: aws.String("10.124.6.0/24")}},
		}},
	})
	require.NoError(t, err)
	_, err = c.UpdateSecurityGroupRuleDescriptionsEgress(ctx, &ec2.UpdateSecurityGroupRuleDescriptionsEgressInput{
		GroupId: aws.String(gid),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(80), ToPort: aws.Int32(80),
			IpRanges: []types.IpRange{{CidrIp: aws.String("10.124.6.0/24"), Description: aws.String("http out")}},
		}},
	})
	require.NoError(t, err)
	var egressDesc string
	for _, rl := range sgRulesFor(t, c, gid) {
		if aws.ToString(rl.CidrIpv4) == "10.124.6.0/24" {
			egressDesc = aws.ToString(rl.Description)
		}
	}
	assert.Equal(t, "http out", egressDesc, "egress rule description must update in place")
}

// TestEC2_NetworkInterfaceTypeSDK covers the omitted interface_type field.
func TestEC2_NetworkInterfaceTypeSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.122.0.0/16")})
	require.NoError(t, err)
	sn, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.122.1.0/24")})
	require.NoError(t, err)
	eni, err := c.CreateNetworkInterface(ctx, &ec2.CreateNetworkInterfaceInput{SubnetId: sn.Subnet.SubnetId})
	require.NoError(t, err)
	out, err := c.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{aws.ToString(eni.NetworkInterface.NetworkInterfaceId)},
	})
	require.NoError(t, err)
	require.Len(t, out.NetworkInterfaces, 1)
	assert.Equal(t, types.NetworkInterfaceTypeInterface, out.NetworkInterfaces[0].InterfaceType)
}
