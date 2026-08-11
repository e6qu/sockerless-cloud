package aws_cli_test

import (
	"strings"
	"testing"
)

// This file exercises the EC2 volume / snapshot / recycle-bin / CoIP /
// default-VPC / prefix-list / security-group-reference / launch-template-data /
// DNS-name-option / IPv6 family via the aws CLI.
//
// A handful of operations are absent from aws CLI 2.26.6 (copy-volumes,
// import-volume, restore-volume-from-recycle-bin, list-volumes-in-recycle-bin,
// create-delegate-mac-volume-ownership-task,
// create-mac-system-integrity-protection-modification-task,
// modify-public-ip-dns-name-options,
// create/update-interruptible-capacity-reservation-allocation). Those are
// covered by the SDK tests (which exercise the same contract hook).

// TestEC2CLI_VolumesSnapshots covers create-snapshots, describe-volume-status,
// and enable-volume-io over a running instance with an attached data volume.
func TestEC2CLI_VolumesSnapshots(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.220.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.220.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	inst := q("ec2", "run-instances", "--image-id", "ami-12345678", "--instance-type", "t3.micro",
		"--subnet-id", subnet, "--query", "Instances[0].InstanceId", "--output", "text")

	vol := q("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "12",
		"--query", "VolumeId", "--output", "text")
	runCLI(t, awsCLI("ec2", "attach-volume", "--volume-id", vol, "--instance-id", inst, "--device", "/dev/sdf"))

	// create-snapshots covers root + data volume.
	snaps := q("ec2", "create-snapshots", "--instance-specification", "InstanceId="+inst,
		"--description", "cli-set", "--query", "Snapshots[].VolumeId", "--output", "text")
	if !strings.Contains(snaps, vol) {
		t.Fatalf("create-snapshots set must include the data volume %q, got %q", vol, snaps)
	}

	status := q("ec2", "describe-volume-status", "--volume-ids", vol,
		"--query", "VolumeStatuses[0].VolumeStatus.Status", "--output", "text")
	if status != "ok" {
		t.Fatalf("describe-volume-status: got %q, want ok", status)
	}

	runCLI(t, awsCLI("ec2", "enable-volume-io", "--volume-id", vol))
}

// TestEC2CLI_RecycleBinTasks covers the recycle-bin list surfaces (snapshots),
// locked-snapshots / import-snapshot-task read-backs, and the replace-root-volume
// task lifecycle.
func TestEC2CLI_RecycleBinTasks(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	if out := q("ec2", "list-snapshots-in-recycle-bin", "--query", "Snapshots", "--output", "json"); out != "[]" {
		t.Fatalf("list-snapshots-in-recycle-bin: got %q, want []", out)
	}
	if out := q("ec2", "describe-locked-snapshots", "--query", "Snapshots", "--output", "json"); out != "[]" {
		t.Fatalf("describe-locked-snapshots: got %q, want []", out)
	}
	if out := q("ec2", "describe-import-snapshot-tasks", "--query", "ImportSnapshotTasks", "--output", "json"); out != "[]" {
		t.Fatalf("describe-import-snapshot-tasks: got %q, want []", out)
	}

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.221.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.221.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	inst := q("ec2", "run-instances", "--image-id", "ami-12345678", "--instance-type", "t3.micro",
		"--subnet-id", subnet, "--query", "Instances[0].InstanceId", "--output", "text")

	task := q("ec2", "create-replace-root-volume-task", "--instance-id", inst,
		"--image-id", "ami-99999999", "--query", "ReplaceRootVolumeTask.ReplaceRootVolumeTaskId", "--output", "text")
	if task == "" {
		t.Fatal("create-replace-root-volume-task returned empty task id")
	}
	gotInst := q("ec2", "describe-replace-root-volume-tasks", "--replace-root-volume-task-ids", task,
		"--query", "ReplaceRootVolumeTasks[0].InstanceId", "--output", "text")
	if gotInst != inst {
		t.Fatalf("describe-replace-root-volume-tasks: instance %q, want %q", gotInst, inst)
	}
}

