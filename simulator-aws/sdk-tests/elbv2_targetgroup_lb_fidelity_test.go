package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_TargetGroupFidelitySDK covers the Matcher (was hardcoded 200),
// protocol_version, and ip_address_type round-trip that aws_lb_target_group
// reads back.
func TestELBv2_TargetGroupFidelitySDK(t *testing.T) {
	c := elbv2Client()
	vpc, err := ec2Client().CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.150.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)

	// HTTP target group with an explicit matcher range.
	httpTG, err := c.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String("tg-http-fidelity"), Protocol: elbv2types.ProtocolEnumHttp,
		Port: aws.Int32(80), VpcId: aws.String(vpcID), TargetType: elbv2types.TargetTypeEnumIp,
		Matcher: &elbv2types.Matcher{HttpCode: aws.String("200-299")},
	})
	require.NoError(t, err)
	got := httpTG.TargetGroups[0]
	require.NotNil(t, got.Matcher)
	assert.Equal(t, "200-299", aws.ToString(got.Matcher.HttpCode), "matcher must round-trip (was hardcoded 200)")
	assert.Equal(t, "/", aws.ToString(got.HealthCheckPath), "HTTP health check defaults HealthCheckPath to /")
	assert.Equal(t, "HTTP1", aws.ToString(got.ProtocolVersion), "HTTP target group protocol_version defaults HTTP1")
	assert.Equal(t, elbv2types.TargetGroupIpAddressTypeEnumIpv4, got.IpAddressType)

	// TCP (NLB) target group: its health check defaults to TCP, so real AWS
	// returns NO Matcher and NO HealthCheckPath (both apply only to HTTP/HTTPS
	// health checks). Emitting either breaks terraform-provider-aws idempotency.
	tcpTG, err := c.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String("tg-tcp-fidelity"), Protocol: elbv2types.ProtocolEnumTcp,
		Port: aws.Int32(443), VpcId: aws.String(vpcID), TargetType: elbv2types.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	assert.Nil(t, tcpTG.TargetGroups[0].Matcher, "TCP health check carries no Matcher")
	assert.Nil(t, tcpTG.TargetGroups[0].HealthCheckPath, "TCP health check carries no HealthCheckPath")
	tcpDesc, err := c.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{aws.ToString(tcpTG.TargetGroups[0].TargetGroupArn)}})
	require.NoError(t, err)
	assert.Nil(t, tcpDesc.TargetGroups[0].Matcher, "DescribeTargetGroups omits Matcher for a TCP health check")
	assert.Nil(t, tcpDesc.TargetGroups[0].HealthCheckPath, "DescribeTargetGroups omits HealthCheckPath for a TCP health check")

	// A TCP target group with an explicit HTTP health check DOES carry a Matcher
	// and the default HealthCheckPath.
	tcpHTTPHC, err := c.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String("tg-tcp-httphc-fidelity"), Protocol: elbv2types.ProtocolEnumTcp,
		Port: aws.Int32(443), VpcId: aws.String(vpcID), TargetType: elbv2types.TargetTypeEnumIp,
		HealthCheckProtocol: elbv2types.ProtocolEnumHttp, HealthCheckPort: aws.String("8080"),
	})
	require.NoError(t, err)
	require.NotNil(t, tcpHTTPHC.TargetGroups[0].Matcher, "an HTTP health check on a TCP target group carries a Matcher")
	assert.Equal(t, "200", aws.ToString(tcpHTTPHC.TargetGroups[0].Matcher.HttpCode), "HTTP health check default matcher is 200")
	assert.Equal(t, "/", aws.ToString(tcpHTTPHC.TargetGroups[0].HealthCheckPath), "an HTTP health check on a TCP target group carries the / HealthCheckPath")

	// ModifyTargetGroup matcher must persist.
	_, err = c.ModifyTargetGroup(ctx, &elbv2.ModifyTargetGroupInput{
		TargetGroupArn: got.TargetGroupArn, Matcher: &elbv2types.Matcher{HttpCode: aws.String("200,202")},
	})
	require.NoError(t, err)
	desc, err := c.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{TargetGroupArns: []string{aws.ToString(got.TargetGroupArn)}})
	require.NoError(t, err)
	assert.Equal(t, "200,202", aws.ToString(desc.TargetGroups[0].Matcher.HttpCode), "ModifyTargetGroup matcher must persist")
}

// TestELBv2_LoadBalancerFidelitySDK covers the NLB enforce-SG field and the
// SetIpAddressType in-place update.
func TestELBv2_LoadBalancerFidelitySDK(t *testing.T) {
	c := elbv2Client()
	ec2c := ec2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.151.0.0/16")})
	require.NoError(t, err)
	sn, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.151.1.0/24"), AvailabilityZone: aws.String("us-east-1a")})
	require.NoError(t, err)
	subnetID := aws.ToString(sn.Subnet.SubnetId)

	nlb, err := c.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String("nlb-fidelity"), Type: elbv2types.LoadBalancerTypeEnumNetwork,
		Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	assert.Equal(t, "on", aws.ToString(nlb.LoadBalancers[0].EnforceSecurityGroupInboundRulesOnPrivateLinkTraffic),
		"NLB defaults enforce_security_group_inbound_rules_on_private_link_traffic to on")
	lbArn := aws.ToString(nlb.LoadBalancers[0].LoadBalancerArn)

	_, err = c.SetIpAddressType(ctx, &elbv2.SetIpAddressTypeInput{
		LoadBalancerArn: aws.String(lbArn), IpAddressType: elbv2types.IpAddressTypeDualstack,
	})
	require.NoError(t, err)
	out, err := c.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	assert.Equal(t, elbv2types.IpAddressTypeDualstack, out.LoadBalancers[0].IpAddressType, "SetIpAddressType must persist")
}
