package aws_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretsManagerCLI_SecretLifecycle(t *testing.T) {
	name := "cli-secret-" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")
	createOut := runCLI(t, awsCLI("secretsmanager", "create-secret",
		"--name", name,
		"--description", "cli secret coverage",
		"--secret-string", "initial",
		"--output", "json"))
	var createResult struct {
		ARN       string `json:"ARN"`
		Name      string `json:"Name"`
		VersionId string `json:"VersionId"`
	}
	parseJSON(t, createOut, &createResult)
	require.Equal(t, name, createResult.Name)
	require.Contains(t, createResult.ARN, ":secret:"+name+"-")
	require.NotEmpty(t, createResult.VersionId)
	t.Cleanup(func() {
		_ = awsCLI("secretsmanager", "delete-secret",
			"--secret-id", name,
			"--force-delete-without-recovery").Run()
	})

	getOut := runCLI(t, awsCLI("secretsmanager", "get-secret-value",
		"--secret-id", name,
		"--output", "json"))
	var getResult struct {
		SecretString string `json:"SecretString"`
	}
	parseJSON(t, getOut, &getResult)
	require.Equal(t, "initial", getResult.SecretString)

	putOut := runCLI(t, awsCLI("secretsmanager", "put-secret-value",
		"--secret-id", name,
		"--secret-string", "rotated",
		"--output", "json"))
	var putResult struct {
		VersionId string `json:"VersionId"`
	}
	parseJSON(t, putOut, &putResult)
	require.NotEmpty(t, putResult.VersionId)
	require.NotEqual(t, createResult.VersionId, putResult.VersionId)

	rotatedOut := runCLI(t, awsCLI("secretsmanager", "get-secret-value",
		"--secret-id", createResult.ARN,
		"--output", "json"))
	var rotatedResult struct {
		SecretString string `json:"SecretString"`
	}
	parseJSON(t, rotatedOut, &rotatedResult)
	require.Equal(t, "rotated", rotatedResult.SecretString)
}

func TestSecretsManagerCLI_GetResourcePolicy(t *testing.T) {
	createOut := runCLI(t, awsCLI("secretsmanager", "create-secret",
		"--name", "grp-cli-secret",
		"--secret-string", "value",
		"--output", "json"))
	var created struct {
		ARN  string `json:"ARN"`
		Name string `json:"Name"`
	}
	parseJSON(t, createOut, &created)
	require.NotEmpty(t, created.ARN)
	t.Cleanup(func() {
		_ = awsCLI("secretsmanager", "delete-secret",
			"--secret-id", "grp-cli-secret",
			"--force-delete-without-recovery").Run()
	})

	// No policy attached — response must have ARN and Name.
	out := runCLI(t, awsCLI("secretsmanager", "get-resource-policy",
		"--secret-id", "grp-cli-secret",
		"--output", "json"))
	var result struct {
		ARN  string `json:"ARN"`
		Name string `json:"Name"`
	}
	parseJSON(t, out, &result)
	assert.Equal(t, created.ARN, result.ARN)
	assert.Equal(t, "grp-cli-secret", result.Name)
}
