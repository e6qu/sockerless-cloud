package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2InstanceKnobFidelityCLI covers the RunInstances knobs + Instance
// response fields and the ModifyInstanceAttribute persist via the aws CLI.
func TestEC2InstanceKnobFidelityCLI(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.132.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.132.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")

	instID := q("ec2", "run-instances", "--image-id", "ami-12345678", "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--key-name", "cli-key", "--ebs-optimized",
		"--metadata-options", "HttpTokens=required,HttpPutResponseHopLimit=2",
		"--query", "Instances[0].InstanceId", "--output", "text")

	out := q("ec2", "describe-instances", "--instance-ids", instID,
		"--query", "Reservations[0].Instances[0].[KeyName,EbsOptimized,MetadataOptions.HttpTokens,SourceDestCheck]",
		"--output", "text")
	f := strings.Fields(out)
	if len(f) != 4 || f[0] != "cli-key" || f[1] != "True" || f[2] != "required" || f[3] != "True" {
		t.Fatalf("instance knobs: got %q, want 'cli-key True required True'", out)
	}

	// ModifyInstanceAttribute must persist (was a no-op).
	runCLI(t, awsCLI("ec2", "modify-instance-attribute", "--instance-id", instID, "--no-source-dest-check"))
	if v := q("ec2", "describe-instances", "--instance-ids", instID,
		"--query", "Reservations[0].Instances[0].SourceDestCheck", "--output", "text"); v != "False" {
		t.Fatalf("source_dest_check did not persist as false: got %q", v)
	}
}