// TestEC2CLI_CoipDefaultVpc covers CoIP CIDR add/delete and the
// default-VPC/subnet surface.
func TestEC2CLI_CoipDefaultVpc(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	pool := q("ec2", "create-coip-pool", "--local-gateway-route-table-id", "lgw-rtb-0123456789abcdef0",
		"--query", "CoipPool.PoolId", "--output", "text")
	if pool == "" {
		t.Fatal("create-coip-pool returned empty pool id")
	}
	cidr := q("ec2", "create-coip-cidr", "--cidr", "10.41.0.0/24", "--coip-pool-id", pool,
		"--query", "CoipCidr.Cidr", "--output", "text")
	if cidr != "10.41.0.0/24" {
		t.Fatalf("create-coip-cidr: got %q, want 10.41.0.0/24", cidr)
	}
	del := q("ec2", "delete-coip-cidr", "--cidr", "10.41.0.0/24", "--coip-pool-id", pool,
		"--query", "CoipCidr.Cidr", "--output", "text")
	if del != "10.41.0.0/24" {
		t.Fatalf("delete-coip-cidr: got %q, want 10.41.0.0/24", del)
	}

	// The account is auto-provisioned with a default VPC; create-default-vpc fails.
	runCLIExpectError(t, awsCLI("ec2", "create-default-vpc"))
	sn := q("ec2", "create-default-subnet", "--availability-zone", "us-east-1c",
		"--query", "Subnet.VpcId", "--output", "text")
	if sn == "" {
		t.Fatal("create-default-subnet returned empty VpcId")
	}
}

// TestEC2CLI_PrefixListsSecurityGroups covers describe-prefix-lists, the managed
// prefix-list association / version-restore surface, and the SG reference /
// stale / for-vpc surfaces.
func TestEC2CLI_PrefixListsSecurityGroups(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	names := q("ec2", "describe-prefix-lists",
		"--query", "PrefixLists[].PrefixListName", "--output", "text")
	if !strings.Contains(names, "com.amazonaws.us-east-1.s3") {
		t.Fatalf("describe-prefix-lists must include the S3 gateway-endpoint list, got %q", names)
	}

	plID := q("ec2", "create-managed-prefix-list", "--prefix-list-name", "cli-test-pl",
		"--address-family", "IPv4", "--max-entries", "5",
		"--entries", "Cidr=10.60.0.0/24,Description=one",
		"--query", "PrefixList.PrefixListId", "--output", "text")
	if assoc := q("ec2", "get-managed-prefix-list-associations", "--prefix-list-id", plID,
		"--query", "PrefixListAssociations", "--output", "json"); assoc != "[]" {
		t.Fatalf("get-managed-prefix-list-associations: got %q, want []", assoc)
	}
	restored := q("ec2", "restore-managed-prefix-list-version", "--prefix-list-id", plID,
		"--current-version", "1", "--previous-version", "1",
		"--query", "PrefixList.PrefixListId", "--output", "text")
	if restored != plID {
		t.Fatalf("restore-managed-prefix-list-version: got %q, want %q", restored, plID)
	}

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.222.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	sg := q("ec2", "create-security-group", "--group-name", "cli-sg-refs",
		"--description", "refs", "--vpc-id", vpc, "--query", "GroupId", "--output", "text")

	forVpc := q("ec2", "get-security-groups-for-vpc", "--vpc-id", vpc,
		"--query", "SecurityGroupForVpcs[].GroupId", "--output", "text")
	if !strings.Contains(forVpc, sg) {
		t.Fatalf("get-security-groups-for-vpc must list %q, got %q", sg, forVpc)
	}
	if refs := q("ec2", "describe-security-group-references", "--group-id", sg,
		"--query", "SecurityGroupReferenceSet", "--output", "json"); refs != "[]" {
		t.Fatalf("describe-security-group-references: got %q, want []", refs)
	}
	if stale := q("ec2", "describe-stale-security-groups", "--vpc-id", vpc,
		"--query", "StaleSecurityGroupSet", "--output", "json"); stale != "[]" {
		t.Fatalf("describe-stale-security-groups: got %q, want []", stale)
	}
}

