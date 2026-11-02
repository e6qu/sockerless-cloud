package aws_cli_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliTOTP is the RFC 6238 code a virtual MFA device with the base32 seed
// shows at the given time.
func cliTOTP(t *testing.T, base32Seed string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimSpace(base32Seed))
	require.NoError(t, err)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1_000_000)
}

// cliTOTPWrong is a six-digit code the device shows at no step near now.
func cliTOTPWrong(t *testing.T, base32Seed string) string {
	t.Helper()
	near := map[string]bool{}
	for step := -3; step <= 3; step++ {
		near[cliTOTP(t, base32Seed, time.Now().Add(time.Duration(step)*30*time.Second))] = true
	}
	for n := 0; ; n++ {
		if code := fmt.Sprintf("%06d", n); !near[code] {
			return code
		}
	}
}

// cliVirtualMFADevice creates a virtual MFA device and returns its serial
// number and the base32 seed the CLI writes to --outfile.
func cliVirtualMFADevice(t *testing.T, name string) (serial, seed string) {
	t.Helper()
	outfile := filepath.Join(t.TempDir(), "seed.txt")
	serial = strings.TrimSpace(runCLI(t, awsCLI("iam", "create-virtual-mfa-device",
		"--virtual-mfa-device-name", name, "--outfile", outfile, "--bootstrap-method", "Base32StringSeed",
		"--query", "VirtualMFADevice.SerialNumber", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-virtual-mfa-device", "--serial-number", serial)) })
	raw, err := os.ReadFile(outfile)
	require.NoError(t, err)
	return serial, string(raw)
}

// cliEnableMFADevice enables a device with the two consecutive codes it showed
// up to at.
func cliEnableMFADevice(t *testing.T, user, serial, seed string, at time.Time) {
	t.Helper()
	runCLI(t, awsCLI("iam", "enable-mfa-device", "--user-name", user, "--serial-number", serial,
		"--authentication-code1", cliTOTP(t, seed, at.Add(-30*time.Second)),
		"--authentication-code2", cliTOTP(t, seed, at)))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "deactivate-mfa-device", "--user-name", user, "--serial-number", serial))
	})
}

// cliLimitsRole creates a role the account trusts whose own policy allows
// iam:ListRoles, iam:ListUsers and sqs:ListQueues.
func cliLimitsRole(t *testing.T, name, trust string, extra ...string) string {
	t.Helper()
	var created struct {
		Role struct {
			Arn string `json:"Arn"`
		} `json:"Role"`
	}
	args := append([]string{"iam", "create-role", "--role-name", name, "--assume-role-policy-document", trust}, extra...)
	parseJSON(t, runCLI(t, awsCLI(args...)), &created)
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-role", "--role-name", name)) })
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", name, "--policy-name", "lists",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:ListRoles","iam:ListUsers","sqs:ListQueues"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-role-policy", "--role-name", name, "--policy-name", "lists"))
	})
	return created.Role.Arn
}

func cliManagedPolicy(t *testing.T, name, document string) string {
	t.Helper()
	arn := strings.TrimSpace(runCLI(t, awsCLI("iam", "create-policy", "--policy-name", name,
		"--policy-document", document, "--query", "Policy.Arn", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-policy", "--policy-arn", arn)) })
	return arn
}

type cliAssumedRole struct {
	cliSessionCredentials
	PackedPolicySize *int `json:"PackedPolicySize"`
}

// cliSessionAllows reports which of iam:ListRoles, iam:ListUsers and
// sqs:ListQueues a session may call.
func cliSessionAllows(t *testing.T, session cliSessionCredentials) (roles, users, queues bool) {
	t.Helper()
	allowed := func(args ...string) bool {
		cmd := session.asSession(awsCLI(args...))
		out, err := cmd.CombinedOutput()
		if err != nil {
			require.Contains(t, string(out), "AccessDenied", "%v", args)
			return false
		}
		return true
	}
	return allowed("iam", "list-roles"), allowed("iam", "list-users"), allowed("sqs", "list-queues")
}

const cliAccountTrust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"sts:AssumeRole"}]}`

// TestSTS_SessionLimits_CLI assumes a role with `aws sts assume-role`: the
// role's permissions boundary caps the session, --policy and --policy-arns
// narrow it, and --duration-seconds stays within the role's
// --max-session-duration.
func TestSTS_SessionLimits_CLI(t *testing.T) {
	t.Parallel()
	boundary := cliManagedPolicy(t, "cli-iam-list-boundary",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:List*","Resource":"*"}]}`)
	roleArn := cliLimitsRole(t, "cli-session-limits", cliAccountTrust,
		"--permissions-boundary", boundary, "--max-session-duration", "7200")
	assume := func(extra ...string) cliAssumedRole {
		var out cliAssumedRole
		args := append([]string{"sts", "assume-role", "--role-arn", roleArn, "--role-session-name", "cli-limits"}, extra...)
		parseJSON(t, runCLI(t, awsCLI(args...)), &out)
		return out
	}

	plain := assume()
	assert.Nil(t, plain.PackedPolicySize)
	roles, users, queues := cliSessionAllows(t, plain.cliSessionCredentials)
	assert.True(t, roles && users)
	assert.False(t, queues, "the boundary caps sqs:ListQueues, which the role's policy allows")

	inline := assume("--policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListRoles","Resource":"*"}]}`)
	require.NotNil(t, inline.PackedPolicySize)
	assert.Positive(t, *inline.PackedPolicySize)
	roles, users, _ = cliSessionAllows(t, inline.cliSessionCredentials)
	assert.True(t, roles)
	assert.False(t, users, "the inline session policy allows only iam:ListRoles")

	usersPolicy := cliManagedPolicy(t, "cli-session-users",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*"}]}`)
	managed := assume("--policy-arns", "arn="+usersPolicy)
	roles, users, _ = cliSessionAllows(t, managed.cliSessionCredentials)
	assert.False(t, roles)
	assert.True(t, users, "the managed session policy allows iam:ListUsers")

	assume("--duration-seconds", "7200")
	refused := runCLIExpectError(t, awsCLI("sts", "assume-role", "--role-arn", roleArn, "--role-session-name", "cli-limits",
		"--duration-seconds", "7201"))
	assert.Contains(t, refused, "ValidationError")
	assert.Contains(t, refused, "MaxSessionDuration")
}

