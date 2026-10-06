package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/samlidp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliSessionCredentials struct {
	Credentials struct {
		AccessKeyId     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
	} `json:"Credentials"`
}

// asSession signs cmd with a temporary credential.
func (c cliSessionCredentials) asSession(cmd *exec.Cmd) *exec.Cmd {
	env := withCreds(cmd.Env, c.Credentials.AccessKeyId, c.Credentials.SecretAccessKey)
	cmd.Env = append(env, "AWS_SESSION_TOKEN="+c.Credentials.SessionToken)
	return cmd
}

// cliTeamRole creates a role with the given trust policy that may list roles
// only as a principal tagged team=blue.
func cliTeamRole(t *testing.T, name, trust string) string {
	t.Helper()
	var created struct {
		Role struct {
			Arn string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-role", "--role-name", name, "--assume-role-policy-document", trust)), &created)
	t.Cleanup(func() { _ = awsCLI("iam", "delete-role", "--role-name", name).Run() })
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", name, "--policy-name", "blue-team",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListRoles",`+
			`"Resource":"*","Condition":{"StringEquals":{"aws:PrincipalTag/team":"blue"}}}]}`))
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-role-policy", "--role-name", name, "--policy-name", "blue-team").Run()
	})
	return created.Role.Arn
}

// TestSTS_AssumeRoleSessionTags_CLI passes session tags with `aws sts
// assume-role --tags`: the session's tag decides what its role lets it do.
func TestSTS_AssumeRoleSessionTags_CLI(t *testing.T) {
	roleArn := cliTeamRole(t, "cli-session-tags", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
		`"Principal":{"AWS":"*"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`)
	assume := func(team string) cliSessionCredentials {
		var session cliSessionCredentials
		parseJSON(t, runCLI(t, awsCLI("sts", "assume-role", "--role-arn", roleArn, "--role-session-name", team,
			"--tags", "Key=team,Value="+team, "--transitive-tag-keys", "team")), &session)
		return session
	}
	runCLI(t, assume("blue").asSession(awsCLI("iam", "list-roles")))
	refused := runCLIExpectError(t, assume("red").asSession(awsCLI("iam", "list-roles")))
	assert.Contains(t, refused, "AccessDenied")
}

// TestSTS_AssumeRoleWithSAMLSessionTags_CLI reads session tags from a SAML
// assertion's PrincipalTag attributes, which the role's trust policy must
// allow with sts:TagSession.
func TestSTS_AssumeRoleWithSAMLSessionTags_CLI(t *testing.T) {
	idp, err := samlidp.New("https://idp.example.test/cli-saml-tags")
	require.NoError(t, err)
	var provider struct {
		SAMLProviderArn string `json:"SAMLProviderArn"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-saml-provider",
		"--name", "cli-saml-tags-idp", "--saml-metadata-document", idp.Metadata())), &provider)
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-saml-provider", "--saml-provider-arn", provider.SAMLProviderArn).Run()
	})
	taggable := cliTeamRole(t, "cli-saml-taggable", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
		`"Principal":{"Federated":"`+provider.SAMLProviderArn+`"},"Action":["sts:AssumeRoleWithSAML","sts:TagSession"]}]}`)
	untaggable := cliTeamRole(t, "cli-saml-untaggable", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
		`"Principal":{"Federated":"`+provider.SAMLProviderArn+`"},"Action":"sts:AssumeRoleWithSAML"}]}`)

	assume := func(roleArn string) *exec.Cmd {
		assertion, err := idp.Response(samlidp.Assertion{Subject: "bob@example.test", Attributes: map[string][]string{
			samlidp.RoleAttribute:                                      {roleArn + "," + provider.SAMLProviderArn},
			samlidp.RoleSessionNameAttribute:                           {"bob"},
			"https://aws.amazon.com/SAML/Attributes/PrincipalTag:team": {"blue"},
		}})
		require.NoError(t, err)
		return awsCLI("sts", "assume-role-with-saml", "--role-arn", roleArn,
			"--principal-arn", provider.SAMLProviderArn, "--saml-assertion", assertion)
	}
	var session cliSessionCredentials
	parseJSON(t, runCLI(t, assume(taggable)), &session)
	runCLI(t, session.asSession(awsCLI("iam", "list-roles")))

	refused := runCLIExpectError(t, assume(untaggable))
	assert.True(t, strings.Contains(refused, "AccessDenied"), "the trust policy does not allow sts:TagSession: %s", refused)
}
