package aws_cli_test

import (
	"strings"
	"testing"
)

// VPN concentrators (create/delete/describe-vpn-concentrator{,s}) are exercised
// SDK-side only: the operations are too new to exist in aws CLI 2.26.6, so the
// CLI binary rejects the command verbs. The SDK suite covers the
// simulator-test-contract hook for those ops.

// TestEC2CLI_AddressTransferLifecycle exercises enable/describe/accept/disable
// of an Elastic IP address transfer through the aws CLI.
func TestEC2CLI_AddressTransferLifecycle(t *testing.T) {
	out := runCLI(t, awsCLI("ec2", "allocate-address", "--domain", "vpc",
		"--query", "[AllocationId,PublicIp]", "--output", "text"))
	f := strings.Fields(out)
	if len(f) != 2 {
		t.Fatalf("allocate-address: got %q", strings.TrimSpace(out))
	}
	allocID, publicIP := f[0], f[1]
	defer func() { _ = awsCLI("ec2", "release-address", "--allocation-id", allocID).Run() }()

	status := strings.TrimSpace(runCLI(t, awsCLI("ec2", "enable-address-transfer",
		"--allocation-id", allocID, "--transfer-account-id", "999988887777",
		"--query", "AddressTransfer.AddressTransferStatus", "--output", "text")))
	if status != "pending" {
		t.Fatalf("enable-address-transfer status: got %q, want pending", status)
	}

	gotIP := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-address-transfers",
		"--allocation-ids", allocID,
		"--query", "AddressTransfers[0].PublicIp", "--output", "text")))
	if gotIP != publicIP {
		t.Fatalf("describe-address-transfers PublicIp: got %q, want %q", gotIP, publicIP)
	}

	accStatus := strings.TrimSpace(runCLI(t, awsCLI("ec2", "accept-address-transfer",
		"--address", publicIP,
		"--query", "AddressTransfer.AddressTransferStatus", "--output", "text")))
	if accStatus != "accepted" {
		t.Fatalf("accept-address-transfer status: got %q, want accepted", accStatus)
	}

	runCLI(t, awsCLI("ec2", "enable-address-transfer",
		"--allocation-id", allocID, "--transfer-account-id", "999988887777"))
	runCLI(t, awsCLI("ec2", "disable-address-transfer", "--allocation-id", allocID))
}

// TestEC2CLI_ByoipCidrLifecycle exercises provision/advertise/describe/withdraw/
// deprovision of a BYOIP CIDR through the aws CLI.
func TestEC2CLI_ByoipCidrLifecycle(t *testing.T) {
	const cidr = "198.51.100.0/24"

	state := strings.TrimSpace(runCLI(t, awsCLI("ec2", "provision-byoip-cidr",
		"--cidr", cidr, "--description", "cli-byoip",
		"--query", "ByoipCidr.State", "--output", "text")))
	if state != "provisioned" {
		t.Fatalf("provision-byoip-cidr state: got %q, want provisioned", state)
	}
	defer func() { _ = awsCLI("ec2", "deprovision-byoip-cidr", "--cidr", cidr).Run() }()

	advState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "advertise-byoip-cidr",
		"--cidr", cidr, "--query", "ByoipCidr.State", "--output", "text")))
	if advState != "advertised" {
		t.Fatalf("advertise-byoip-cidr state: got %q, want advertised", advState)
	}

	cidrs := runCLI(t, awsCLI("ec2", "describe-byoip-cidrs", "--max-results", "10",
		"--query", "ByoipCidrs[].Cidr", "--output", "text"))
	if !strings.Contains(cidrs, cidr) {
		t.Fatalf("describe-byoip-cidrs: %q missing from %q", cidr, strings.TrimSpace(cidrs))
	}

	wdState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "withdraw-byoip-cidr",
		"--cidr", cidr, "--query", "ByoipCidr.State", "--output", "text")))
	if wdState != "provisioned" {
		t.Fatalf("withdraw-byoip-cidr state: got %q, want provisioned", wdState)
	}

	runCLI(t, awsCLI("ec2", "deprovision-byoip-cidr", "--cidr", cidr))
}

