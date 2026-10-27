package aws_cli_test

import (
	"strings"
	"testing"
)

// aws CLI 2.26.6 lacks the IPAM policy and prefix-list resolver command
// families and modify-ipam-pool-allocation; the SDK test covers them.

func ec2cliIpamExtrasFixture(t *testing.T) (ipamID, poolID string) {
	t.Helper()
	ipamID = strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-ipam",
		"--description", "cli-ipam-extras",
		"--operating-regions", "RegionName=us-east-1",
		"--query", "Ipam.IpamId", "--output", "text")))
	if ipamID == "" {
		t.Fatal("no ipam id")
	}
	t.Cleanup(func() { _ = awsCLI("ec2", "delete-ipam", "--ipam-id", ipamID).Run() })

	scopeID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-ipams", "--ipam-ids", ipamID,
		"--query", "Ipams[0].PrivateDefaultScopeId", "--output", "text")))
	if scopeID == "" {
		t.Fatal("no private default scope id")
	}
	poolID = strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-ipam-pool",
		"--ipam-scope-id", scopeID, "--address-family", "ipv4",
		"--query", "IpamPool.IpamPoolId", "--output", "text")))
	if poolID == "" {
		t.Fatal("no pool id")
	}
	t.Cleanup(func() { _ = awsCLI("ec2", "delete-ipam-pool", "--ipam-pool-id", poolID).Run() })
	return ipamID, poolID
}

func TestEC2CLI_IpamByoasn(t *testing.T) {
	t.Parallel()
	ipamID, poolID := ec2cliIpamExtrasFixture(t)
	const asn = "64513"

	asnState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "provision-ipam-byoasn",
		"--ipam-id", ipamID, "--asn", asn,
		"--asn-authorization-context", "Message=authorize-asn,Signature=c2lnbmF0dXJl",
		"--query", "Byoasn.[Asn,IpamId,State]", "--output", "text")))
	if f := strings.Fields(asnState); len(f) != 3 || f[0] != asn || f[1] != ipamID {
		t.Fatalf("provision-ipam-byoasn: got %q", asnState)
	}
	t.Cleanup(func() {
		_ = awsCLI("ec2", "deprovision-ipam-byoasn", "--ipam-id", ipamID, "--asn", asn).Run()
	})

	out := runCLI(t, awsCLI("ec2", "describe-ipam-byoasn",
		"--query", "Byoasns[?Asn=='"+asn+"'].IpamId", "--output", "text"))
	if strings.TrimSpace(out) != ipamID {
		t.Fatalf("describe-ipam-byoasn: got %q", strings.TrimSpace(out))
	}

	assoc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-ipam-byoasn",
		"--asn", asn, "--cidr", "192.0.2.0/24",
		"--query", "AsnAssociation.[Asn,Cidr]", "--output", "text")))
	if f := strings.Fields(assoc); len(f) != 2 || f[0] != asn || f[1] != "192.0.2.0/24" {
		t.Fatalf("associate-ipam-byoasn: got %q", assoc)
	}

	disassoc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "disassociate-ipam-byoasn",
		"--asn", asn, "--cidr", "192.0.2.0/24",
		"--query", "AsnAssociation.Asn", "--output", "text")))
	if disassoc != asn {
		t.Fatalf("disassociate-ipam-byoasn: got %q", disassoc)
	}

	moved := strings.TrimSpace(runCLI(t, awsCLI("ec2", "move-byoip-cidr-to-ipam",
		"--cidr", "198.51.100.0/24", "--ipam-pool-id", poolID, "--ipam-pool-owner", "123456789012",
		"--query", "ByoipCidr.Cidr", "--output", "text")))
	if moved != "198.51.100.0/24" {
		t.Fatalf("move-byoip-cidr-to-ipam: got %q", moved)
	}
}

func TestEC2CLI_IpamExternalResourceVerificationToken(t *testing.T) {
	t.Parallel()
	ipamID, _ := ec2cliIpamExtrasFixture(t)

	tokenID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-ipam-external-resource-verification-token",
		"--ipam-id", ipamID,
		"--query", "IpamExternalResourceVerificationToken.IpamExternalResourceVerificationTokenId", "--output", "text")))
	if tokenID == "" {
		t.Fatal("no token id")
	}
	t.Cleanup(func() {
		_ = awsCLI("ec2", "delete-ipam-external-resource-verification-token",
			"--ipam-external-resource-verification-token-id", tokenID).Run()
	})

	out := runCLI(t, awsCLI("ec2", "describe-ipam-external-resource-verification-tokens",
		"--ipam-external-resource-verification-token-ids", tokenID,
		"--query", "IpamExternalResourceVerificationTokens[0].[IpamId,Status]", "--output", "text"))
	if f := strings.Fields(out); len(f) != 2 || f[0] != ipamID || f[1] != "valid" {
		t.Fatalf("describe tokens: got %q", strings.TrimSpace(out))
	}
}

// TestEC2CLI_IpamDiscoveredResources expects each read-only discovery probe
// to return an empty result set.
func TestEC2CLI_IpamDiscoveredResources(t *testing.T) {
	t.Parallel()
	ipamID, _ := ec2cliIpamExtrasFixture(t)
	discoID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-ipams", "--ipam-ids", ipamID,
		"--query", "Ipams[0].DefaultResourceDiscoveryId", "--output", "text")))
	if discoID == "" {
		t.Fatal("no resource discovery id")
	}

	accts := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-ipam-discovered-accounts",
		"--ipam-resource-discovery-id", discoID, "--discovery-region", "us-east-1",
		"--query", "length(IpamDiscoveredAccounts)", "--output", "text")))
	if accts != "0" {
		t.Fatalf("get-ipam-discovered-accounts: got %q", accts)
	}

	addrs := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-ipam-discovered-public-addresses",
		"--ipam-resource-discovery-id", discoID, "--address-region", "us-east-1",
		"--query", "length(IpamDiscoveredPublicAddresses)", "--output", "text")))
	if addrs != "0" {
		t.Fatalf("get-ipam-discovered-public-addresses: got %q", addrs)
	}

	cidrs := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-ipam-discovered-resource-cidrs",
		"--ipam-resource-discovery-id", discoID, "--resource-region", "us-east-1",
		"--query", "length(IpamDiscoveredResourceCidrs)", "--output", "text")))
	if cidrs != "0" {
		t.Fatalf("get-ipam-discovered-resource-cidrs: got %q", cidrs)
	}
}
