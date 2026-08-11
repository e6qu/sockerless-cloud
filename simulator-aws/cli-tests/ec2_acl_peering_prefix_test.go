package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_NetworkAclLifecycle exercises the network ACL ops through the aws
// CLI: create-network-acl, create-network-acl-entry, replace-network-acl-entry,
// describe-network-acls read-back, delete-network-acl-entry, delete-network-acl.
func TestEC2CLI_NetworkAclLifecycle(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.210.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()

	aclID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-acl",
		"--vpc-id", vpcID, "--query", "NetworkAcl.NetworkAclId", "--output", "text")))
	if aclID == "" {
		t.Fatal("no network acl id")
	}
	defer func() { _ = awsCLI("ec2", "delete-network-acl", "--network-acl-id", aclID).Run() }()

	runCLI(t, awsCLI("ec2", "create-network-acl-entry",
		"--network-acl-id", aclID, "--rule-number", "100", "--protocol", "6",
		"--rule-action", "allow", "--ingress", "--cidr-block", "0.0.0.0/0",
		"--port-range", "From=22,To=22"))

	// Read back rule 100's action + port range. Entries is a list of structs.
	out := runCLI(t, awsCLI("ec2", "describe-network-acls", "--network-acl-ids", aclID,
		"--query", "NetworkAcls[0].Entries[?RuleNumber==`100` && Egress==`false`].[RuleAction,PortRange.From]",
		"--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != "allow" || f[1] != "22" {
		t.Fatalf("network acl entry: got %q, want 'allow 22'", strings.TrimSpace(out))
	}

	// Replace it with a deny on port 443.
	runCLI(t, awsCLI("ec2", "replace-network-acl-entry",
		"--network-acl-id", aclID, "--rule-number", "100", "--protocol", "6",
		"--rule-action", "deny", "--ingress", "--cidr-block", "0.0.0.0/0",
		"--port-range", "From=443,To=443"))

	out = runCLI(t, awsCLI("ec2", "describe-network-acls", "--network-acl-ids", aclID,
		"--query", "NetworkAcls[0].Entries[?RuleNumber==`100` && Egress==`false`].[RuleAction,PortRange.From]",
		"--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != "deny" || f[1] != "443" {
		t.Fatalf("replaced acl entry: got %q, want 'deny 443'", strings.TrimSpace(out))
	}

	runCLI(t, awsCLI("ec2", "delete-network-acl-entry",
		"--network-acl-id", aclID, "--rule-number", "100", "--ingress"))
}

// TestEC2CLI_VpcPeeringLifecycle exercises create/describe/accept/delete of a
// VPC peering connection through the aws CLI.
func TestEC2CLI_VpcPeeringLifecycle(t *testing.T) {
	vpcA := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.211.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	vpcB := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.212.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcA == "" || vpcB == "" {
		t.Fatal("no vpc ids")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcA).Run() }()
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcB).Run() }()

	pcxID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc-peering-connection",
		"--vpc-id", vpcA, "--peer-vpc-id", vpcB,
		"--query", "VpcPeeringConnection.VpcPeeringConnectionId", "--output", "text")))
	if pcxID == "" {
		t.Fatal("no peering connection id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-vpc-peering-connection", "--vpc-peering-connection-id", pcxID).Run()
	}()

	if code := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-peering-connections",
		"--vpc-peering-connection-ids", pcxID,
		"--query", "VpcPeeringConnections[0].Status.Code", "--output", "text"))); code != "pending-acceptance" {
		t.Fatalf("peering status: got %q, want pending-acceptance", code)
	}

	runCLI(t, awsCLI("ec2", "accept-vpc-peering-connection", "--vpc-peering-connection-id", pcxID))

	if code := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-peering-connections",
		"--vpc-peering-connection-ids", pcxID,
		"--query", "VpcPeeringConnections[0].Status.Code", "--output", "text"))); code != "active" {
		t.Fatalf("peering status after accept: got %q, want active", code)
	}
}

