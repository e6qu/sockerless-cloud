package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_IPAMLifecycle drives the IPAM control plane through the aws CLI:
// create-ipam (with its default scopes + resource discovery), describe-ipams,
// modify-ipam, create-ipam-scope / modify-ipam-scope / describe-ipam-scopes,
// the resource-discovery + association ops, and the Organizations admin ops.
func TestEC2CLI_IPAMLifecycle(t *testing.T) {
	ipamID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-ipam",
		"--description", "cli-ipam",
		"--operating-regions", "RegionName=us-east-1",
		"--tag-specifications", "ResourceType=ipam,Tags=[{Key=Name,Value=cli-ipam}]",
		"--query", "Ipam.IpamId", "--output", "text")))
	if ipamID == "" {
		t.Fatal("no ipam id")
	}
	defer func() { _ = awsCLI("ec2", "delete-ipam", "--ipam-id", ipamID).Run() }()

	// describe-ipams read-back of the default scope IDs + description.
	out := runCLI(t, awsCLI("ec2", "describe-ipams", "--ipam-ids", ipamID,
		"--query", "Ipams[0].[Description,ScopeCount,State]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 3 || f[0] != "cli-ipam" || f[1] != "2" || f[2] != "create-complete" {
		t.Fatalf("describe-ipams: got %q", strings.TrimSpace(out))
	}

	privScopeID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-ipams", "--ipam-ids", ipamID,
		"--query", "Ipams[0].PrivateDefaultScopeId", "--output", "text")))
	if privScopeID == "" {
		t.Fatal("no private default scope id")
	}

	// modify-ipam updates the description.
	runCLI(t, awsCLI("ec2", "modify-ipam", "--ipam-id", ipamID, "--description", "cli-ipam-updated"))
	out = runCLI(t, awsCLI("ec2", "describe-ipams", "--ipam-ids", ipamID,
		"--query", "Ipams[0].Description", "--output", "text"))
	if strings.TrimSpace(out) != "cli-ipam-updated" {
		t.Fatalf("modify-ipam: got %q", strings.TrimSpace(out))
	}

	// create-ipam-scope / modify-ipam-scope / describe-ipam-scopes.
	scopeID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-ipam-scope",
		"--ipam-id", ipamID, "--description", "cli-scope",
		"--query", "IpamScope.IpamScopeId", "--output", "text")))
	if scopeID == "" {
		t.Fatal("no scope id")
	}
	defer func() { _ = awsCLI("ec2", "delete-ipam-scope", "--ipam-scope-id", scopeID).Run() }()

	runCLI(t, awsCLI("ec2", "modify-ipam-scope", "--ipam-scope-id", scopeID, "--description", "cli-scope-updated"))
	out = runCLI(t, awsCLI("ec2", "describe-ipam-scopes", "--ipam-scope-ids", scopeID,
		"--query", "IpamScopes[0].[Description,IsDefault]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != "cli-scope-updated" || f[1] != "False" {
		t.Fatalf("describe-ipam-scopes: got %q", strings.TrimSpace(out))
	}

	// Resource discovery + association.
	rdID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-ipam-resource-discovery",
		"--description", "cli-rd", "--operating-regions", "RegionName=us-east-1",
		"--query", "IpamResourceDiscovery.IpamResourceDiscoveryId", "--output", "text")))
	if rdID == "" {
		t.Fatal("no resource discovery id")
	}
	defer func() {
		_ = awsCLI("ec2", "delete-ipam-resource-discovery", "--ipam-resource-discovery-id", rdID).Run()
	}()

	runCLI(t, awsCLI("ec2", "modify-ipam-resource-discovery", "--ipam-resource-discovery-id", rdID,
		"--description", "cli-rd-updated"))
	out = runCLI(t, awsCLI("ec2", "describe-ipam-resource-discoveries", "--ipam-resource-discovery-ids", rdID,
		"--query", "IpamResourceDiscoveries[0].Description", "--output", "text"))
	if strings.TrimSpace(out) != "cli-rd-updated" {
		t.Fatalf("describe-ipam-resource-discoveries: got %q", strings.TrimSpace(out))
	}

	assocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-ipam-resource-discovery",
		"--ipam-id", ipamID, "--ipam-resource-discovery-id", rdID,
		"--query", "IpamResourceDiscoveryAssociation.IpamResourceDiscoveryAssociationId", "--output", "text")))
	if assocID == "" {
		t.Fatal("no association id")
	}
	out = runCLI(t, awsCLI("ec2", "describe-ipam-resource-discovery-associations",
		"--ipam-resource-discovery-association-ids", assocID,
		"--query", "IpamResourceDiscoveryAssociations[0].IpamId", "--output", "text"))
	if strings.TrimSpace(out) != ipamID {
		t.Fatalf("describe-ipam-resource-discovery-associations: got %q want %q", strings.TrimSpace(out), ipamID)
	}
	runCLI(t, awsCLI("ec2", "disassociate-ipam-resource-discovery",
		"--ipam-resource-discovery-association-id", assocID))

	// Organizations delegated admin.
	runCLI(t, awsCLI("ec2", "enable-ipam-organization-admin-account", "--delegated-admin-account-id", "210987654321"))
	runCLI(t, awsCLI("ec2", "disable-ipam-organization-admin-account", "--delegated-admin-account-id", "210987654321"))
}

