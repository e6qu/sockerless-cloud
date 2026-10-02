package aws_sdk_test

import (
	"io"
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

// TestELBv2_IdleTimeoutBoundsASilentTarget holds the Application Load
// Balancer's data plane to its idle_timeout.timeout_seconds attribute: "HTTP
// 504: Gateway timeout ... The load balancer established a connection to the
// target but the target did not respond before the idle timeout period
// elapsed." The load balancer reads the attribute for each request, so
// ModifyLoadBalancerAttributes applies to the next one.
//
// Actions exercised: ModifyLoadBalancerAttributes,
// DescribeLoadBalancerAttributes, and the load balancer's data plane.
func TestELBv2_IdleTimeoutBoundsASilentTarget(t *testing.T) {
	ec2c := ec2Client()
	elb := elbv2Client()
	targetDone := make(chan struct{})
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/silent" {
			select {
			case <-r.Context().Done():
			case <-targetDone:
			}
			return
		}
		_, _ = w.Write([]byte("prompt"))
	}))
	defer targetServer.Close()
	defer close(targetDone)
	targetURL, err := url.Parse(targetServer.URL)
	require.NoError(t, err)
	targetHost, targetPortText, err := net.SplitHostPort(targetURL.Host)
	require.NoError(t, err)
	targetPort, err := strconv.Atoi(targetPortText)
	require.NoError(t, err)

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.87.0.0/16")})
	require.NoError(t, err)
	vpcID := *vpcOut.Vpc.VpcId
	subnet1, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.87.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnet2, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.87.2.0/24"),
		AvailabilityZone: aws.String("us-east-1b"),
	})
	require.NoError(t, err)
	lbOut, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("sdk-idle-lb"),
		Type:    elbtypes.LoadBalancerTypeEnumApplication,
		Subnets: []string{*subnet1.Subnet.SubnetId, *subnet2.Subnet.SubnetId},
	})
	require.NoError(t, err)
	lbArn := *lbOut.LoadBalancers[0].LoadBalancerArn
	lbDNSName := *lbOut.LoadBalancers[0].DNSName

	tgOut, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:                       aws.String("sdk-idle-tg"),
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

	_, err = elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(lbArn),
		Attributes: []elbtypes.LoadBalancerAttribute{{
			Key:   aws.String("idle_timeout.timeout_seconds"),
			Value: aws.String("1"),
		}},
	})
	require.NoError(t, err)
	attrs, err := elb.DescribeLoadBalancerAttributes(ctx, &elbv2.DescribeLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(lbArn),
	})
	require.NoError(t, err)
	assert.Contains(t, attrs.Attributes, elbtypes.LoadBalancerAttribute{
		Key:   aws.String("idle_timeout.timeout_seconds"),
		Value: aws.String("1"),
	})

	get := func(path string) (int, string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
		require.NoError(t, err)
		req.Host = lbDNSName
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}
	status, body := get("/silent")
	assert.Equal(t, http.StatusGatewayTimeout, status)
	assert.Contains(t, body, "504 Gateway Time-out")
	status, body = get("/prompt")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "prompt", body)

	_, err = elb.ModifyLoadBalancerAttributes(ctx, &elbv2.ModifyLoadBalancerAttributesInput{
		LoadBalancerArn: aws.String(lbArn),
		Attributes: []elbtypes.LoadBalancerAttribute{{
			Key:   aws.String("idle_timeout.timeout_seconds"),
			Value: aws.String("4001"),
		}},
	})
	var invalid *elbtypes.InvalidConfigurationRequestException
	require.ErrorAs(t, err, &invalid, "idle_timeout.timeout_seconds accepts 1 to 4000 seconds")

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
