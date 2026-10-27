package azure_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Azure CLI over two surfaces a console reads: the Activity Log
// (`az monitor activity-log list`) and the Azure Blob Storage CORS rules
// (`az storage cors`), which the ARM blobServices/default resource reports and
// the service enforces.
func TestConsoleSurfacesCLI(t *testing.T) {
	t.Parallel()
	env := startAzLoginSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-console-surfaces",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-console-surfaces"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	const group, account = "cli-console-rg", "cliconsolecors"
	runCLI(t, env.command("group", "create", "-n", group, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("storage", "account", "create", "-n", account, "-g", group,
		"-l", "eastus", "--sku", "Standard_LRS", "-o", "json"))

	var events []struct {
		OperationName struct {
			Value string `json:"value"`
		} `json:"operationName"`
		Status struct {
			Value string `json:"value"`
		} `json:"status"`
		EventName struct {
			Value string `json:"value"`
		} `json:"eventName"`
		ResourceID string `json:"resourceId"`
	}
	parseJSON(t, runCLI(t, env.command("monitor", "activity-log", "list", "-g", group, "--offset", "1h", "-o", "json")), &events)
	operations := map[string]string{}
	for _, event := range events {
		if event.EventName.Value == "EndRequest" {
			operations[event.OperationName.Value] = event.Status.Value
		}
	}
	assert.Equal(t, "Succeeded", operations["Microsoft.Resources/subscriptions/resourceGroups/write"])
	assert.Contains(t, operations, "Microsoft.Storage/storageAccounts/write")

	var keys []struct {
		Value string `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "keys", "list", "-n", account, "-g", group, "-o", "json")), &keys)
	require.NotEmpty(t, keys)
	connection := "DefaultEndpointsProtocol=https;AccountName=" + account + ";AccountKey=" + keys[0].Value +
		";BlobEndpoint=" + env.baseURL + "/" + account
	runCLI(t, env.command("storage", "cors", "add", "--services", "b", "--methods", "GET", "PUT",
		"--origins", "https://console.example.com", "--allowed-headers", "x-ms-*", "--max-age", "600",
		"--connection-string", connection))

	var shown struct {
		Cors struct {
			CorsRules []struct {
				AllowedOrigins []string `json:"allowedOrigins"`
				AllowedMethods []string `json:"allowedMethods"`
			} `json:"corsRules"`
		} `json:"cors"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "blob-service-properties", "show",
		"-n", account, "-g", group, "-o", "json")), &shown)
	require.Len(t, shown.Cors.CorsRules, 1, "the data plane's rule is the one ARM reports")
	assert.Equal(t, []string{"https://console.example.com"}, shown.Cors.CorsRules[0].AllowedOrigins)

	_, debug := runCLIStreams(t, env.command("rest", "--method", "options", "--skip-authorization-header",
		"--url", env.baseURL+"/"+account+"/?comp=list",
		"--headers", "Origin=https://console.example.com", "Access-Control-Request-Method=GET",
		"Access-Control-Request-Headers=x-ms-version", "--debug"))
	assert.True(t, strings.Contains(debug, "'Access-Control-Allow-Origin': 'https://console.example.com'"),
		"the preflight names the allowed origin")
}