// TestEC2CLI_PublicIpv4PoolLifecycle exercises create/provision-cidr/describe/
// deprovision-cidr/delete of a public IPv4 pool through the aws CLI.
func TestEC2CLI_PublicIpv4PoolLifecycle(t *testing.T) {
	poolID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-public-ipv4-pool",
		"--network-border-group", "us-east-1",
		"--query", "PoolId", "--output", "text")))
	if poolID == "" {
		t.Fatal("no public ipv4 pool id")
	}
	defer func() { _ = awsCLI("ec2", "delete-public-ipv4-pool", "--pool-id", poolID).Run() }()

	count := strings.TrimSpace(runCLI(t, awsCLI("ec2", "provision-public-ipv4-pool-cidr",
		"--pool-id", poolID, "--ipam-pool-id", "ipam-pool-00000000000000000",
		"--netmask-length", "28",
		"--query", "PoolAddressRange.AddressCount", "--output", "text")))
	if count != "16" {
		t.Fatalf("provision-public-ipv4-pool-cidr AddressCount: got %q, want 16", count)
	}

	total := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-public-ipv4-pools",
		"--pool-ids", poolID,
		"--query", "PublicIpv4Pools[0].TotalAddressCount", "--output", "text")))
	if total != "16" {
		t.Fatalf("describe-public-ipv4-pools TotalAddressCount: got %q, want 16", total)
	}

	deprov := strings.TrimSpace(runCLI(t, awsCLI("ec2", "deprovision-public-ipv4-pool-cidr",
		"--pool-id", poolID, "--cidr", "10.0.0.0/28",
		"--query", "DeprovisionedAddresses[0]", "--output", "text")))
	if deprov != "10.0.0.0/28" {
		t.Fatalf("deprovision-public-ipv4-pool-cidr: got %q, want 10.0.0.0/28", deprov)
	}
}

// TestEC2CLI_NatGatewayAddressLifecycle exercises assign/associate/disassociate/
// unassign of NAT-gateway secondary addresses through the aws CLI.
func TestEC2CLI_NatGatewayAddressLifecycle(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.230.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()
	subID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.230.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")))
	allocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "allocate-address", "--domain", "vpc",
		"--query", "AllocationId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "release-address", "--allocation-id", allocID).Run() }()

	natID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-nat-gateway",
		"--subnet-id", subID, "--allocation-id", allocID,
		"--query", "NatGateway.NatGatewayId", "--output", "text")))
	if natID == "" {
		t.Fatal("no nat gateway id")
	}

	secondaryIP := strings.TrimSpace(runCLI(t, awsCLI("ec2", "assign-private-nat-gateway-address",
		"--nat-gateway-id", natID, "--private-ip-address-count", "1",
		"--query", "NatGatewayAddresses[?IsPrimary==`false`]|[0].PrivateIp", "--output", "text")))
	if secondaryIP == "" || secondaryIP == "None" {
		t.Fatalf("assign-private-nat-gateway-address: no secondary IP, got %q", secondaryIP)
	}

	allocID2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "allocate-address", "--domain", "vpc",
		"--query", "AllocationId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "release-address", "--allocation-id", allocID2).Run() }()
	assocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-nat-gateway-address",
		"--nat-gateway-id", natID, "--allocation-ids", allocID2,
		"--query", "NatGatewayAddresses[?AllocationId=='"+allocID2+"']|[0].AssociationId", "--output", "text")))
	if assocID == "" || assocID == "None" {
		t.Fatalf("associate-nat-gateway-address: no association id, got %q", assocID)
	}

	runCLI(t, awsCLI("ec2", "disassociate-nat-gateway-address",
		"--nat-gateway-id", natID, "--association-ids", assocID))
	runCLI(t, awsCLI("ec2", "unassign-private-nat-gateway-address",
		"--nat-gateway-id", natID, "--private-ip-addresses", secondaryIP))
}

