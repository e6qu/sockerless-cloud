package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_SecurityGroupRuleFidelity expects an all-traffic (-1) rule to
// report no ports, and a referenced group as a bare sg- id with no UserId.
func TestEC2CLI_SecurityGroupRuleFidelity(t *testing.T) {
	t.Parallel()
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.81.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	mkSG := func(name string) string {
		return strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group",
			"--group-name", name, "--description", name, "--vpc-id", vpc,
			"--query", "GroupId", "--output", "text")))
	}
	sgAlb := mkSG("cli-fidelity-alb")
	sgTasks := mkSG("cli-fidelity-tasks")

	// Revoke the default allow-all egress rule so the test rule is new.
	runCLI(t, awsCLI("ec2", "revoke-security-group-egress", "--group-id", sgTasks,
		"--ip-permissions", `[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]`))
	runCLI(t, awsCLI("ec2", "authorize-security-group-egress", "--group-id", sgTasks,
		"--ip-permissions", `[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]`))
	runCLI(t, awsCLI("ec2", "authorize-security-group-ingress", "--group-id", sgTasks,
		"--ip-permissions", `[{"IpProtocol":"tcp","FromPort":3000,"ToPort":3000,"UserIdGroupPairs":[{"GroupId":"`+sgAlb+`"}]}]`))

	// The CLI renders the absent ports as "None".
	from := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+sgTasks,
		"--query", "SecurityGroupRules[?IpProtocol=='-1'].FromPort | [0]", "--output", "text")))
	if from != "None" {
		t.Fatalf("all-traffic FromPort = %q, want None", from)
	}

	// Without a UserId terraform-provider-aws keeps the bare id.
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
