package aws_cli_test

import (
	"net/netip"
	"strings"
	"testing"
)

func elbv2VPCIpv6BlockCLI(t *testing.T, vpcID string) string {
	t.Helper()
	return strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-vpc-cidr-block", "--vpc-id", vpcID,
		"--amazon-provided-ipv6-cidr-block", "--query", "Ipv6CidrBlockAssociation.Ipv6CidrBlock", "--output", "text")))
}

func elbv2Ipv6SubnetRangeCLI(t *testing.T, block string, index int) string {
	t.Helper()
	prefix, err := netip.ParsePrefix(block)
	if err != nil {
		t.Fatalf("VPC IPv6 block %q: %v", block, err)
	}
	addr := prefix.Masked().Addr().As16()
	addr[7] += byte(index)
	return netip.PrefixFrom(netip.AddrFrom16(addr), 64).String()
}

// TestELBv2CLI_SubnetRules expects create-load-balancer, set-subnets and
// set-ip-address-type to refuse an Application Load Balancer in one
// Availability Zone, two subnets in one zone, and a dualstack load balancer on
// a subnet without an IPv6 CIDR block; a Network Load Balancer takes one zone.
func TestELBv2CLI_SubnetRules(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.165.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	ipv6 := elbv2VPCIpv6BlockCLI(t, vpcID)
	subnet := func(cidr, zone string, extra ...string) string {
		return q(append([]string{"ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", cidr,
			"--availability-zone", zone, "--query", "Subnet.SubnetId", "--output", "text"}, extra...)...)
	}
	zoneA := subnet("10.165.1.0/24", "us-east-1a", "--ipv6-cidr-block", elbv2Ipv6SubnetRangeCLI(t, ipv6, 1))
	zoneAToo := subnet("10.165.2.0/24", "us-east-1a")
	zoneB := subnet("10.165.3.0/24", "us-east-1b")
	refused := func(want string, args ...string) {
		t.Helper()
		out := runCLIExpectError(t, awsCLI(append([]string{"elbv2"}, args...)...))
		if !strings.Contains(out, want) {
			t.Fatalf("elbv2 %s: got %q, want %q", args[0], out, want)
		}
	}

	refused("(ValidationError)", "create-load-balancer", "--name", "cli-one-zone-alb", "--type", "application", "--subnets", zoneA)
	refused("(InvalidConfigurationRequest)", "create-load-balancer", "--name", "cli-same-zone-nlb", "--type", "network",
		"--subnets", zoneA, zoneAToo)
	refused("(InvalidSubnet)", "create-load-balancer", "--name", "cli-dualstack-alb", "--type", "application",
		"--ip-address-type", "dualstack", "--subnets", zoneA, zoneB)

	nlb := q("elbv2", "create-load-balancer", "--name", "cli-one-zone-nlb", "--type", "network", "--subnets", zoneAToo,
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", nlb).Run() })
	alb := q("elbv2", "create-load-balancer", "--name", "cli-two-zone-alb", "--type", "application", "--subnets", zoneA, zoneB,
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", alb).Run() })

	refused("At least two subnets in two different Availability Zones must be specified",
		"set-subnets", "--load-balancer-arn", alb, "--subnets", zoneA)
	refused("(InvalidSubnet)", "set-ip-address-type", "--load-balancer-arn", alb, "--ip-address-type", "dualstack")

	q("ec2", "associate-subnet-cidr-block", "--subnet-id", zoneB, "--ipv6-cidr-block", elbv2Ipv6SubnetRangeCLI(t, ipv6, 3))
	if got := q("elbv2", "set-ip-address-type", "--load-balancer-arn", alb, "--ip-address-type", "dualstack",
		"--query", "IpAddressType", "--output", "text"); got != "dualstack" {
		t.Fatalf("set-ip-address-type once every subnet holds an IPv6 block: got %q, want dualstack", got)
	}
}
