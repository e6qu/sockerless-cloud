package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_TransitGatewayCore drives the transit gateway core CRUD path over
// the aws CLI: gateway → route table → VPC attachment → association /
// propagation / routes, with tolerant teardown.
func TestEC2CLI_TransitGatewayCore(t *testing.T) {
	// --- Transit gateway ---
	tgwID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway",
		"--description", "cli-core-tgw",
		"--options", "AmazonSideAsn=64514,DnsSupport=enable,MulticastSupport=enable",
		"--tag-specifications", "ResourceType=transit-gateway,Tags=[{Key=Name,Value=cli-tgw}]",
		"--query", "TransitGateway.TransitGatewayId", "--output", "text")))
	if !strings.HasPrefix(tgwID, "tgw-") {
		t.Fatalf("expected tgw id, got %q", tgwID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway", "--transit-gateway-id", tgwID))

	assocRT := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateways",
		"--transit-gateway-ids", tgwID,
		"--query", "TransitGateways[0].Options.AssociationDefaultRouteTableId", "--output", "text")))
	if !strings.HasPrefix(assocRT, "tgw-rtb-") {
		t.Fatalf("expected default route table id, got %q", assocRT)
	}

	runCLI(t, awsCLI("ec2", "modify-transit-gateway", "--transit-gateway-id", tgwID,
		"--description", "cli-updated"))

	// --- Route table ---
	rtID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-route-table",
		"--transit-gateway-id", tgwID,
		"--query", "TransitGatewayRouteTable.TransitGatewayRouteTableId", "--output", "text")))
	if !strings.HasPrefix(rtID, "tgw-rtb-") {
		t.Fatalf("expected route table id, got %q", rtID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway-route-table", "--transit-gateway-route-table-id", rtID))

	rtState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateway-route-tables",
		"--transit-gateway-route-table-ids", rtID,
		"--query", "TransitGatewayRouteTables[0].State", "--output", "text")))
	if rtState != "available" {
		t.Fatalf("route table state = %q, want available", rtState)
	}

	// --- VPC + subnet + attachment ---
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.60.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.60.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")))

	attID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-vpc-attachment",
		"--transit-gateway-id", tgwID, "--vpc-id", vpcID, "--subnet-ids", subnetID,
		"--query", "TransitGatewayVpcAttachment.TransitGatewayAttachmentId", "--output", "text")))
	if !strings.HasPrefix(attID, "tgw-attach-") {
		t.Fatalf("expected attachment id, got %q", attID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway-vpc-attachment", "--transit-gateway-attachment-id", attID))

	attVPC := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateway-vpc-attachments",
		"--transit-gateway-attachment-ids", attID,
		"--query", "TransitGatewayVpcAttachments[0].VpcId", "--output", "text")))
	if attVPC != vpcID {
		t.Fatalf("attachment vpc = %q, want %q", attVPC, vpcID)
	}

	runCLI(t, awsCLI("ec2", "modify-transit-gateway-vpc-attachment",
		"--transit-gateway-attachment-id", attID,
		"--options", "ApplianceModeSupport=enable"))
	runCLI(t, awsCLI("ec2", "accept-transit-gateway-vpc-attachment", "--transit-gateway-attachment-id", attID))

	// Cross-type attachment listing. The list-of-structs JMESPath needs the
	// struct field (ResourceType) to assert on.
	attResType := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateway-attachments",
		"--filters", "Name=transit-gateway-id,Values="+tgwID,
		"--query", "TransitGatewayAttachments[0].ResourceType", "--output", "text")))
	if attResType != "vpc" {
		t.Fatalf("attachment resourceType = %q, want vpc", attResType)
	}

	// --- Associations + propagations ---
	runCLI(t, awsCLI("ec2", "disassociate-transit-gateway-route-table",
		"--transit-gateway-route-table-id", assocRT, "--transit-gateway-attachment-id", attID))
	runCLI(t, awsCLI("ec2", "associate-transit-gateway-route-table",
		"--transit-gateway-route-table-id", rtID, "--transit-gateway-attachment-id", attID))

	assocAtt := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-transit-gateway-route-table-associations",
		"--transit-gateway-route-table-id", rtID,
		"--query", "Associations[0].TransitGatewayAttachmentId", "--output", "text")))
	if assocAtt != attID {
		t.Fatalf("association attachment = %q, want %q", assocAtt, attID)
	}

	runCLI(t, awsCLI("ec2", "enable-transit-gateway-route-table-propagation",
		"--transit-gateway-route-table-id", rtID, "--transit-gateway-attachment-id", attID))
	propAtt := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-transit-gateway-route-table-propagations",
		"--transit-gateway-route-table-id", rtID,
		"--query", "TransitGatewayRouteTablePropagations[0].TransitGatewayAttachmentId", "--output", "text")))
	if propAtt != attID {
		t.Fatalf("propagation attachment = %q, want %q", propAtt, attID)
	}
	// The attachment auto-propagated to the gateway's default propagation route
	// table at create time, plus rtID via the explicit Enable above — filter the
	// list-of-structs for rtID rather than assuming an ordering.
	attPropRT := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-transit-gateway-attachment-propagations",
		"--transit-gateway-attachment-id", attID,
		"--query", "TransitGatewayAttachmentPropagations[?TransitGatewayRouteTableId=='"+rtID+"'].TransitGatewayRouteTableId | [0]",
		"--output", "text")))
	if attPropRT != rtID {
		t.Fatalf("attachment propagation route table = %q, want %q", attPropRT, rtID)
	}
	runCLI(t, awsCLI("ec2", "disable-transit-gateway-route-table-propagation",
		"--transit-gateway-route-table-id", rtID, "--transit-gateway-attachment-id", attID))

	// --- Routes ---
	routeType := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-route",
		"--transit-gateway-route-table-id", rtID, "--destination-cidr-block", "10.88.0.0/16",
		"--transit-gateway-attachment-id", attID,
		"--query", "Route.Type", "--output", "text")))
	if routeType != "static" {
		t.Fatalf("route type = %q, want static", routeType)
	}
	runCLI(t, awsCLI("ec2", "replace-transit-gateway-route",
		"--transit-gateway-route-table-id", rtID, "--destination-cidr-block", "10.88.0.0/16", "--blackhole"))
	searchCidr := strings.TrimSpace(runCLI(t, awsCLI("ec2", "search-transit-gateway-routes",
		"--transit-gateway-route-table-id", rtID, "--filters", "Name=type,Values=static",
		"--query", "Routes[0].DestinationCidrBlock", "--output", "text")))
	if searchCidr != "10.88.0.0/16" {
		t.Fatalf("search route cidr = %q, want 10.88.0.0/16", searchCidr)
	}
	runCLI(t, awsCLI("ec2", "delete-transit-gateway-route",
		"--transit-gateway-route-table-id", rtID, "--destination-cidr-block", "10.88.0.0/16"))

	s3loc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "export-transit-gateway-routes",
		"--transit-gateway-route-table-id", rtID, "--s3-bucket", "cli-bucket",
		"--query", "S3Location", "--output", "text")))
	if !strings.HasPrefix(s3loc, "s3://cli-bucket") {
		t.Fatalf("export s3 location = %q", s3loc)
	}

	runCLIIgnore(awsCLI("ec2", "reject-transit-gateway-vpc-attachment", "--transit-gateway-attachment-id", attID))
}

