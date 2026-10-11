package aws_sdk_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_ForwardedHeaders sends requests through an Application Load
// Balancer to a target that echoes the headers it received. The load balancer
// appends the client's IP to X-Forwarded-For, names the listener's protocol and
// port in X-Forwarded-Proto and X-Forwarded-Port, and adds X-Amzn-Trace-Id; the
// routing.http.xff_header_processing.mode and
// routing.http.xff_client_port.enabled attributes decide what it does with
// X-Forwarded-For.
//
// Actions exercised: ModifyLoadBalancerAttributes,
// DescribeLoadBalancerAttributes, and the load balancer's data plane.
func TestELBv2_ForwardedHeaders(t *testing.T) {
	t.Parallel()
	ec2c := ec2Client()
	elb := elbv2Client()
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(r.Header)
	}))
	defer targetServer.Close()
	targetURL, err := url.Parse(targetServer.URL)
	require.NoError(t, err)
	targetHost, targetPortText, err := net.SplitHostPort(targetURL.Host)
	require.NoError(t, err)
	targetPort, err := strconv.Atoi(targetPortText)
	require.NoError(t, err)

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.86.0.0/16")})
	require.NoError(t, err)
	vpcID := *vpcOut.Vpc.VpcId
	subnet1, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.86.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnet2, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.86.2.0/24"),
		AvailabilityZone: aws.String("us-east-1b"),
	})
	require.NoError(t, err)
	lbOut, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("sdk-xff-lb"),
		Type:    elbtypes.LoadBalancerTypeEnumApplication,
		Subnets: []string{*subnet1.Subnet.SubnetId, *subnet2.Subnet.SubnetId},
	})
	require.NoError(t, err)
	lbArn := *lbOut.LoadBalancers[0].LoadBalancerArn
	lbDNSName := *lbOut.LoadBalancers[0].DNSName

	tgOut, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:                       aws.String("sdk-xff-tg"),
		Protocol:                   elbtypes.ProtocolEnumHttp,
		Port:                       aws.Int32(80),
		VpcId:                      aws.String(vpcID),
		TargetType:                 elbtypes.TargetTypeEnumIp,
		HealthCheckPath:            aws.String("/healthz"),
		HealthCheckIntervalSeconds: aws.Int32(5),
		HealthCheckTimeoutSeconds:  aws.Int32(2),
	})
	require.NoError(t, err)
	tgArn := *tgOut.TargetGroups[0].TargetGroupArn
	target := elbtypes.TargetDescription{Id: aws.String(targetHost), Port: aws.Int32(int32(targetPort))}
	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(tgArn),
		Targets:        []elbtypes.TargetDescription{target},
	})
	require.NoError(t, err)
	listenerOut, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn),
		Protocol:        elbtypes.ProtocolEnumHttp,
		Port:            aws.Int32(80),
		DefaultActions: []elbtypes.Action{{
			Type:           elbtypes.ActionTypeEnumForward,
			TargetGroupArn: aws.String(tgArn),
		}},
	})
	require.NoError(t, err)
	listenerArn := *listenerOut.Listeners[0].ListenerArn
	waitForELBv2TargetHealth(t, tgArn, target, elbtypes.TargetHealthStateEnumHealthy)

	attrs, err := elb.DescribeLoadBalancerAttributes(ctx, &elbv2.DescribeLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(lbArn),
	})
	require.NoError(t, err)
	assert.Contains(t, attrs.Attributes, elbtypes.LoadBalancerAttribute{
		Key: aws.String("routing.http.xff_header_processing.mode"), Value: aws.String("append"),
	})
	assert.Contains(t, attrs.Attributes, elbtypes.LoadBalancerAttribute{
		Key: aws.String("routing.http.xff_client_port.enabled"), Value: aws.String("false"),
	})

	received := func(header http.Header) http.Header {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/headers", nil)
		require.NoError(t, err)
		req.Host = lbDNSName
		for name, values := range header {
			req.Header[name] = values
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var seen http.Header
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&seen))
		return seen
	}
	modify := func(key, value string) error {
		_, err := elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
			LoadBalancerArn: aws.String(lbArn),
			Attributes:      []elbtypes.LoadBalancerAttribute{{Key: aws.String(key), Value: aws.String(value)}},
		})
		return err
	}

	seen := received(http.Header{"X-Forwarded-For": {"203.0.113.9"}, "X-Forwarded-Proto": {"https"}})
	assert.Equal(t, "203.0.113.9, 127.0.0.1", seen.Get("X-Forwarded-For"))
	assert.Equal(t, "http", seen.Get("X-Forwarded-Proto"), "the listener's protocol replaces the client's claim")
	assert.Equal(t, "80", seen.Get("X-Forwarded-Port"))
	assert.Regexp(t, `^Root=1-[0-9a-f]{8}-[0-9a-f]{24}$`, seen.Get("X-Amzn-Trace-Id"))

	seen = received(http.Header{"X-Amzn-Trace-Id": {"Root=1-67891233-abcdef012345678912345678;CalledFrom=app"}})
	assert.Regexp(t, `^Self=1-[0-9a-f]{8}-[0-9a-f]{24};Root=1-67891233-abcdef012345678912345678;CalledFrom=app$`,
		seen.Get("X-Amzn-Trace-Id"))

	require.NoError(t, modify("routing.http.xff_client_port.enabled", "true"))
	assert.Regexp(t, `^127\.0\.0\.1:\d+$`, received(nil).Get("X-Forwarded-For"))

	require.NoError(t, modify("routing.http.xff_header_processing.mode", "preserve"))
	assert.Equal(t, "203.0.113.9", received(http.Header{"X-Forwarded-For": {"203.0.113.9"}}).Get("X-Forwarded-For"))

	require.NoError(t, modify("routing.http.xff_header_processing.mode", "remove"))
	assert.Empty(t, received(http.Header{"X-Forwarded-For": {"203.0.113.9"}}).Values("X-Forwarded-For"))

	var invalid *elbtypes.InvalidConfigurationRequestException
	require.ErrorAs(t, modify("routing.http.xff_header_processing.mode", "replace"), &invalid)

	_, err = elb.DeleteListener(ctx, &elbv2.DeleteListenerInput{ListenerArn: aws.String(listenerArn)})
	require.NoError(t, err)
	_, err = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(tgArn)})
	require.NoError(t, err)
	_, err = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	require.NoError(t, err)
	_, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnet2.Subnet.SubnetId})
	_, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: subnet1.Subnet.SubnetId})
	_, _ = ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
}