// TestEC2CLI_TrunkInterfaceLifecycle exercises associate/describe/disassociate
// of a trunk-interface association through the aws CLI.
func TestEC2CLI_TrunkInterfaceLifecycle(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.231.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()
	subID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.231.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")))

	trunkID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-interface",
		"--subnet-id", subID, "--query", "NetworkInterface.NetworkInterfaceId", "--output", "text")))
	branchID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-interface",
		"--subnet-id", subID, "--query", "NetworkInterface.NetworkInterfaceId", "--output", "text")))

	assocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-trunk-interface",
		"--trunk-interface-id", trunkID, "--branch-interface-id", branchID, "--vlan-id", "101",
		"--query", "InterfaceAssociation.AssociationId", "--output", "text")))
	if assocID == "" {
		t.Fatal("no trunk interface association id")
	}

	gotTrunk := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-trunk-interface-associations",
		"--association-ids", assocID,
		"--query", "InterfaceAssociations[0].TrunkInterfaceId", "--output", "text")))
	if gotTrunk != trunkID {
		t.Fatalf("describe-trunk-interface-associations TrunkInterfaceId: got %q, want %q", gotTrunk, trunkID)
	}

	runCLI(t, awsCLI("ec2", "disassociate-trunk-interface", "--association-id", assocID))
}

// TestEC2CLI_CarrierGatewayLifecycle exercises create/describe/delete of a
// carrier gateway through the aws CLI.
func TestEC2CLI_CarrierGatewayLifecycle(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.232.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()

	cagwID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-carrier-gateway",
		"--vpc-id", vpcID, "--query", "CarrierGateway.CarrierGatewayId", "--output", "text")))
	if cagwID == "" {
		t.Fatal("no carrier gateway id")
	}
	defer func() { _ = awsCLI("ec2", "delete-carrier-gateway", "--carrier-gateway-id", cagwID).Run() }()

	out := runCLI(t, awsCLI("ec2", "describe-carrier-gateways", "--carrier-gateway-ids", cagwID,
		"--query", "CarrierGateways[0].[VpcId,State]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != vpcID || f[1] != "available" {
		t.Fatalf("describe-carrier-gateways: got %q, want '%s available'", strings.TrimSpace(out), vpcID)
	}

	delState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "delete-carrier-gateway",
		"--carrier-gateway-id", cagwID, "--query", "CarrierGateway.State", "--output", "text")))
	if delState != "deleted" {
		t.Fatalf("delete-carrier-gateway state: got %q, want deleted", delState)
	}
}

// TestEC2CLI_CoipPoolLifecycle exercises create/describe/get-usage/delete of a
// customer-owned IP (COIP) pool through the aws CLI.
func TestEC2CLI_CoipPoolLifecycle(t *testing.T) {
	poolID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-coip-pool",
		"--local-gateway-route-table-id", "lgw-rtb-00000000000000000",
		"--query", "CoipPool.PoolId", "--output", "text")))
	if poolID == "" {
		t.Fatal("no coip pool id")
	}
	defer func() { _ = awsCLI("ec2", "delete-coip-pool", "--coip-pool-id", poolID).Run() }()

	gotLgw := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-coip-pools", "--pool-ids", poolID,
		"--query", "CoipPools[0].LocalGatewayRouteTableId", "--output", "text")))
	if gotLgw != "lgw-rtb-00000000000000000" {
		t.Fatalf("describe-coip-pools LocalGatewayRouteTableId: got %q", gotLgw)
	}

	usageID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-coip-pool-usage", "--pool-id", poolID,
		"--query", "CoipPoolId", "--output", "text")))
	if usageID != poolID {
		t.Fatalf("get-coip-pool-usage CoipPoolId: got %q, want %q", usageID, poolID)
	}

	runCLI(t, awsCLI("ec2", "delete-coip-pool", "--coip-pool-id", poolID))
}

