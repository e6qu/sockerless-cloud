package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_NetworkInsights drives the Reachability Analyzer and Network
// Access Analyzer families over the aws CLI: path → analysis, and access
// scope → content → scope analysis → findings, with tolerant teardown.
func TestEC2CLI_NetworkInsights(t *testing.T) {
	// --- Reachability Analyzer path + analysis ---
	pathID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-insights-path",
		"--source", "eni-cli-source", "--destination", "eni-cli-destination",
		"--protocol", "tcp", "--destination-port", "443",
		"--tag-specifications", "ResourceType=network-insights-path,Tags=[{Key=Name,Value=nip-cli}]",
		"--query", "NetworkInsightsPath.NetworkInsightsPathId", "--output", "text")))
	if !strings.HasPrefix(pathID, "nip-") {
		t.Fatalf("expected nip id, got %q", pathID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-network-insights-path", "--network-insights-path-id", pathID))

	descPort := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-network-insights-paths",
		"--network-insights-path-ids", pathID,
		"--query", "NetworkInsightsPaths[0].DestinationPort", "--output", "text")))
	if descPort != "443" {
		t.Fatalf("destination port = %q, want 443", descPort)
	}

	anaID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "start-network-insights-analysis",
		"--network-insights-path-id", pathID,
		"--query", "NetworkInsightsAnalysis.NetworkInsightsAnalysisId", "--output", "text")))
	if !strings.HasPrefix(anaID, "nia-") {
		t.Fatalf("expected nia id, got %q", anaID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-network-insights-analysis", "--network-insights-analysis-id", anaID))

	anaStatus := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-network-insights-analyses",
		"--network-insights-analysis-ids", anaID,
		"--query", "NetworkInsightsAnalyses[0].Status", "--output", "text")))
	if anaStatus != "succeeded" {
		t.Fatalf("analysis status = %q, want succeeded", anaStatus)
	}

	// --- Network Access Analyzer access scope ---
	scopeID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-insights-access-scope",
		"--tag-specifications", "ResourceType=network-insights-access-scope,Tags=[{Key=Name,Value=nis-cli}]",
		"--query", "NetworkInsightsAccessScope.NetworkInsightsAccessScopeId", "--output", "text")))
	if !strings.HasPrefix(scopeID, "nis-") {
		t.Fatalf("expected nis id, got %q", scopeID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-network-insights-access-scope", "--network-insights-access-scope-id", scopeID))

	contentScope := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-network-insights-access-scope-content",
		"--network-insights-access-scope-id", scopeID,
		"--query", "NetworkInsightsAccessScopeContent.NetworkInsightsAccessScopeId", "--output", "text")))
	if contentScope != scopeID {
		t.Fatalf("content scope id = %q, want %q", contentScope, scopeID)
	}

	scopeAnaID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "start-network-insights-access-scope-analysis",
		"--network-insights-access-scope-id", scopeID,
		"--query", "NetworkInsightsAccessScopeAnalysis.NetworkInsightsAccessScopeAnalysisId", "--output", "text")))
	if !strings.HasPrefix(scopeAnaID, "nisa-") {
		t.Fatalf("expected nisa id, got %q", scopeAnaID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-network-insights-access-scope-analysis", "--network-insights-access-scope-analysis-id", scopeAnaID))

	scopeAnaStatus := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-network-insights-access-scope-analyses",
		"--network-insights-access-scope-analysis-ids", scopeAnaID,
		"--query", "NetworkInsightsAccessScopeAnalyses[0].Status", "--output", "text")))
	if scopeAnaStatus != "succeeded" {
		t.Fatalf("scope analysis status = %q, want succeeded", scopeAnaStatus)
	}

	findingStatus := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-network-insights-access-scope-analysis-findings",
		"--network-insights-access-scope-analysis-id", scopeAnaID,
		"--query", "AnalysisStatus", "--output", "text")))
	if findingStatus != "succeeded" {
		t.Fatalf("findings analysis status = %q, want succeeded", findingStatus)
	}
}

