package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_ModifySecurityGroupRules drives an in-place security-group-rule
// update via the aws CLI: authorize a rule, find its id, then modify it.
func TestEC2CLI_ModifySecurityGroupRules(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.72.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sgID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group",
		"--group-name", "cli-modify-sgr", "--description", "cli", "--vpc-id", vpcID,
		"--query", "GroupId", "--output", "text")))
	runCLI(t, awsCLI("ec2", "authorize-security-group-ingress",
		"--group-id", sgID, "--protocol", "tcp", "--port", "80", "--cidr", "0.0.0.0/0"))

	ruleID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+sgID,
		"--query", "SecurityGroupRules[0].SecurityGroupRuleId", "--output", "text")))
	if !strings.HasPrefix(ruleID, "sgr-") {
		t.Fatalf("expected an sgr- rule id, got %q", ruleID)
	}

	runCLI(t, awsCLI("ec2", "modify-security-group-rules",
		"--group-id", sgID,
		"--security-group-rules", "SecurityGroupRuleId="+ruleID+",SecurityGroupRule={IpProtocol=tcp,FromPort=443,ToPort=443,CidrIpv4=10.0.0.0/8,Description=cli-updated}"))

	desc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-rules",
		"--security-group-rule-ids", ruleID,
		"--query", "SecurityGroupRules[0].{Desc:Description,From:FromPort,Cidr:CidrIpv4}", "--output", "text")))
	if !strings.Contains(desc, "cli-updated") || !strings.Contains(desc, "443") || !strings.Contains(desc, "10.0.0.0/8") {
		t.Fatalf("modify-security-group-rules did not take effect: %q", desc)
	}
}