// TestEC2CLI_NetworkInterfacePermissionLifecycle exercises create/describe/delete
// of a network-interface permission through the aws CLI.
func TestEC2CLI_NetworkInterfacePermissionLifecycle(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.233.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()
	subID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.233.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")))
	eniID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-interface",
		"--subnet-id", subID, "--query", "NetworkInterface.NetworkInterfaceId", "--output", "text")))

	permID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-interface-permission",
		"--network-interface-id", eniID, "--aws-account-id", "999988887777",
		"--permission", "INSTANCE-ATTACH",
		"--query", "InterfacePermission.NetworkInterfacePermissionId", "--output", "text")))
	if permID == "" {
		t.Fatal("no network interface permission id")
	}

	gotENI := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-network-interface-permissions",
		"--network-interface-permission-ids", permID,
		"--query", "NetworkInterfacePermissions[0].NetworkInterfaceId", "--output", "text")))
	if gotENI != eniID {
		t.Fatalf("describe-network-interface-permissions NetworkInterfaceId: got %q, want %q", gotENI, eniID)
	}

	runCLI(t, awsCLI("ec2", "delete-network-interface-permission",
		"--network-interface-permission-id", permID))
}

// TestEC2CLI_VgwRoutePropagation exercises enable/disable of virtual private
// gateway route propagation into a route table through the aws CLI.
func TestEC2CLI_VgwRoutePropagation(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.234.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()
	rtID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-route-table",
		"--vpc-id", vpcID, "--query", "RouteTable.RouteTableId", "--output", "text")))
	vgwID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpn-gateway",
		"--type", "ipsec.1", "--query", "VpnGateway.VpnGatewayId", "--output", "text")))
	if rtID == "" || vgwID == "" {
		t.Fatalf("missing route table (%q) or vpn gateway (%q)", rtID, vgwID)
	}

	runCLI(t, awsCLI("ec2", "enable-vgw-route-propagation",
		"--gateway-id", vgwID, "--route-table-id", rtID))
	runCLI(t, awsCLI("ec2", "disable-vgw-route-propagation",
		"--gateway-id", vgwID, "--route-table-id", rtID))
}

// TestEC2CLI_ModifyManagedPrefixList exercises modify-managed-prefix-list:
// adding and removing entries on an existing managed prefix list through the CLI.
func TestEC2CLI_ModifyManagedPrefixList(t *testing.T) {
	plID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-managed-prefix-list",
		"--prefix-list-name", "cli-modify", "--address-family", "IPv4", "--max-entries", "10",
		"--entries", "Cidr=10.0.0.0/8",
		"--query", "PrefixList.PrefixListId", "--output", "text")))
	if plID == "" {
		t.Fatal("no prefix list id")
	}
	defer func() { _ = awsCLI("ec2", "delete-managed-prefix-list", "--prefix-list-id", plID).Run() }()

	startVersion := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-managed-prefix-lists",
		"--prefix-list-ids", plID, "--query", "PrefixLists[0].Version", "--output", "text")))

	newVersion := strings.TrimSpace(runCLI(t, awsCLI("ec2", "modify-managed-prefix-list",
		"--prefix-list-id", plID, "--current-version", startVersion,
		"--add-entries", "Cidr=172.16.0.0/12,Description=new",
		"--remove-entries", "Cidr=10.0.0.0/8",
		"--query", "PrefixList.Version", "--output", "text")))
	if newVersion == startVersion {
		t.Fatalf("modify-managed-prefix-list version did not advance: still %q", newVersion)
	}

	entries := runCLI(t, awsCLI("ec2", "get-managed-prefix-list-entries", "--prefix-list-id", plID,
		"--query", "Entries[].Cidr", "--output", "text"))
	if !strings.Contains(entries, "172.16.0.0/12") {
		t.Fatalf("modified prefix list missing added entry: %q", strings.TrimSpace(entries))
	}
	if strings.Contains(entries, "10.0.0.0/8") {
		t.Fatalf("modified prefix list still has removed entry: %q", strings.TrimSpace(entries))
	}
}
