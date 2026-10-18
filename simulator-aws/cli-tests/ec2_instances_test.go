package aws_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ssmShellCLI runs a shell command on the instance through Run Command and
// returns its standard output.
func ssmShellCLI(t *testing.T, instanceID, command string) string {
	t.Helper()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	commandID := q("ssm", "send-command", "--document-name", "AWS-RunShellScript",
		"--instance-ids", instanceID, "--parameters", "commands="+command,
		"--query", "Command.CommandId", "--output", "text")
	runCLI(t, awsCLI("ssm", "wait", "command-executed", "--command-id", commandID, "--instance-id", instanceID))
	status := q("ssm", "get-command-invocation", "--command-id", commandID, "--instance-id", instanceID,
		"--query", "[Status,StandardErrorContent]", "--output", "text")
	if !strings.HasPrefix(status, "Success") {
		t.Fatalf("command %q on %s: %s", command, instanceID, status)
	}
	return runCLI(t, awsCLI("ssm", "get-command-invocation", "--command-id", commandID, "--instance-id", instanceID,
		"--query", "StandardOutputContent", "--output", "text"))
}

// TestEC2InstanceUserDataAndImageCLI launches an instance from the published
// Amazon Linux 2023 AMI with a user data script, reads the script's marker
// back through Run Command and the console, stops and starts the instance
// on the same root disk, and captures it into an AMI another instance boots.
func TestEC2InstanceUserDataAndImageCLI(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.216.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.216.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	userData := filepath.Join(tmpDir, "user-data-"+strings.TrimPrefix(vpcID, "vpc-")+".sh")
	if err := os.WriteFile(userData, []byte("#!/bin/sh\necho \"cli user data ran on $(uname -n)\"\necho golden > /var/tmp/marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ami := ec2AmazonLinuxAMI(t)
	instanceID := q("ec2", "run-instances", "--image-id", ami, "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--user-data", "file://"+userData,
		"--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, instanceID)
	runCLI(t, awsCLI("ec2", "wait", "instance-running", "--instance-ids", instanceID))

	privateIP := q("ec2", "describe-instances", "--instance-ids", instanceID,
		"--query", "Reservations[0].Instances[0].PrivateIpAddress", "--output", "text")
	hostname := "ip-" + strings.ReplaceAll(privateIP, ".", "-")
	if got := strings.TrimSpace(ssmShellCLI(t, instanceID, "uname -n")); got != hostname {
		t.Fatalf("hostname = %q, want %q", got, hostname)
	}
	if got := strings.TrimSpace(ssmShellCLI(t, instanceID, "cat /var/tmp/marker")); got != "golden" {
		t.Fatalf("user data marker = %q, want golden", got)
	}
	console := q("ec2", "get-console-output", "--instance-id", instanceID, "--query", "Output", "--output", "text")
	if !strings.Contains(console, "cli user data ran on "+hostname) {
		t.Fatalf("console output lacks the user data's line: %q", console)
	}

	runCLI(t, awsCLI("ec2", "stop-instances", "--instance-ids", instanceID))
	runCLI(t, awsCLI("ec2", "wait", "instance-stopped", "--instance-ids", instanceID))
	runCLI(t, awsCLI("ec2", "start-instances", "--instance-ids", instanceID))
	runCLI(t, awsCLI("ec2", "wait", "instance-running", "--instance-ids", instanceID))
	if got := strings.TrimSpace(ssmShellCLI(t, instanceID, "cat /var/tmp/marker")); got != "golden" {
		t.Fatalf("marker after stop and start = %q, want golden", got)
	}

	imageID := q("ec2", "create-image", "--instance-id", instanceID, "--name", "cli-captured-"+instanceID,
		"--query", "ImageId", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("ec2", "deregister-image", "--image-id", imageID).Run() })
	runCLI(t, awsCLI("ec2", "wait", "image-available", "--image-ids", imageID))
	clone := q("ec2", "run-instances", "--image-id", imageID, "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, clone)
	runCLI(t, awsCLI("ec2", "wait", "instance-running", "--instance-ids", clone))
	if got := strings.TrimSpace(ssmShellCLI(t, clone, "cat /var/tmp/marker")); got != "golden" {
		t.Fatalf("marker on the instance launched from the captured AMI = %q, want golden", got)
	}
}