// TestEC2CLI_LaunchTemplateDnsEni covers get-launch-template-data,
// delete-launch-template-versions, the DNS-name-option / VPC-tenancy /
// VPC-endpoint / route-table-association / ENI-attribute / diagnostic-interrupt
// surfaces.
func TestEC2CLI_LaunchTemplateDnsEni(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.223.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.223.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	inst := q("ec2", "run-instances", "--image-id", "ami-12345678", "--instance-type", "t3.small",
		"--subnet-id", subnet, "--query", "Instances[0].InstanceId", "--output", "text")

	img := q("ec2", "get-launch-template-data", "--instance-id", inst,
		"--query", "LaunchTemplateData.ImageId", "--output", "text")
	if img != "ami-12345678" {
		t.Fatalf("get-launch-template-data: ImageId %q, want ami-12345678", img)
	}

	ltID := q("ec2", "create-launch-template", "--launch-template-name", "cli-lt-data",
		"--launch-template-data", "ImageId=ami-11111111",
		"--query", "LaunchTemplate.LaunchTemplateId", "--output", "text")
	runCLI(t, awsCLI("ec2", "create-launch-template-version", "--launch-template-id", ltID,
		"--launch-template-data", "ImageId=ami-22222222"))
	deleted := q("ec2", "delete-launch-template-versions", "--launch-template-id", ltID,
		"--versions", "2", "--query", "SuccessfullyDeletedLaunchTemplateVersions[0].VersionNumber", "--output", "text")
	if deleted != "2" {
		t.Fatalf("delete-launch-template-versions: deleted %q, want 2", deleted)
	}

	tenancy := q("ec2", "modify-vpc-tenancy", "--vpc-id", vpc, "--instance-tenancy", "default",
		"--query", "ReturnValue", "--output", "text")
	if tenancy != "True" {
		t.Fatalf("modify-vpc-tenancy: got %q, want True", tenancy)
	}
	runCLI(t, awsCLI("ec2", "modify-private-dns-name-options", "--instance-id", inst,
		"--enable-resource-name-dns-a-record"))
	runCLI(t, awsCLI("ec2", "send-diagnostic-interrupt", "--instance-id", inst))

	eni := q("ec2", "create-network-interface", "--subnet-id", subnet,
		"--query", "NetworkInterface.NetworkInterfaceId", "--output", "text")
	check := q("ec2", "describe-network-interface-attribute", "--network-interface-id", eni,
		"--attribute", "sourceDestCheck", "--query", "SourceDestCheck.Value", "--output", "text")
	if check != "True" {
		t.Fatalf("describe-network-interface-attribute sourceDestCheck: got %q, want True", check)
	}
	runCLI(t, awsCLI("ec2", "reset-network-interface-attribute", "--network-interface-id", eni,
		"--source-dest-check", "sourceDestCheck"))

	// Route-table association replacement.
	rt := q("ec2", "create-route-table", "--vpc-id", vpc, "--query", "RouteTable.RouteTableId", "--output", "text")
	assoc := q("ec2", "associate-route-table", "--route-table-id", rt, "--subnet-id", subnet,
		"--query", "AssociationId", "--output", "text")
	rt2 := q("ec2", "create-route-table", "--vpc-id", vpc, "--query", "RouteTable.RouteTableId", "--output", "text")
	newAssoc := q("ec2", "replace-route-table-association", "--association-id", assoc, "--route-table-id", rt2,
		"--query", "NewAssociationId", "--output", "text")
	if newAssoc == "" || newAssoc == assoc {
		t.Fatalf("replace-route-table-association must return a fresh id, got %q (old %q)", newAssoc, assoc)
	}

	ep := q("ec2", "create-vpc-endpoint", "--vpc-id", vpc, "--service-name", "com.amazonaws.us-east-1.s3",
		"--vpc-endpoint-type", "Gateway", "--query", "VpcEndpoint.VpcEndpointId", "--output", "text")
	mod := q("ec2", "modify-vpc-endpoint", "--vpc-endpoint-id", ep, "--add-route-table-ids", rt,
		"--query", "Return", "--output", "text")
	if mod != "True" {
		t.Fatalf("modify-vpc-endpoint: got %q, want True", mod)
	}
}

