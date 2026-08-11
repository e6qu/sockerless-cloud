package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2SecurityGroupRuleTargetsCLI covers the IPv6 SecurityGroupRule row (the
// P1 gap) and revoke removing only the matching rule, via the aws CLI.
func TestEC2SecurityGroupRuleTargetsCLI(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.123.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")
	gid := q("ec2", "create-security-group", "--group-name", "cli-sgr-targets",
		"--description", "t", "--vpc-id", vpcID, "--query", "GroupId", "--output", "text")

	// IPv6 egress rule → must create a rule row carrying cidrIpv6.
	runCLI(t, awsCLI("ec2", "authorize-security-group-egress", "--group-id", gid,
		"--ip-permissions", "IpProtocol=-1,Ipv6Ranges=[{CidrIpv6=::/0}]"))
	v6 := q("ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+gid,
		"--query", "SecurityGroupRules[?CidrIpv6=='::/0'].SecurityGroupRuleId | [0]",
		"--output", "text")
	if v6 == "" || v6 == "None" {
		t.Fatal("IPv6 egress rule produced no SecurityGroupRule row")
	}

	// Two TCP:443 ingress rules; revoking one must leave the other.
	for _, cidr := range []string{"10.123.1.0/24", "10.123.2.0/24"} {
		runCLI(t, awsCLI("ec2", "authorize-security-group-ingress", "--group-id", gid,
			"--ip-permissions", "IpProtocol=tcp,FromPort=443,ToPort=443,IpRanges=[{CidrIp="+cidr+"}]"))
	}
	runCLI(t, awsCLI("ec2", "revoke-security-group-ingress", "--group-id", gid,
		"--ip-permissions", "IpProtocol=tcp,FromPort=443,ToPort=443,IpRanges=[{CidrIp=10.123.1.0/24}]"))

	remaining := q("ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+gid,
		"--query", "length(SecurityGroupRules[?CidrIpv4=='10.123.2.0/24'])", "--output", "text")
	if remaining != "1" {
		t.Fatalf("surviving ingress rule count: got %q, want 1", remaining)
	}
	gone := q("ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+gid,
		"--query", "length(SecurityGroupRules[?CidrIpv4=='10.123.1.0/24'])", "--output", "text")
	if gone != "0" {
		t.Fatalf("revoked ingress rule must be gone (no orphan), got count %q", gone)
	}
}
