package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_VpcEndpointsRoundTrip drives create-vpc-endpoint /
// describe-vpc-endpoints / delete-vpc-endpoints through the aws CLI, covering a
// gateway endpoint (route tables) and an interface endpoint (subnets + security
// groups + DNS).
func TestEC2CLI_VpcEndpointsRoundTrip(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.91.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	rt := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-route-table",
		"--vpc-id", vpc, "--query", "RouteTable.RouteTableId", "--output", "text")))
	sub := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpc, "--cidr-block", "10.91.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))
	sg := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group",
		"--group-name", "vpce-cli-sg", "--description", "vpce cli sg", "--vpc-id", vpc,
		"--query", "GroupId", "--output", "text")))

	// Gateway endpoint.
	gw := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc-endpoint",
		"--vpc-id", vpc, "--service-name", "com.amazonaws.us-east-1.s3",
		"--vpc-endpoint-type", "Gateway", "--route-table-ids", rt,
		"--query", "VpcEndpoint.VpcEndpointId", "--output", "text")))
	if gw == "" {
		t.Fatal("gateway endpoint id empty")
	}

	// Interface endpoint.
	iface := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc-endpoint",
		"--vpc-id", vpc, "--service-name", "com.amazonaws.us-east-1.ecr.api",
		"--vpc-endpoint-type", "Interface", "--subnet-ids", sub,
		"--security-group-ids", sg, "--private-dns-enabled",
		"--query", "VpcEndpoint.VpcEndpointId", "--output", "text")))
	if iface == "" {
		t.Fatal("interface endpoint id empty")
	}

	gotType := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoints",
		"--vpc-endpoint-ids", iface, "--query", "VpcEndpoints[0].VpcEndpointType", "--output", "text")))
	if gotType != "Interface" {
		t.Fatalf("interface endpoint type = %q, want Interface", gotType)
	}

	gotState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoints",
		"--vpc-endpoint-ids", gw, "--query", "VpcEndpoints[0].State", "--output", "text")))
	if gotState != "Available" {
		t.Fatalf("gateway endpoint state = %q, want Available", gotState)
	}

	// Filter by vpc-id returns both.
	count := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoints",
		"--filters", "Name=vpc-id,Values="+vpc,
		"--query", "length(VpcEndpoints)", "--output", "text")))
	if count != "2" {
		t.Fatalf("vpc-id filter returned %q endpoints, want 2", count)
	}

	// Delete both; a successful delete reports an empty Unsuccessful set.
	unsucc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "delete-vpc-endpoints",
		"--vpc-endpoint-ids", gw, iface, "--query", "length(Unsuccessful)", "--output", "text")))
	if unsucc != "0" {
		t.Fatalf("delete reported %q unsuccessful, want 0", unsucc)
	}

	gone := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoints",
		"--filters", "Name=vpc-id,Values="+vpc,
		"--query", "length(VpcEndpoints)", "--output", "text")))
	if gone != "0" {
		t.Fatalf("after delete, vpc-id filter returned %q endpoints, want 0", gone)
	}
}
