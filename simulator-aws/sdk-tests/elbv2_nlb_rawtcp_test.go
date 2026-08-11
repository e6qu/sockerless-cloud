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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_NLBRawTCPRoundTrip drives the Network Load Balancer raw-TCP data
// plane end-to-end through the real sim: create an NLB + TCP target group + TCP
// listener, register a live raw-TCP target, then discover the reachable endpoint
// the way a real client does (DescribeLoadBalancers -> DNSName) and prove a raw
// byte stream round-trips through to the target (the SSH-through-NLB shape). No
// HTTP is involved on the data path.
func TestELBv2_NLBRawTCPRoundTrip(t *testing.T) {
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

	// A raw-TCP echo backend (not HTTP): reads a line, writes "echo:<line>".
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
		Name: aws.String("nlb-rawtcp"), Type: elbtypes.LoadBalancerTypeEnumNetwork, Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(nlb.LoadBalancers[0].LoadBalancerArn)
	// Delete the NLB at the end so its stream proxy (bound on the listener port)
	// and the DNS hosts entry it injects into workload containers don't leak into
	// the rest of the shared-sim test suite and interfere with later tests.
	defer elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})

	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String("nlb-rawtcp-tg"), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(int32(backendPort)),
		VpcId: aws.String(vpcID), TargetType: elbtypes.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)
	// A TCP target group carries no Matcher (issue #685).
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

	// #691: DescribeLoadBalancers returns the stable AWS-shaped hostname, never a
	// host:port. (The full shape/stability contract is asserted in the SDK
	// idempotency test below; here we just take the DNS name to resolve.)
	desc, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dnsName := aws.ToString(desc.LoadBalancers[0].DNSName)
	require.NotContains(t, dnsName, ":", "DNSName must be a hostname, not host:port")

	// Reach the data plane the way an in-network client does: resolve the
	// AWS-shaped DNS name to the NLB's stream-proxy host, then connect on the
	// listener port. A workload container is given a hosts entry mapping the DNS
	// name to the proxy host; here the resolveNLBHostname helper stands in for
	// that resolution (the sim binds the first NLB on a listener port at
	// 127.0.0.1:<port>, the host every same-host client reaches). We resolve the
	// hostname → IP and connect on the listener port — never a host:port from
	// DNSName.
	proxyHost := resolveNLBHostname(dnsName)
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

// resolveNLBHostname resolves an NLB's AWS-shaped DNS name to its stream-proxy
// host, standing in for the hosts entry the sim injects into a workload
// container so an in-network client reaches the proxy. The first NLB on a
// listener port binds 127.0.0.1, the host any same-host client reaches.
func resolveNLBHostname(dnsName string) string {
	if !strings.Contains(dnsName, ".elb.") {
		return dnsName
	}
	return "127.0.0.1"
}

// TestELBv2_NLBDescribeStableHostname is the #691 contract guard: an NLB's
// DescribeLoadBalancers DNSName is a stable AWS-shaped hostname (never a
// host:port), unchanged across calls, and a CanonicalHostedZoneId is present so
// a Route53 alias target can reference it.
func TestELBv2_NLBDescribeStableHostname(t *testing.T) {
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
		Name: aws.String("nlb-stable-dns"), Type: elbtypes.LoadBalancerTypeEnumNetwork, Subnets: []string{subnetID},
	})
	require.NoError(t, err)
	lbArn := aws.ToString(nlb.LoadBalancers[0].LoadBalancerArn)
	// Clean up so this NLB's proxy/host-entry doesn't leak into the shared suite.
	defer elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(lbArn)})

	tg, err := elb.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name: aws.String("nlb-stable-dns-tg"), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(443),
		VpcId: vpc.Vpc.VpcId, TargetType: elbtypes.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	tgArn := aws.ToString(tg.TargetGroups[0].TargetGroupArn)
	listenerPort := availableELBv2ListenerPort(t)
	_, err = elb.CreateListener(ctx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(lbArn), Protocol: elbtypes.ProtocolEnumTcp, Port: aws.Int32(listenerPort),
		DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: aws.String(tgArn)}},
	})
	require.NoError(t, err)

	shape := regexp.MustCompile(`^nlb-stable-dns-[0-9a-f]+\.elb\.us-east-1\.amazonaws\.com$`)

	desc1, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dns1 := aws.ToString(desc1.LoadBalancers[0].DNSName)
	assert.NotContains(t, dns1, ":", "DNSName must be a hostname, never host:port")
	assert.Regexp(t, shape, dns1, "DNSName must be the AWS-shaped NLB hostname")
	assert.NotEmpty(t, aws.ToString(desc1.LoadBalancers[0].CanonicalHostedZoneId), "CanonicalHostedZoneId is required for a Route53 alias target")

	// A second describe returns the SAME DNSName (no ephemeral-port drift).
	desc2, err := elb.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{LoadBalancerArns: []string{lbArn}})
	require.NoError(t, err)
	dns2 := aws.ToString(desc2.LoadBalancers[0].DNSName)
	assert.Equal(t, dns1, dns2, "NLB DNSName must be stable across DescribeLoadBalancers calls")
	assert.False(t, strings.Contains(dns2, ":"), "stable DNSName must remain a hostname")
}
