package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEC2NetworkInterfaceCLI(t *testing.T) {
	t.Parallel()
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.78.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.78.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))

	eniID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-interface",
		"--subnet-id", subnetID, "--description", "cli-eni",
		"--query", "NetworkInterface.NetworkInterfaceId", "--output", "text")))
	if !strings.HasPrefix(eniID, "eni-") {
		t.Fatalf("expected an eni- id, got %q", eniID)
	}

	// NAT instances disable source/dest check.
	runCLI(t, awsCLI("ec2", "modify-network-interface-attribute",
		"--network-interface-id", eniID, "--no-source-dest-check"))

	sdc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-network-interfaces",
		"--network-interface-ids", eniID,
		"--query", "NetworkInterfaces[0].SourceDestCheck", "--output", "text")))
	if sdc != "False" {
		t.Fatalf("expected SourceDestCheck False after --no-source-dest-check, got %q", sdc)
	}

	deleteBlocked := runCLIExpectError(t, awsCLI("ec2", "delete-subnet", "--subnet-id", subnetID))
	assert.Contains(t, deleteBlocked, "DependencyViolation")
	runCLI(t, awsCLI("ec2", "delete-network-interface", "--network-interface-id", eniID))
	runCLI(t, awsCLI("ec2", "delete-subnet", "--subnet-id", subnetID))
	runCLI(t, awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID))
}
