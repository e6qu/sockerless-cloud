package aws_sdk_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_SecurityGroupEnforce_RejectsMalformedCIDR covers Tier 1 validation:
// AuthorizeSecurityGroupIngress must reject a syntactically invalid IPv4 CIDR
// with InvalidPermission.Malformed, matching real AWS' Authorize boundary.
func TestEC2_SecurityGroupEnforce_RejectsMalformedCIDR(t *testing.T) {
	c := ec2Client()
	vpc := mustCreateVpc(t, c, "10.150.0.0/16")
	sg := mustCreateSG(t, c, "enforce-malformed-cidr", vpc)

	_, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sg.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(80), ToPort: aws.Int32(80),
			IpRanges: []types.IpRange{{CidrIp: aws.String("not-a-cidr")}},
		}},
	})
	require.Error(t, err)
	requireSGErrorCode(t, err, "InvalidPermission.Malformed")
}

// TestEC2_SecurityGroupEnforce_RejectsMalformedIPv6CIDR verifies IPv6 CIDR
// validation.
func TestEC2_SecurityGroupEnforce_RejectsMalformedIPv6CIDR(t *testing.T) {
	c := ec2Client()
	vpc := mustCreateVpc(t, c, "10.151.0.0/16")
	sg := mustCreateSG(t, c, "enforce-malformed-v6", vpc)

	_, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sg.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443),
			Ipv6Ranges: []types.Ipv6Range{{CidrIpv6: aws.String("zzz::/64")}},
		}},
	})
	require.Error(t, err)
	requireSGErrorCode(t, err, "InvalidPermission.Malformed")
}

// TestEC2_SecurityGroupEnforce_RejectsUnknownSGRef covers Tier 1: a UserIdGroupPair
// referencing a non-existent SG must come back as InvalidGroup.NotFound.
func TestEC2_SecurityGroupEnforce_RejectsUnknownSGRef(t *testing.T) {
	c := ec2Client()
	vpc := mustCreateVpc(t, c, "10.152.0.0/16")
	sg := mustCreateSG(t, c, "enforce-unknown-ref", vpc)

	_, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sg.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(80), ToPort: aws.Int32(80),
			UserIdGroupPairs: []types.UserIdGroupPair{{GroupId: aws.String("sg-doesnotexist0001")}},
		}},
	})
	require.Error(t, err)
	requireSGErrorCode(t, err, "InvalidGroup.NotFound")
}

// TestEC2_SecurityGroupEnforce_RejectsBadProtocol verifies unsupported protocol
// names are rejected with InvalidPermission.Malformed.
func TestEC2_SecurityGroupEnforce_RejectsBadProtocol(t *testing.T) {
	c := ec2Client()
	vpc := mustCreateVpc(t, c, "10.153.0.0/16")
	sg := mustCreateSG(t, c, "enforce-bad-proto", vpc)

	_, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sg.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("sctp"),
		}},
	})
	require.Error(t, err)
	requireSGErrorCode(t, err, "InvalidPermission.Malformed")
}

// TestEC2_SecurityGroupEnforce_RejectsInvertedPortRange verifies FromPort>ToPort
// for TCP is rejected.
func TestEC2_SecurityGroupEnforce_RejectsInvertedPortRange(t *testing.T) {
	c := ec2Client()
	vpc := mustCreateVpc(t, c, "10.154.0.0/16")
	sg := mustCreateSG(t, c, "enforce-inverted-ports", vpc)

	_, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sg.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(9000), ToPort: aws.Int32(80),
		}},
	})
	require.Error(t, err)
	requireSGErrorCode(t, err, "InvalidPortRange.Malformed")
}

// TestEC2_SecurityGroupEnforce_RejectsUnknownGroup verifies the Authorize
// boundary rejects an Authorize against a non-existent group itself.
func TestEC2_SecurityGroupEnforce_RejectsUnknownGroup(t *testing.T) {
	c := ec2Client()

	_, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String("sg-missing99999"),
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(80), ToPort: aws.Int32(80),
		}},
	})
	require.Error(t, err)
	requireSGErrorCode(t, err, "InvalidGroup.NotFound")
}

