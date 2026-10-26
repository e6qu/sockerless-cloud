package aws_cli_test

import (
	"strings"
	"testing"
)

// TestELBv2CLI_SubnetMappings expects a Network Load Balancer to hold the
// Elastic IP address a mapping names and list it under the subnet, the same
// request again to answer the same load balancer, and an Application Load
// Balancer to be refused an Elastic IP address.
func TestELBv2CLI_SubnetMappings(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.25.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	zoneA := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.25.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")
	zoneB := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.25.2.0/24",
		"--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")
	allocation := q("ec2", "allocate-address", "--domain", "vpc", "--query", "[AllocationId,PublicIp]", "--output", "text")
	fields := strings.Fields(allocation)
	if len(fields) != 2 {
		t.Fatalf("allocate-address: got %q", allocation)
	}
	allocationID, publicIP := fields[0], fields[1]

	out := runCLIExpectError(t, awsCLI("elbv2", "create-load-balancer", "--name", "cli-map-alb", "--type", "application",
		"--subnet-mappings", "SubnetId="+zoneA+",AllocationId="+allocationID, "SubnetId="+zoneB))
	if !strings.Contains(out, "(InvalidConfigurationRequest)") {
		t.Fatalf("an Application Load Balancer with an Elastic IP address: got %q", out)
	}
	out = runCLIExpectError(t, awsCLI("elbv2", "create-load-balancer", "--name", "cli-map-nlb", "--type", "network",
		"--subnet-mappings", "SubnetId="+zoneA+",AllocationId=eipalloc-0000000000000000f"))
	if !strings.Contains(out, "(AllocationIdNotFound)") {
		t.Fatalf("an unknown allocation: got %q", out)
	}

	create := []string{"elbv2", "create-load-balancer", "--name", "cli-map-nlb", "--type", "network",
		"--subnet-mappings", "SubnetId=" + zoneA + ",AllocationId=" + allocationID,
		"--query", "LoadBalancers[0].[LoadBalancerArn,AvailabilityZones[0].LoadBalancerAddresses[0].AllocationId,AvailabilityZones[0].LoadBalancerAddresses[0].IpAddress]",
		"--output", "text"}
	created := strings.Fields(q(create...))
	if len(created) != 3 {
		t.Fatalf("create-load-balancer: got %q", created)
	}
	t.Cleanup(func() { _ = awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", created[0]).Run() })
	if created[1] != allocationID || created[2] != publicIP {
		t.Fatalf("the subnet's load balancer address: got %q, want %s at %s", created, allocationID, publicIP)
	}
	if again := strings.Fields(q(create...)); len(again) != 3 || again[0] != created[0] {
		t.Fatalf("the same settings again: got %q, want load balancer %s", again, created[0])
	}
	described := q("elbv2", "describe-load-balancers", "--load-balancer-arns", created[0],
		"--query", "LoadBalancers[0].AvailabilityZones[0].LoadBalancerAddresses[0].AllocationId", "--output", "text")
	if described != allocationID {
		t.Fatalf("describe-load-balancers reads back the allocation: got %q, want %s", described, allocationID)
	}
}
