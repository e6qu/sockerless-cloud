package aws_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEC2InstanceCloudConfigCLI launches an Amazon Linux 2023 instance with a
// #cloud-config user data file and a key pair, and reads back through Run
// Command the file cloud-init wrote, the command it ran and the key it gave
// the default user ec2-user.
func TestEC2InstanceCloudConfigCLI(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.218.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.218.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	keyName := "cli-cloud-init-" + strings.TrimPrefix(vpcID, "vpc-")
	q("ec2", "create-key-pair", "--key-name", keyName, "--key-type", "ed25519", "--query", "KeyPairId", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("ec2", "delete-key-pair", "--key-name", keyName).Run() })
	publicKey := q("ec2", "describe-key-pairs", "--key-names", keyName, "--include-public-key",
		"--query", "KeyPairs[0].PublicKey", "--output", "text")

	userData := filepath.Join(tmpDir, "cloud-config-"+strings.TrimPrefix(vpcID, "vpc-")+".yaml")
	cloudConfig := "#cloud-config\n" +
		"write_files:\n" +
		"  - path: /etc/cli/marker\n" +
		"    content: from the cli\n" +
		"runcmd:\n" +
		"  - [sh, -c, 'echo \"runcmd $(id -un)\" > /var/tmp/runcmd']\n"
	if err := os.WriteFile(userData, []byte(cloudConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	instanceID := q("ec2", "run-instances", "--image-id", ec2AmazonLinuxAMI(t), "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--key-name", keyName, "--user-data", "file://"+userData,
		"--iam-instance-profile", "Name="+ssmManagedInstanceProfileCLI(t),
		"--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, instanceID)
	runCLI(t, awsCLI("ec2", "wait", "instance-running", "--instance-ids", instanceID))

	if got := strings.TrimSpace(ssmShellCLI(t, instanceID, "cat /etc/cli/marker")); got != "from the cli" {
		t.Fatalf("write_files: %q", got)
	}
	if got := strings.TrimSpace(ssmShellCLI(t, instanceID, "cat /var/tmp/runcmd")); got != "runcmd root" {
		t.Fatalf("runcmd: %q", got)
	}
	if got := strings.TrimSpace(ssmShellCLI(t, instanceID, "cat /home/ec2-user/.ssh/authorized_keys")); got != publicKey {
		t.Fatalf("ec2-user's key = %q, want the key pair's %q", got, publicKey)
	}
}