// TestEC2_SecurityGroupEnforce_StoresAndQueries covers Tier 2 non-Linux: even
// without real-exec capabilities, SG rules are stored, queryable via the API,
// and reference-resolvable. This runs on every platform.
func TestEC2_SecurityGroupEnforce_StoresAndQueries(t *testing.T) {
	c := ec2Client()
	vpc := mustCreateVpc(t, c, "10.155.0.0/16")
	webSG := mustCreateSG(t, c, "enforce-web", vpc)
	dbSG := mustCreateSG(t, c, "enforce-db", vpc)

	// Web SG opens TCP 80 from anywhere.
	_, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: webSG.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(80), ToPort: aws.Int32(80),
			IpRanges: []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)

	// DB SG opens 5432 to webSG (referenced).
	_, err = c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: dbSG.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(5432), ToPort: aws.Int32(5432),
			UserIdGroupPairs: []types.UserIdGroupPair{{GroupId: webSG.GroupId}},
		}},
	})
	require.NoError(t, err)

	// Restrictive egress: only TCP 443 to 0.0.0.0/0.
	_, err = c.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
		GroupId: dbSG.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443),
			IpRanges: []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	})
	require.NoError(t, err)

	// Both ingress + the egress rule are queryable by group id.
	rules := sgRulesFor(t, c, aws.ToString(dbSG.GroupId))
	var sawIngressRef, sawEgressHTTPS bool
	for i := range rules {
		r := &rules[i]
		if r.ReferencedGroupInfo != nil && aws.ToString(r.ReferencedGroupInfo.GroupId) == aws.ToString(webSG.GroupId) {
			sawIngressRef = true
		}
		if aws.ToBool(r.IsEgress) && aws.ToInt32(r.FromPort) == 443 && aws.ToInt32(r.ToPort) == 443 {
			sawEgressHTTPS = true
		}
	}
	assert.True(t, sawIngressRef, "ingress SG reference rule must be queryable")
	assert.True(t, sawEgressHTTPS, "restrictive egress rule must be queryable")

	// And via DescribeSecurityGroups the IpPermissions carry the SG reference.
	desc, err := c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{aws.ToString(dbSG.GroupId)}})
	require.NoError(t, err)
	require.NotEmpty(t, desc.SecurityGroups)
	var foundRef bool
	for _, p := range desc.SecurityGroups[0].IpPermissions {
		for _, gp := range p.UserIdGroupPairs {
			if aws.ToString(gp.GroupId) == aws.ToString(webSG.GroupId) {
				foundRef = true
			}
		}
	}
	assert.True(t, foundRef, "DescribeSecurityGroups must surface the SG reference")

}

// TestEC2_SecurityGroupEnforce_IPv6IngressStored verifies IPv6 ingress rules
// survive the validation boundary and round-trip through Describe.
func TestEC2_SecurityGroupEnforce_IPv6IngressStored(t *testing.T) {
	c := ec2Client()
	vpc := mustCreateVpc(t, c, "10.156.0.0/16")
	sg := mustCreateSG(t, c, "enforce-v6", vpc)

	_, err := c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sg.GroupId,
		IpPermissions: []types.IpPermission{{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
			Ipv6Ranges: []types.Ipv6Range{{CidrIpv6: aws.String("2001:db8::/32")}},
		}},
	})
	require.NoError(t, err)

	desc, err := c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{aws.ToString(sg.GroupId)}})
	require.NoError(t, err)
	require.NotEmpty(t, desc.SecurityGroups[0].IpPermissions)
	require.NotEmpty(t, desc.SecurityGroups[0].IpPermissions[0].Ipv6Ranges)
	assert.Equal(t, "2001:db8::/32", aws.ToString(desc.SecurityGroups[0].IpPermissions[0].Ipv6Ranges[0].CidrIpv6))
}

func mustCreateVpc(t *testing.T, c *ec2.Client, cidr string) *ec2.CreateVpcOutput {
	t.Helper()
	out, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String(cidr)})
	require.NoError(t, err)
	return out
}

func mustCreateSG(t *testing.T, c *ec2.Client, name string, vpc *ec2.CreateVpcOutput) *ec2.CreateSecurityGroupOutput {
	t.Helper()
	out, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(name),
		Description: aws.String("enforce-test"),
		VpcId:       vpc.Vpc.VpcId,
	})
	require.NoError(t, err)
	return out
}

func requireSGErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	require.Error(t, err)
	var ae smithy.APIError
	require.True(t, errors.As(err, &ae), "expected smithy.APIError, got %T: %v", err, err)
	assert.True(t, strings.EqualFold(ae.ErrorCode(), want),
		"expected error code %q, got %q (message: %s)", want, ae.ErrorCode(), ae.ErrorMessage())
}