// TestEC2CLI_RouteServer drives the route-server graph over the aws CLI:
// route server → association → endpoint → peer → propagation → routing
// database, with tolerant teardown.
func TestEC2CLI_RouteServer(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.71.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.71.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")))

	rsID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-route-server",
		"--amazon-side-asn", "65010",
		"--tag-specifications", "ResourceType=route-server,Tags=[{Key=Name,Value=rs-cli}]",
		"--query", "RouteServer.RouteServerId", "--output", "text")))
	if !strings.HasPrefix(rsID, "rs-") {
		t.Fatalf("expected rs id, got %q", rsID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-route-server", "--route-server-id", rsID))

	rsState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-route-servers",
		"--route-server-ids", rsID, "--query", "RouteServers[0].State", "--output", "text")))
	if rsState != "available" {
		t.Fatalf("route server state = %q, want available", rsState)
	}

	runCLI(t, awsCLI("ec2", "modify-route-server", "--route-server-id", rsID, "--persist-routes", "enable"))

	assocState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-route-server",
		"--route-server-id", rsID, "--vpc-id", vpcID,
		"--query", "RouteServerAssociation.State", "--output", "text")))
	if assocState != "associated" {
		t.Fatalf("association state = %q, want associated", assocState)
	}
	defer runCLIIgnore(awsCLI("ec2", "disassociate-route-server", "--route-server-id", rsID, "--vpc-id", vpcID))

	getAssocVpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-route-server-associations",
		"--route-server-id", rsID, "--query", "RouteServerAssociations[0].VpcId", "--output", "text")))
	if getAssocVpc != vpcID {
		t.Fatalf("association vpc = %q, want %q", getAssocVpc, vpcID)
	}

	epID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-route-server-endpoint",
		"--route-server-id", rsID, "--subnet-id", subnetID,
		"--query", "RouteServerEndpoint.RouteServerEndpointId", "--output", "text")))
	if !strings.HasPrefix(epID, "rse-") {
		t.Fatalf("expected rse id, got %q", epID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-route-server-endpoint", "--route-server-endpoint-id", epID))

	epSubnet := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-route-server-endpoints",
		"--route-server-endpoint-ids", epID, "--query", "RouteServerEndpoints[0].SubnetId", "--output", "text")))
	if epSubnet != subnetID {
		t.Fatalf("endpoint subnet = %q, want %q", epSubnet, subnetID)
	}

	peerID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-route-server-peer",
		"--route-server-endpoint-id", epID, "--peer-address", "10.71.1.30",
		"--bgp-options", "PeerAsn=65011",
		"--query", "RouteServerPeer.RouteServerPeerId", "--output", "text")))
	if !strings.HasPrefix(peerID, "rsp-") {
		t.Fatalf("expected rsp id, got %q", peerID)
	}
	defer runCLIIgnore(awsCLI("ec2", "delete-route-server-peer", "--route-server-peer-id", peerID))

	peerAddr := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-route-server-peers",
		"--route-server-peer-ids", peerID, "--query", "RouteServerPeers[0].PeerAddress", "--output", "text")))
	if peerAddr != "10.71.1.30" {
		t.Fatalf("peer address = %q, want 10.71.1.30", peerAddr)
	}

	rtID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-route-table",
		"--vpc-id", vpcID, "--query", "RouteTable.RouteTableId", "--output", "text")))

	propRT := strings.TrimSpace(runCLI(t, awsCLI("ec2", "enable-route-server-propagation",
		"--route-server-id", rsID, "--route-table-id", rtID,
		"--query", "RouteServerPropagation.RouteTableId", "--output", "text")))
	if propRT != rtID {
		t.Fatalf("propagation route table = %q, want %q", propRT, rtID)
	}
	defer runCLIIgnore(awsCLI("ec2", "disable-route-server-propagation", "--route-server-id", rsID, "--route-table-id", rtID))

	getPropRT := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-route-server-propagations",
		"--route-server-id", rsID, "--query", "RouteServerPropagations[0].RouteTableId", "--output", "text")))
	if getPropRT != rtID {
		t.Fatalf("get propagation route table = %q, want %q", getPropRT, rtID)
	}

	persisted := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-route-server-routing-database",
		"--route-server-id", rsID, "--query", "AreRoutesPersisted", "--output", "text")))
	if persisted != "True" {
		t.Fatalf("AreRoutesPersisted = %q, want True", persisted)
	}
}

// TestEC2CLI_LocalGatewayRoute drives the local-gateway-route CRUD path over
// the aws CLI: create → search → modify → delete.
func TestEC2CLI_LocalGatewayRoute(t *testing.T) {
	rtID := "lgw-rtb-0fedcba9876543210"

	cidr := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-local-gateway-route",
		"--local-gateway-route-table-id", rtID,
		"--destination-cidr-block", "192.168.20.0/24",
		"--local-gateway-virtual-interface-group-id", "lgw-vif-grp-0123456789abcdef0",
		"--query", "Route.DestinationCidrBlock", "--output", "text")))
	if cidr != "192.168.20.0/24" {
		t.Fatalf("created route cidr = %q, want 192.168.20.0/24", cidr)
	}

	searchType := strings.TrimSpace(runCLI(t, awsCLI("ec2", "search-local-gateway-routes",
		"--local-gateway-route-table-id", rtID,
		"--filters", "Name=type,Values=static",
		"--query", "Routes[?DestinationCidrBlock=='192.168.20.0/24'].Type | [0]", "--output", "text")))
	if searchType != "static" {
		t.Fatalf("searched route type = %q, want static", searchType)
	}

	modEni := strings.TrimSpace(runCLI(t, awsCLI("ec2", "modify-local-gateway-route",
		"--local-gateway-route-table-id", rtID,
		"--destination-cidr-block", "192.168.20.0/24",
		"--network-interface-id", "eni-0fedcba9876543210",
		"--query", "Route.NetworkInterfaceId", "--output", "text")))
	if modEni != "eni-0fedcba9876543210" {
		t.Fatalf("modified route eni = %q, want eni-0fedcba9876543210", modEni)
	}

	delState := strings.TrimSpace(runCLI(t, awsCLI("ec2", "delete-local-gateway-route",
		"--local-gateway-route-table-id", rtID,
		"--destination-cidr-block", "192.168.20.0/24",
		"--query", "Route.State", "--output", "text")))
	if delState != "deleted" {
		t.Fatalf("deleted route state = %q, want deleted", delState)
	}
}
