package aws_cli_test

import (
	"strings"
	"testing"
)

// TestIAMCLI_RoleBoundaryAndDescription drives the role permissions-boundary
// and description ops via the aws CLI: put/delete-role-permissions-boundary
// and update-role-description (the aws_iam_role.permissions_boundary path).
func TestIAMCLI_RoleBoundaryAndDescription(t *testing.T) {
	runCLI(t, awsCLI("iam", "create-role",
		"--role-name", "cli-rpb-role",
		"--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-role", "--role-name", "cli-rpb-role")) })

	boundaryArn := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-policy",
		"--policy-name", "cli-rpb-boundary",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`,
		"--query", "Policy.Arn", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-policy", "--policy-arn", boundaryArn)) })

	runCLI(t, awsCLI("iam", "put-role-permissions-boundary",
		"--role-name", "cli-rpb-role",
		"--permissions-boundary", boundaryArn))

	bound := runCLI(t, awsCLI("iam", "get-role",
		"--role-name", "cli-rpb-role",
		"--query", "Role.PermissionsBoundary.PermissionsBoundaryArn", "--output", "text"))
	if !strings.Contains(bound, "cli-rpb-boundary") {
		t.Fatalf("get-role did not report the permissions boundary: %s", bound)
	}

	desc := runCLI(t, awsCLI("iam", "update-role-description",
		"--role-name", "cli-rpb-role",
		"--description", "cli boundary role",
		"--query", "Role.Description", "--output", "text"))
	if !strings.Contains(desc, "cli boundary role") {
		t.Fatalf("update-role-description did not return the new description: %s", desc)
	}

	runCLI(t, awsCLI("iam", "delete-role-permissions-boundary", "--role-name", "cli-rpb-role"))
}

// TestIAMCLI_PolicyVersionsAndTags drives the managed-policy version and tag
// ops via the aws CLI: create-policy-version (--set-as-default),
// set-default-policy-version, delete-policy-version, tag-policy, untag-policy.
func TestIAMCLI_PolicyVersionsAndTags(t *testing.T) {
	arn := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-policy",
		"--policy-name", "cli-pv-policy",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		"--query", "Policy.Arn", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-policy", "--policy-arn", arn)) })

	newVer := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-policy-version",
		"--policy-arn", arn,
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`,
		"--set-as-default",
		"--query", "PolicyVersion.VersionId", "--output", "text")))
	if newVer != "v2" {
		t.Fatalf("create-policy-version returned %q, want v2", newVer)
	}

	defVer := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-policy",
		"--policy-arn", arn, "--query", "Policy.DefaultVersionId", "--output", "text")))
	if defVer != "v2" {
		t.Fatalf("default version is %q after set-as-default, want v2", defVer)
	}

	runCLI(t, awsCLI("iam", "set-default-policy-version",
		"--policy-arn", arn, "--version-id", "v1"))
	runCLI(t, awsCLI("iam", "delete-policy-version",
		"--policy-arn", arn, "--version-id", "v2"))

	runCLI(t, awsCLI("iam", "tag-policy",
		"--policy-arn", arn,
		"--tags", "Key=owner,Value=platform"))
	tags := runCLI(t, awsCLI("iam", "list-policy-tags",
		"--policy-arn", arn, "--query", "Tags[?Key=='owner'].Value", "--output", "text"))
	if !strings.Contains(tags, "platform") {
		t.Fatalf("list-policy-tags did not include the owner tag: %s", tags)
	}
	runCLI(t, awsCLI("iam", "untag-policy",
		"--policy-arn", arn, "--tag-keys", "owner"))
}

