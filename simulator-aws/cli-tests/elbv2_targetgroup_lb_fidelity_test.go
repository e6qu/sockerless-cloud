package aws_cli_test

import (
	"strings"
	"testing"
)

func TestELBv2TargetGroupLBFidelityCLI(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.152.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	ipv6 := elbv2VPCIpv6BlockCLI(t, vpcID)
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.152.1.0/24",
		"--ipv6-cidr-block", elbv2Ipv6SubnetRangeCLI(t, ipv6, 0),
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")

	tgArn := q("elbv2", "create-target-group", "--name", "cli-tg-fid", "--protocol", "HTTP", "--port", "80",
		"--vpc-id", vpcID, "--target-type", "ip", "--matcher", "HttpCode=200-299",
		"--query", "TargetGroups[0].TargetGroupArn", "--output", "text")
	out := q("elbv2", "describe-target-groups", "--target-group-arns", tgArn,
		"--query", "TargetGroups[0].[Matcher.HttpCode,ProtocolVersion]", "--output", "text")
	if f := strings.Fields(out); len(f) != 2 || f[0] != "200-299" || f[1] != "HTTP1" {
		t.Fatalf("target group matcher/protocol_version: got %q, want '200-299 HTTP1'", out)
	}

	// A TCP target group's health check defaults to TCP, so AWS returns no
	// Matcher, which JMESPath prints as None.
	tcpTGArn := q("elbv2", "create-target-group", "--name", "cli-tg-tcp-fid", "--protocol", "TCP", "--port", "443",
		"--vpc-id", vpcID, "--target-type", "ip",
		"--query", "TargetGroups[0].TargetGroupArn", "--output", "text")
	if v := q("elbv2", "describe-target-groups", "--target-group-arns", tcpTGArn,
		"--query", "TargetGroups[0].Matcher.HttpCode", "--output", "text"); v != "None" {
		t.Fatalf("TCP target group Matcher: got %q, want None (no Matcher for TCP health checks)", v)
	}
	// AWS omits HealthCheckPath for a TCP health check too.
	if v := q("elbv2", "describe-target-groups", "--target-group-arns", tcpTGArn,
		"--query", "TargetGroups[0].HealthCheckPath", "--output", "text"); v != "None" {
		t.Fatalf("TCP target group HealthCheckPath: got %q, want None (no path for TCP health checks)", v)
	}
	// A TCP target group with an HTTP health check carries a Matcher.
	tcpHTTPArn := q("elbv2", "create-target-group", "--name", "cli-tg-tcp-httphc-fid", "--protocol", "TCP", "--port", "443",
		"--vpc-id", vpcID, "--target-type", "ip", "--health-check-protocol", "HTTP", "--health-check-port", "8080",
		"--query", "TargetGroups[0].TargetGroupArn", "--output", "text")
	if v := q("elbv2", "describe-target-groups", "--target-group-arns", tcpHTTPArn,
		"--query", "TargetGroups[0].Matcher.HttpCode", "--output", "text"); v != "200" {
		t.Fatalf("TCP target group with HTTP health check Matcher: got %q, want 200", v)
	}

	lbArn := q("elbv2", "create-load-balancer", "--name", "cli-nlb-fid", "--type", "network",
		"--subnets", subnetID, "--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")
	if v := q("elbv2", "describe-load-balancers", "--load-balancer-arns", lbArn,
		"--query", "LoadBalancers[0].EnforceSecurityGroupInboundRulesOnPrivateLinkTraffic", "--output", "text"); v != "on" {
		t.Fatalf("NLB enforce-sg default: got %q, want on", v)
	}
	runCLI(t, awsCLI("elbv2", "set-ip-address-type", "--load-balancer-arn", lbArn, "--ip-address-type", "dualstack"))
	if v := q("elbv2", "describe-load-balancers", "--load-balancer-arns", lbArn,
		"--query", "LoadBalancers[0].IpAddressType", "--output", "text"); v != "dualstack" {
		t.Fatalf("ip_address_type after set: got %q, want dualstack", v)
	}
}
