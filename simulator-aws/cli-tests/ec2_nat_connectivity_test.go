package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_NatGatewayConnectivityType drives the ConnectivityType round-trip
// (explicit "public" and the omitted default) via the aws CLI.
func TestEC2CLI_NatGatewayConnectivityType(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.82.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sub := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpc, "--cidr-block", "10.82.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))

	mkNat := func(extra ...string) string {
		eip := strings.TrimSpace(runCLI(t, awsCLI("ec2", "allocate-address",
			"--domain", "vpc", "--query", "AllocationId", "--output", "text")))
		args := append([]string{"ec2", "create-nat-gateway", "--subnet-id", sub,
			"--allocation-id", eip, "--query", "NatGateway.NatGatewayId", "--output", "text"}, extra...)
		return strings.TrimSpace(runCLI(t, awsCLI(args...)))
	}

	pub := mkNat("--connectivity-type", "public")
	got := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-nat-gateways",
		"--nat-gateway-ids", pub, "--query", "NatGateways[0].ConnectivityType", "--output", "text")))
	if got != "public" {
		t.Fatalf("explicit connectivity_type = %q, want public", got)
	}

	def := mkNat()
	gotDef := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-nat-gateways",
		"--nat-gateway-ids", def, "--query", "NatGateways[0].ConnectivityType", "--output", "text")))
	if gotDef != "public" {
		t.Fatalf("omitted connectivity_type = %q, want default public", gotDef)
	}
}
