package aws_cli_test

import (
	"strings"
	"testing"
	"time"
)

func TestEC2InstanceLifecycleCLI(t *testing.T) {
	out := runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.77.0.0/16",
		"--query", "Vpc.VpcId",
		"--output", "text"))
	vpcID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID,
		"--cidr-block", "10.77.1.0/24",
		"--query", "Subnet.SubnetId",
		"--output", "text"))
	subnetID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-security-group",
		"--group-name", "cli-instance-sg",
		"--description", "cli instance lifecycle",
		"--vpc-id", vpcID,
		"--query", "GroupId",
		"--output", "text"))
	sgID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "run-instances",
		"--image-id", "ami-cli1234",
		"--instance-type", "t3.micro",
		"--subnet-id", subnetID,
		"--security-group-ids", sgID,
		"--query", "Instances[0].InstanceId",
		"--output", "text"))
	instanceID := strings.TrimSpace(out)
	if instanceID == "" || !strings.HasPrefix(instanceID, "i-") {
		t.Fatalf("expected EC2 instance id, got %q", instanceID)
	}

	waitForCLIInstanceState(t, instanceID, "running")

	runCLI(t, awsCLI("ec2", "stop-instances", "--instance-ids", instanceID))
	runCLI(t, awsCLI("ec2", "start-instances", "--instance-ids", instanceID))
	runCLI(t, awsCLI("ec2", "terminate-instances", "--instance-ids", instanceID))
}

func waitForCLIInstanceState(t *testing.T, instanceID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		out := runCLI(t, awsCLI("ec2", "describe-instances",
			"--instance-ids", instanceID,
			"--query", "Reservations[0].Instances[0].State.Name",
			"--output", "text"))
		last = strings.TrimSpace(out)
		if last == want {
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("expected instance %s state %s, got %s", instanceID, want, last)
}

func TestEC2NatGatewayCLI(t *testing.T) {
	out := runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.78.0.0/16",
		"--query", "Vpc.VpcId",
		"--output", "text"))
	vpcID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID,
		"--cidr-block", "10.78.1.0/24",
		"--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId",
		"--output", "text"))
	subnetID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "allocate-address",
		"--domain", "vpc",
		"--query", "AllocationId",
		"--output", "text"))
	allocationID := strings.TrimSpace(out)
	if allocationID == "" || !strings.HasPrefix(allocationID, "eipalloc-") {
		t.Fatalf("expected EIP allocation id, got %q", allocationID)
	}

	out = runCLI(t, awsCLI("ec2", "describe-addresses",
		"--allocation-ids", allocationID,
		"--query", "Addresses[0].PublicIp",
		"--output", "text"))
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected allocated public IP, got %q", out)
	}

	out = runCLI(t, awsCLI("ec2", "create-nat-gateway",
		"--subnet-id", subnetID,
		"--allocation-id", allocationID,
		"--query", "NatGateway.NatGatewayId",
		"--output", "text"))
	natID := strings.TrimSpace(out)
	if natID == "" || !strings.HasPrefix(natID, "nat-") {
		t.Fatalf("expected NAT gateway id, got %q", natID)
	}

	out = runCLI(t, awsCLI("ec2", "describe-nat-gateways",
		"--nat-gateway-ids", natID,
		"--query", "NatGateways[0].State",
		"--output", "text"))
	if strings.TrimSpace(out) != "available" {
		t.Fatalf("expected available NAT gateway, got %q", out)
	}

	out = runCLI(t, awsCLI("ec2", "create-route-table",
		"--vpc-id", vpcID,
		"--query", "RouteTable.RouteTableId",
		"--output", "text"))
	routeTableID := strings.TrimSpace(out)

	runCLI(t, awsCLI("ec2", "create-route",
		"--route-table-id", routeTableID,
		"--destination-cidr-block", "0.0.0.0/0",
		"--nat-gateway-id", natID))

	out = runCLI(t, awsCLI("ec2", "describe-route-tables",
		"--route-table-ids", routeTableID,
		"--query", "RouteTables[0].Routes[?NatGatewayId=='"+natID+"'].NatGatewayId | [0]",
		"--output", "text"))
	if strings.TrimSpace(out) != natID {
		t.Fatalf("expected NAT gateway route, got %q", out)
	}

	runCLI(t, awsCLI("ec2", "delete-nat-gateway", "--nat-gateway-id", natID))
	runCLI(t, awsCLI("ec2", "release-address", "--allocation-id", allocationID))
}