// TestEC2CLI_ManagedPrefixListLifecycle exercises create/describe/get-entries/
// delete of a managed prefix list through the aws CLI.
func TestEC2CLI_ManagedPrefixListLifecycle(t *testing.T) {
	plID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-managed-prefix-list",
		"--prefix-list-name", "cli-corp", "--address-family", "IPv4", "--max-entries", "4",
		"--entries", "Cidr=172.16.0.0/12,Description=corp", "Cidr=10.1.0.0/16",
		"--query", "PrefixList.PrefixListId", "--output", "text")))
	if plID == "" {
		t.Fatal("no prefix list id")
	}
	defer func() { _ = awsCLI("ec2", "delete-managed-prefix-list", "--prefix-list-id", plID).Run() }()

	out := runCLI(t, awsCLI("ec2", "describe-managed-prefix-lists", "--prefix-list-ids", plID,
		"--query", "PrefixLists[0].[PrefixListName,AddressFamily,MaxEntries]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 3 || f[0] != "cli-corp" || f[1] != "IPv4" || f[2] != "4" {
		t.Fatalf("prefix list: got %q, want 'cli-corp IPv4 4'", strings.TrimSpace(out))
	}

	// Entries is a list of structs; query the Cidr field of each.
	entries := runCLI(t, awsCLI("ec2", "get-managed-prefix-list-entries", "--prefix-list-id", plID,
		"--query", "Entries[].Cidr", "--output", "text"))
	if !strings.Contains(entries, "172.16.0.0/12") || !strings.Contains(entries, "10.1.0.0/16") {
		t.Fatalf("prefix list entries: got %q", strings.TrimSpace(entries))
	}
}

// TestEC2CLI_FlowLogsLifecycle exercises create/describe/delete flow logs
// against a VPC through the aws CLI.
func TestEC2CLI_FlowLogsLifecycle(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.213.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()

	flID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-flow-logs",
		"--resource-ids", vpcID, "--resource-type", "VPC", "--traffic-type", "ALL",
		"--log-destination-type", "cloud-watch-logs", "--log-group-name", "/vpc/flow",
		"--deliver-logs-permission-arn", "arn:aws:iam::123456789012:role/flow",
		"--query", "FlowLogIds[0]", "--output", "text")))
	if flID == "" {
		t.Fatal("no flow log id")
	}
	defer func() { _ = awsCLI("ec2", "delete-flow-logs", "--flow-log-ids", flID).Run() }()

	out := runCLI(t, awsCLI("ec2", "describe-flow-logs", "--flow-log-ids", flID,
		"--query", "FlowLogs[0].[ResourceId,TrafficType,FlowLogStatus]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 3 || f[0] != vpcID || f[1] != "ALL" || f[2] != "ACTIVE" {
		t.Fatalf("flow log: got %q, want '%s ALL ACTIVE'", strings.TrimSpace(out), vpcID)
	}

	runCLI(t, awsCLI("ec2", "delete-flow-logs", "--flow-log-ids", flID))
}

// TestEC2CLI_EgressOnlyInternetGatewayLifecycle exercises create/describe/delete
// of an egress-only internet gateway through the aws CLI.
func TestEC2CLI_EgressOnlyInternetGatewayLifecycle(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.214.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	if vpcID == "" {
		t.Fatal("no vpc id")
	}
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()

	eigwID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-egress-only-internet-gateway",
		"--vpc-id", vpcID,
		"--query", "EgressOnlyInternetGateway.EgressOnlyInternetGatewayId", "--output", "text")))
	if eigwID == "" {
		t.Fatal("no egress-only internet gateway id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-egress-only-internet-gateway", "--egress-only-internet-gateway-id", eigwID).Run()
	}()

	out := runCLI(t, awsCLI("ec2", "describe-egress-only-internet-gateways",
		"--egress-only-internet-gateway-ids", eigwID,
		"--query", "EgressOnlyInternetGateways[0].Attachments[0].VpcId", "--output", "text"))
	if strings.TrimSpace(out) != vpcID {
		t.Fatalf("egress-only igw attachment vpc: got %q, want %q", strings.TrimSpace(out), vpcID)
	}
}
