package aws_cli_test

import (
	"strings"
	"testing"
)

func TestEC2CLI_InstanceInitiatedShutdownBehavior(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	vpc := q("ec2", "create-vpc", "--cidr-block", "10.233.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.233.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	inst := q("ec2", "run-instances", "--image-id", ec2AmazonLinuxAMI(t),
		"--instance-type", "t3.micro", "--subnet-id", subnet,
		"--instance-initiated-shutdown-behavior", "terminate",
		"--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, inst)

	behavior := func() string {
		return q("ec2", "describe-instance-attribute", "--instance-id", inst,
			"--attribute", "instanceInitiatedShutdownBehavior",
			"--query", "InstanceInitiatedShutdownBehavior.Value", "--output", "text")
	}
	if got := behavior(); got != "terminate" {
		t.Fatalf("launched with terminate, describe-instance-attribute says %q", got)
	}
	runCLI(t, awsCLI("ec2", "modify-instance-attribute", "--instance-id", inst,
		"--instance-initiated-shutdown-behavior", "Value=stop"))
	if got := behavior(); got != "stop" {
		t.Fatalf("modified to stop, describe-instance-attribute says %q", got)
	}
}

func TestEC2CLI_ImagesWithoutARootFilesystemOrSnapshotAreRefused(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	out := runCLIExpectError(t, awsCLI("ec2", "register-image", "--name", "cli-nowhere-ami",
		"--root-device-name", "/dev/sda1",
		"--block-device-mappings", "DeviceName=/dev/sda1,Ebs={SnapshotId=snap-0123456789abcdef0}"))
	if !strings.Contains(out, "InvalidSnapshot.NotFound") {
		t.Fatalf("register-image over a missing snapshot: got %q, want InvalidSnapshot.NotFound", out)
	}

	ami := registerCLIAMI(t, q, "cli-mapping-only-ami")
	vpc := q("ec2", "create-vpc", "--cidr-block", "10.236.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.236.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	out = runCLIExpectError(t, awsCLI("ec2", "run-instances", "--image-id", ami,
		"--instance-type", "t3.micro", "--subnet-id", subnet))
	if !strings.Contains(out, "InvalidAMIID.Unavailable") {
		t.Fatalf("run-instances of an AMI with no root filesystem: got %q, want InvalidAMIID.Unavailable", out)
	}
}
