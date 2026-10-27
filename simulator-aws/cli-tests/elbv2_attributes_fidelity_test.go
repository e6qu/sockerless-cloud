package aws_cli_test

import (
	"strings"
	"testing"
)

// TestELBv2CLI_CapacityReservationOmitsUnsetMinimum expects an ALB with no
// minimum capacity to report no MinimumLoadBalancerCapacity, which the CLI
// renders as None.
func TestELBv2CLI_CapacityReservationOmitsUnsetMinimum(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.83.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sub := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpc, "--cidr-block", "10.83.1.0/24", "--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId", "--output", "text")))
	sub2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpc, "--cidr-block", "10.83.2.0/24", "--availability-zone", "us-east-1b",
		"--query", "Subnet.SubnetId", "--output", "text")))
	lb := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-load-balancer",
		"--name", "cli-cap-res-lb", "--type", "application", "--subnets", sub, sub2,
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")))

	got := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "describe-capacity-reservation",
		"--load-balancer-arn", lb, "--query", "MinimumLoadBalancerCapacity", "--output", "text")))
	if got != "None" {
		t.Fatalf("MinimumLoadBalancerCapacity = %q, want None when unconfigured", got)
	}
}
