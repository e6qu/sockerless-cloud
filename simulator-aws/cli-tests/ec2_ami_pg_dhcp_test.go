package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_AMIPlacementDhcp covers the AMI, placement-group, and DHCP-option
// control planes via the aws CLI: CreateImage/RegisterImage/CopyImage +
// DescribeImages + DeregisterImage; CreatePlacementGroup + DescribePlacementGroups
// + DeletePlacementGroup; CreateDhcpOptions + DescribeDhcpOptions +
// AssociateDhcpOptions + DeleteDhcpOptions.
func TestEC2CLI_AMIPlacementDhcp(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	// --- AMIs ---
	vpc := q("ec2", "create-vpc", "--cidr-block", "10.150.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.150.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	inst := q("ec2", "run-instances", "--image-id", "ami-12345678", "--instance-type", "t3.micro",
		"--subnet-id", subnet, "--query", "Instances[0].InstanceId", "--output", "text")

	ami := q("ec2", "create-image", "--instance-id", inst, "--name", "cli-golden-ami",
		"--description", "cli snapshot", "--query", "ImageId", "--output", "text")
	if ami == "" {
		t.Fatal("create-image returned empty ImageId")
	}
	got := q("ec2", "describe-images", "--image-ids", ami,
		"--query", "Images[0].[Name,State,RootDeviceType]", "--output", "text")
	if f := strings.Fields(got); len(f) != 3 || f[0] != "cli-golden-ami" || f[1] != "available" || f[2] != "ebs" {
		t.Fatalf("describe-images: got %q, want 'cli-golden-ami available ebs'", got)
	}

	reg := q("ec2", "register-image", "--name", "cli-registered-ami", "--architecture", "arm64",
		"--root-device-name", "/dev/xvda",
		"--block-device-mappings", "DeviceName=/dev/xvda,Ebs={SnapshotId=snap-0123456789abcdef0,VolumeSize=20}",
		"--query", "ImageId", "--output", "text")
	if v := q("ec2", "describe-images", "--image-ids", reg,
		"--query", "Images[0].Architecture", "--output", "text"); v != "arm64" {
		t.Fatalf("registered AMI architecture: got %q, want arm64", v)
	}

	cp := q("ec2", "copy-image", "--source-image-id", ami, "--source-region", "us-east-1",
		"--name", "cli-copied-ami", "--query", "ImageId", "--output", "text")
	if cp == ami || cp == "" {
		t.Fatalf("copy-image must return a fresh id, got %q (source %q)", cp, ami)
	}

	runCLI(t, awsCLI("ec2", "deregister-image", "--image-id", ami))
	n := q("ec2", "describe-images", "--image-ids", reg, "--query", "length(Images)", "--output", "text")
	if n != "1" {
		t.Fatalf("registered AMI must still be present after deregistering the other, got count %q", n)
	}

	// --- Placement groups ---
	runCLI(t, awsCLI("ec2", "create-placement-group", "--group-name", "cli-pg",
		"--strategy", "spread"))
	pg := q("ec2", "describe-placement-groups", "--group-names", "cli-pg",
		"--query", "PlacementGroups[0].[GroupName,State,Strategy]", "--output", "text")
	if f := strings.Fields(pg); len(f) != 3 || f[0] != "cli-pg" || f[1] != "available" || f[2] != "spread" {
		t.Fatalf("describe-placement-groups: got %q, want 'cli-pg available spread'", pg)
	}
	runCLI(t, awsCLI("ec2", "delete-placement-group", "--group-name", "cli-pg"))
	gone := q("ec2", "describe-placement-groups",
		"--query", "length(PlacementGroups[?GroupName=='cli-pg'])", "--output", "text")
	if gone != "0" {
		t.Fatalf("deleted placement group must be gone, got count %q", gone)
	}

	// --- DHCP options ---
	dopt := q("ec2", "create-dhcp-options",
		"--dhcp-configurations", "Key=domain-name,Values=cli.internal",
		"Key=domain-name-servers,Values=10.0.0.2,8.8.8.8",
		"--query", "DhcpOptions.DhcpOptionsId", "--output", "text")
	if dopt == "" {
		t.Fatal("create-dhcp-options returned empty id")
	}
	dn := q("ec2", "describe-dhcp-options", "--dhcp-options-ids", dopt,
		"--query", "DhcpOptions[0].DhcpConfigurations[?Key=='domain-name'] | [0].Values[0].Value", "--output", "text")
	if dn != "cli.internal" {
		t.Fatalf("dhcp domain-name: got %q, want cli.internal", dn)
	}

	dvpc := q("ec2", "create-vpc", "--cidr-block", "10.151.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	runCLI(t, awsCLI("ec2", "associate-dhcp-options", "--dhcp-options-id", dopt, "--vpc-id", dvpc))
	assoc := q("ec2", "describe-vpcs", "--vpc-ids", dvpc,
		"--query", "Vpcs[0].DhcpOptionsId", "--output", "text")
	if assoc != dopt {
		t.Fatalf("VPC dhcpOptionsId: got %q, want %q", assoc, dopt)
	}
	// Revert to default so the set can be deleted.
	runCLI(t, awsCLI("ec2", "associate-dhcp-options", "--dhcp-options-id", "default", "--vpc-id", dvpc))
	runCLI(t, awsCLI("ec2", "delete-dhcp-options", "--dhcp-options-id", dopt))

	// Tolerant cleanup.
	_ = awsCLI("ec2", "deregister-image", "--image-id", reg).Run()
	_ = awsCLI("ec2", "deregister-image", "--image-id", cp).Run()
}
