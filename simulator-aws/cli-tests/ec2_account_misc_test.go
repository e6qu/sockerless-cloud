package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_IdFormatRoundTrip exercises the account-level ID-format settings
// via the aws CLI: describe-id-format, modify-id-format,
// describe-identity-id-format, modify-identity-id-format,
// describe-aggregate-id-format, describe-principal-id-format.
func TestEC2CLI_IdFormatRoundTrip(t *testing.T) {
	useLong := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-id-format",
		"--resource", "vpc",
		"--query", "Statuses[0].UseLongIds", "--output", "text")))
	if useLong != "True" {
		t.Fatalf("describe-id-format UseLongIds: got %q, want True", useLong)
	}

	runCLI(t, awsCLI("ec2", "modify-id-format", "--resource", "instance", "--use-long-ids"))

	res := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-identity-id-format",
		"--principal-arn", "arn:aws:iam::000000000000:role/r",
		"--resource", "subnet",
		"--query", "Statuses[0].Resource", "--output", "text")))
	if res != "subnet" {
		t.Fatalf("describe-identity-id-format Resource: got %q, want subnet", res)
	}

	runCLI(t, awsCLI("ec2", "modify-identity-id-format",
		"--principal-arn", "arn:aws:iam::000000000000:role/r",
		"--resource", "subnet", "--use-long-ids"))

	agg := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-aggregate-id-format",
		"--query", "UseLongIdsAggregated", "--output", "text")))
	if agg != "True" {
		t.Fatalf("describe-aggregate-id-format UseLongIdsAggregated: got %q, want True", agg)
	}

	arn := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-principal-id-format",
		"--resources", "vpc",
		"--query", "Principals[0].Arn", "--output", "text")))
	if arn == "" || arn == "None" {
		t.Fatalf("describe-principal-id-format Arn: got %q", arn)
	}
}

// TestEC2CLI_AddressAttributeRoundTrip exercises modify-address-attribute,
// reset-address-attribute, and the EC2-Classic ↔ VPC domain transitions
// (move-address-to-vpc, restore-address-to-classic, describe-moving-addresses).
func TestEC2CLI_AddressAttributeRoundTrip(t *testing.T) {
	out := runCLI(t, awsCLI("ec2", "allocate-address", "--domain", "vpc",
		"--query", "[AllocationId,PublicIp]", "--output", "text"))
	f := strings.Fields(out)
	if len(f) != 2 {
		t.Fatalf("allocate-address: got %q", strings.TrimSpace(out))
	}
	allocID, publicIP := f[0], f[1]
	defer func() { _ = awsCLI("ec2", "release-address", "--allocation-id", allocID).Run() }()

	ptr := strings.TrimSpace(runCLI(t, awsCLI("ec2", "modify-address-attribute",
		"--allocation-id", allocID, "--domain-name", "example.com",
		"--query", "Address.PtrRecord", "--output", "text")))
	if ptr != "example.com." {
		t.Fatalf("modify-address-attribute PtrRecord: got %q, want example.com.", ptr)
	}

	gotAlloc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "reset-address-attribute",
		"--allocation-id", allocID, "--attribute", "domain-name",
		"--query", "Address.AllocationId", "--output", "text")))
	if gotAlloc != allocID {
		t.Fatalf("reset-address-attribute AllocationId: got %q, want %q", gotAlloc, allocID)
	}

	rcStatus := strings.TrimSpace(runCLI(t, awsCLI("ec2", "restore-address-to-classic",
		"--public-ip", publicIP,
		"--query", "Status", "--output", "text")))
	if rcStatus != "InClassic" {
		t.Fatalf("restore-address-to-classic Status: got %q, want InClassic", rcStatus)
	}

	movedIP := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-moving-addresses",
		"--public-ips", publicIP,
		"--query", "MovingAddressStatuses[0].PublicIp", "--output", "text")))
	if movedIP != publicIP {
		t.Fatalf("describe-moving-addresses PublicIp: got %q, want %q", movedIP, publicIP)
	}

	mvStatus := strings.TrimSpace(runCLI(t, awsCLI("ec2", "move-address-to-vpc",
		"--public-ip", publicIP,
		"--query", "Status", "--output", "text")))
	if mvStatus != "InVpc" {
		t.Fatalf("move-address-to-vpc Status: got %q, want InVpc", mvStatus)
	}
}

// TestEC2CLI_ClassicLinkRoundTrip exercises attach-classic-link-vpc and
// detach-classic-link-vpc on a real instance + VPC.
func TestEC2CLI_ClassicLinkRoundTrip(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.73.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()

	instID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "run-instances",
		"--image-id", "ami-classiclink", "--instance-type", "t3.micro",
		"--query", "Instances[0].InstanceId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "terminate-instances", "--instance-ids", instID).Run() }()

	ret := strings.TrimSpace(runCLI(t, awsCLI("ec2", "attach-classic-link-vpc",
		"--instance-id", instID, "--vpc-id", vpcID, "--groups", "sg-12345678",
		"--query", "Return", "--output", "text")))
	if ret != "True" {
		t.Fatalf("attach-classic-link-vpc Return: got %q, want True", ret)
	}

	ret = strings.TrimSpace(runCLI(t, awsCLI("ec2", "detach-classic-link-vpc",
		"--instance-id", instID, "--vpc-id", vpcID,
		"--query", "Return", "--output", "text")))
	if ret != "True" {
		t.Fatalf("detach-classic-link-vpc Return: got %q, want True", ret)
	}
}

