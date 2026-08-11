package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_VpcAndSgFilters drives the DescribeVpcs / DescribeSecurityGroups
// filter fixes via the aws CLI.
func TestEC2CLI_VpcAndSgFilters(t *testing.T) {
	vpcA := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.65.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	vpcB := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.66.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))

	// vpc-id filter returns exactly the requested VPC.
	gotID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpcs",
		"--filters", "Name=vpc-id,Values="+vpcB,
		"--query", "Vpcs[*].VpcId", "--output", "text")))
	if gotID != vpcB {
		t.Fatalf("describe-vpcs vpc-id filter returned %q, want %q", gotID, vpcB)
	}

	// CidrBlockAssociationSet carries the primary CIDR.
	assocCidr := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpcs",
		"--vpc-ids", vpcB,
		"--query", "Vpcs[0].CidrBlockAssociationSet[0].CidrBlock", "--output", "text")))
	if assocCidr != "10.66.0.0/16" {
		t.Fatalf("CidrBlockAssociationSet CIDR = %q, want 10.66.0.0/16", assocCidr)
	}

	runCLI(t, awsCLI("ec2", "create-security-group",
		"--group-name", "cli-filter-sg-a", "--description", "a", "--vpc-id", vpcA))
	runCLI(t, awsCLI("ec2", "create-security-group",
		"--group-name", "cli-filter-sg-b", "--description", "b", "--vpc-id", vpcB))

	sgVpcs := runCLI(t, awsCLI("ec2", "describe-security-groups",
		"--filters", "Name=vpc-id,Values="+vpcA,
		"--query", "SecurityGroups[*].VpcId", "--output", "text"))
	for _, field := range strings.Fields(sgVpcs) {
		if field != vpcA {
			t.Fatalf("describe-security-groups vpc-id filter leaked SG from %q (want only %q)", field, vpcA)
		}
	}
	if strings.TrimSpace(sgVpcs) == "" {
		t.Fatal("expected at least the VPC-A security group")
	}
}
