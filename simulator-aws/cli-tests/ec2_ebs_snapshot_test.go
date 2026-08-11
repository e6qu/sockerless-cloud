package aws_cli_test

import (
	"strings"
	"testing"
)

// snapshotForEBSCLI creates a volume + snapshot and returns the snapshot id.
func snapshotForEBSCLI(t *testing.T, q func(args ...string) string) string {
	t.Helper()
	vol := q("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "8",
		"--query", "VolumeId", "--output", "text")
	return q("ec2", "create-snapshot", "--volume-id", vol, "--query", "SnapshotId", "--output", "text")
}

// TestEC2CLI_EbsEncryptionByDefault round-trips the account-level EBS
// encryption-by-default flag and the default KMS key id via the aws CLI.
func TestEC2CLI_EbsEncryptionByDefault(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	if v := q("ec2", "enable-ebs-encryption-by-default",
		"--query", "EbsEncryptionByDefault", "--output", "text"); v != "True" {
		t.Fatalf("enable: got %q, want True", v)
	}
	if v := q("ec2", "get-ebs-encryption-by-default",
		"--query", "EbsEncryptionByDefault", "--output", "text"); v != "True" {
		t.Fatalf("get after enable: got %q, want True", v)
	}
	if v := q("ec2", "disable-ebs-encryption-by-default",
		"--query", "EbsEncryptionByDefault", "--output", "text"); v != "False" {
		t.Fatalf("disable: got %q, want False", v)
	}

	const cmk = "arn:aws:kms:us-east-1:000000000000:key/aaaa1111-2222-3333-4444-555566667777"
	if v := q("ec2", "modify-ebs-default-kms-key-id", "--kms-key-id", cmk,
		"--query", "KmsKeyId", "--output", "text"); v != cmk {
		t.Fatalf("modify kms key: got %q, want %q", v, cmk)
	}
	if v := q("ec2", "get-ebs-default-kms-key-id",
		"--query", "KmsKeyId", "--output", "text"); v != cmk {
		t.Fatalf("get kms key: got %q, want %q", v, cmk)
	}
	if v := q("ec2", "reset-ebs-default-kms-key-id",
		"--query", "KmsKeyId", "--output", "text"); !strings.Contains(v, "aws/ebs") {
		t.Fatalf("reset kms key: got %q, want AWS-managed key", v)
	}
}

// TestEC2CLI_FastSnapshotRestores enables, describes (settles to enabled), and
// disables fast snapshot restores via the aws CLI.
func TestEC2CLI_FastSnapshotRestores(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	snap := snapshotForEBSCLI(t, q)

	n := q("ec2", "enable-fast-snapshot-restores",
		"--source-snapshot-ids", snap, "--availability-zones", "us-east-1a", "us-east-1b",
		"--query", "length(Successful)", "--output", "text")
	if n != "2" {
		t.Fatalf("enable-fast-snapshot-restores Successful: got %q, want 2", n)
	}

	state := q("ec2", "describe-fast-snapshot-restores",
		"--filters", "Name=snapshot-id,Values="+snap,
		"--query", "FastSnapshotRestores[0].State", "--output", "text")
	if state != "enabled" {
		t.Fatalf("describe state: got %q, want enabled", state)
	}

	dis := q("ec2", "disable-fast-snapshot-restores",
		"--source-snapshot-ids", snap, "--availability-zones", "us-east-1a",
		"--query", "Successful[0].State", "--output", "text")
	if dis != "disabling" {
		t.Fatalf("disable state: got %q, want disabling", dis)
	}
}

// TestEC2CLI_SnapshotTier archives a snapshot then restores it via the aws CLI,
// and confirms a live snapshot is rejected by restore-snapshot-from-recycle-bin.
func TestEC2CLI_SnapshotTier(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	snap := snapshotForEBSCLI(t, q)

	if v := q("ec2", "modify-snapshot-tier", "--snapshot-id", snap, "--storage-tier", "archive",
		"--query", "SnapshotId", "--output", "text"); v != snap {
		t.Fatalf("modify-snapshot-tier SnapshotId: got %q, want %q", v, snap)
	}

	perm := q("ec2", "restore-snapshot-tier", "--snapshot-id", snap, "--permanent-restore",
		"--query", "IsPermanentRestore", "--output", "text")
	if perm != "True" {
		t.Fatalf("restore-snapshot-tier IsPermanentRestore: got %q, want True", perm)
	}

	dur := q("ec2", "restore-snapshot-tier", "--snapshot-id", snap, "--temporary-restore-days", "7",
		"--query", "RestoreDuration", "--output", "text")
	if dur != "7" {
		t.Fatalf("restore-snapshot-tier RestoreDuration: got %q, want 7", dur)
	}

	// A live snapshot is not in the Recycle Bin, so the restore is rejected.
	runCLIExpectError(t, awsCLI("ec2", "restore-snapshot-from-recycle-bin", "--snapshot-id", snap))
}

// TestEC2CLI_SnapshotBlockPublicAccess round-trips the account-level snapshot
// block-public-access state via the aws CLI.
func TestEC2CLI_SnapshotBlockPublicAccess(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	if v := q("ec2", "enable-snapshot-block-public-access", "--state", "block-all-sharing",
		"--query", "State", "--output", "text"); v != "block-all-sharing" {
		t.Fatalf("enable: got %q, want block-all-sharing", v)
	}
	if v := q("ec2", "get-snapshot-block-public-access-state",
		"--query", "State", "--output", "text"); v != "block-all-sharing" {
		t.Fatalf("get after enable: got %q, want block-all-sharing", v)
	}
	if v := q("ec2", "disable-snapshot-block-public-access",
		"--query", "State", "--output", "text"); v != "unblocked" {
		t.Fatalf("disable: got %q, want unblocked", v)
	}
	if v := q("ec2", "get-snapshot-block-public-access-state",
		"--query", "State", "--output", "text"); v != "unblocked" {
		t.Fatalf("get after disable: got %q, want unblocked", v)
	}
}

// TestEC2CLI_VolumeAttribute round-trips the autoEnableIO volume attribute via
// the aws CLI (describe-volume-attribute + modify-volume-attribute).
func TestEC2CLI_VolumeAttribute(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vol := q("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "8",
		"--query", "VolumeId", "--output", "text")

	if v := q("ec2", "describe-volume-attribute", "--volume-id", vol, "--attribute", "autoEnableIO",
		"--query", "AutoEnableIO.Value", "--output", "text"); v != "False" {
		t.Fatalf("autoEnableIO default: got %q, want False", v)
	}

	runCLI(t, awsCLI("ec2", "modify-volume-attribute", "--volume-id", vol, "--auto-enable-io"))

	if v := q("ec2", "describe-volume-attribute", "--volume-id", vol, "--attribute", "autoEnableIO",
		"--query", "AutoEnableIO.Value", "--output", "text"); v != "True" {
		t.Fatalf("autoEnableIO after modify: got %q, want True", v)
	}
}
