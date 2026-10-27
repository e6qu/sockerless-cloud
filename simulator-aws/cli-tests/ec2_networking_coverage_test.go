package aws_cli_test

import (
	"strings"
	"testing"
)

func TestEC2CLI_RouteTableAssociationLifecycle(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.114.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sub := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.114.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))
	rt := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-route-table", "--vpc-id", vpc, "--query", "RouteTable.RouteTableId", "--output", "text")))

	assoc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-route-table", "--route-table-id", rt, "--subnet-id", sub, "--query", "AssociationId", "--output", "text")))
	got := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-route-tables", "--route-table-ids", rt,
		"--query", "RouteTables[0].Associations[?SubnetId=='"+sub+"'].RouteTableAssociationId | [0]", "--output", "text")))
	if got != assoc {
		t.Fatalf("associate-route-table association = %q, want %q", got, assoc)
	}

	if out := runCLIExpectError(t, awsCLI("ec2", "delete-route-table", "--route-table-id", rt)); !strings.Contains(out, "DependencyViolation") {
		t.Fatalf("delete-route-table on an associated table did not answer DependencyViolation: %s", out)
	}
	runCLI(t, awsCLI("ec2", "disassociate-route-table", "--association-id", assoc))
	count := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-route-tables", "--route-table-ids", rt,
		"--query", "length(RouteTables[0].Associations[?SubnetId=='"+sub+"'])", "--output", "text")))
	if count != "0" {
		t.Fatalf("associations for subnet after disassociate = %q, want 0", count)
	}
	runCLI(t, awsCLI("ec2", "delete-route-table", "--route-table-id", rt))
}

func TestEC2CLI_InternetGatewayAttachDetachDelete(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.115.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	igw := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-internet-gateway", "--query", "InternetGateway.InternetGatewayId", "--output", "text")))

	runCLI(t, awsCLI("ec2", "attach-internet-gateway", "--internet-gateway-id", igw, "--vpc-id", vpc))
	attachedVpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-internet-gateways", "--internet-gateway-ids", igw,
		"--query", "InternetGateways[0].Attachments[0].VpcId", "--output", "text")))
	if attachedVpc != vpc {
		t.Fatalf("attached VpcId = %q, want %q", attachedVpc, vpc)
	}

	runCLI(t, awsCLI("ec2", "detach-internet-gateway", "--internet-gateway-id", igw, "--vpc-id", vpc))
	count := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-internet-gateways", "--internet-gateway-ids", igw,
		"--query", "length(InternetGateways[0].Attachments)", "--output", "text")))
	if count != "0" {
		t.Fatalf("attachments after detach = %q, want 0", count)
	}
	runCLI(t, awsCLI("ec2", "delete-internet-gateway", "--internet-gateway-id", igw))
}

func TestEC2CLI_VpcAndSubnetAttributes(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.116.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sub := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.116.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))

	runCLI(t, awsCLI("ec2", "modify-vpc-attribute", "--vpc-id", vpc, "--enable-dns-hostnames"))
	dns := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-attribute", "--vpc-id", vpc, "--attribute", "enableDnsHostnames",
		"--query", "EnableDnsHostnames.Value", "--output", "text")))
	if dns != "True" {
		t.Fatalf("enableDnsHostnames after modify = %q, want True", dns)
	}

	runCLI(t, awsCLI("ec2", "modify-subnet-attribute", "--subnet-id", sub, "--map-public-ip-on-launch"))
	mp := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-subnets", "--subnet-ids", sub,
		"--query", "Subnets[0].MapPublicIpOnLaunch", "--output", "text")))
	if mp != "True" {
		t.Fatalf("MapPublicIpOnLaunch after modify = %q, want True", mp)
	}
}

func TestEC2CLI_RevokeSecurityGroupRules(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.117.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sg := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group", "--group-name", "cli-revoke-cov", "--description", "r", "--vpc-id", vpc, "--query", "GroupId", "--output", "text")))

	ingress := `[{"IpProtocol":"tcp","FromPort":22,"ToPort":22,"IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]`
	runCLI(t, awsCLI("ec2", "authorize-security-group-ingress", "--group-id", sg, "--ip-permissions", ingress))
	runCLI(t, awsCLI("ec2", "revoke-security-group-ingress", "--group-id", sg, "--ip-permissions", ingress))

	out := runCLIExpectError(t, awsCLI("ec2", "revoke-security-group-ingress", "--group-id", sg, "--ip-permissions", ingress))
	if !strings.Contains(out, "InvalidPermission.NotFound") {
		t.Fatalf("re-revoke ingress error = %q, want InvalidPermission.NotFound", out)
	}

	// Amazon EC2 creates a VPC security group with an allow-all egress rule.
	egress := `[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]`
	runCLI(t, awsCLI("ec2", "revoke-security-group-egress", "--group-id", sg, "--ip-permissions", egress))

	out = runCLIExpectError(t, awsCLI("ec2", "revoke-security-group-egress", "--group-id", sg, "--ip-permissions", egress))
	if !strings.Contains(out, "InvalidPermission.NotFound") {
		t.Fatalf("re-revoke egress error = %q, want InvalidPermission.NotFound", out)
	}

	ingressCount := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-groups", "--group-ids", sg,
		"--query", "length(SecurityGroups[0].IpPermissions)", "--output", "text")))
	if ingressCount != "0" {
		t.Fatalf("ingress permissions after revoke = %q, want 0", ingressCount)
	}
}

func TestEC2CLI_RevokeSecurityGroupRulesByID(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.118.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sg := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group", "--group-name", "cli-revoke-by-id", "--description", "r", "--vpc-id", vpc, "--query", "GroupId", "--output", "text")))

	ruleID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "authorize-security-group-ingress",
		"--group-id", sg,
		"--ip-permissions", `[{"IpProtocol":"tcp","FromPort":443,"ToPort":443,"IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]`,
		"--query", "SecurityGroupRules[0].SecurityGroupRuleId", "--output", "text")))
	if ruleID == "" {
		t.Fatal("expected a non-empty security group rule id")
	}

	runCLI(t, awsCLI("ec2", "revoke-security-group-ingress", "--group-id", sg, "--security-group-rule-ids", ruleID))

	count := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-rules",
		"--security-group-rule-ids", ruleID,
		"--query", "length(SecurityGroupRules)", "--output", "text")))
	if count != "0" {
		t.Fatalf("rule count after revoke-by-id = %q, want 0", count)
	}

	runCLI(t, awsCLI("ec2", "revoke-security-group-ingress", "--group-id", sg, "--security-group-rule-ids", ruleID))

	egressRuleID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-rules",
		"--filters", `Name=group-id,Values=`+sg, `Name=egress,Values=true`,
		"--query", "SecurityGroupRules[0].SecurityGroupRuleId", "--output", "text")))
	if egressRuleID == "" {
		t.Fatal("expected a non-empty default egress rule id")
	}

	runCLI(t, awsCLI("ec2", "revoke-security-group-egress", "--group-id", sg, "--security-group-rule-ids", egressRuleID))

	egressCount := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-groups", "--group-ids", sg,
		"--query", "length(SecurityGroups[0].IpPermissionsEgress)", "--output", "text")))
	if egressCount != "0" {
		t.Fatalf("egress permissions after revoke-by-id = %q, want 0", egressCount)
	}
}