// TestEC2CLI_IPAMPoolAllocation drives the pool/CIDR/allocation surface through
// the aws CLI: create-ipam-pool, provision-ipam-pool-cidr, get-ipam-pool-cidrs,
// allocate-ipam-pool-cidr (carving sub-CIDRs), get-ipam-pool-allocations,
// describe-ipam-pool-allocations, release-ipam-pool-allocation, modify-ipam-pool,
// deprovision-ipam-pool-cidr, plus get-ipam-resource-cidrs / get-ipam-address-history
// / modify-ipam-resource-cidr.
func TestEC2CLI_IPAMPoolAllocation(t *testing.T) {
	ipamID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-ipam",
		"--operating-regions", "RegionName=us-east-1",
		"--query", "Ipam.IpamId", "--output", "text")))
	if ipamID == "" {
		t.Fatal("no ipam id")
	}
	defer func() { _ = awsCLI("ec2", "delete-ipam", "--ipam-id", ipamID).Run() }()

	scopeID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-ipams", "--ipam-ids", ipamID,
		"--query", "Ipams[0].PrivateDefaultScopeId", "--output", "text")))
	if scopeID == "" {
		t.Fatal("no scope id")
	}

	poolID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-ipam-pool",
		"--ipam-scope-id", scopeID, "--address-family", "ipv4", "--description", "cli-pool",
		"--query", "IpamPool.IpamPoolId", "--output", "text")))
	if poolID == "" {
		t.Fatal("no pool id")
	}
	defer func() { _ = awsCLI("ec2", "delete-ipam-pool", "--ipam-pool-id", poolID).Run() }()

	out := runCLI(t, awsCLI("ec2", "describe-ipam-pools", "--ipam-pool-ids", poolID,
		"--query", "IpamPools[0].[Description,AddressFamily,State]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 3 || f[0] != "cli-pool" || f[1] != "ipv4" || f[2] != "create-complete" {
		t.Fatalf("describe-ipam-pools: got %q", strings.TrimSpace(out))
	}

	// provision-ipam-pool-cidr + get-ipam-pool-cidrs read-back.
	out = runCLI(t, awsCLI("ec2", "provision-ipam-pool-cidr", "--ipam-pool-id", poolID,
		"--cidr", "10.10.0.0/16", "--query", "IpamPoolCidr.State", "--output", "text"))
	if strings.TrimSpace(out) != "provisioned" {
		t.Fatalf("provision-ipam-pool-cidr: got %q", strings.TrimSpace(out))
	}
	out = runCLI(t, awsCLI("ec2", "get-ipam-pool-cidrs", "--ipam-pool-id", poolID,
		"--query", "IpamPoolCidrs[?Cidr=='10.10.0.0/16'].State", "--output", "text"))
	if strings.TrimSpace(out) != "provisioned" {
		t.Fatalf("get-ipam-pool-cidrs: got %q", strings.TrimSpace(out))
	}

	// allocate-ipam-pool-cidr by netmask length carves a sub-CIDR.
	allocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "allocate-ipam-pool-cidr",
		"--ipam-pool-id", poolID, "--netmask-length", "24",
		"--query", "IpamPoolAllocation.IpamPoolAllocationId", "--output", "text")))
	if allocID == "" {
		t.Fatal("no allocation id")
	}
	allocCidr := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-ipam-pool-allocations", "--ipam-pool-id", poolID,
		"--query", "IpamPoolAllocations[?IpamPoolAllocationId=='"+allocID+"'].Cidr", "--output", "text")))
	if allocCidr != "10.10.0.0/24" {
		t.Fatalf("get-ipam-pool-allocations: got %q want 10.10.0.0/24", allocCidr)
	}

	// modify-ipam-pool description update.
	runCLI(t, awsCLI("ec2", "modify-ipam-pool", "--ipam-pool-id", poolID, "--description", "cli-pool-updated"))

	// get-ipam-resource-cidrs + get-ipam-address-history + modify-ipam-resource-cidr.
	runCLI(t, awsCLI("ec2", "get-ipam-resource-cidrs", "--ipam-scope-id", scopeID))
	runCLI(t, awsCLI("ec2", "get-ipam-address-history", "--ipam-scope-id", scopeID, "--cidr", "10.10.0.0/24"))
	out = runCLI(t, awsCLI("ec2", "modify-ipam-resource-cidr",
		"--resource-id", "vpc-cli12345", "--resource-cidr", "10.10.0.0/24",
		"--resource-region", "us-east-1", "--current-ipam-scope-id", scopeID, "--monitored",
		"--query", "IpamResourceCidr.ResourceId", "--output", "text"))
	if strings.TrimSpace(out) != "vpc-cli12345" {
		t.Fatalf("modify-ipam-resource-cidr: got %q", strings.TrimSpace(out))
	}

	// release-ipam-pool-allocation then deprovision-ipam-pool-cidr.
	runCLI(t, awsCLI("ec2", "release-ipam-pool-allocation", "--ipam-pool-id", poolID,
		"--ipam-pool-allocation-id", allocID, "--cidr", "10.10.0.0/24"))
	out = runCLI(t, awsCLI("ec2", "deprovision-ipam-pool-cidr", "--ipam-pool-id", poolID,
		"--cidr", "10.10.0.0/16", "--query", "IpamPoolCidr.State", "--output", "text"))
	if strings.TrimSpace(out) != "deprovisioned" {
		t.Fatalf("deprovision-ipam-pool-cidr: got %q", strings.TrimSpace(out))
	}
}
