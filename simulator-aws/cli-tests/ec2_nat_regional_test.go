package aws_cli_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

type cliNatGateway struct {
	NatGatewayId        string
	SubnetId            string
	State               string
	AvailabilityMode    string
	AutoProvisionZones  string
	AutoScalingIps      string
	RouteTableId        string
	NatGatewayAddresses []struct {
		AllocationId       string
		AssociationId      string
		PublicIp           string
		PrivateIp          string
		NetworkInterfaceId string
		AvailabilityZone   string
		AvailabilityZoneId string
		Status             string
	}
}

func cliDescribeNatGateway(t *testing.T, natID string) cliNatGateway {
	t.Helper()
	var out struct{ NatGateways []cliNatGateway }
	parseJSON(t, runCLI(t, awsCLI("ec2", "describe-nat-gateways", "--nat-gateway-ids", natID, "--output", "json")), &out)
	if len(out.NatGateways) != 1 {
		t.Fatalf("describe-nat-gateways %s: %d gateways", natID, len(out.NatGateways))
	}
	return out.NatGateways[0]
}

func (n cliNatGateway) zones() string {
	var zones []string
	for _, a := range n.NatGatewayAddresses {
		zones = append(zones, a.AvailabilityZone)
	}
	sort.Strings(zones)
	return strings.Join(zones, ",")
}

// TestEC2CLI_RegionalNatGatewayAutomaticZones drives a regional NAT gateway
// in automatic mode through the aws CLI: it covers each zone the VPC has a
// subnet in, with an address there, and leaves a zone whose subnet goes.
func TestEC2CLI_RegionalNatGatewayAutomaticZones(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.85.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	natID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-nat-gateway",
		"--vpc-id", vpc, "--availability-mode", "regional",
		"--query", "NatGateway.NatGatewayId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-nat-gateway", "--nat-gateway-id", natID).Run() }()
	runCLI(t, awsCLI("ec2", "wait", "nat-gateway-available", "--nat-gateway-ids", natID))

	n := cliDescribeNatGateway(t, natID)
	if n.AvailabilityMode != "regional" || n.AutoProvisionZones != "enabled" || n.AutoScalingIps != "enabled" || n.SubnetId != "" || n.RouteTableId != natID {
		t.Fatalf("regional gateway = %+v", n)
	}
	subnets := map[string]string{}
	for i, zone := range []string{"us-east-1a", "us-east-1b"} {
		subnets[zone] = strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpc,
			"--cidr-block", fmt.Sprintf("10.85.%d.0/24", i+1), "--availability-zone", zone,
			"--query", "Subnet.SubnetId", "--output", "text")))
	}
	n = cliDescribeNatGateway(t, natID)
	if got := n.zones(); got != "us-east-1a,us-east-1b" {
		t.Fatalf("zones with subnets = %q, want us-east-1a,us-east-1b", got)
	}
	for _, a := range n.NatGatewayAddresses {
		if a.PublicIp == "" || a.AllocationId == "" || a.NetworkInterfaceId == "" || a.AvailabilityZoneId == "" || a.Status != "succeeded" {
			t.Fatalf("address = %+v", a)
		}
	}
	runCLI(t, awsCLI("ec2", "delete-subnet", "--subnet-id", subnets["us-east-1b"]))
	if got := cliDescribeNatGateway(t, natID).zones(); got != "us-east-1a" {
		t.Fatalf("zones after the us-east-1b subnet went = %q", got)
	}
}

// TestEC2CLI_RegionalNatGatewayManualZones drives a regional NAT gateway
// given per-zone Elastic IP addresses, and associates and disassociates its
// addresses by zone, through the aws CLI.
func TestEC2CLI_RegionalNatGatewayManualZones(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.86.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	var eips []string
	for range 2 {
		eip := strings.TrimSpace(runCLI(t, awsCLI("ec2", "allocate-address", "--domain", "vpc", "--query", "AllocationId", "--output", "text")))
		defer func() { _ = awsCLI("ec2", "release-address", "--allocation-id", eip).Run() }()
		eips = append(eips, eip)
	}
	natID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-nat-gateway",
		"--vpc-id", vpc, "--availability-mode", "regional",
		"--availability-zone-addresses", fmt.Sprintf(`[{"AvailabilityZone":"us-east-1a","AllocationIds":[%q]}]`, eips[0]),
		"--query", "NatGateway.NatGatewayId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-nat-gateway", "--nat-gateway-id", natID).Run() }()

	n := cliDescribeNatGateway(t, natID)
	if n.AutoProvisionZones != "disabled" || n.AutoScalingIps != "disabled" || n.zones() != "us-east-1a" {
		t.Fatalf("manual regional gateway = %+v", n)
	}
	assocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-nat-gateway-address",
		"--nat-gateway-id", natID, "--allocation-ids", eips[1], "--availability-zone", "us-east-1b",
		"--query", "NatGatewayAddresses[?AllocationId=='"+eips[1]+"']|[0].AssociationId", "--output", "text")))
	if got := cliDescribeNatGateway(t, natID).zones(); got != "us-east-1a,us-east-1b" {
		t.Fatalf("zones after associate = %q", got)
	}
	runCLI(t, awsCLI("ec2", "disassociate-nat-gateway-address", "--nat-gateway-id", natID, "--association-ids", assocID))
	if got := cliDescribeNatGateway(t, natID).zones(); got != "us-east-1a" {
		t.Fatalf("zones after disassociate = %q", got)
	}
}
