package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_FpgaImageLifecycle covers the Amazon FPGA Image (AFI) control
// plane via the aws CLI: create-fpga-image, describe-fpga-images read-back,
// copy-fpga-image, the attribute set (describe/modify load-permission/reset),
// and delete-fpga-image.
func TestEC2CLI_FpgaImageLifecycle(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	afi := q("ec2", "create-fpga-image",
		"--input-storage-location", "Bucket=my-dcp-bucket,Key=dcp.tar",
		"--name", "cli-afi", "--description", "cli fpga image",
		"--query", "FpgaImageId", "--output", "text")
	if afi == "" {
		t.Fatal("create-fpga-image returned empty FpgaImageId")
	}
	defer runCLI(t, awsCLI("ec2", "delete-fpga-image", "--fpga-image-id", afi))

	if v := q("ec2", "describe-fpga-images", "--fpga-image-ids", afi,
		"--query", "FpgaImages[0].Name", "--output", "text"); v != "cli-afi" {
		t.Fatalf("describe-fpga-images Name: got %q, want cli-afi", v)
	}
	if v := q("ec2", "describe-fpga-images", "--fpga-image-ids", afi,
		"--query", "FpgaImages[0].State.Code", "--output", "text"); v != "available" {
		t.Fatalf("describe-fpga-images State.Code: got %q, want available", v)
	}
	if v := q("ec2", "describe-fpga-images", "--fpga-image-ids", afi,
		"--query", "FpgaImages[0].Description", "--output", "text"); v != "cli fpga image" {
		t.Fatalf("describe-fpga-images Description: got %q, want 'cli fpga image'", v)
	}

	// copy-fpga-image into a fresh AFI.
	copyID := q("ec2", "copy-fpga-image", "--source-fpga-image-id", afi,
		"--source-region", "us-east-1", "--name", "cli-afi-copy",
		"--query", "FpgaImageId", "--output", "text")
	if copyID == "" || copyID == afi {
		t.Fatalf("copy-fpga-image returned %q (want a new id != %q)", copyID, afi)
	}
	defer runCLI(t, awsCLI("ec2", "delete-fpga-image", "--fpga-image-id", copyID))
	if v := q("ec2", "describe-fpga-images", "--fpga-image-ids", copyID,
		"--query", "FpgaImages[0].Name", "--output", "text"); v != "cli-afi-copy" {
		t.Fatalf("copied AFI name: got %q, want cli-afi-copy", v)
	}

	// Attribute set: modify load-permission, read it back, reset.
	q("ec2", "modify-fpga-image-attribute", "--fpga-image-id", afi,
		"--attribute", "loadPermission", "--operation-type", "add", "--user-ids", "123456789012")
	if v := q("ec2", "describe-fpga-image-attribute", "--fpga-image-id", afi,
		"--attribute", "loadPermission",
		"--query", "FpgaImageAttribute.LoadPermissions[0].UserId", "--output", "text"); v != "123456789012" {
		t.Fatalf("load-permission userId: got %q, want 123456789012", v)
	}
	if v := q("ec2", "describe-fpga-image-attribute", "--fpga-image-id", afi,
		"--attribute", "name",
		"--query", "FpgaImageAttribute.Name", "--output", "text"); v != "cli-afi" {
		t.Fatalf("name attribute: got %q, want cli-afi", v)
	}
	q("ec2", "reset-fpga-image-attribute", "--fpga-image-id", afi, "--attribute", "loadPermission")
	if v := q("ec2", "describe-fpga-image-attribute", "--fpga-image-id", afi,
		"--attribute", "loadPermission",
		"--query", "FpgaImageAttribute.LoadPermissions", "--output", "json"); !strings.Contains(v, "[]") {
		t.Fatalf("load-permission after reset: got %q, want empty list", v)
	}
}

// TestEC2CLI_AllowedImagesSettings covers the account-level Allowed AMIs
// settings via the aws CLI: enable (audit-mode), get, replace-image-criteria,
// get again, disable.
func TestEC2CLI_AllowedImagesSettings(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	t.Cleanup(func() { runCLI(t, awsCLI("ec2", "disable-allowed-images-settings")) })

	if v := q("ec2", "enable-allowed-images-settings",
		"--allowed-images-settings-state", "audit-mode",
		"--query", "AllowedImagesSettingsState", "--output", "text"); v != "audit-mode" {
		t.Fatalf("enable-allowed-images-settings: got %q, want audit-mode", v)
	}
	if v := q("ec2", "get-allowed-images-settings", "--query", "State", "--output", "text"); v != "audit-mode" {
		t.Fatalf("get-allowed-images-settings state: got %q, want audit-mode", v)
	}

	q("ec2", "replace-image-criteria-in-allowed-images-settings",
		"--image-criteria", "ImageProviders=amazon,123456789012")
	if v := q("ec2", "get-allowed-images-settings",
		"--query", "ImageCriteria[0].ImageProviders", "--output", "text"); !strings.Contains(v, "amazon") {
		t.Fatalf("get-allowed-images-settings criteria: got %q, want it to contain amazon", v)
	}

	if v := q("ec2", "disable-allowed-images-settings",
		"--query", "AllowedImagesSettingsState", "--output", "text"); v != "disabled" {
		t.Fatalf("disable-allowed-images-settings: got %q, want disabled", v)
	}
}

