package aws_cli_test

import (
	"strings"
	"testing"
)

// TestIAMCLI_UpdateUserAndGroup renames a user and a group through the aws CLI,
// asserting the ARN re-keys to the new name/path.
func TestIAMCLI_UpdateUserAndGroup(t *testing.T) {
	user := "uc-cli-user"
	newUser := "uc-cli-user-renamed"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", user))
		runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", newUser))
	})

	runCLI(t, awsCLI("iam", "update-user",
		"--user-name", user, "--new-user-name", newUser, "--new-path", "/team/"))

	arn := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-user",
		"--user-name", newUser, "--query", "User.Arn", "--output", "text")))
	if !strings.Contains(arn, ":user/team/"+newUser) {
		t.Fatalf("get-user after rename returned arn %q", arn)
	}

	group := "uc-cli-group"
	newGroup := "uc-cli-group-renamed"
	runCLI(t, awsCLI("iam", "create-group", "--group-name", group))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-group", "--group-name", group))
		runCLIIgnore(awsCLI("iam", "delete-group", "--group-name", newGroup))
	})

	runCLI(t, awsCLI("iam", "update-group",
		"--group-name", group, "--new-group-name", newGroup, "--new-path", "/div/"))

	garn := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-group",
		"--group-name", newGroup, "--query", "Group.Arn", "--output", "text")))
	if !strings.Contains(garn, ":group/div/"+newGroup) {
		t.Fatalf("get-group after rename returned arn %q", garn)
	}
}

// TestIAMCLI_LoginProfile drives the console-password profile lifecycle through
// the aws CLI.
func TestIAMCLI_LoginProfile(t *testing.T) {
	user := "uc-cli-login-user"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-login-profile", "--user-name", user))
		runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", user))
	})

	runCLI(t, awsCLI("iam", "create-login-profile",
		"--user-name", user, "--password", "InitPass-123!", "--password-reset-required"))

	out := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-login-profile",
		"--user-name", user, "--query", "LoginProfile.UserName", "--output", "text")))
	if out != user {
		t.Fatalf("get-login-profile returned user %q, want %q", out, user)
	}

	runCLI(t, awsCLI("iam", "update-login-profile",
		"--user-name", user, "--password", "NextPass-456!", "--no-password-reset-required"))

	reset := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-login-profile",
		"--user-name", user, "--query", "LoginProfile.PasswordResetRequired", "--output", "text")))
	if reset != "False" {
		t.Fatalf("PasswordResetRequired after update = %q, want False", reset)
	}

	runCLI(t, awsCLI("iam", "delete-login-profile", "--user-name", user))
}

// TestIAMCLI_AccessKeyLastUsed updates a key's status and reads its last-used
// metadata through the aws CLI.
func TestIAMCLI_AccessKeyLastUsed(t *testing.T) {
	user := "uc-cli-key-user"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", user)) })

	akid := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-access-key",
		"--user-name", user, "--query", "AccessKey.AccessKeyId", "--output", "text")))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-access-key", "--user-name", user, "--access-key-id", akid))
	})

	runCLI(t, awsCLI("iam", "update-access-key",
		"--user-name", user, "--access-key-id", akid, "--status", "Inactive"))

	status := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-access-keys",
		"--user-name", user, "--query", "AccessKeyMetadata[0].Status", "--output", "text")))
	if status != "Inactive" {
		t.Fatalf("access key status = %q, want Inactive", status)
	}

	svc := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-access-key-last-used",
		"--access-key-id", akid, "--query", "AccessKeyLastUsed.ServiceName", "--output", "text")))
	if svc != "N/A" {
		t.Fatalf("AccessKeyLastUsed.ServiceName = %q, want N/A", svc)
	}
}

// TestIAMCLI_UserTags exercises tag-user / list-user-tags / untag-user.
func TestIAMCLI_UserTags(t *testing.T) {
	user := "uc-cli-tag-user"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", user)) })

	runCLI(t, awsCLI("iam", "tag-user", "--user-name", user,
		"--tags", "Key=team,Value=platform", "Key=env,Value=prod"))

	val := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-user-tags",
		"--user-name", user, "--query", "Tags[?Key=='team'].Value", "--output", "text")))
	if val != "platform" {
		t.Fatalf("list-user-tags team = %q, want platform", val)
	}

	runCLI(t, awsCLI("iam", "untag-user", "--user-name", user, "--tag-keys", "env"))

	remaining := strings.TrimSpace(runCLI(t, awsCLI("iam", "list-user-tags",
		"--user-name", user, "--query", "Tags[].Key", "--output", "text")))
	if strings.Contains(remaining, "env") {
		t.Fatalf("untag-user did not remove env: %q", remaining)
	}
}

// TestIAMCLI_AccountPasswordPolicy exercises the account password-policy
// singleton through the aws CLI.
func TestIAMCLI_AccountPasswordPolicy(t *testing.T) {
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-account-password-policy")) })

	runCLI(t, awsCLI("iam", "update-account-password-policy",
		"--minimum-password-length", "14",
		"--require-symbols", "--require-numbers",
		"--require-uppercase-characters", "--require-lowercase-characters",
		"--max-password-age", "60", "--password-reuse-prevention", "5"))

	length := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-account-password-policy",
		"--query", "PasswordPolicy.MinimumPasswordLength", "--output", "text")))
	if length != "14" {
		t.Fatalf("MinimumPasswordLength = %q, want 14", length)
	}
	expire := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-account-password-policy",
		"--query", "PasswordPolicy.ExpirePasswords", "--output", "text")))
	if expire != "True" {
		t.Fatalf("ExpirePasswords = %q, want True", expire)
	}

	runCLI(t, awsCLI("iam", "delete-account-password-policy"))
	runCLIExpectError(t, awsCLI("iam", "get-account-password-policy"))
}
