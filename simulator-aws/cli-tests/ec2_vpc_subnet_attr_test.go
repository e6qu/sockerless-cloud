package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2SubnetVpcAttributeFidelityCLI covers the ModifySubnetAttribute /
// ModifyVpcAttribute silent-no-op fixes and the new computed subnet fields via
// the aws CLI.
func TestEC2SubnetVpcAttributeFidelityCLI(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.98.0.0/16", "--instance-tenancy", "default",
		"--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}

	// instance_tenancy + dhcp_options_id round-trip on describe.
	out := runCLI(t, awsCLI("ec2", "describe-vpcs", "--vpc-ids", vpcID,
		"--query", "Vpcs[0].[InstanceTenancy,DhcpOptionsId]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != "default" || f[1] != "default" {
		t.Fatalf("vpc tenancy/dhcp: got %q, want 'default default'", strings.TrimSpace(out))
	}

	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.98.1.0/24", "--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId", "--output", "text")))

	// availableIpAddressCount computed (256-5 for a /24).
	if c := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-subnets", "--subnet-ids", subnetID,
		"--query", "Subnets[0].AvailableIpAddressCount", "--output", "text"))); c != "251" {
		t.Fatalf("available ip count: got %q, want 251", c)
	}

	// ModifySubnetAttribute must persist (previously silently dropped).
	runCLI(t, awsCLI("ec2", "modify-subnet-attribute", "--subnet-id", subnetID,
		"--enable-dns64"))
	if v := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-subnets", "--subnet-ids", subnetID,
		"--query", "Subnets[0].EnableDns64", "--output", "text"))); v != "True" {
		t.Fatalf("enable_dns64 not persisted: got %q", v)
	}

	// ModifyVpcAttribute enable_network_address_usage_metrics must persist.
	runCLI(t, awsCLI("ec2", "modify-vpc-attribute", "--vpc-id", vpcID,
		"--enable-network-address-usage-metrics"))
	if v := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-attribute", "--vpc-id", vpcID,
		"--attribute", "enableNetworkAddressUsageMetrics",
		"--query", "EnableNetworkAddressUsageMetrics.Value", "--output", "text"))); v != "True" {
		t.Fatalf("enable_network_address_usage_metrics not persisted: got %q", v)
	}
}
