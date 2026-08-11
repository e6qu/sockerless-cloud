package aws_cli_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAmplify_DomainAndBackend_Lifecycle(t *testing.T) {
	name := "cli-dom-" + time.Now().Format("150405.000000")
	appOut := runCLI(t, awsCLI("amplify", "create-app",
		"--name", name, "--output", "json"))
	var appResult struct {
		App struct {
			AppId string `json:"appId"`
		} `json:"app"`
	}
	require.NoError(t, json.Unmarshal([]byte(appOut), &appResult))
	appID := appResult.App.AppId
	runCLI(t, awsCLI("amplify", "create-branch",
		"--app-id", appID, "--branch-name", "main"))

	// Domain
	domOut := runCLI(t, awsCLI("amplify", "create-domain-association",
		"--app-id", appID,
		"--domain-name", "cli.example.com",
		"--sub-domain-settings", "prefix=www,branchName=main",
		"--output", "json",
	))
	var domResult struct {
		DomainAssociation struct {
			DomainName   string `json:"domainName"`
			DomainStatus string `json:"domainStatus"`
		} `json:"domainAssociation"`
	}
	require.NoError(t, json.Unmarshal([]byte(domOut), &domResult))
	require.Equal(t, "cli.example.com", domResult.DomainAssociation.DomainName)
	// No verification records exist in the sim's Route 53, so the
	// association honestly reports PENDING_VERIFICATION.
	require.Equal(t, "PENDING_VERIFICATION", domResult.DomainAssociation.DomainStatus)

	runCLI(t, awsCLI("amplify", "get-domain-association",
		"--app-id", appID, "--domain-name", "cli.example.com", "--output", "json"))
	runCLI(t, awsCLI("amplify", "list-domain-associations",
		"--app-id", appID, "--output", "json"))

	// Backend environment
	beOut := runCLI(t, awsCLI("amplify", "create-backend-environment",
		"--app-id", appID,
		"--environment-name", "cli-staging",
		"--stack-name", "cli-stack",
		"--output", "json",
	))
	var beResult struct {
		BackendEnvironment struct {
			EnvironmentName string `json:"environmentName"`
		} `json:"backendEnvironment"`
	}
	require.NoError(t, json.Unmarshal([]byte(beOut), &beResult))
	require.Equal(t, "cli-staging", beResult.BackendEnvironment.EnvironmentName)

	// Cleanup
	runCLI(t, awsCLI("amplify", "delete-domain-association",
		"--app-id", appID, "--domain-name", "cli.example.com"))
	runCLI(t, awsCLI("amplify", "delete-backend-environment",
		"--app-id", appID, "--environment-name", "cli-staging"))
	runCLI(t, awsCLI("amplify", "delete-app", "--app-id", appID))
}
