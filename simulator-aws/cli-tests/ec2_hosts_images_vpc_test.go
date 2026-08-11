package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_HostsAndEventWindows covers the Dedicated Host control plane
// (allocate-hosts / describe-hosts / modify-hosts / release-hosts /
// describe-mac-hosts) and the Instance Event Window CRUD (create / describe /
// modify / associate / disassociate / delete) via the aws CLI.
func TestEC2CLI_HostsAndEventWindows(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	// --- Dedicated Hosts ---
	hostID := q("ec2", "allocate-hosts", "--availability-zone", "us-east-1a",
		"--instance-family", "m5", "--quantity", "1",
		"--query", "HostIds[0]", "--output", "text")
	if hostID == "" {
		t.Fatal("allocate-hosts returned empty HostId")
	}
	defer runCLIIgnore(awsCLI("ec2", "release-hosts", "--host-ids", hostID))

	if v := q("ec2", "describe-hosts", "--host-ids", hostID,
		"--query", "Hosts[0].[State,AvailabilityZone,HostProperties.InstanceFamily]", "--output", "text"); v != "available\tus-east-1a\tm5" {
		t.Fatalf("describe-hosts: got %q, want 'available\tus-east-1a\tm5'", v)
	}

	q("ec2", "modify-hosts", "--host-ids", hostID, "--auto-placement", "on")
	if v := q("ec2", "describe-hosts", "--host-ids", hostID,
		"--query", "Hosts[0].AutoPlacement", "--output", "text"); v != "on" {
		t.Fatalf("modify-hosts auto-placement: got %q, want 'on'", v)
	}

	// describe-mac-hosts faithfully returns an empty list (no mac hosts).
	if v := q("ec2", "describe-mac-hosts", "--query", "length(MacHosts)", "--output", "text"); v != "0" {
		t.Fatalf("describe-mac-hosts: got %q macHosts, want 0", v)
	}

	if v := q("ec2", "release-hosts", "--host-ids", hostID,
		"--query", "Successful[0]", "--output", "text"); v != hostID {
		t.Fatalf("release-hosts: got %q, want %q", v, hostID)
	}

	// --- Instance Event Windows ---
	iewID := q("ec2", "create-instance-event-window", "--name", "cli-maint",
		"--time-range", "StartWeekDay=sunday,StartHour=2,EndWeekDay=sunday,EndHour=4",
		"--query", "InstanceEventWindow.InstanceEventWindowId", "--output", "text")
	if iewID == "" {
		t.Fatal("create-instance-event-window returned empty id")
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-instance-event-window", "--instance-event-window-id", iewID))

	if v := q("ec2", "describe-instance-event-windows", "--instance-event-window-ids", iewID,
		"--query", "InstanceEventWindows[0].Name", "--output", "text"); v != "cli-maint" {
		t.Fatalf("describe-instance-event-windows: got %q, want 'cli-maint'", v)
	}

	if v := q("ec2", "modify-instance-event-window", "--instance-event-window-id", iewID,
		"--name", "cli-renamed", "--query", "InstanceEventWindow.Name", "--output", "text"); v != "cli-renamed" {
		t.Fatalf("modify-instance-event-window: got %q, want 'cli-renamed'", v)
	}

	if v := q("ec2", "associate-instance-event-window", "--instance-event-window-id", iewID,
		"--association-target", "InstanceIds=i-0123456789abcdef0",
		"--query", "InstanceEventWindow.AssociationTarget.InstanceIds[0]", "--output", "text"); v != "i-0123456789abcdef0" {
		t.Fatalf("associate-instance-event-window: got %q", v)
	}

	q("ec2", "disassociate-instance-event-window", "--instance-event-window-id", iewID,
		"--association-target", "InstanceIds=i-0123456789abcdef0")

	if v := q("ec2", "delete-instance-event-window", "--instance-event-window-id", iewID,
		"--query", "InstanceEventWindowState.InstanceEventWindowId", "--output", "text"); v != iewID {
		t.Fatalf("delete-instance-event-window: got %q, want %q", v, iewID)
	}
}

// TestEC2CLI_ImageAndSnapshotAttributes covers per-AMI launchPermission +
// description attributes (modify / describe / reset), AMI lifecycle (disable /
// enable / export / import / restore), and per-snapshot createVolumePermission
// + tier/lock/import ops via the aws CLI.
func TestEC2CLI_ImageAndSnapshotAttributes(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	// --- AMI attributes + lifecycle ---
	ami := q("ec2", "register-image", "--name", "cli-attr-ami", "--architecture", "x86_64",
		"--root-device-name", "/dev/sda1", "--query", "ImageId", "--output", "text")
	if ami == "" {
		t.Fatal("register-image returned empty ImageId")
	}

	q("ec2", "modify-image-attribute", "--image-id", ami,
		"--launch-permission", "Add=[{UserId=210987654321},{Group=all}]")
	if v := q("ec2", "describe-image-attribute", "--image-id", ami, "--attribute", "launchPermission",
		"--query", "LaunchPermissions[?UserId=='210987654321'].UserId | [0]", "--output", "text"); v != "210987654321" {
		t.Fatalf("describe-image-attribute launchPermission: got %q", v)
	}

	q("ec2", "modify-image-attribute", "--image-id", ami, "--description", "Value=cli golden")
	if v := q("ec2", "describe-image-attribute", "--image-id", ami, "--attribute", "description",
		"--query", "Description.Value", "--output", "text"); v != "cli golden" {
		t.Fatalf("describe-image-attribute description: got %q, want 'cli golden'", v)
	}

	q("ec2", "reset-image-attribute", "--image-id", ami, "--attribute", "launchPermission")
	if v := q("ec2", "describe-image-attribute", "--image-id", ami, "--attribute", "launchPermission",
		"--query", "length(LaunchPermissions)", "--output", "text"); v != "0" {
		t.Fatalf("reset-image-attribute: got %q permissions, want 0", v)
	}

	if v := q("ec2", "disable-image", "--image-id", ami, "--query", "Return", "--output", "text"); v != "True" {
		t.Fatalf("disable-image: got %q", v)
	}
	if v := q("ec2", "enable-image", "--image-id", ami, "--query", "Return", "--output", "text"); v != "True" {
		t.Fatalf("enable-image: got %q", v)
	}
	if v := q("ec2", "export-image", "--image-id", ami, "--disk-image-format", "VMDK",
		"--s3-export-location", "S3Bucket=cli-bucket", "--role-name", "vmimport",
		"--query", "ImageId", "--output", "text"); v != ami {
		t.Fatalf("export-image: got %q, want %q", v, ami)
	}
	if v := q("ec2", "import-image", "--architecture", "x86_64", "--description", "cli-import",
		"--query", "ImportTaskId", "--output", "text"); v == "" {
		t.Fatal("import-image returned empty ImportTaskId")
	}
	if v := q("ec2", "restore-image-from-recycle-bin", "--image-id", ami,
		"--query", "Return", "--output", "text"); v != "True" {
		t.Fatalf("restore-image-from-recycle-bin: got %q", v)
	}
	cr := q("ec2", "create-restore-image-task", "--object-key", "ami-backup", "--bucket", "cli-bucket",
		"--name", "cli-restored", "--query", "ImageId", "--output", "text")
	if cr == "" {
		t.Fatal("create-restore-image-task returned empty ImageId")
	}

	// --- Snapshot attributes + lifecycle ---
	vol := q("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "8",
		"--query", "VolumeId", "--output", "text")
	snap := q("ec2", "create-snapshot", "--volume-id", vol, "--query", "SnapshotId", "--output", "text")
	if snap == "" {
		t.Fatal("create-snapshot returned empty SnapshotId")
	}

	q("ec2", "modify-snapshot-attribute", "--snapshot-id", snap,
		"--attribute", "createVolumePermission", "--operation-type", "add", "--user-ids", "210987654321")
	if v := q("ec2", "describe-snapshot-attribute", "--snapshot-id", snap,
		"--attribute", "createVolumePermission",
		"--query", "CreateVolumePermissions[0].UserId", "--output", "text"); v != "210987654321" {
		t.Fatalf("describe-snapshot-attribute: got %q", v)
	}
	q("ec2", "reset-snapshot-attribute", "--snapshot-id", snap, "--attribute", "createVolumePermission")
	if v := q("ec2", "describe-snapshot-attribute", "--snapshot-id", snap,
		"--attribute", "createVolumePermission",
		"--query", "length(CreateVolumePermissions)", "--output", "text"); v != "0" {
		t.Fatalf("reset-snapshot-attribute: got %q, want 0", v)
	}

	if v := q("ec2", "describe-snapshot-tier-status",
		"--query", "SnapshotTierStatuses[?SnapshotId=='"+snap+"'].StorageTier | [0]", "--output", "text"); v != "standard" {
		t.Fatalf("describe-snapshot-tier-status: got %q, want 'standard'", v)
	}

	if v := q("ec2", "lock-snapshot", "--snapshot-id", snap, "--lock-mode", "governance",
		"--lock-duration", "1", "--query", "LockState", "--output", "text"); v != "governance" {
		t.Fatalf("lock-snapshot: got %q, want 'governance'", v)
	}
	if v := q("ec2", "unlock-snapshot", "--snapshot-id", snap,
		"--query", "SnapshotId", "--output", "text"); v != snap {
		t.Fatalf("unlock-snapshot: got %q, want %q", v, snap)
	}
	if v := q("ec2", "import-snapshot", "--description", "cli-imp",
		"--disk-container", "Format=VMDK,UserBucket={S3Bucket=b,S3Key=k}",
		"--query", "ImportTaskId", "--output", "text"); v == "" {
		t.Fatal("import-snapshot returned empty ImportTaskId")
	}
}

// TestEC2CLI_VpcClassicLinkAndBpa covers VPC ClassicLink + ClassicLink-DNS
// flags, VPC endpoint connection notifications, and the VPC Block Public Access
// options + exclusions via the aws CLI.
func TestEC2CLI_VpcClassicLinkAndBpa(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.182.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")

	// --- ClassicLink ---
	if v := q("ec2", "enable-vpc-classic-link", "--vpc-id", vpc,
		"--query", "Return", "--output", "text"); v != "True" {
		t.Fatalf("enable-vpc-classic-link: got %q", v)
	}
	if v := q("ec2", "describe-vpc-classic-link", "--vpc-ids", vpc,
		"--query", "Vpcs[0].ClassicLinkEnabled", "--output", "text"); v != "True" {
		t.Fatalf("describe-vpc-classic-link: got %q, want True", v)
	}
	q("ec2", "enable-vpc-classic-link-dns-support", "--vpc-id", vpc)
	if v := q("ec2", "describe-vpc-classic-link-dns-support", "--vpc-ids", vpc,
		"--query", "Vpcs[?VpcId=='"+vpc+"'].ClassicLinkDnsSupported | [0]", "--output", "text"); v != "True" {
		t.Fatalf("describe-vpc-classic-link-dns-support: got %q, want True", v)
	}
	q("ec2", "disable-vpc-classic-link-dns-support", "--vpc-id", vpc)
	if v := q("ec2", "disable-vpc-classic-link", "--vpc-id", vpc,
		"--query", "Return", "--output", "text"); v != "True" {
		t.Fatalf("disable-vpc-classic-link: got %q", v)
	}

	// --- VPC endpoint connections + notifications ---
	nfn := q("ec2", "create-vpc-endpoint-connection-notification",
		"--connection-notification-arn", "arn:aws:sns:us-east-1:123456789012:vpce-events",
		"--service-id", "vpce-svc-0123456789abcdef0",
		"--connection-events", "Accept", "Reject",
		"--query", "ConnectionNotification.ConnectionNotificationId", "--output", "text")
	if nfn == "" {
		t.Fatal("create-vpc-endpoint-connection-notification returned empty id")
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-vpc-endpoint-connection-notifications",
		"--connection-notification-ids", nfn))

	if v := q("ec2", "describe-vpc-endpoint-connection-notifications",
		"--connection-notification-id", nfn,
		"--query", "ConnectionNotificationSet[0].ConnectionNotificationArn", "--output", "text"); v != "arn:aws:sns:us-east-1:123456789012:vpce-events" {
		t.Fatalf("describe-vpc-endpoint-connection-notifications: got %q", v)
	}
	q("ec2", "modify-vpc-endpoint-connection-notification", "--connection-notification-id", nfn,
		"--connection-events", "Accept", "Reject", "Connect")

	if v := q("ec2", "describe-vpc-endpoint-connections",
		"--query", "length(VpcEndpointConnections)", "--output", "text"); v != "0" {
		t.Fatalf("describe-vpc-endpoint-connections: got %q, want 0", v)
	}
	if v := q("ec2", "accept-vpc-endpoint-connections", "--service-id", "vpce-svc-0123456789abcdef0",
		"--vpc-endpoint-ids", "vpce-missing",
		"--query", "Unsuccessful[0].ResourceId", "--output", "text"); v != "vpce-missing" {
		t.Fatalf("accept-vpc-endpoint-connections unsuccessful: got %q", v)
	}
	if v := q("ec2", "reject-vpc-endpoint-connections", "--service-id", "vpce-svc-0123456789abcdef0",
		"--vpc-endpoint-ids", "vpce-missing",
		"--query", "Unsuccessful[0].ResourceId", "--output", "text"); v != "vpce-missing" {
		t.Fatalf("reject-vpc-endpoint-connections unsuccessful: got %q", v)
	}

	// --- VPC Block Public Access ---
	if v := q("ec2", "modify-vpc-block-public-access-options",
		"--internet-gateway-block-mode", "block-bidirectional",
		"--query", "VpcBlockPublicAccessOptions.InternetGatewayBlockMode", "--output", "text"); v != "block-bidirectional" {
		t.Fatalf("modify-vpc-block-public-access-options: got %q", v)
	}
	if v := q("ec2", "describe-vpc-block-public-access-options",
		"--query", "VpcBlockPublicAccessOptions.InternetGatewayBlockMode", "--output", "text"); v != "block-bidirectional" {
		t.Fatalf("describe-vpc-block-public-access-options: got %q", v)
	}

	exID := q("ec2", "create-vpc-block-public-access-exclusion", "--vpc-id", vpc,
		"--internet-gateway-exclusion-mode", "allow-bidirectional",
		"--query", "VpcBlockPublicAccessExclusion.ExclusionId", "--output", "text")
	if exID == "" {
		t.Fatal("create-vpc-block-public-access-exclusion returned empty ExclusionId")
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-vpc-block-public-access-exclusion", "--exclusion-id", exID))

	if v := q("ec2", "describe-vpc-block-public-access-exclusions", "--exclusion-ids", exID,
		"--query", "VpcBlockPublicAccessExclusions[0].InternetGatewayExclusionMode", "--output", "text"); v != "allow-bidirectional" {
		t.Fatalf("describe-vpc-block-public-access-exclusions: got %q", v)
	}
	if v := q("ec2", "modify-vpc-block-public-access-exclusion", "--exclusion-id", exID,
		"--internet-gateway-exclusion-mode", "allow-egress",
		"--query", "VpcBlockPublicAccessExclusion.InternetGatewayExclusionMode", "--output", "text"); v != "allow-egress" {
		t.Fatalf("modify-vpc-block-public-access-exclusion: got %q", v)
	}
	q("ec2", "delete-vpc-block-public-access-exclusion", "--exclusion-id", exID)
}
