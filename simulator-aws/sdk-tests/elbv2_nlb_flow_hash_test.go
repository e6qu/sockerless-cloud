package aws_sdk_test

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_NLBFlowHashAndSourceIPStickiness expects a Network Load Balancer to
// route each TCP connection by flow hash, so one client's connections, with
// their own source ports and sequence numbers, reach different targets
// (nlb-introduction), until source_ip stickiness routes all traffic from that
// client's address to one target (nlb-target-group-attributes).
func TestELBv2_NLBFlowHashAndSourceIPStickiness(t *testing.T) {
	t.Parallel()
	elb := elbv2Client()
	ec2c := ec2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.162.0.0/16")})
	require.NoError(t, err)
	sn, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.162.1.0/24"), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)

	nlb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(uniquename.New("nlb-flow")), Type: elbtypes.LoadBalancerTypeEnumNetwork, Subnets: []string{aws.ToString(sn.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(nlb.LoadBalancers[0].LoadBalancerArn)
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})
	})
	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String(uniquename.New("nlb-flow-tg")), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(80),
		VpcId: vpc.Vpc.VpcId, TargetType: elbtypes.TargetTypeEnumIp,
		HealthCheckIntervalSeconds: aws.Int32(5), HealthCheckTimeoutSeconds: aws.Int32(2),
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)
	t.Cleanup(func() {
		_, _ = elb.DeleteTargetGroup(ctx, &elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(tgArn)})
	})

	var targets []elbtypes.TargetDescription
	for _, name := range []string{"flow-a", "flow-b"} {
		backend, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = backend.Close() })
		go func() {
			for {
				conn, err := backend.Accept()
				if err != nil {
					return
				}
				_, _ = io.WriteString(conn, name)
				_ = conn.Close()
			}
		}()
		host, portText, err := net.SplitHostPort(backend.Addr().String())
		require.NoError(t, err)
		port, err := strconv.Atoi(portText)
		require.NoError(t, err)
		targets = append(targets, elbtypes.TargetDescription{Id: aws.String(host), Port: aws.Int32(int32(port))})
	}
	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{TargetGroupArn: aws.String(tgArn), Targets: targets})
	require.NoError(t, err)
	listenerPort := elbv2CreateListenerOnFreePort(t, func(port int32) error {
		_, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
			LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(port),
			DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn)}},
		})
		return err
	})
	for _, target := range targets {
		waitForELBv2TargetHealth(t, tgArn, target, elbtypes.TargetHealthStateEnumHealthy)
	}

	desc, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	proxyHost, _ := dnsResolveA(t, aws.ToString(desc.LoadBalancers[0].DNSName))
	endpoint := net.JoinHostPort(proxyHost, strconv.Itoa(int(listenerPort)))
	connect := func() string {
		t.Helper()
		conn, err := net.DialTimeout("tcp", endpoint, 5*time.Second)
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		body, err := io.ReadAll(conn)
		require.NoError(t, err)
		return string(body)
	}

	reached := map[string]int{}
	for range 40 {
		reached[connect()]++
	}
	assert.Len(t, reached, 2, "one client's connections hash to every target: %v", reached)

	stickiness := func(kind string) error {
		_, err := elb.ModifyTargetGroupAttributes(ctx, &elbv2.ModifyTargetGroupAttributesInput{
			TargetGroupArn: aws.String(tgArn),
			Attributes: []elbtypes.TargetGroupAttribute{
				{Key: aws.String("stickiness.enabled"), Value: aws.String("true")},
				{Key: aws.String("stickiness.type"), Value: aws.String(kind)},
			},
		})
		return err
	}
	// The model gives source_ip to Network Load Balancers alone.
	for _, kind := range []string{"lb_cookie", "app_cookie", "source_ip_dest_ip"} {
		requireELBv2Error(t, stickiness(kind), "InvalidConfigurationRequest", "")
	}
	require.NoError(t, stickiness("source_ip"))
	attrs, err := elb.DescribeTargetGroupAttributes(ctx, &elbv2.DescribeTargetGroupAttributesInput{TargetGroupArn: aws.String(tgArn)})
	require.NoError(t, err)
	readBack := map[string]string{}
	for _, a := range attrs.Attributes {
		readBack[aws.ToString(a.Key)] = aws.ToString(a.Value)
	}
	assert.Equal(t, "source_ip", readBack["stickiness.type"])
	sticky := map[string]int{}
	for range 20 {
		sticky[connect()]++
	}
	assert.Len(t, sticky, 1, "source_ip stickiness holds one client address on one target: %v", sticky)
}
