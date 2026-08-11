package aws_cli_test

import (
	"strings"
	"testing"
)

// TestIAMCLI_ListRolesAndPolicyVersions drives ListRoles and
// ListPolicyVersions via the aws CLI — the audit-enumeration and
// aws_iam_policy destroy paths.
func TestIAMCLI_ListRolesAndPolicyVersions(t *testing.T) {
	runCLI(t, awsCLI("iam", "create-role",
		"--role-name", "eddsim-cli-role",
		"--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[]}`))

	roles := runCLI(t, awsCLI("iam", "list-roles",
		"--query", "Roles[?starts_with(RoleName, `eddsim`)].RoleName", "--output", "text"))
	if !strings.Contains(roles, "eddsim-cli-role") {
		t.Fatalf("list-roles did not include eddsim-cli-role: %s", roles)
	}

	arn := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-policy",
		"--policy-name", "cli-lpv-policy",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		"--query", "Policy.Arn", "--output", "text")))

	version := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-policy-versions",
		"--policy-arn", arn, "--query", "Versions[0].VersionId", "--output", "text")))
	if version != "v1" {
		t.Fatalf("list-policy-versions returned %q, want v1", version)
	}
}
