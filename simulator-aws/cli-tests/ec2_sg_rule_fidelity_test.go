package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_SecurityGroupRuleFidelity drives the all-traffic port omission
// (#-1 rules report no ports) and the bare referenced-sg-id (no account prefix,
// no UserId) via the aws CLI.
func TestEC2CLI_SecurityGroupRuleFidelity(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.81.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	mkSG := func(name string) string {
		return strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group",
			"--group-name", name, "--description", name, "--vpc-id", vpc,
			"--query", "GroupId", "--output", "text")))
	}
	sgAlb := mkSG("cli-fidelity-alb")
	sgTasks := mkSG("cli-fidelity-tasks")

	// Revoke the default ALLOW ALL egress rule before authorizing a test rule.
	runCLI(t, awsCLI("ec2", "revoke-security-group-egress", "--group-id", sgTasks,
		"--ip-permissions", `[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]`))
	runCLI(t, awsCLI("ec2", "authorize-security-group-egress", "--group-id", sgTasks,
		"--ip-permissions", `[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]`))
	runCLI(t, awsCLI("ec2", "authorize-security-group-ingress", "--group-id", sgTasks,
		"--ip-permissions", `[{"IpProtocol":"tcp","FromPort":3000,"ToPort":3000,"UserIdGroupPairs":[{"GroupId":"`+sgAlb+`"}]}]`))

	// #457: the all-traffic rule reports no ports (CLI renders null as "None").
	from := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+sgTasks,
		"--query", "SecurityGroupRules[?IpProtocol=='-1'].FromPort | [0]", "--output", "text")))
	if from != "None" {
		t.Fatalf("all-traffic FromPort = %q, want None", from)
	}

	// #458: referenced sg-id is bare; no UserId so the provider keeps the bare id.
	ref := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+sgTasks,
		"--query", "SecurityGroupRules[?IpProtocol=='tcp'].ReferencedGroupInfo.GroupId | [0]", "--output", "text")))
	if ref != sgAlb {
		t.Fatalf("ReferencedGroupInfo.GroupId = %q, want bare %q", ref, sgAlb)
	}
	refUser := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+sgTasks,
		"--query", "SecurityGroupRules[?IpProtocol=='tcp'].ReferencedGroupInfo.UserId | [0]", "--output", "text")))
	if refUser != "None" {
		t.Fatalf("ReferencedGroupInfo.UserId = %q, want None for same-account ref", refUser)
	}
}
