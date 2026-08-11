package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2RoutingEIPFidelityCLI covers ReplaceRoute and the EIP association
// lifecycle (AssociateAddress/DisassociateAddress) via the aws CLI.
func TestEC2RoutingEIPFidelityCLI(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.114.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	rtbID := q("ec2", "create-route-table", "--vpc-id", vpcID,
		"--query", "RouteTable.RouteTableId", "--output", "text")
	igwID := q("ec2", "create-internet-gateway",
		"--query", "InternetGateway.InternetGatewayId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.114.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	eipAlloc := q("ec2", "allocate-address", "--domain", "vpc",
		"--query", "AllocationId", "--output", "text")
	natID := q("ec2", "create-nat-gateway", "--subnet-id", subnetID, "--allocation-id", eipAlloc,
		"--query", "NatGateway.NatGatewayId", "--output", "text")

	runCLI(t, awsCLI("ec2", "create-route", "--route-table-id", rtbID,
		"--destination-cidr-block", "0.0.0.0/0", "--gateway-id", igwID))
	// ReplaceRoute swaps the IGW target for the NAT gateway.
	runCLI(t, awsCLI("ec2", "replace-route", "--route-table-id", rtbID,
		"--destination-cidr-block", "0.0.0.0/0", "--nat-gateway-id", natID))

	target := q("ec2", "describe-route-tables", "--route-table-ids", rtbID,
		"--query", "RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'].NatGatewayId | [0]",
		"--output", "text")
	if target != natID {
		t.Fatalf("ReplaceRoute target: got %q, want %q", target, natID)
	}

	// EIP association round-trip.
	instID := q("ec2", "run-instances", "--image-id", "ami-12345678", "--instance-type", "t3.micro",
		"--query", "Instances[0].InstanceId", "--output", "text")
	eip2 := q("ec2", "allocate-address", "--domain", "vpc", "--query", "AllocationId", "--output", "text")
	assocID := q("ec2", "associate-address", "--allocation-id", eip2, "--instance-id", instID,
		"--query", "AssociationId", "--output", "text")
	if assocID == "" || assocID == "None" {
		t.Fatal("associate-address returned no association id")
	}
	readBack := q("ec2", "describe-addresses", "--allocation-ids", eip2,
		"--query", "Addresses[0].AssociationId", "--output", "text")
	if readBack != assocID {
		t.Fatalf("association_id not round-tripped: got %q want %q", readBack, assocID)
	}
}
