package aws_cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIAM_ServiceLinkedRole_Lifecycle(t *testing.T) {
	suffix := "cli" + time.Now().Format("150405")
	out := runCLI(t, awsCLI("iam", "create-service-linked-role",
		"--aws-service-name", "cloudfront.amazonaws.com",
		"--custom-suffix", suffix,
		"--description", "cli SLR test",
		"--output", "json",
	))
	var createResult struct {
		Role struct {
			RoleName string `json:"RoleName"`
			Arn      string `json:"Arn"`
		} `json:"Role"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.True(t, strings.HasPrefix(createResult.Role.RoleName, "AWSServiceRoleForCloudFrontLogger_"))
	require.Contains(t, createResult.Role.Arn, "aws-service-role/cloudfront.amazonaws.com")

	delOut := runCLI(t, awsCLI("iam", "delete-service-linked-role",
		"--role-name", createResult.Role.RoleName, "--output", "json"))
	var delResult struct {
		DeletionTaskId string `json:"DeletionTaskId"`
	}
	require.NoError(t, json.Unmarshal([]byte(delOut), &delResult))
	require.NotEmpty(t, delResult.DeletionTaskId)

	statusOut := runCLI(t, awsCLI("iam", "get-service-linked-role-deletion-status",
		"--deletion-task-id", delResult.DeletionTaskId, "--output", "json"))
	var statusResult struct {
		Status string `json:"Status"`
	}
	require.NoError(t, json.Unmarshal([]byte(statusOut), &statusResult))
	require.Equal(t, "SUCCEEDED", statusResult.Status)
}

func TestIAM_OIDCProvider_Lifecycle(t *testing.T) {
	url := "https://oidc.eks.us-east-1.amazonaws.com/id/cli" + time.Now().Format("150405")
	out := runCLI(t, awsCLI("iam", "create-open-id-connect-provider",
		"--url", url,
		"--client-id-list", "sts.amazonaws.com",
		"--thumbprint-list", "9e99a48a9960b14926bb7f3b02e22da2b0ab7280",
		"--output", "json",
	))
	var createResult struct {
		OpenIDConnectProviderArn string `json:"OpenIDConnectProviderArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	arn := createResult.OpenIDConnectProviderArn
	require.NotEmpty(t, arn)

	runCLI(t, awsCLI("iam", "get-open-id-connect-provider",
		"--open-id-connect-provider-arn", arn, "--output", "json"))

	runCLI(t, awsCLI("iam", "add-client-id-to-open-id-connect-provider",
		"--open-id-connect-provider-arn", arn,
		"--client-id", "extra-client"))

	runCLI(t, awsCLI("iam", "update-open-id-connect-provider-thumbprint",
		"--open-id-connect-provider-arn", arn,
		"--thumbprint-list", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"))

	runCLI(t, awsCLI("iam", "list-open-id-connect-providers", "--output", "json"))

	runCLI(t, awsCLI("iam", "delete-open-id-connect-provider",
		"--open-id-connect-provider-arn", arn))
}