// TestSTS_GetFederationTokenPolicy_CLI federates an IAM user with
// `aws sts get-federation-token`: without --policy the session may do nothing,
// with it the session may do what both allow, and never call IAM.
func TestSTS_GetFederationTokenPolicy_CLI(t *testing.T) {
	t.Parallel()
	user := "cli-federator"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", user)) })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user, "--policy-name", "federate",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sts:GetFederationToken","sqs:ListQueues","iam:ListRoles"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-user-policy", "--user-name", user, "--policy-name", "federate"))
	})
	var key struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user)), &key)
	federate := func(extra ...string) cliAssumedRole {
		cmd := awsCLI(append([]string{"sts", "get-federation-token", "--name", "cli-fed"}, extra...)...)
		cmd.Env = withCreds(cmd.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
		var out cliAssumedRole
		parseJSON(t, runCLI(t, cmd), &out)
		return out
	}

	bare := federate()
	assert.Contains(t, runCLIExpectError(t, bare.asSession(awsCLI("sqs", "list-queues"))), "AccessDenied",
		"a federated session without a session policy has no permissions")

	scoped := federate("--policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:ListQueues","iam:ListRoles"],"Resource":"*"}]}`)
	require.NotNil(t, scoped.PackedPolicySize)
	runCLI(t, scoped.asSession(awsCLI("sqs", "list-queues")))
	assert.Contains(t, runCLIExpectError(t, scoped.asSession(awsCLI("iam", "list-roles"))), "AccessDenied",
		"a federated session calls no IAM operation")
	runCLI(t, scoped.asSession(awsCLI("sts", "get-caller-identity")))
}

// cliRootAccessPrincipal turns on trusted access for IAM and root sessions in
// the organization, creates a member account and an IAM user of the
// management account allowed sts:AssumeRoot, and returns the user's access
// key and the member account's ID.
func cliRootAccessPrincipal(t *testing.T, name string) (akid, secret, member string) {
	t.Helper()
	runCLI(t, awsCLI("organizations", "enable-aws-service-access", "--service-principal", "iam.amazonaws.com"))
	runCLI(t, awsCLI("iam", "enable-organizations-root-sessions"))
	member = strings.TrimSpace(runCLI(t, awsCLI("organizations", "create-account", "--account-name", name,
		"--email", name+"@sim.invalid", "--query", "CreateAccountStatus.AccountId", "--output", "text")))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("organizations", "remove-account-from-organization", "--account-id", member))
	})
	runCLI(t, awsCLI("iam", "create-user", "--user-name", name))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", name)) })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", name, "--policy-name", "assume-root",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRoot","Resource":"*"}]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-user-policy", "--user-name", name, "--policy-name", "assume-root"))
	})
	var key struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-access-key", "--user-name", name)), &key)
	return key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey, member
}

// TestSTS_AssumeRootTaskPolicy_CLI opens a root session on a member account
// with `aws sts assume-root`; the S3UnlockBucketPolicy task policy scopes it
// to bucket policies and listing buckets. A root user, and the management
// account as target, open no session.
func TestSTS_AssumeRootTaskPolicy_CLI(t *testing.T) {
	t.Parallel()
	akid, secret, member := cliRootAccessPrincipal(t, "cli-root-unlock")
	asCaller := func(args ...string) *exec.Cmd {
		cmd := awsCLI(args...)
		cmd.Env = withCreds(cmd.Env, akid, secret)
		return cmd
	}
	unlock := "arn=arn:aws:iam::aws:policy/root-task/S3UnlockBucketPolicy"
	assert.Contains(t, runCLIExpectError(t, asCaller("sts", "assume-root", "--target-principal", "123456789012", "--task-policy-arn", unlock)),
		"AccessDenied", "the management account is not a member account")
	refused := runCLIExpectError(t, asCaller("sts", "assume-root", "--target-principal", member,
		"--task-policy-arn", "arn=arn:aws:iam::aws:policy/ReadOnlyAccess"))
	assert.Contains(t, refused, "ValidationError")

	var root cliSessionCredentials
	parseJSON(t, runCLI(t, asCaller("sts", "assume-root", "--target-principal", member,
		"--task-policy-arn", unlock, "--duration-seconds", "900")), &root)
	identity := strings.TrimSpace(runCLI(t, root.asSession(awsCLI("sts", "get-caller-identity", "--query", "Arn", "--output", "text"))))
	assert.Equal(t, "arn:aws:iam::"+member+":root", identity)
	runCLI(t, root.asSession(awsCLI("s3api", "list-buckets")))
	assert.Contains(t, runCLIExpectError(t, root.asSession(awsCLI("sts", "assume-root", "--target-principal", member,
		"--task-policy-arn", unlock))), "AccessDenied", "a root user cannot call AssumeRoot")
	assert.Contains(t, runCLIExpectError(t, root.asSession(awsCLI("sqs", "list-queues"))), "AccessDenied",
		"the task policy denies every action outside it")
}

// TestSTS_GetSessionTokenRestrictions_CLI opens sessions with
// `aws sts get-session-token`: an IAM user's session keeps the user's
// permissions but calls no IAM operation without MFA and opens no further
// session.
func TestSTS_GetSessionTokenRestrictions_CLI(t *testing.T) {
	t.Parallel()
	user := "cli-session-token"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { runCLIIgnore(awsCLI("iam", "delete-user", "--user-name", user)) })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user, "--policy-name", "session",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:ListUsers","sqs:ListQueues"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-user-policy", "--user-name", user, "--policy-name", "session"))
	})
	var key struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user)), &key)
	getSessionToken := awsCLI("sts", "get-session-token")
	getSessionToken.Env = withCreds(getSessionToken.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
	var session cliSessionCredentials
	parseJSON(t, runCLI(t, getSessionToken), &session)

	runCLI(t, session.asSession(awsCLI("sqs", "list-queues")))
	assert.Contains(t, runCLIExpectError(t, session.asSession(awsCLI("iam", "list-users"))), "AccessDenied",
		"a session MFA did not authenticate calls no IAM operation")
	assert.Contains(t, runCLIExpectError(t, session.asSession(awsCLI("sts", "get-session-token"))), "AccessDenied",
		"session credentials cannot call GetSessionToken")
}

// TestSTS_AssumeRoleMFA_CLI enables a virtual MFA device with codes computed
// from the seed `aws iam create-virtual-mfa-device` writes, then assumes a role
// whose trust policy requires MFA with `--serial-number` and `--token-code`:
// the device's next code authenticates once.
func TestSTS_AssumeRoleMFA_CLI(t *testing.T) {
	t.Parallel()
	user := "cli-mfa-assumer"
	iamMFACLIUser(t, user)
	var key struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user)), &key)
	serial, seed := cliVirtualMFADevice(t, "cli-mfa-assumer")

	wrong := runCLIExpectError(t, awsCLI("iam", "enable-mfa-device", "--user-name", user, "--serial-number", serial,
		"--authentication-code1", cliTOTPWrong(t, seed), "--authentication-code2", cliTOTP(t, seed, time.Now())))
	assert.Contains(t, wrong, "InvalidAuthenticationCode")
	enabledAt := time.Now()
	cliEnableMFADevice(t, user, serial, seed, enabledAt)

	roleArn := cliLimitsRole(t, "cli-mfa-role", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
		`"Principal":{"AWS":"arn:aws:iam::123456789012:user/`+user+`"},"Action":"sts:AssumeRole",`+
		`"Condition":{"Bool":{"aws:MultiFactorAuthPresent":"true"}}}]}`)
	assume := func(code string) []string {
		args := []string{"sts", "assume-role", "--role-arn", roleArn, "--role-session-name", "cli-mfa"}
		if code != "" {
			args = append(args, "--serial-number", serial, "--token-code", code)
		}
		return args
	}
	run := func(args []string) *exec.Cmd {
		cmd := awsCLI(args...)
		cmd.Env = withCreds(cmd.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
		return cmd
	}
	assert.Contains(t, runCLIExpectError(t, run(assume(""))), "AccessDenied", "the trust policy requires MFA")
	assert.Contains(t, runCLIExpectError(t, run(assume(cliTOTPWrong(t, seed)))), "AccessDenied",
		"a code the device did not show is refused")
	assert.Contains(t, runCLIExpectError(t, run(assume(cliTOTP(t, seed, enabledAt)))), "AccessDenied",
		"the code that enabled the device authenticates nothing again")
	next := cliTOTP(t, seed, enabledAt.Add(30*time.Second))
	runCLI(t, run(assume(next)))
	assert.Contains(t, runCLIExpectError(t, run(assume(next))), "AccessDenied", "a code authenticates once")
}
