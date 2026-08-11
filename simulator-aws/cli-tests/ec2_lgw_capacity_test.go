package aws_cli_test

import (
	"strings"
	"testing"
)

// Capacity Manager operations (enable-capacity-manager,
// get-capacity-manager-attributes, *-data-export, get-capacity-manager-metric-*,
// *-monitored-tag-keys, update-capacity-manager-organizations-access) are absent
// from aws CLI 2.26.6 (a newer EC2 feature), so they are exercised only through
// the SDK suite (TestEC2_CapacityManager), which satisfies the testing contract
// hook for those ops. The Local Gateway, Declarative Policies, and AWS Network
// Performance families are present in the CLI and covered below.

// TestEC2CLI_LocalGateway drives the AWS Outposts local gateway control plane
// over the aws CLI: discover the seeded gateway, then route-table,
// virtual-interface-group, virtual-interface, and the VPC + virtual-interface-
// group associations.
func TestEC2CLI_LocalGateway(t *testing.T) {
	lgwID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-local-gateways",
		"--query", "LocalGateways[0].LocalGatewayId", "--output", "text")))
	if !strings.HasPrefix(lgwID, "lgw-") {
		t.Fatalf("expected a seeded local gateway id, got %q", lgwID)
	}

	rtID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-local-gateway-route-table",
		"--local-gateway-id", lgwID, "--mode", "direct-vpc-routing",
		"--query", "LocalGatewayRouteTable.LocalGatewayRouteTableId", "--output", "text")))
	if !strings.HasPrefix(rtID, "lgw-rtb-") {
		t.Fatalf("expected lgw route table id, got %q", rtID)
	}
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("ec2", "delete-local-gateway-route-table", "--local-gateway-route-table-id", rtID))
	})

	descRT := runCLI(t, awsCLI("ec2", "describe-local-gateway-route-tables",
		"--local-gateway-route-table-ids", rtID,
		"--query", "LocalGatewayRouteTables[0].LocalGatewayRouteTableId", "--output", "text"))
	if !strings.Contains(descRT, rtID) {
		t.Fatalf("expected describe to return %q, got %q", rtID, descRT)
	}

	// create/delete-local-gateway-virtual-interface[-group] are absent from aws
	// CLI 2.26.6 (only the describe variants exist), so those creates are
	// exercised through the SDK suite (TestEC2_LocalGateway). A real Outpost
	// local gateway ships with its virtual interface group already provisioned,
	// so describe returns one here, which the VIG-association below uses.
	vifID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-local-gateway-virtual-interfaces",
		"--query", "LocalGatewayVirtualInterfaces[0].LocalGatewayVirtualInterfaceId", "--output", "text")))
	if !strings.HasPrefix(vifID, "lgw-vif-") {
		t.Fatalf("expected a seeded lgw vif id, got %q", vifID)
	}
	descVif := runCLI(t, awsCLI("ec2", "describe-local-gateway-virtual-interfaces",
		"--local-gateway-virtual-interface-ids", vifID,
		"--query", "LocalGatewayVirtualInterfaces[0].Vlan", "--output", "text"))
	if strings.TrimSpace(descVif) == "" || descVif == "None\n" {
		t.Fatalf("expected a vlan in vif describe, got %q", descVif)
	}

	vigID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-local-gateway-virtual-interface-groups",
		"--query", "LocalGatewayVirtualInterfaceGroups[0].LocalGatewayVirtualInterfaceGroupId", "--output", "text")))
	if !strings.HasPrefix(vigID, "lgw-vif-grp-") {
		t.Fatalf("expected a seeded lgw vif group id, got %q", vigID)
	}
	descVig := runCLI(t, awsCLI("ec2", "describe-local-gateway-virtual-interface-groups",
		"--local-gateway-virtual-interface-group-ids", vigID,
		"--query", "LocalGatewayVirtualInterfaceGroups[0].LocalGatewayVirtualInterfaceIds", "--output", "text"))
	if !strings.Contains(descVig, vifID) {
		t.Fatalf("expected vif %q in group id set, got %q", vifID, descVig)
	}

	// VPC association.
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.81.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("ec2", "delete-vpc", "--vpc-id", vpcID)) })

	vpcAssocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-local-gateway-route-table-vpc-association",
		"--local-gateway-route-table-id", rtID, "--vpc-id", vpcID,
		"--query", "LocalGatewayRouteTableVpcAssociation.LocalGatewayRouteTableVpcAssociationId", "--output", "text")))
	if !strings.HasPrefix(vpcAssocID, "lgw-vpc-assoc-") {
		t.Fatalf("expected lgw vpc assoc id, got %q", vpcAssocID)
	}
	descVpcAssoc := runCLI(t, awsCLI("ec2", "describe-local-gateway-route-table-vpc-associations",
		"--local-gateway-route-table-vpc-association-ids", vpcAssocID,
		"--query", "LocalGatewayRouteTableVpcAssociations[0].VpcId", "--output", "text"))
	if !strings.Contains(descVpcAssoc, vpcID) {
		t.Fatalf("expected vpc %q in association, got %q", vpcID, descVpcAssoc)
	}
	runCLI(t, awsCLI("ec2", "delete-local-gateway-route-table-vpc-association",
		"--local-gateway-route-table-vpc-association-id", vpcAssocID))

	// Virtual interface group association.
	vigAssocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-local-gateway-route-table-virtual-interface-group-association",
		"--local-gateway-route-table-id", rtID,
		"--local-gateway-virtual-interface-group-id", vigID,
		"--query", "LocalGatewayRouteTableVirtualInterfaceGroupAssociation.LocalGatewayRouteTableVirtualInterfaceGroupAssociationId", "--output", "text")))
	if !strings.HasPrefix(vigAssocID, "lgw-vif-grp-assoc-") {
		t.Fatalf("expected lgw vif group assoc id, got %q", vigAssocID)
	}
	descVigAssoc := runCLI(t, awsCLI("ec2", "describe-local-gateway-route-table-virtual-interface-group-associations",
		"--local-gateway-route-table-virtual-interface-group-association-ids", vigAssocID,
		"--query", "LocalGatewayRouteTableVirtualInterfaceGroupAssociations[0].LocalGatewayVirtualInterfaceGroupId", "--output", "text"))
	if !strings.Contains(descVigAssoc, vigID) {
		t.Fatalf("expected vif group %q in association, got %q", vigID, descVigAssoc)
	}
	runCLI(t, awsCLI("ec2", "delete-local-gateway-route-table-virtual-interface-group-association",
		"--local-gateway-route-table-virtual-interface-group-association-id", vigAssocID))
}

