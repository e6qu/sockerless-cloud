package aws_cli_test

import (
	"strings"
	"testing"
)

// TestIAMRoleOIDCFidelityCLI covers the role attribute round-trip + UpdateRole +
// TagRole/UntagRole and the OIDC provider tag lifecycle via the aws CLI.
func TestIAMRoleOIDCFidelityCLI(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	assumeDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

	boundaryArn := q("iam", "create-policy", "--policy-name", "cli-fidelity-boundary",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
		"--query", "Policy.Arn", "--output", "text")

	q("iam", "create-role", "--role-name", "cli-fidelity-role",
		"--assume-role-policy-document", assumeDoc,
		"--description", "cli reconciler", "--max-session-duration", "7200",
		"--permissions-boundary", boundaryArn,
		"--tags", "Key=team,Value=platform",
		"--query", "Role.RoleName", "--output", "text")

	out := q("iam", "get-role", "--role-name", "cli-fidelity-role",
		"--query", "Role.[Description,MaxSessionDuration,PermissionsBoundary.PermissionsBoundaryArn]", "--output", "text")
	f := strings.Split(out, "\t")
	if len(f) != 3 || f[0] != "cli reconciler" || f[1] != "7200" || f[2] != boundaryArn {
		t.Fatalf("role attributes round-trip: got %q (boundary=%q)", out, boundaryArn)
	}

	// UpdateRole.
	runCLI(t, awsCLI("iam", "update-role", "--role-name", "cli-fidelity-role",
		"--description", "cli updated", "--max-session-duration", "3600"))
	if d := q("iam", "get-role", "--role-name", "cli-fidelity-role", "--query", "Role.Description", "--output", "text"); d != "cli updated" {
		t.Fatalf("update-role description: got %q", d)
	}

	// TagRole / UntagRole.
	runCLI(t, awsCLI("iam", "tag-role", "--role-name", "cli-fidelity-role", "--tags", "Key=env,Value=ci"))
	if v := q("iam", "get-role", "--role-name", "cli-fidelity-role",
		"--query", "Role.Tags[?Key=='env'].Value | [0]", "--output", "text"); v != "ci" {
		t.Fatalf("tag-role: got %q", v)
	}
	runCLI(t, awsCLI("iam", "untag-role", "--role-name", "cli-fidelity-role", "--tag-keys", "env"))
	if v := q("iam", "get-role", "--role-name", "cli-fidelity-role",
		"--query", "Role.Tags[?Key=='env'].Value | [0]", "--output", "text"); v != "None" && v != "" {
		t.Fatalf("untag-role left the key: got %q", v)
	}

	// OIDC provider tags.
	oidcArn := q("iam", "create-open-id-connect-provider",
		"--url", "https://oidc-cli.fidelity.example.test", "--client-id-list", "sts.amazonaws.com",
		"--thumbprint-list", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--tags", "Key=purpose,Value=gha",
		"--query", "OpenIDConnectProviderArn", "--output", "text")
	if v := q("iam", "get-open-id-connect-provider", "--open-id-connect-provider-arn", oidcArn,
		"--query", "Tags[?Key=='purpose'].Value | [0]", "--output", "text"); v != "gha" {
		t.Fatalf("OIDC tags round-trip: got %q", v)
	}
	runCLI(t, awsCLI("iam", "tag-open-id-connect-provider", "--open-id-connect-provider-arn", oidcArn,
		"--tags", "Key=env,Value=prod"))
	runCLI(t, awsCLI("iam", "untag-open-id-connect-provider", "--open-id-connect-provider-arn", oidcArn,
		"--tag-keys", "purpose"))
	keys := q("iam", "get-open-id-connect-provider", "--open-id-connect-provider-arn", oidcArn,
		"--query", "Tags[].Key", "--output", "text")
	if strings.Contains(keys, "purpose") || !strings.Contains(keys, "env") {
		t.Fatalf("OIDC tag/untag: got keys %q", keys)
	}
}
