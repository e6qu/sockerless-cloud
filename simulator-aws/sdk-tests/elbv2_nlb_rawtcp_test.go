package aws_sdk_test

import (
	"bufio"
	"net"
	"regexp"
	"strconv"
	"strings"
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

// TestELBv2_NLBRawTCPRoundTrip registers a raw-TCP target behind a Network Load
// Balancer, reaches it as a client does (DescribeLoadBalancers, then DNSName)
// and round-trips a byte stream to it with no HTTP involved, the
// SSH-through-NLB shape.
func TestELBv2_NLBRawTCPRoundTrip(t *testing.T) {
	t.Parallel()
	lbName := uniquename.New("nlb-rawtcp")
	tgName := uniquename.New("nlb-rawtcp-tg")
	elb := elbv2Client()
	ec2c := ec2Client()

	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.160.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	sn, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.160.1.0/24"), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(sn.Subnet.SubnetId)

	// A raw-TCP echo backend: reads a line, writes "echo:<line>".
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend.Close()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := c.Write([]byte("echo:" + line)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	backendHost, backendPortText, err := net.SplitHostPort(backend.Addr().String())
	require.NoError(t, err)
	backendPort, err := strconv.Atoi(backendPortText)
	require.NoError(t, err)

	nlb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(lbName), Type: elbtypes.LoadBalancerTypeEnumNetwork, Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(nlb.LoadBalancers[0].LoadBalancerArn)
	// Delete the NLB so its stream proxy on the listener port and the DNS hosts
	// entry it gives workload containers stay out of later tests.
	defer elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})

	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String(tgName), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(int32(backendPort)),
		VpcId: aws.String(vpcID), TargetType: elbtypes.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)
	assert.Nil(t, tg.TargetGroups[0].Matcher, "TCP target group carries no Matcher")

	_, err = elb.RegisterTargets(ctx, &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(tgArn),
		Targets:        []elbtypes.TargetDescription{{Id: aws.String(backendHost), Port: aws.Int32(int32(backendPort))}},
	})
	require.NoError(t, err)

	const listenerPort = 2222
	_, err = elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(listenerPort),
		DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn)}},
	})
	require.NoError(t, err)

	// TestELBv2_NLBDescribeStableHostname asserts the DNSName's shape and
	// stability; here it is only resolved.
	desc, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dnsName := aws.ToString(desc.LoadBalancers[0].DNSName)
	require.NotContains(t, dnsName, ":", "DNSName must be a hostname, not host:port")

	// The listener forwards only to a target in service, so wait for the first
	// health check to pass.
	waitForELBv2TargetHealth(t, tgArn,
		elbtypes.TargetDescription{Id: aws.String(backendHost), Port: aws.Int32(int32(backendPort))},
		elbtypes.TargetHealthStateEnumHealthy)

	proxyHost, _ := dnsResolveA(t, dnsName)
	endpoint := net.JoinHostPort(proxyHost, strconv.Itoa(listenerPort))
	conn, err := net.DialTimeout("tcp", endpoint, 5*time.Second)
	require.NoError(t, err, "dial NLB %s (resolved to %s)", dnsName, endpoint)
	defer conn.Close()
	_, err = conn.Write([]byte("hello-nlb\n"))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	got, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "echo:hello-nlb\n", got, "raw TCP byte stream must round-trip through the NLB to the target")
}

// TestELBv2_NLBDescribeStableHostname expects an NLB's DNSName to be a stable
// AWS-shaped hostname, never a host:port, alongside a CanonicalHostedZoneId a
// Route 53 alias target can name.
func TestELBv2_NLBDescribeStableHostname(t *testing.T) {
	t.Parallel()
	lbName := uniquename.New("nlb-stable")
	tgName := uniquename.New("nlb-stable-tg")
	elb := elbv2Client()
	ec2c := ec2Client()

	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.161.0.0/16")})
	require.NoError(t, err)
	sn, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.161.1.0/24"), AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(sn.Subnet.SubnetId)

	nlb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String(lbName), Type: elbtypes.LoadBalancerTypeEnumNetwork, Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(nlb.LoadBalancers[0].LoadBalancerArn)
	// Delete the NLB so its proxy and hosts entry stay out of later tests.
	defer elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})

	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String(tgName), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(443),
		VpcId: vpc.Vpc.VpcId, TargetType: elbtypes.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)
	listenerPort := elbv2CreateListenerOnFreePort(t, func(port int32) error {
		_, err := elb.CreateListener(ctx, &elbv2.CreateListenerInput{
			LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(port),
			DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn)}},
		})
		return err
	})
	_ = listenerPort

	shape := regexp.MustCompile(`^` + regexp.QuoteMeta(lbName) + `-[0-9a-f]+\.elb\.us-east-1\.amazonaws\.com$`)

	desc1, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dns1 := aws.ToString(desc1.LoadBalancers[0].DNSName)
	assert.NotContains(t, dns1, ":", "DNSName must be a hostname, never host:port")
	assert.Regexp(t, shape, dns1, "DNSName must be the AWS-shaped NLB hostname")
	assert.Equal(t, "Z26RNL4JYFTOTI", aws.ToString(desc1.LoadBalancers[0].CanonicalHostedZoneId),
		"a Network Load Balancer in us-east-1 reports that Region's Network Load Balancer hosted zone")

	// A second describe returns the same DNSName.
	desc2, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dns2 := aws.ToString(desc2.LoadBalancers[0].DNSName)
	assert.Equal(t, dns1, dns2, "NLB DNSName must be stable across DescribeLoadBalancers calls")
	assert.False(t, strings.Contains(dns2, ":"), "stable DNSName must remain a hostname")
}
