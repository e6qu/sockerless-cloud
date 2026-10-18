package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2KeyPairsImagesMetadataCLI covers key pairs, DescribeImages filters, and
// ModifyInstanceMetadataOptions via the aws CLI.
func TestEC2KeyPairsImagesMetadataCLI(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	// Key pair create -> describe -> delete.
	fp := q("ec2", "create-key-pair", "--key-name", "cli-kp", "--query", "KeyFingerprint", "--output", "text")
	if fp == "" {
		t.Fatal("create-key-pair returned no fingerprint")
	}
	if n := q("ec2", "describe-key-pairs", "--key-names", "cli-kp",
		"--query", "length(KeyPairs)", "--output", "text"); n != "1" {
		t.Fatalf("describe-key-pairs after create: got %q, want 1", n)
	}
	runCLI(t, awsCLI("ec2", "delete-key-pair", "--key-name", "cli-kp"))
	if n := q("ec2", "describe-key-pairs", "--filters", "Name=key-name,Values=cli-kp",
		"--query", "length(KeyPairs)", "--output", "text"); n != "0" {
		t.Fatalf("describe-key-pairs after delete: got %q, want 0", n)
	}

	// DescribeImages must honour the name + architecture filters.
	out := q("ec2", "describe-images", "--owners", "amazon",
		"--filters", "Name=name,Values=al2023-ami-2023*", "Name=architecture,Values=arm64",
		"--query", "Images[0].[Name,Architecture,ImageOwnerAlias]", "--output", "text")
	if f := strings.Fields(out); len(f) != 3 || !strings.HasPrefix(f[0], "al2023-ami-2023.") || !strings.HasSuffix(f[0], "-arm64") || f[1] != "arm64" || f[2] != "amazon" {
		t.Fatalf("describe-images filters: got %q, want the Amazon Linux 2023 arm64 AMI", out)
	}
	if n := q("ec2", "describe-images", "--owners", "136693071363",
		"--filters", "Name=name,Values=debian-12-*", "--query", "length(Images)", "--output", "text"); n != "2" {
		t.Fatalf("describe-images lists Debian 12 for amd64 and arm64, got %q", n)
	}

	// ModifyInstanceMetadataOptions in place.
	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.141.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.141.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	instID := q("ec2", "run-instances", "--image-id", ec2AmazonLinuxAMI(t), "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, instID)
	runCLI(t, awsCLI("ec2", "modify-instance-metadata-options", "--instance-id", instID,
		"--http-tokens", "required", "--http-put-response-hop-limit", "3"))
	if v := q("ec2", "describe-instances", "--instance-ids", instID,
		"--query", "Reservations[0].Instances[0].MetadataOptions.HttpTokens", "--output", "text"); v != "required" {
		t.Fatalf("metadata http_tokens after modify: got %q, want required", v)
	}
}