// TestEC2CLI_DeclarativePolicies drives the declarative-policies report
// lifecycle over the aws CLI: start → describe (running → complete) → summary,
// plus the cancel path.
func TestEC2CLI_DeclarativePolicies(t *testing.T) {
	reportID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "start-declarative-policies-report",
		"--s3-bucket", "my-dp-report-bucket", "--s3-prefix", "reports/", "--target-id", "r-abcd",
		"--query", "ReportId", "--output", "text")))
	if reportID == "" {
		t.Fatalf("expected a report id")
	}

	descStatus := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-declarative-policies-reports",
		"--report-ids", reportID,
		"--query", "Reports[0].Status", "--output", "text")))
	if descStatus != "complete" {
		t.Fatalf("expected report status complete, got %q", descStatus)
	}

	summaryTarget := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-declarative-policies-report-summary",
		"--report-id", reportID,
		"--query", "TargetId", "--output", "text")))
	if summaryTarget != "r-abcd" {
		t.Fatalf("expected summary target r-abcd, got %q", summaryTarget)
	}

	report2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "start-declarative-policies-report",
		"--s3-bucket", "my-dp-report-bucket", "--target-id", "ou-1234",
		"--query", "ReportId", "--output", "text")))
	cancelRet := strings.TrimSpace(runCLI(t, awsCLI("ec2", "cancel-declarative-policies-report",
		"--report-id", report2, "--query", "Return", "--output", "text")))
	if cancelRet != "True" {
		t.Fatalf("expected cancel Return True, got %q", cancelRet)
	}
}

// TestEC2CLI_NetworkPerformance drives the AWS Network Performance metric
// subscription lifecycle and the GetAwsNetworkPerformanceData read over the CLI.
func TestEC2CLI_NetworkPerformance(t *testing.T) {
	enOut := strings.TrimSpace(runCLI(t, awsCLI("ec2", "enable-aws-network-performance-metric-subscription",
		"--source", "us-east-1", "--destination", "eu-west-1",
		"--metric", "aggregate-latency", "--statistic", "p50",
		"--query", "Output", "--output", "text")))
	if enOut != "True" {
		t.Fatalf("expected enable Output True, got %q", enOut)
	}
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("ec2", "disable-aws-network-performance-metric-subscription",
			"--source", "us-east-1", "--destination", "eu-west-1",
			"--metric", "aggregate-latency", "--statistic", "p50"))
	})

	descSrc := runCLI(t, awsCLI("ec2", "describe-aws-network-performance-metric-subscriptions",
		"--query", "Subscriptions[?Source=='us-east-1'].Destination", "--output", "text"))
	if !strings.Contains(descSrc, "eu-west-1") {
		t.Fatalf("expected eu-west-1 destination in subscriptions, got %q", descSrc)
	}

	status := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-aws-network-performance-data",
		"--start-time", "2024-01-01T00:00:00Z", "--end-time", "2024-01-01T00:30:00Z",
		"--data-queries", "Id=q1,Source=us-east-1,Destination=eu-west-1,Metric=aggregate-latency,Statistic=p50,Period=five-minutes",
		"--query", "DataResponses[0].MetricPoints[0].Status", "--output", "text")))
	if status != "OK" {
		t.Fatalf("expected metric point status OK, got %q", status)
	}
}