// TestEC2CLI_TransitGatewayExtras covers prefix-list references, connect
// attachments, multicast domains, and peering attachments over the aws CLI.
func TestEC2CLI_TransitGatewayExtras(t *testing.T) {
	tgwID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway",
		"--options", "MulticastSupport=enable",
		"--query", "TransitGateway.TransitGatewayId", "--output", "text")))
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway", "--transit-gateway-id", tgwID))
	assocRT := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateways",
		"--transit-gateway-ids", tgwID,
		"--query", "TransitGateways[0].Options.AssociationDefaultRouteTableId", "--output", "text")))

	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.61.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.61.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))
	attID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-vpc-attachment",
		"--transit-gateway-id", tgwID, "--vpc-id", vpcID, "--subnet-ids", subnetID,
		"--query", "TransitGatewayVpcAttachment.TransitGatewayAttachmentId", "--output", "text")))
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway-vpc-attachment", "--transit-gateway-attachment-id", attID))

	// --- Prefix list references ---
	plID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-managed-prefix-list",
		"--prefix-list-name", "cli-tgw-pl", "--max-entries", "5", "--address-family", "IPv4",
		"--query", "PrefixList.PrefixListId", "--output", "text")))
	refPL := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-prefix-list-reference",
		"--transit-gateway-route-table-id", assocRT, "--prefix-list-id", plID,
		"--transit-gateway-attachment-id", attID,
		"--query", "TransitGatewayPrefixListReference.PrefixListId", "--output", "text")))
	if refPL != plID {
		t.Fatalf("prefix list reference id = %q, want %q", refPL, plID)
	}
	getRefPL := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-transit-gateway-prefix-list-references",
		"--transit-gateway-route-table-id", assocRT,
		"--query", "TransitGatewayPrefixListReferences[0].PrefixListId", "--output", "text")))
	if getRefPL != plID {
		t.Fatalf("get prefix list reference id = %q, want %q", getRefPL, plID)
	}
	runCLI(t, awsCLI("ec2", "modify-transit-gateway-prefix-list-reference",
		"--transit-gateway-route-table-id", assocRT, "--prefix-list-id", plID, "--blackhole"))
	runCLI(t, awsCLI("ec2", "delete-transit-gateway-prefix-list-reference",
		"--transit-gateway-route-table-id", assocRT, "--prefix-list-id", plID))

	// --- Connect ---
	connID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-connect",
		"--transport-transit-gateway-attachment-id", attID, "--options", "Protocol=gre",
		"--query", "TransitGatewayConnect.TransitGatewayAttachmentId", "--output", "text")))
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway-connect", "--transit-gateway-attachment-id", connID))
	connTransport := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateway-connects",
		"--transit-gateway-attachment-ids", connID,
		"--query", "TransitGatewayConnects[0].TransportTransitGatewayAttachmentId", "--output", "text")))
	if connTransport != attID {
		t.Fatalf("connect transport = %q, want %q", connTransport, attID)
	}

	// --- Multicast domain ---
	mcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-multicast-domain",
		"--transit-gateway-id", tgwID, "--options", "Igmpv2Support=enable",
		"--query", "TransitGatewayMulticastDomain.TransitGatewayMulticastDomainId", "--output", "text")))
	if !strings.HasPrefix(mcID, "tgw-mcast-domain-") {
		t.Fatalf("expected multicast domain id, got %q", mcID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway-multicast-domain", "--transit-gateway-multicast-domain-id", mcID))
	mcState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateway-multicast-domains",
		"--transit-gateway-multicast-domain-ids", mcID,
		"--query", "TransitGatewayMulticastDomains[0].State", "--output", "text")))
	if mcState != "available" {
		t.Fatalf("multicast domain state = %q, want available", mcState)
	}

	// --- Peering ---
	peerTGW := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway",
		"--query", "TransitGateway.TransitGatewayId", "--output", "text")))
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway", "--transit-gateway-id", peerTGW))
	peerID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-transit-gateway-peering-attachment",
		"--transit-gateway-id", tgwID, "--peer-transit-gateway-id", peerTGW,
		"--peer-account-id", "123456789012", "--peer-region", "us-west-2",
		"--query", "TransitGatewayPeeringAttachment.TransitGatewayAttachmentId", "--output", "text")))
	defer runCLIIgnore(awsCLI("ec2", "delete-transit-gateway-peering-attachment", "--transit-gateway-attachment-id", peerID))
	peerState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-transit-gateway-peering-attachments",
		"--transit-gateway-attachment-ids", peerID,
		"--query", "TransitGatewayPeeringAttachments[0].State", "--output", "text")))
	if peerState != "pendingAcceptance" {
		t.Fatalf("peering state = %q, want pendingAcceptance", peerState)
	}
	acceptState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "accept-transit-gateway-peering-attachment",
		"--transit-gateway-attachment-id", peerID,
		"--query", "TransitGatewayPeeringAttachment.State", "--output", "text")))
	if acceptState != "available" {
		t.Fatalf("accepted peering state = %q, want available", acceptState)
	}
	runCLIIgnore(awsCLI("ec2", "reject-transit-gateway-peering-attachment", "--transit-gateway-attachment-id", peerID))
}
