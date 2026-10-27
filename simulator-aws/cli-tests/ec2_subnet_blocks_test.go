package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_SubnetBlocksAndZones holds subnets to their blocks and zones: a
// VPC created with --amazon-provided-ipv6-cidr-block lists its /56, a subnet's
// IPv4 block lies within the VPC's and overlaps no other subnet, its IPv6
// block is a /64 of the VPC's, and describe-availability-zones answers by zone
// name and filter.
func TestEC2CLI_SubnetBlocksAndZones(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	refused := func(code string, args ...string) {
		t.Helper()
		out := runCLIExpectError(t, awsCLI(args...))
		if !strings.Contains(out, "("+code+")") {
			t.Fatalf("%s: got %q, want %s", strings.Join(args[1:3], " "), out, code)
		}
	}

	if got := q("ec2", "describe-availability-zones", "--zone-names", "us-east-1b", "--query", "AvailabilityZones[].ZoneName", "--output", "text"); got != "us-east-1b" {
		t.Fatalf("describe-availability-zones by name: got %q", got)
	}
	if got := q("ec2", "describe-availability-zones", "--filters", "Name=zone-type,Values=local-zone", "--query", "length(AvailabilityZones)", "--output", "text"); got != "0" {
		t.Fatalf("describe-availability-zones filtered to Local Zones: got %q, want none", got)
	}
	refused("InvalidParameterValue", "ec2", "describe-availability-zones", "--zone-names", "us-east-1z")

	vpc := strings.Fields(q("ec2", "create-vpc", "--cidr-block", "10.27.0.0/16", "--amazon-provided-ipv6-cidr-block",
		"--query", "Vpc.[VpcId,Ipv6CidrBlockAssociationSet[0].Ipv6CidrBlock]", "--output", "text"))
	if len(vpc) != 2 || !strings.HasSuffix(vpc[1], "/56") {
		t.Fatalf("create-vpc with an Amazon-provided IPv6 block: got %q", vpc)
	}
	vpcID, ipv6 := vpc[0], vpc[1]
	if got := q("ec2", "describe-vpcs", "--vpc-ids", vpcID, "--query", "Vpcs[0].Ipv6CidrBlockAssociationSet[0].Ipv6CidrBlock", "--output", "text"); got != ipv6 {
		t.Fatalf("describe-vpcs lists the IPv6 block: got %q, want %s", got, ipv6)
	}

	refused("InvalidParameterValue", "ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.27.1.0/24", "--availability-zone", "us-east-1z")
	refused("InvalidSubnet.Range", "ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.28.1.0/24")
	refused("InvalidSubnet.Range", "ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.27.1.0/24", "--ipv6-cidr-block", "2001:db8::/64")
	first := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.27.1.0/24", "--availability-zone", "us-east-1a",
		"--ipv6-cidr-block", elbv2Ipv6SubnetRangeCLI(t, ipv6, 1), "--query", "Subnet.[SubnetId,DefaultForAz]", "--output", "text")
	if !strings.HasPrefix(first, "subnet-") || !strings.HasSuffix(first, "False") {
		t.Fatalf("create-subnet: got %q", first)
	}
	refused("InvalidSubnet.Conflict", "ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.27.1.0/25", "--availability-zone", "us-east-1b")
	refused("InvalidSubnet.Conflict", "ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.27.2.0/24",
		"--ipv6-cidr-block", elbv2Ipv6SubnetRangeCLI(t, ipv6, 1))
}