// TestIAMCLI_InstanceProfileTags drives tag/untag/list-instance-profile-tags
// via the aws CLI (the aws_iam_instance_profile.tags path).
func TestIAMCLI_InstanceProfileTags(t *testing.T) {
	runCLI(t, awsCLI("iam", "create-instance-profile",
		"--instance-profile-name", "cli-ipt-profile"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-instance-profile", "--instance-profile-name", "cli-ipt-profile"))
	})

	runCLI(t, awsCLI("iam", "tag-instance-profile",
		"--instance-profile-name", "cli-ipt-profile",
		"--tags", "Key=env,Value=prod", "Key=team,Value=infra"))

	tags := runCLI(t, awsCLI("iam", "list-instance-profile-tags",
		"--instance-profile-name", "cli-ipt-profile",
		"--query", "Tags[?Key=='env'].Value", "--output", "text"))
	if !strings.Contains(tags, "prod") {
		t.Fatalf("list-instance-profile-tags did not include the env tag: %s", tags)
	}

	runCLI(t, awsCLI("iam", "untag-instance-profile",
		"--instance-profile-name", "cli-ipt-profile",
		"--tag-keys", "env"))

	after := runCLI(t, awsCLI("iam", "list-instance-profile-tags",
		"--instance-profile-name", "cli-ipt-profile",
		"--query", "Tags[?Key=='team'].Value", "--output", "text"))
	if !strings.Contains(after, "infra") {
		t.Fatalf("team tag should remain after untag: %s", after)
	}
}

// TestIAMCLI_EntitiesAndContextKeys drives list-entities-for-policy,
// list-policies-granting-service-access, get-context-keys-for-custom-policy,
// and get-context-keys-for-principal-policy via the aws CLI.
func TestIAMCLI_EntitiesAndContextKeys(t *testing.T) {
	arn := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-policy",
		"--policy-name", "cli-efp-policy",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		"--query", "Policy.Arn", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-policy", "--policy-arn", arn)) })

	roleArn := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-role",
		"--role-name", "cli-efp-role",
		"--assume-role-policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		"--query", "Role.Arn", "--output", "text")))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "detach-role-policy", "--role-name", "cli-efp-role", "--policy-arn", arn))
		runCLIIgnore(awsCLI("iam", "delete-role", "--role-name", "cli-efp-role"))
	})

	runCLI(t, awsCLI("iam", "attach-role-policy",
		"--role-name", "cli-efp-role", "--policy-arn", arn))

	roles := runCLI(t, awsCLI("iam", "list-entities-for-policy",
		"--policy-arn", arn,
		"--query", "PolicyRoles[].RoleName", "--output", "text"))
	if !strings.Contains(roles, "cli-efp-role") {
		t.Fatalf("list-entities-for-policy did not include the attached role: %s", roles)
	}

	granted := runCLI(t, awsCLI("iam", "list-policies-granting-service-access",
		"--arn", roleArn,
		"--service-namespaces", "s3", "ec2",
		"--query", "PoliciesGrantingServiceAccess[?ServiceNamespace=='s3'].Policies[].PolicyName", "--output", "text"))
	if !strings.Contains(granted, "cli-efp-policy") {
		t.Fatalf("list-policies-granting-service-access did not report the s3-granting policy: %s", granted)
	}

	customKeys := runCLI(t, awsCLI("iam", "get-context-keys-for-custom-policy",
		"--policy-input-list", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalTag/team":"infra"}}}]}`,
		"--query", "ContextKeyNames", "--output", "text"))
	if !strings.Contains(customKeys, "aws:PrincipalTag/team") {
		t.Fatalf("get-context-keys-for-custom-policy did not return the condition key: %s", customKeys)
	}

	runCLI(t, awsCLI("iam", "put-role-policy",
		"--role-name", "cli-efp-role",
		"--policy-name", "cli-efp-inline",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:RunInstances","Resource":"*","Condition":{"StringEquals":{"aws:RequestTag/cost-center":"42"}}}]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-role-policy", "--role-name", "cli-efp-role", "--policy-name", "cli-efp-inline"))
	})

	principalKeys := runCLI(t, awsCLI("iam", "get-context-keys-for-principal-policy",
		"--policy-source-arn", roleArn,
		"--query", "ContextKeyNames", "--output", "text"))
	if !strings.Contains(principalKeys, "aws:RequestTag/cost-center") {
		t.Fatalf("get-context-keys-for-principal-policy did not return the inline-policy condition key: %s", principalKeys)
	}
}
