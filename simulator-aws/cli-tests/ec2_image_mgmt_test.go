package aws_cli_test

import (
	"strings"
	"testing"
)

// registerCLIAMI registers a user AMI via the aws CLI and returns its id.
func registerCLIAMI(t *testing.T, q func(args ...string) string, name string) string {
	t.Helper()
	ami := q("ec2", "register-image", "--name", name, "--architecture", "x86_64",
		"--root-device-name", "/dev/sda1",
		"--block-device-mappings", "DeviceName=/dev/sda1,Ebs={SnapshotId=snap-0123456789abcdef0,VolumeSize=8}",
		"--query", "ImageId", "--output", "text")
	if ami == "" {
		t.Fatal("register-image returned empty ImageId")
	}
	return ami
}

// TestEC2CLI_FastLaunch covers enable-fast-launch (state enabled, config echoed),
// describe-fast-launch-images, and disable-fast-launch via the aws CLI.
func TestEC2CLI_FastLaunch(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	ami := registerCLIAMI(t, q, "cli-fast-launch-ami")

	en := q("ec2", "enable-fast-launch", "--image-id", ami, "--max-parallel-launches", "8",
		"--snapshot-configuration", "TargetResourceCount=10",
		"--query", "[ImageId,State,ResourceType]", "--output", "text")
	if f := strings.Fields(en); len(f) != 3 || f[0] != ami || f[1] != "enabled" || f[2] != "snapshot" {
		t.Fatalf("enable-fast-launch: got %q, want '%s enabled snapshot'", en, ami)
	}

	desc := q("ec2", "describe-fast-launch-images", "--image-ids", ami,
		"--query", "FastLaunchImages[0].[ImageId,State,SnapshotConfiguration.TargetResourceCount]", "--output", "text")
	if f := strings.Fields(desc); len(f) != 3 || f[0] != ami || f[1] != "enabled" || f[2] != "10" {
		t.Fatalf("describe-fast-launch-images: got %q, want '%s enabled 10'", desc, ami)
	}

	dis := q("ec2", "disable-fast-launch", "--image-id", ami,
		"--query", "ImageId", "--output", "text")
	if dis != ami {
		t.Fatalf("disable-fast-launch ImageId: got %q, want %q", dis, ami)
	}
	gone := q("ec2", "describe-fast-launch-images", "--image-ids", ami,
		"--query", "length(FastLaunchImages)", "--output", "text")
	if gone != "0" {
		t.Fatalf("disabled fast-launch must leave the describe set, got count %q", gone)
	}
}

// TestEC2CLI_ImageDeprecation covers enable-image-deprecation and
// disable-image-deprecation (idempotent) via the aws CLI.
func TestEC2CLI_ImageDeprecation(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	ami := registerCLIAMI(t, q, "cli-deprecation-ami")

	en := q("ec2", "enable-image-deprecation", "--image-id", ami,
		"--deprecate-at", "2030-01-01T00:00:00Z", "--query", "Return", "--output", "text")
	if en != "True" {
		t.Fatalf("enable-image-deprecation Return: got %q, want True", en)
	}
	dis := q("ec2", "disable-image-deprecation", "--image-id", ami,
		"--query", "Return", "--output", "text")
	if dis != "True" {
		t.Fatalf("disable-image-deprecation Return: got %q, want True", dis)
	}
}

// TestEC2CLI_LaunchPermissionExportRecycleBin covers cancel-image-launch-permission,
// describe-export-image-tasks / describe-import-image-tasks (honest-empty read
// sides), and list-images-in-recycle-bin (honest-empty) via the aws CLI.
func TestEC2CLI_LaunchPermissionExportRecycleBin(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	ami := registerCLIAMI(t, q, "cli-cancel-perm-ami")

	cancel := q("ec2", "cancel-image-launch-permission", "--image-id", ami,
		"--query", "Return", "--output", "text")
	if cancel != "True" {
		t.Fatalf("cancel-image-launch-permission Return: got %q, want True", cancel)
	}

	// The export/import task read sides accept the call and return well-shaped
	// lists (typically empty in a fresh sim).
	if v := q("ec2", "describe-export-image-tasks",
		"--query", "ExportImageTasks", "--output", "json"); v == "" {
		t.Fatal("describe-export-image-tasks returned empty output")
	}
	if v := q("ec2", "describe-import-image-tasks",
		"--query", "ImportImageTasks", "--output", "json"); v == "" {
		t.Fatal("describe-import-image-tasks returned empty output")
	}

	// No AMI has been sent to the Recycle Bin -> honest-empty list.
	n := q("ec2", "list-images-in-recycle-bin", "--query", "length(Images)", "--output", "text")
	if n != "0" {
		t.Fatalf("list-images-in-recycle-bin: got count %q, want 0", n)
	}
}
