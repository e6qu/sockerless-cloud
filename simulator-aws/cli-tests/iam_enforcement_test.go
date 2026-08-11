package aws_cli_test

import (
	"strings"
	"testing"
)

// TestIAM_UserLifecycleAndEnforcementCLI covers the IAM user ops over the aws
// CLI (create-user / get-user / create-access-key / list-access-keys /
// put-user-policy / get-user-policy / attach-user-policy /
// list-attached-user-policies / detach-user-policy / delete-user-policy /
// delete-access-key / delete-user) and proves call-time enforcement (#657): a
// CLI call made with the restricted user's minted key is denied
// UnauthorizedOperation for an action its policy doesn't grant.
func TestIAM_UserLifecycleAndEnforcementCLI(t *testing.T) {
	user := "cli-restricted"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })

	out := runCLI(t, awsCLI("iam", "get-user", "--user-name", user, "--output", "json"))
	var gu struct {
		User struct {
			UserName string `json:"UserName"`
			Arn      string `json:"Arn"`
		} `json:"User"`
	}
	parseJSON(t, out, &gu)
	if gu.User.UserName != user {
		t.Fatalf("get-user returned %q", gu.User.UserName)
	}

	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user,
		"--policy-name", "least-priv",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeVolumes","Resource":"*"}]}`))
	runCLI(t, awsCLI("iam", "get-user-policy", "--user-name", user, "--policy-name", "least-priv"))
	if lp := runCLI(t, awsCLI("iam", "list-user-policies", "--user-name", user, "--output", "json")); !strings.Contains(lp, "least-priv") {
		t.Fatalf("list-user-policies missing the inline policy: %s", lp)
	}

	runCLI(t, awsCLI("iam", "create-policy", "--policy-name", "cli-managed",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`))
	arn := "arn:aws:iam::000000000000:policy/cli-managed"
	runCLI(t, awsCLI("iam", "attach-user-policy", "--user-name", user, "--policy-arn", arn))
	if la := runCLI(t, awsCLI("iam", "list-attached-user-policies", "--user-name", user, "--output", "json")); !strings.Contains(la, "cli-managed") {
		t.Fatalf("list-attached-user-policies missing the managed policy: %s", la)
	}
	runCLI(t, awsCLI("iam", "detach-user-policy", "--user-name", user, "--policy-arn", arn))
	runCLI(t, awsCLI("iam", "delete-user-policy", "--user-name", user, "--policy-name", "least-priv"))

	// Re-grant just DescribeVolumes, mint a key, and prove enforcement.
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user,
		"--policy-name", "least-priv",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeVolumes","Resource":"*"}]}`))
	out = runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json"))
	var ck struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, out, &ck)
	if ck.AccessKey.AccessKeyId == "" {
		t.Fatal("create-access-key returned no key id")
	}
	if lk := runCLI(t, awsCLI("iam", "list-access-keys", "--user-name", user, "--output", "json")); !strings.Contains(lk, ck.AccessKey.AccessKeyId) {
		t.Fatalf("list-access-keys missing the minted key: %s", lk)
	}

	// Denied action with the restricted key → UnauthorizedOperation.
	denyCmd := awsCLI("ec2", "create-volume", "--availability-zone", "us-east-1a", "--size", "1")
	denyCmd.Env = withCreds(denyCmd.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	deny := runCLIExpectError(t, denyCmd)
	if !strings.Contains(deny, "UnauthorizedOperation") {
		t.Fatalf("create-volume with restricted key expected UnauthorizedOperation; got: %s", deny)
	}

	// Allowed action with the restricted key → succeeds.
	allowCmd := awsCLI("ec2", "describe-volumes")
	allowCmd.Env = withCreds(allowCmd.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	runCLI(t, allowCmd)

	runCLI(t, awsCLI("iam", "delete-access-key", "--user-name", user, "--access-key-id", ck.AccessKey.AccessKeyId))
}

// TestIAM_MintedKeyCallerIdentityAndWrongSecretCLI proves the credential-minting
// loop the console's Security credentials page drives, over the plain aws CLI:
// create a user, mint an access key with `aws iam create-access-key`, sign a
// call with the minted pair (`aws sts get-caller-identity` — the verification
// command the console shows), and confirm the caller identity is the minted
// user. The same access key ID presented with a wrong secret is rejected by the
// Signature Version 4 verifier with SignatureDoesNotMatch.
func TestIAM_MintedKeyCallerIdentityAndWrongSecretCLI(t *testing.T) {
	user := "cli-minted-operator"
	created := runCLI(t, awsCLI("iam", "create-user", "--user-name", user, "--output", "json"))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })
	var cu struct {
		User struct {
			Arn string `json:"Arn"`
		} `json:"User"`
	}
	parseJSON(t, created, &cu)
	if cu.User.Arn == "" {
		t.Fatalf("create-user returned no ARN: %s", created)
	}

	out := runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json"))
	var ck struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, out, &ck)
	if ck.AccessKey.AccessKeyId == "" || ck.AccessKey.SecretAccessKey == "" {
		t.Fatalf("create-access-key returned incomplete credential material: %s", out)
	}
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-access-key", "--user-name", user, "--access-key-id", ck.AccessKey.AccessKeyId).Run()
	})

	whoami := awsCLI("sts", "get-caller-identity", "--output", "json")
	whoami.Env = withCreds(whoami.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	var identity struct {
		Arn string `json:"Arn"`
	}
	parseJSON(t, runCLI(t, whoami), &identity)
	if identity.Arn != cu.User.Arn {
		t.Fatalf("get-caller-identity as the minted key returned %q, want the minted user's ARN %q", identity.Arn, cu.User.Arn)
	}

	wrong := awsCLI("sts", "get-caller-identity")
	wrong.Env = withCreds(wrong.Env, ck.AccessKey.AccessKeyId, "wrong-secret-0000000000000000000000000000")
	deny := runCLIExpectError(t, wrong)
	if !strings.Contains(deny, "SignatureDoesNotMatch") {
		t.Fatalf("get-caller-identity with a wrong secret expected SignatureDoesNotMatch; got: %s", deny)
	}
}

