package aws_cli_test

import (
	"strings"
	"testing"
)

// ec2LaunchInstanceCLI creates a VPC + /24 subnet inside it and runs one
// instance, returning the instance ID for the instance-attribute ops.
func ec2LaunchInstanceCLI(t *testing.T, q func(args ...string) string, base string) string {
	t.Helper()
	vpc := q("ec2", "create-vpc", "--cidr-block", base+".0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", base+".1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	inst := q("ec2", "run-instances", "--image-id", "ami-12345678",
		"--instance-type", "t3.micro", "--subnet-id", subnet,
		"--query", "Instances[0].InstanceId", "--output", "text")
	if inst == "" {
		t.Fatal("run-instances returned empty instance id")
	}
	return inst
}

// TestEC2CLI_IamInstanceProfileAssociation covers the IAM instance-profile
// association lifecycle via the aws CLI: associate over a running instance,
// describe read-back + instance-id filter, replace, and disassociate.
func TestEC2CLI_IamInstanceProfileAssociation(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	inst := ec2LaunchInstanceCLI(t, q, "10.210")

	assocID := q("ec2", "associate-iam-instance-profile",
		"--instance-id", inst, "--iam-instance-profile", "Name=my-role",
		"--query", "IamInstanceProfileAssociation.AssociationId", "--output", "text")
	if assocID == "" {
		t.Fatal("associate-iam-instance-profile returned empty association id")
	}

	got := q("ec2", "describe-iam-instance-profile-associations",
		"--association-ids", assocID,
		"--query", "IamInstanceProfileAssociations[0].[InstanceId,State]", "--output", "text")
	if f := strings.Fields(got); len(f) != 2 || f[0] != inst || f[1] != "associated" {
		t.Fatalf("describe assoc: got %q, want '%s associated'", got, inst)
	}

	byInst := q("ec2", "describe-iam-instance-profile-associations",
		"--filters", "Name=instance-id,Values="+inst,
		"--query", "IamInstanceProfileAssociations[0].AssociationId", "--output", "text")
	if byInst != assocID {
		t.Fatalf("instance-id filter: got %q, want %q", byInst, assocID)
	}

	runCLI(t, awsCLI("ec2", "replace-iam-instance-profile-association",
		"--association-id", assocID, "--iam-instance-profile", "Name=other-role"))

	runCLI(t, awsCLI("ec2", "disassociate-iam-instance-profile", "--association-id", assocID))
	gone := q("ec2", "describe-iam-instance-profile-associations",
		"--query", "length(IamInstanceProfileAssociations[?AssociationId=='"+assocID+"'])", "--output", "text")
	if gone != "0" {
		t.Fatalf("disassociated association must be gone, got count %q", gone)
	}
}

// TestEC2CLI_InstanceCreditSpecification covers the credit-option modify/read
// plus the CPU-options modification via the aws CLI.
func TestEC2CLI_InstanceCreditSpecification(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	inst := ec2LaunchInstanceCLI(t, q, "10.211")

	if v := q("ec2", "describe-instance-credit-specifications", "--instance-ids", inst,
		"--query", "InstanceCreditSpecifications[0].CpuCredits", "--output", "text"); v != "standard" {
		t.Fatalf("default credit option: got %q, want standard", v)
	}

	runCLI(t, awsCLI("ec2", "modify-instance-credit-specification",
		"--instance-credit-specifications", "InstanceId="+inst+",CpuCredits=unlimited"))

	if v := q("ec2", "describe-instance-credit-specifications", "--instance-ids", inst,
		"--query", "InstanceCreditSpecifications[0].CpuCredits", "--output", "text"); v != "unlimited" {
		t.Fatalf("modified credit option: got %q, want unlimited", v)
	}

	cpu := q("ec2", "modify-instance-cpu-options", "--instance-id", inst,
		"--core-count", "2", "--threads-per-core", "1",
		"--query", "[CoreCount,ThreadsPerCore]", "--output", "text")
	if f := strings.Fields(cpu); len(f) != 2 || f[0] != "2" || f[1] != "1" {
		t.Fatalf("modify-instance-cpu-options: got %q, want '2 1'", cpu)
	}
}

