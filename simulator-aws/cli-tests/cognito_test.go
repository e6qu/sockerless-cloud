package aws_cli_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var cognitoCLISeq atomic.Int64

// TestCognitoCLI_UserPoolClientDomainAndUsers drives a user pool through
// `aws cognito-idp`: the pool and its password policy, an app client with a
// secret, a prefix domain, a user whose password the policy checks, and the
// teardown a pool with a domain requires.
func TestCognitoCLI_UserPoolClientDomainAndUsers(t *testing.T) {
	t.Parallel()
	suffix := fmt.Sprintf("%d-%d", time.Now().UnixNano()%1_000_000, cognitoCLISeq.Add(1))

	var pool struct {
		UserPool struct {
			Id       string `json:"Id"`
			Arn      string `json:"Arn"`
			Policies struct {
				PasswordPolicy struct {
					MinimumLength int `json:"MinimumLength"`
				} `json:"PasswordPolicy"`
			} `json:"Policies"`
		} `json:"UserPool"`
	}
	parseJSON(t, runCLI(t, awsCLI("cognito-idp", "create-user-pool",
		"--pool-name", "cli-pool-"+suffix,
		"--policies", "PasswordPolicy={MinimumLength=12,RequireNumbers=true}",
		"--user-pool-tags", "team=auth",
		"--output", "json")), &pool)
	poolID := pool.UserPool.Id
	require.True(t, strings.HasPrefix(poolID, "us-east-1_"), poolID)
	assert.Equal(t, 12, pool.UserPool.Policies.PasswordPolicy.MinimumLength)

	var client struct {
		UserPoolClient struct {
			ClientId          string   `json:"ClientId"`
			ClientSecret      string   `json:"ClientSecret"`
			AllowedOAuthFlows []string `json:"AllowedOAuthFlows"`
			CallbackURLs      []string `json:"CallbackURLs"`
		} `json:"UserPoolClient"`
	}
	parseJSON(t, runCLI(t, awsCLI("cognito-idp", "create-user-pool-client",
		"--user-pool-id", poolID, "--client-name", "alb",
		"--generate-secret",
		"--allowed-o-auth-flows", "code",
		"--allowed-o-auth-flows-user-pool-client",
		"--allowed-o-auth-scopes", "openid", "email",
		"--callback-urls", "https://app.example.com/oauth2/idpresponse",
		"--supported-identity-providers", "COGNITO",
		"--output", "json")), &client)
	require.NotEmpty(t, client.UserPoolClient.ClientId)
	assert.NotEmpty(t, client.UserPoolClient.ClientSecret)
	assert.Equal(t, []string{"code"}, client.UserPoolClient.AllowedOAuthFlows)

	domain := "cli-login-" + suffix
	runCLI(t, awsCLI("cognito-idp", "create-user-pool-domain", "--user-pool-id", poolID, "--domain", domain, "--output", "json"))
	var described struct {
		DomainDescription struct {
			UserPoolId string `json:"UserPoolId"`
			Status     string `json:"Status"`
		} `json:"DomainDescription"`
	}
	parseJSON(t, runCLI(t, awsCLI("cognito-idp", "describe-user-pool-domain", "--domain", domain, "--output", "json")), &described)
	assert.Equal(t, poolID, described.DomainDescription.UserPoolId)
	assert.Equal(t, "ACTIVE", described.DomainDescription.Status)

	runCLI(t, awsCLI("cognito-idp", "admin-create-user", "--user-pool-id", poolID, "--username", "dave",
		"--user-attributes", "Name=email,Value=dave@example.com", "--message-action", "SUPPRESS", "--output", "json"))
	weak, err := awsCLI("cognito-idp", "admin-set-user-password", "--user-pool-id", poolID, "--username", "dave",
		"--password", "short1", "--permanent").CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(weak), "InvalidPasswordException")
	runCLI(t, awsCLI("cognito-idp", "admin-set-user-password", "--user-pool-id", poolID, "--username", "dave",
		"--password", "long-enough-pass-7", "--permanent"))
	var user struct {
		UserStatus string `json:"UserStatus"`
	}
	parseJSON(t, runCLI(t, awsCLI("cognito-idp", "admin-get-user", "--user-pool-id", poolID, "--username", "dave", "--output", "json")), &user)
	assert.Equal(t, "CONFIRMED", user.UserStatus)
	var users struct {
		Users []struct {
			Username string `json:"Username"`
		} `json:"Users"`
	}
	parseJSON(t, runCLI(t, awsCLI("cognito-idp", "list-users", "--user-pool-id", poolID,
		"--filter", `email = "dave@example.com"`, "--output", "json")), &users)
	require.Len(t, users.Users, 1)
	assert.Equal(t, "dave", users.Users[0].Username)

	var tags struct {
		Tags map[string]string `json:"Tags"`
	}
	parseJSON(t, runCLI(t, awsCLI("cognito-idp", "list-tags-for-resource", "--resource-arn", pool.UserPool.Arn, "--output", "json")), &tags)
	assert.Equal(t, map[string]string{"team": "auth"}, tags.Tags)

	refused, err := awsCLI("cognito-idp", "delete-user-pool", "--user-pool-id", poolID).CombinedOutput()
	require.Error(t, err, "a pool with a domain refuses the delete")
	assert.Contains(t, string(refused), "InvalidParameterException")
	runCLI(t, awsCLI("cognito-idp", "delete-user-pool-domain", "--user-pool-id", poolID, "--domain", domain))
	runCLI(t, awsCLI("cognito-idp", "admin-delete-user", "--user-pool-id", poolID, "--username", "dave"))
	runCLI(t, awsCLI("cognito-idp", "delete-user-pool-client", "--user-pool-id", poolID, "--client-id", client.UserPoolClient.ClientId))
	runCLI(t, awsCLI("cognito-idp", "delete-user-pool", "--user-pool-id", poolID))
	missing, err := awsCLI("cognito-idp", "describe-user-pool", "--user-pool-id", poolID).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(missing), "ResourceNotFoundException")
}