// TestIAM_DynamoDBTransactionsResourceScopedCLI proves `aws dynamodb
// transact-write-items` works under the standard least-privilege pattern — a
// policy whose Resource is one table's ARN — because the enforcement gate
// derives table ARNs from TransactItems rather than a (missing) top-level
// TableName (GitHub issue #870). A transaction touching an ungranted table is
// denied AccessDeniedException.
func TestIAM_DynamoDBTransactionsResourceScopedCLI(t *testing.T) {
	for _, table := range []string{"cli-txn-scoped", "cli-txn-other"} {
		runCLI(t, awsCLI("dynamodb", "create-table",
			"--table-name", table,
			"--billing-mode", "PAY_PER_REQUEST",
			"--attribute-definitions", "AttributeName=id,AttributeType=S",
			"--key-schema", "AttributeName=id,KeyType=HASH"))
		table := table
		t.Cleanup(func() { _ = awsCLI("dynamodb", "delete-table", "--table-name", table).Run() })
	}

	user := "cli-txn-scoped-user"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user,
		"--policy-name", "one-table-txn",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
			`"Action":["dynamodb:TransactWriteItems"],`+
			`"Resource":["arn:aws:dynamodb:us-east-1:123456789012:table/cli-txn-scoped",`+
			`"arn:aws:dynamodb:us-east-1:123456789012:table/cli-txn-scoped/index/*"]}]}`))
	out := runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json"))
	var ck struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, out, &ck)
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-access-key", "--user-name", user, "--access-key-id", ck.AccessKey.AccessKeyId).Run()
	})

	granted := awsCLI("dynamodb", "transact-write-items", "--transact-items",
		`[{"Put":{"TableName":"cli-txn-scoped","Item":{"id":{"S":"txn-1"}}}}]`)
	granted.Env = withCreds(granted.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	runCLI(t, granted)

	denied := awsCLI("dynamodb", "transact-write-items", "--transact-items",
		`[{"Put":{"TableName":"cli-txn-other","Item":{"id":{"S":"txn-1"}}}}]`)
	denied.Env = withCreds(denied.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	deny := runCLIExpectError(t, denied)
	if !strings.Contains(deny, "AccessDeniedException") {
		t.Fatalf("transact-write-items on an ungranted table expected AccessDeniedException; got: %s", deny)
	}
}

// TestIAM_ECRResourceScopedCLI proves `aws ecr describe-images` works under a
// policy scoped to a repository namespace, because the enforcement gate derives
// the repository ARN from the request's repositoryName. Repository names carry
// slashes and IAM's `*` spans them, so a nested name under the granted prefix
// is allowed; a repository outside the prefix is denied AccessDeniedException.
func TestIAM_ECRResourceScopedCLI(t *testing.T) {
	for _, repo := range []string{"cli-scoped-ns/app", "cli-scoped-ns/golden/omnibus", "cli-other-ns/app"} {
		runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))
		repo := repo
		t.Cleanup(func() { _ = awsCLI("ecr", "delete-repository", "--repository-name", repo, "--force").Run() })
	}

	user := "cli-ecr-scoped-user"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user,
		"--policy-name", "one-namespace",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
			`"Action":["ecr:DescribeImages","ecr:ListImages"],`+
			`"Resource":"arn:aws:ecr:us-east-1:123456789012:repository/cli-scoped-ns/*"}]}`))
	// The inline policy has to go before the user does, exactly as on real IAM.
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-user-policy", "--user-name", user, "--policy-name", "one-namespace").Run()
	})
	out := runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json"))
	var ck struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, out, &ck)
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-access-key", "--user-name", user, "--access-key-id", ck.AccessKey.AccessKeyId).Run()
	})

	for _, repo := range []string{"cli-scoped-ns/app", "cli-scoped-ns/golden/omnibus"} {
		granted := awsCLI("ecr", "describe-images", "--repository-name", repo)
		granted.Env = withCreds(granted.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
		runCLI(t, granted)
	}

	denied := awsCLI("ecr", "describe-images", "--repository-name", "cli-other-ns/app")
	denied.Env = withCreds(denied.Env, ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey)
	deny := runCLIExpectError(t, denied)
	if !strings.Contains(deny, "AccessDeniedException") {
		t.Fatalf("describe-images on a repository outside the granted prefix expected AccessDeniedException; got: %s", deny)
	}
}

// withCreds replaces the AWS credential env entries so the CLI call is signed
// with the given (restricted) access key.
func withCreds(env []string, akid, secret string) []string {
	out := make([]string, 0, len(env)+2)
	for _, e := range env {
		if strings.HasPrefix(e, "AWS_ACCESS_KEY_ID=") || strings.HasPrefix(e, "AWS_SECRET_ACCESS_KEY=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "AWS_ACCESS_KEY_ID="+akid, "AWS_SECRET_ACCESS_KEY="+secret)
}
