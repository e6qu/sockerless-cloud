package aws_sdk_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_LoadBalancerAttributesPerType reads each load balancer type's
// attributes: an Application, a Network and a Gateway Load Balancer each
// report the attributes the LoadBalancerAttribute reference lists for their
// type at its default, and ModifyLoadBalancerAttributes refuses a key that
// belongs to another type and an Application Load Balancer's cross-zone load
// balancing turned off.
//
// Actions exercised: CreateLoadBalancer, DescribeLoadBalancerAttributes,
// ModifyLoadBalancerAttributes.
func TestELBv2_LoadBalancerAttributesPerType(t *testing.T) {
	ec2c := ec2Client()
	elb := elbv2Client()
	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.87.0.0/16")})
	require.NoError(t, err)
	subnet, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.87.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnet2, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            vpcOut.Vpc.VpcId,
		CidrBlock:        aws.String("10.87.2.0/24"),
		AvailabilityZone: aws.String("us-east-1b"),
	})
	require.NoError(t, err)
	subnets := []string{*subnet.Subnet.SubnetId, *subnet2.Subnet.SubnetId}

	create := func(name string, lbType elbtypes.LoadBalancerTypeEnum, scheme elbtypes.LoadBalancerSchemeEnum) string {
		t.Helper()
		out, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
			Name: aws.String(name), Type: lbType, Scheme: scheme, Subnets: subnets,
		})
		require.NoError(t, err)
		arn := *out.LoadBalancers[0].LoadBalancerArn
		t.Cleanup(func() {
			_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(arn)})
		})
		return arn
	}
	describe := func(arn string) map[string]string {
		t.Helper()
		out, err := elb.DescribeLoadBalancerAttributes(ctx, &elbv2.DescribeLoadBalancerAttributesInput{LoadBalancerArn: aws.String(arn)})
		require.NoError(t, err)
		attrs := map[string]string{}
		for _, a := range out.Attributes {
			attrs[aws.ToString(a.Key)] = aws.ToString(a.Value)
		}
		return attrs
	}
	keys := func(attrs map[string]string) []string {
		var out []string
		for k := range attrs {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}

	albArn := create("sdk-attrs-alb", elbtypes.LoadBalancerTypeEnumApplication, elbtypes.LoadBalancerSchemeEnumInternetFacing)
	alb := describe(albArn)
	assert.Equal(t, []string{
		"access_logs.s3.bucket", "access_logs.s3.enabled", "access_logs.s3.prefix",
		"client_keep_alive.seconds",
		"connection_logs.s3.bucket", "connection_logs.s3.enabled", "connection_logs.s3.prefix",
		"deletion_protection.enabled",
		"health_check_logs.s3.bucket", "health_check_logs.s3.enabled", "health_check_logs.s3.prefix",
		"idle_timeout.timeout_seconds", "ipv6.deny_all_igw_traffic", "load_balancing.cross_zone.enabled",
		"routing.http.desync_mitigation_mode", "routing.http.drop_invalid_header_fields.enabled",
		"routing.http.preserve_host_header.enabled", "routing.http.x_amzn_tls_version_and_cipher_suite.enabled",
		"routing.http.xff_client_port.enabled", "routing.http.xff_header_processing.mode",
		"routing.http2.enabled", "waf.fail_open.enabled", "zonal_shift.config.enabled",
	}, keys(alb))
	assert.Equal(t, "true", alb["load_balancing.cross_zone.enabled"])
	assert.Equal(t, "60", alb["idle_timeout.timeout_seconds"])
	assert.Equal(t, "3600", alb["client_keep_alive.seconds"])
	assert.Equal(t, "defensive", alb["routing.http.desync_mitigation_mode"])
	assert.Equal(t, "false", alb["ipv6.deny_all_igw_traffic"])

	nlbArn := create("sdk-attrs-nlb", elbtypes.LoadBalancerTypeEnumNetwork, elbtypes.LoadBalancerSchemeEnumInternal)
	nlb := describe(nlbArn)
	assert.Equal(t, []string{
		"access_logs.s3.bucket", "access_logs.s3.enabled", "access_logs.s3.prefix",
		"deletion_protection.enabled", "dns_record.client_routing_policy", "ipv6.deny_all_igw_traffic",
		"load_balancing.cross_zone.enabled", "secondary_ips.auto_assigned.per_subnet", "zonal_shift.config.enabled",
	}, keys(nlb))
	assert.Equal(t, "false", nlb["load_balancing.cross_zone.enabled"])
	assert.Equal(t, "any_availability_zone", nlb["dns_record.client_routing_policy"])
	assert.Equal(t, "0", nlb["secondary_ips.auto_assigned.per_subnet"])
	assert.Equal(t, "true", nlb["ipv6.deny_all_igw_traffic"], "an internal load balancer blocks internet gateway access")

	gwlbArn := create("sdk-attrs-gwlb", elbtypes.LoadBalancerTypeEnumGateway, "")
	assert.Equal(t, map[string]string{
		"deletion_protection.enabled":       "false",
		"load_balancing.cross_zone.enabled": "false",
	}, describe(gwlbArn))

	modified, err := elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(nlbArn),
		Attributes: []elbtypes.LoadBalancerAttribute{{
			Key: aws.String("dns_record.client_routing_policy"), Value: aws.String("availability_zone_affinity"),
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, modified.Attributes, elbtypes.LoadBalancerAttribute{
		Key: aws.String("dns_record.client_routing_policy"), Value: aws.String("availability_zone_affinity"),
	})

	for arn, key := range map[string]string{
		nlbArn:  "routing.http.xff_header_processing.mode",
		gwlbArn: "access_logs.s3.enabled",
	} {
		_, err := elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
			LoadBalancerArn: aws.String(arn),
			Attributes:      []elbtypes.LoadBalancerAttribute{{Key: aws.String(key), Value: aws.String("true")}},
		})
		var apiErr smithy.APIError
		require.True(t, errors.As(err, &apiErr), "%s on %s: %v", key, arn, err)
		assert.Equal(t, "InvalidConfigurationRequest", apiErr.ErrorCode())
		assert.Equal(t, "Key "+key+" not valid", apiErr.ErrorMessage())
	}

	_, err = elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(albArn),
		Attributes: []elbtypes.LoadBalancerAttribute{{
			Key: aws.String("load_balancing.cross_zone.enabled"), Value: aws.String("false"),
		}},
	})
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "cross-zone load balancing off on an Application Load Balancer: %v", err)
	assert.Equal(t, "InvalidConfigurationRequest", apiErr.ErrorCode())
	assert.Equal(t, "true", describe(albArn)["load_balancing.cross_zone.enabled"])
}