// TestEC2CLI_ImageBlockPublicAccess covers the account-level image
// block-public-access state via the aws CLI.
func TestEC2CLI_ImageBlockPublicAccess(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	t.Cleanup(func() { runCLI(t, awsCLI("ec2", "disable-image-block-public-access")) })

	if v := q("ec2", "enable-image-block-public-access",
		"--image-block-public-access-state", "block-new-sharing",
		"--query", "ImageBlockPublicAccessState", "--output", "text"); v != "block-new-sharing" {
		t.Fatalf("enable-image-block-public-access: got %q, want block-new-sharing", v)
	}
	if v := q("ec2", "get-image-block-public-access-state",
		"--query", "ImageBlockPublicAccessState", "--output", "text"); v != "block-new-sharing" {
		t.Fatalf("get-image-block-public-access-state: got %q, want block-new-sharing", v)
	}
	if v := q("ec2", "disable-image-block-public-access",
		"--query", "ImageBlockPublicAccessState", "--output", "text"); v != "unblocked" {
		t.Fatalf("disable-image-block-public-access: got %q, want unblocked", v)
	}
}

// TestEC2CLI_ImageDeregistrationProtection covers per-AMI deregistration
// protection via the aws CLI on a real registered AMI.
func TestEC2CLI_ImageDeregistrationProtection(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	ami := ec2RegisterTestAMICLI(t, q)

	if v := q("ec2", "enable-image-deregistration-protection", "--image-id", ami,
		"--query", "Return", "--output", "text"); v == "" || v == "None" {
		t.Fatalf("enable-image-deregistration-protection Return: got %q", v)
	}
	if v := q("ec2", "disable-image-deregistration-protection", "--image-id", ami,
		"--query", "Return", "--output", "text"); v != "disabled" {
		t.Fatalf("disable-image-deregistration-protection: got %q, want disabled", v)
	}
}

// TestEC2CLI_StoreImageTask covers create-store-image-task on a real AMI and
// the describe-store-image-tasks read-back; bundle/conversion task lists are
// asserted honest-empty.
func TestEC2CLI_StoreImageTask(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	ami := ec2RegisterTestAMICLI(t, q)

	key := q("ec2", "create-store-image-task", "--image-id", ami, "--bucket", "cli-store-bucket",
		"--query", "ObjectKey", "--output", "text")
	if key == "" || key == "None" {
		t.Fatalf("create-store-image-task ObjectKey: got %q", key)
	}
	got := q("ec2", "describe-store-image-tasks", "--image-ids", ami,
		"--query", "StoreImageTaskResults[0].[AmiId,Bucket,StoreTaskState]", "--output", "text")
	if f := strings.Fields(got); len(f) != 3 || f[0] != ami || f[1] != "cli-store-bucket" || f[2] != "Completed" {
		t.Fatalf("describe-store-image-tasks: got %q, want '%s cli-store-bucket Completed'", got, ami)
	}

	// Bundle and conversion tasks are honest-empty.
	if v := q("ec2", "describe-bundle-tasks", "--query", "BundleTasks", "--output", "json"); !strings.Contains(v, "[]") {
		t.Fatalf("describe-bundle-tasks: got %q, want empty list", v)
	}
	if v := q("ec2", "describe-conversion-tasks", "--query", "ConversionTasks", "--output", "json"); !strings.Contains(v, "[]") {
		t.Fatalf("describe-conversion-tasks: got %q, want empty list", v)
	}
}

// ec2RegisterTestAMICLI registers a minimal EBS-backed AMI via the CLI and
// returns its id, for tests needing a real AMI in the image store.
func ec2RegisterTestAMICLI(t *testing.T, q func(args ...string) string) string {
	t.Helper()
	ami := q("ec2", "register-image", "--name", "imf-cli-test-ami", "--architecture", "x86_64",
		"--root-device-name", "/dev/sda1",
		"--block-device-mappings", "DeviceName=/dev/sda1,Ebs={SnapshotId=snap-0123456789abcdef0,VolumeSize=8}",
		"--query", "ImageId", "--output", "text")
	if ami == "" {
		t.Fatal("register-image returned empty ImageId")
	}
	t.Cleanup(func() { runCLI(t, awsCLI("ec2", "deregister-image", "--image-id", ami)) })
	return ami
}