func TestEC2EBSVolumeSnapshotCLI(t *testing.T) {
	out := runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.81.0.0/16",
		"--query", "Vpc.VpcId",
		"--output", "text"))
	vpcID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID,
		"--cidr-block", "10.81.1.0/24",
		"--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId",
		"--output", "text"))
	subnetID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "run-instances",
		"--image-id", "ami-cli-ebs",
		"--instance-type", "t3.micro",
		"--subnet-id", subnetID,
		"--query", "Instances[0].InstanceId",
		"--output", "text"))
	instanceID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-volume",
		"--availability-zone", "us-east-1a",
		"--size", "1",
		"--volume-type", "gp3",
		"--query", "VolumeId",
		"--output", "text"))
	volumeID := strings.TrimSpace(out)
	if volumeID == "" || !strings.HasPrefix(volumeID, "vol-") {
		t.Fatalf("expected EBS volume id, got %q", volumeID)
	}

	runCLI(t, awsCLI("ec2", "attach-volume",
		"--volume-id", volumeID,
		"--instance-id", instanceID,
		"--device", "/dev/sdf"))

	out = runCLI(t, awsCLI("ec2", "describe-volumes",
		"--volume-ids", volumeID,
		"--query", "Volumes[0].Attachments[0].InstanceId",
		"--output", "text"))
	if strings.TrimSpace(out) != instanceID {
		t.Fatalf("expected EBS attachment to %s, got %q", instanceID, out)
	}

	out = runCLI(t, awsCLI("ec2", "create-snapshot",
		"--volume-id", volumeID,
		"--description", "cli snapshot",
		"--query", "SnapshotId",
		"--output", "text"))
	snapshotID := strings.TrimSpace(out)
	if snapshotID == "" || !strings.HasPrefix(snapshotID, "snap-") {
		t.Fatalf("expected EBS snapshot id, got %q", snapshotID)
	}
	waitCLISnapshotStatus(t, snapshotID, "completed")

	out = runCLI(t, awsCLI("ec2", "create-volume",
		"--availability-zone", "us-east-1a",
		"--snapshot-id", snapshotID,
		"--volume-type", "gp3",
		"--query", "VolumeId",
		"--output", "text"))
	restoredVolumeID := strings.TrimSpace(out)
	if restoredVolumeID == "" || !strings.HasPrefix(restoredVolumeID, "vol-") {
		t.Fatalf("expected restored EBS volume id, got %q", restoredVolumeID)
	}

	runCLI(t, awsCLI("ec2", "detach-volume", "--volume-id", volumeID))
	runCLI(t, awsCLI("ec2", "delete-volume", "--volume-id", volumeID))
	runCLI(t, awsCLI("ec2", "delete-volume", "--volume-id", restoredVolumeID))
	runCLI(t, awsCLI("ec2", "delete-snapshot", "--snapshot-id", snapshotID))
	runCLI(t, awsCLI("ec2", "terminate-instances", "--instance-ids", instanceID))
}

func TestEC2CopySnapshotCLI(t *testing.T) {
	out := runCLI(t, awsCLI("ec2", "create-volume",
		"--availability-zone", "us-east-1a", "--size", "1", "--volume-type", "gp3",
		"--query", "VolumeId", "--output", "text"))
	volumeID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-snapshot",
		"--volume-id", volumeID, "--description", "src",
		"--query", "SnapshotId", "--output", "text"))
	srcID := strings.TrimSpace(out)
	waitCLISnapshotStatus(t, srcID, "completed")

	out = runCLI(t, awsCLI("ec2", "copy-snapshot",
		"--source-region", "us-east-1", "--source-snapshot-id", srcID,
		"--description", "dr-copy",
		"--query", "SnapshotId", "--output", "text"))
	copyID := strings.TrimSpace(out)
	if copyID == "" || !strings.HasPrefix(copyID, "snap-") {
		t.Fatalf("expected copied snapshot id, got %q", copyID)
	}
	if copyID == srcID {
		t.Fatalf("copy must get a new id; got source id %s", srcID)
	}
	waitCLISnapshotStatus(t, copyID, "completed")

	out = runCLI(t, awsCLI("ec2", "describe-snapshots",
		"--snapshot-ids", copyID, "--query", "Snapshots[0].Description", "--output", "text"))
	if strings.TrimSpace(out) != "dr-copy" {
		t.Fatalf("copy description = %q, want dr-copy", strings.TrimSpace(out))
	}

	runCLI(t, awsCLI("ec2", "delete-snapshot", "--snapshot-id", copyID))
	runCLI(t, awsCLI("ec2", "delete-snapshot", "--snapshot-id", srcID))
	runCLI(t, awsCLI("ec2", "delete-volume", "--volume-id", volumeID))
}

func waitCLISnapshotStatus(t *testing.T, snapshotID, want string) {
	t.Helper()
	// Generous deadline: the snapshot transition is fast, but a tight 2s window
	// can expire under CI scheduling stalls / GC pauses and flake the test.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out := runCLI(t, awsCLI("ec2", "describe-snapshots",
			"--snapshot-ids", snapshotID,
			"--query", "Snapshots[0].State",
			"--output", "text"))
		if strings.TrimSpace(out) == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("snapshot %s did not reach %s", snapshotID, want)
}