// TestEC2CLI_EnclaveCertificateRoundTrip exercises
// associate-enclave-certificate-iam-role,
// get-associated-enclave-certificate-iam-roles, and
// disassociate-enclave-certificate-iam-role.
func TestEC2CLI_EnclaveCertificateRoundTrip(t *testing.T) {
	const certArn = "arn:aws:acm:us-east-1:000000000000:certificate/cli-1234"
	const roleArn = "arn:aws:iam::000000000000:role/cli-enclave-role"

	bucket := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-enclave-certificate-iam-role",
		"--certificate-arn", certArn, "--role-arn", roleArn,
		"--query", "CertificateS3BucketName", "--output", "text")))
	if bucket == "" || bucket == "None" {
		t.Fatalf("associate-enclave-certificate-iam-role CertificateS3BucketName: got %q", bucket)
	}

	gotRole := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-associated-enclave-certificate-iam-roles",
		"--certificate-arn", certArn,
		"--query", "AssociatedRoles[0].AssociatedRoleArn", "--output", "text")))
	if gotRole != roleArn {
		t.Fatalf("get-associated-enclave-certificate-iam-roles AssociatedRoleArn: got %q, want %q", gotRole, roleArn)
	}

	runCLI(t, awsCLI("ec2", "disassociate-enclave-certificate-iam-role",
		"--certificate-arn", certArn, "--role-arn", roleArn))
}

// TestEC2CLI_TgwConnectPeerRoundTrip exercises create/describe/delete of a
// transit-gateway Connect peer over a real Connect attachment.
func TestEC2CLI_TgwConnectPeerRoundTrip(t *testing.T) {
	tgwID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway",
		"--query", "TransitGateway.TransitGatewayId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-transit-gateway", "--transit-gateway-id", tgwID).Run() }()

	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.82.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()
	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.82.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")))

	attID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-vpc-attachment",
		"--transit-gateway-id", tgwID, "--vpc-id", vpcID, "--subnet-ids", subnetID,
		"--query", "TransitGatewayVpcAttachment.TransitGatewayAttachmentId", "--output", "text")))

	connAttID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-connect",
		"--transport-transit-gateway-attachment-id", attID,
		"--options", "Protocol=gre",
		"--query", "TransitGatewayConnect.TransitGatewayAttachmentId", "--output", "text")))

	peerID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-connect-peer",
		"--transit-gateway-attachment-id", connAttID,
		"--peer-address", "10.82.2.10",
		"--inside-cidr-blocks", "169.254.6.0/29",
		"--bgp-options", "PeerAsn=64513",
		"--query", "TransitGatewayConnectPeer.TransitGatewayConnectPeerId", "--output", "text")))
	if peerID == "" || !strings.HasPrefix(peerID, "tgw-connect-peer-") {
		t.Fatalf("create-transit-gateway-connect-peer id: got %q", peerID)
	}

	gotID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateway-connect-peers",
		"--transit-gateway-connect-peer-ids", peerID,
		"--query", "TransitGatewayConnectPeers[0].TransitGatewayConnectPeerId", "--output", "text")))
	if gotID != peerID {
		t.Fatalf("describe-transit-gateway-connect-peers id: got %q, want %q", gotID, peerID)
	}

	runCLI(t, awsCLI("ec2", "delete-transit-gateway-connect-peer",
		"--transit-gateway-connect-peer-id", peerID))
}

// TestEC2CLI_VpcPeeringOptionsRoundTrip exercises
// modify-vpc-peering-connection-options and reject-vpc-peering-connection.
func TestEC2CLI_VpcPeeringOptionsRoundTrip(t *testing.T) {
	vpcA := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.95.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcA).Run() }()
	vpcB := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.96.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcB).Run() }()

	pcxID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc-peering-connection",
		"--vpc-id", vpcA, "--peer-vpc-id", vpcB,
		"--query", "VpcPeeringConnection.VpcPeeringConnectionId", "--output", "text")))

	gotDns := strings.TrimSpace(runCLI(t, awsCLI("ec2", "modify-vpc-peering-connection-options",
		"--vpc-peering-connection-id", pcxID,
		"--requester-peering-connection-options", "AllowDnsResolutionFromRemoteVpc=true",
		"--query", "RequesterPeeringConnectionOptions.AllowDnsResolutionFromRemoteVpc", "--output", "text")))
	if gotDns != "True" {
		t.Fatalf("modify-vpc-peering-connection-options AllowDnsResolutionFromRemoteVpc: got %q, want True", gotDns)
	}

	// Reject an independent peering so the cleanup of the one above stays simple.
	vpcC := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.97.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcC).Run() }()
	pcx2ID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc-peering-connection",
		"--vpc-id", vpcA, "--peer-vpc-id", vpcC,
		"--query", "VpcPeeringConnection.VpcPeeringConnectionId", "--output", "text")))
	ret := strings.TrimSpace(runCLI(t, awsCLI("ec2", "reject-vpc-peering-connection",
		"--vpc-peering-connection-id", pcx2ID,
		"--query", "Return", "--output", "text")))
	if ret != "True" {
		t.Fatalf("reject-vpc-peering-connection Return: got %q, want True", ret)
	}

	_ = awsCLI("ec2", "delete-vpc-peering-connection", "--vpc-peering-connection-id", pcxID).Run()
}