// TestEC2CLI_InstancePlacement covers the placement / maintenance / network-
// performance / event-start-time modifications via the aws CLI.
func TestEC2CLI_InstancePlacement(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	inst := ec2LaunchInstanceCLI(t, q, "10.212")

	if v := q("ec2", "modify-instance-placement", "--instance-id", inst, "--tenancy", "default",
		"--query", "Return", "--output", "text"); v != "True" {
		t.Fatalf("modify-instance-placement: got %q, want True", v)
	}

	if v := q("ec2", "modify-instance-maintenance-options", "--instance-id", inst,
		"--auto-recovery", "disabled",
		"--query", "AutoRecovery", "--output", "text"); v != "disabled" {
		t.Fatalf("modify-instance-maintenance-options: got %q, want disabled", v)
	}

	if v := q("ec2", "modify-instance-network-performance-options", "--instance-id", inst,
		"--bandwidth-weighting", "vpc-1",
		"--query", "BandwidthWeighting", "--output", "text"); v != "vpc-1" {
		t.Fatalf("modify-instance-network-performance-options: got %q, want vpc-1", v)
	}

	if v := q("ec2", "modify-instance-event-start-time", "--instance-id", inst,
		"--instance-event-id", "instance-event-0abcd1234", "--not-before", "2030-01-01T00:00:00Z",
		"--query", "Event.InstanceEventId", "--output", "text"); v != "instance-event-0abcd1234" {
		t.Fatalf("modify-instance-event-start-time: got %q", v)
	}
}

// TestEC2CLI_ConsoleOutput covers the console / screenshot / password / TPM /
// UEFI reads plus topology and image-metadata via the aws CLI.
func TestEC2CLI_ConsoleOutput(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	inst := ec2LaunchInstanceCLI(t, q, "10.213")

	if v := q("ec2", "get-console-output", "--instance-id", inst,
		"--query", "InstanceId", "--output", "text"); v != inst {
		t.Fatalf("get-console-output: got %q, want %q", v, inst)
	}
	if v := q("ec2", "get-console-screenshot", "--instance-id", inst,
		"--query", "InstanceId", "--output", "text"); v != inst {
		t.Fatalf("get-console-screenshot: got %q, want %q", v, inst)
	}
	if v := q("ec2", "get-password-data", "--instance-id", inst,
		"--query", "InstanceId", "--output", "text"); v != inst {
		t.Fatalf("get-password-data: got %q, want %q", v, inst)
	}
	if v := q("ec2", "get-instance-tpm-ek-pub", "--instance-id", inst,
		"--key-type", "rsa-2048", "--key-format", "der",
		"--query", "InstanceId", "--output", "text"); v != inst {
		t.Fatalf("get-instance-tpm-ek-pub: got %q, want %q", v, inst)
	}
	if v := q("ec2", "get-instance-uefi-data", "--instance-id", inst,
		"--query", "InstanceId", "--output", "text"); v != inst {
		t.Fatalf("get-instance-uefi-data: got %q, want %q", v, inst)
	}

	if v := q("ec2", "describe-instance-topology", "--instance-ids", inst,
		"--query", "Instances[0].InstanceId", "--output", "text"); v != inst {
		t.Fatalf("describe-instance-topology: got %q, want %q", v, inst)
	}
	if v := q("ec2", "describe-instance-image-metadata", "--instance-ids", inst,
		"--query", "InstanceImageMetadata[0].[InstanceId,ImageMetadata.ImageId]", "--output", "text"); !strings.Contains(v, inst) {
		t.Fatalf("describe-instance-image-metadata: got %q, want to contain %q", v, inst)
	}
}

// TestEC2CLI_BundleAndExport covers bundle-instance, confirm-product-instance,
// create-instance-export-task, and the instance-requirements type lookup via
// the aws CLI.
//
// import-instance and the SQL Server High Availability ops
// (describe-instance-sql-ha-states / -history-states, enable/disable-instance-
// sql-ha-standby-detections) are absent from aws CLI 2.26.6, so they are
// exercised via the SDK test (which satisfies the contract hook).
func TestEC2CLI_BundleAndExport(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	inst := ec2LaunchInstanceCLI(t, q, "10.214")

	if v := q("ec2", "bundle-instance", "--instance-id", inst,
		"--storage", "S3={Bucket=my-bucket,Prefix=ami/}",
		"--query", "BundleTask.InstanceId", "--output", "text"); v != inst {
		t.Fatalf("bundle-instance: got %q, want %q", v, inst)
	}

	if v := q("ec2", "confirm-product-instance", "--instance-id", inst,
		"--product-code", "774F4FF8",
		"--query", "Return", "--output", "text"); v != "False" {
		t.Fatalf("confirm-product-instance: got %q, want False", v)
	}

	if v := q("ec2", "create-instance-export-task", "--instance-id", inst,
		"--target-environment", "vmware",
		"--export-to-s3-task", "DiskImageFormat=VMDK,S3Bucket=export-bucket",
		"--query", "ExportTask.InstanceExportDetails.InstanceId", "--output", "text"); v != inst {
		t.Fatalf("create-instance-export-task: got %q, want %q", v, inst)
	}

	types := q("ec2", "get-instance-types-from-instance-requirements",
		"--architecture-types", "x86_64", "--virtualization-types", "hvm",
		"--instance-requirements", "VCpuCount={Min=2,Max=2},MemoryMiB={Min=1024,Max=8192}",
		"--query", "length(InstanceTypes)", "--output", "text")
	if types == "" || types == "0" {
		t.Fatalf("get-instance-types-from-instance-requirements: got %q matches, want >0", types)
	}
}
