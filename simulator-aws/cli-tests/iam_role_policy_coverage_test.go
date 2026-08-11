package aws_cli_test

import (
	"strings"
	"testing"
)

// CLI coverage backfill for IAM role-policy operations that had no CLI test.

const cliTrustDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
const cliPermDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeInstances","Resource":"*"}]}`

func TestIAMCLI_RoleManagedPolicyAttachDetach(t *testing.T) {
	runCLI(t, awsCLI("iam", "create-role", "--role-name", "cli-cov-attach", "--assume-role-policy-document", cliTrustDoc))
	arn := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-policy", "--policy-name", "cli-cov-pol", "--policy-document", cliPermDoc, "--query", "Policy.Arn", "--output", "text")))

	runCLI(t, awsCLI("iam", "attach-role-policy", "--role-name", "cli-cov-attach", "--policy-arn", arn))
	got := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-attached-role-policies", "--role-name", "cli-cov-attach",
		"--query", "AttachedPolicies[?PolicyArn=='"+arn+"'].PolicyArn | [0]", "--output", "text")))
	if got != arn {
		t.Fatalf("attached policy arn = %q, want %q", got, arn)
	}

	runCLI(t, awsCLI("iam", "detach-role-policy", "--role-name", "cli-cov-attach", "--policy-arn", arn))
	count := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-attached-role-policies", "--role-name", "cli-cov-attach",
		"--query", "length(AttachedPolicies)", "--output", "text")))
	if count != "0" {
		t.Fatalf("attached policies after detach = %q, want 0", count)
	}
}

func TestIAMCLI_RoleInlinePolicyLifecycle(t *testing.T) {
	runCLI(t, awsCLI("iam", "create-role", "--role-name", "cli-cov-inline", "--assume-role-policy-document", cliTrustDoc))
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", "cli-cov-inline", "--policy-name", "inline-1", "--policy-document", cliPermDoc))

	got := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-role-policies", "--role-name", "cli-cov-inline",
		"--query", "PolicyNames[?@=='inline-1'] | [0]", "--output", "text")))
	if got != "inline-1" {
		t.Fatalf("list-role-policies missing inline-1, got %q", got)
	}

	runCLI(t, awsCLI("iam", "delete-role-policy", "--role-name", "cli-cov-inline", "--policy-name", "inline-1"))
	count := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-role-policies", "--role-name", "cli-cov-inline",
		"--query", "length(PolicyNames)", "--output", "text")))
	if count != "0" {
		t.Fatalf("inline policies after delete = %q, want 0", count)
	}
}

func TestIAMCLI_UpdateAssumeRolePolicyAndListProfiles(t *testing.T) {
	runCLI(t, awsCLI("iam", "create-role", "--role-name", "cli-cov-trust", "--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[]}`))
	runCLI(t, awsCLI("iam", "update-assume-role-policy", "--role-name", "cli-cov-trust", "--policy-document", cliTrustDoc))
	doc := runCLI(t, awsCLI("iam", "get-role", "--role-name", "cli-cov-trust", "--query", "Role.AssumeRolePolicyDocument", "--output", "json"))
	if !strings.Contains(doc, "ec2.amazonaws.com") {
		t.Fatalf("updated trust policy not reflected in get-role: %s", doc)
	}

	runCLI(t, awsCLI("iam", "create-instance-profile", "--instance-profile-name", "cli-cov-profile"))
	prof := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-instance-profiles",
		"--query", "InstanceProfiles[?InstanceProfileName=='cli-cov-profile'].InstanceProfileName | [0]", "--output", "text")))
	if prof != "cli-cov-profile" {
		t.Fatalf("list-instance-profiles missing cli-cov-profile, got %q", prof)
	}
}