// TestEC2CLI_NetworkAclAssociationRoundTrip exercises
// replace-network-acl-association.
func TestEC2CLI_NetworkAclAssociationRoundTrip(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.98.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID).Run() }()
	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.98.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")))

	acl1 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-acl",
		"--vpc-id", vpcID, "--query", "NetworkAcl.NetworkAclId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-network-acl", "--network-acl-id", acl1).Run() }()
	acl2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-acl",
		"--vpc-id", vpcID, "--query", "NetworkAcl.NetworkAclId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-network-acl", "--network-acl-id", acl2).Run() }()

	seedAssoc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "replace-network-acl-association",
		"--association-id", "aclassoc-seed-"+subnetID, "--network-acl-id", acl1,
		"--query", "NewAssociationId", "--output", "text")))
	if seedAssoc == "" || seedAssoc == "None" {
		t.Fatalf("seed replace-network-acl-association NewAssociationId: got %q", seedAssoc)
	}

	newAssoc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "replace-network-acl-association",
		"--association-id", seedAssoc, "--network-acl-id", acl2,
		"--query", "NewAssociationId", "--output", "text")))
	if newAssoc == "" || newAssoc == "None" || newAssoc == seedAssoc {
		t.Fatalf("replace-network-acl-association NewAssociationId: got %q", newAssoc)
	}
}

// TestEC2CLI_MiscHonestEmpty exercises the read-only / honest-empty operations
// that round out the family and that aws CLI 2.26.6 supports:
// describe-vpc-endpoint-associations, describe-elastic-gpus,
// enable-reachability-analyzer-organization-sharing.
//
// The remaining ops in this family lack an aws CLI 2.26.6 verb, so the SDK
// suite (TestEC2_MiscHonestEmptySDK / TestEC2_OutpostLagsAndFlowLogsTemplateSDK)
// owns the simulator-test-contract hook for them:
// describe-secondary-interfaces, describe-service-link-virtual-interfaces,
// describe-outpost-lags, describe-transit-gateway-metering-policies (SDK in
// TestEC2_TgwClientVpnAttachmentSDK), get-managed-resource-visibility,
// modify-managed-resource-visibility,
// get-vpc-resources-blocking-encryption-enforcement, and
// get-flow-logs-integration-template.
func TestEC2CLI_MiscHonestEmpty(t *testing.T) {
	runCLI(t, awsCLI("ec2", "describe-vpc-endpoint-associations"))
	runCLI(t, awsCLI("ec2", "describe-elastic-gpus"))
	runCLI(t, awsCLI("ec2", "enable-reachability-analyzer-organization-sharing"))
}

// TestEC2CLI_ClientVpnExportRoundTrip exercises
// export-client-vpn-client-configuration,
// import-client-vpn-client-certificate-revocation-list, and
// export-client-vpn-client-certificate-revocation-list.
func TestEC2CLI_ClientVpnExportRoundTrip(t *testing.T) {
	epID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-client-vpn-endpoint",
		"--client-cidr-block", "10.210.0.0/22",
		"--server-certificate-arn", "arn:aws:acm:us-east-1:000000000000:certificate/server",
		"--authentication-options", "Type=certificate-authentication,MutualAuthentication={ClientRootCertificateChainArn=arn:aws:acm:us-east-1:000000000000:certificate/client}",
		"--connection-log-options", "Enabled=false",
		"--query", "ClientVpnEndpointId", "--output", "text")))
	defer func() { _ = awsCLI("ec2", "delete-client-vpn-endpoint", "--client-vpn-endpoint-id", epID).Run() }()

	cfg := runCLI(t, awsCLI("ec2", "export-client-vpn-client-configuration",
		"--client-vpn-endpoint-id", epID,
		"--query", "ClientConfiguration", "--output", "text"))
	if !strings.Contains(cfg, "client") {
		t.Fatalf("export-client-vpn-client-configuration: got %q", strings.TrimSpace(cfg))
	}

	runCLI(t, awsCLI("ec2", "import-client-vpn-client-certificate-revocation-list",
		"--client-vpn-endpoint-id", epID,
		"--certificate-revocation-list", "-----BEGIN X509 CRL-----\nMIIBxzCC\n-----END X509 CRL-----\n"))

	code := strings.TrimSpace(runCLI(t, awsCLI("ec2", "export-client-vpn-client-certificate-revocation-list",
		"--client-vpn-endpoint-id", epID,
		"--query", "Status.Code", "--output", "text")))
	if code != "active" {
		t.Fatalf("export-client-vpn-client-certificate-revocation-list Status.Code: got %q, want active", code)
	}
}