// TestEC2CLI_Ipv6Credit covers IPv6 assign/unassign, unassign-private-ip,
// the IPv6-pool describe surface, default credit specification, AZ-group opt-in,
// and the export/import-task surfaces.
func TestEC2CLI_Ipv6Credit(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.224.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.224.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	eni := q("ec2", "create-network-interface", "--subnet-id", subnet,
		"--query", "NetworkInterface.NetworkInterfaceId", "--output", "text")

	assigned := q("ec2", "assign-ipv6-addresses", "--network-interface-id", eni, "--ipv6-address-count", "2",
		"--query", "AssignedIpv6Addresses", "--output", "json")
	addrs := strings.Count(assigned, "\"")
	if addrs != 4 { // 2 addresses => 4 quote chars in a JSON array
		t.Fatalf("assign-ipv6-addresses: expected 2 addresses, got %q", assigned)
	}
	runCLI(t, awsCLI("ec2", "unassign-ipv6-addresses", "--network-interface-id", eni,
		"--ipv6-addresses", "2600:1f18:1::1"))
	runCLI(t, awsCLI("ec2", "unassign-private-ip-addresses", "--network-interface-id", eni,
		"--private-ip-addresses", "10.224.1.55"))

	if pools := q("ec2", "describe-ipv6-pools", "--query", "Ipv6Pools", "--output", "json"); pools != "[]" {
		t.Fatalf("describe-ipv6-pools: got %q, want []", pools)
	}
	if cidrs := q("ec2", "get-associated-ipv6-pool-cidrs", "--pool-id", "ipv6pool-ec2-0123456789abcdef0",
		"--query", "Ipv6CidrAssociations", "--output", "json"); cidrs != "[]" {
		t.Fatalf("get-associated-ipv6-pool-cidrs: got %q, want []", cidrs)
	}

	credit := q("ec2", "get-default-credit-specification", "--instance-family", "t3",
		"--query", "InstanceFamilyCreditSpecification.CpuCredits", "--output", "text")
	if credit != "unlimited" {
		t.Fatalf("get-default-credit-specification t3: got %q, want unlimited", credit)
	}
	mod := q("ec2", "modify-default-credit-specification", "--instance-family", "t3", "--cpu-credits", "standard",
		"--query", "InstanceFamilyCreditSpecification.CpuCredits", "--output", "text")
	if mod != "standard" {
		t.Fatalf("modify-default-credit-specification: got %q, want standard", mod)
	}
	az := q("ec2", "modify-availability-zone-group", "--group-name", "us-east-1-wl1-bos-wlz-1",
		"--opt-in-status", "opted-in", "--query", "Return", "--output", "text")
	if az != "True" {
		t.Fatalf("modify-availability-zone-group: got %q, want True", az)
	}

	if tasks := q("ec2", "describe-export-tasks", "--query", "ExportTasks", "--output", "json"); tasks != "[]" {
		t.Fatalf("describe-export-tasks: got %q, want []", tasks)
	}
	runCLI(t, awsCLI("ec2", "cancel-export-task", "--export-task-id", "export-i-0123456789abcdef0"))
	imp := q("ec2", "cancel-import-task", "--import-task-id", "import-ami-0123456789abcdef0",
		"--query", "ImportTaskId", "--output", "text")
	if imp != "import-ami-0123456789abcdef0" {
		t.Fatalf("cancel-import-task: got %q, want import-ami-0123456789abcdef0", imp)
	}
}
