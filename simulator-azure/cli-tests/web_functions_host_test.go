package azure_cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFunctionApp_CLI_NodeHostRunsTheDeployedFunctions drives a Linux
// function app on a built-in stack with the native az commands: `az
// functionapp list-runtimes` reads the function app stack catalogue, `az
// functionapp create --runtime node` creates the app on a stack from it, `az
// functionapp deploy` deploys a package the Azure Functions host then runs,
// and the keys `az functionapp keys` and `az functionapp function keys` manage
// are the keys the running host accepts. list-runtimes reads
// GET /providers/Microsoft.Web/functionAppStacks.
func TestFunctionApp_CLI_NodeHostRunsTheDeployedFunctions(t *testing.T) {
	env := startAzTLSSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-funchost",
		"--endpoint-resource-manager", env.baseURL+"/",
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-funchost"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	var listed []struct {
		Runtime string `json:"runtime"`
		Version string `json:"version"`
		Config  string `json:"linux_fx_version"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "list-runtimes", "--os", "linux", "-o", "json")), &listed)
	require.Len(t, listed, 1)
	assert.Equal(t, "node", listed[0].Runtime)
	assert.Equal(t, "22", listed[0].Version)
	assert.Equal(t, "Node|22", listed[0].Config)

	rg, plan, storage, app := "funchost-cli-rg", "funchost-cli-plan", "funchostclisa", "funchost-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("storage", "account", "create", "-g", rg, "-n", storage, "-l", "eastus",
		"--sku", "Standard_LRS", "-o", "json"))
	runCLI(t, env.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "B1", "-o", "json"))
	runCLI(t, env.command("functionapp", "create", "-g", rg, "-p", plan, "-n", app,
		"--storage-account", storage, "--runtime", "node", "--runtime-version", "22",
		"--functions-version", "4", "--os-type", "Linux", "--disable-app-insights", "-o", "json"))
	defer runCLI(t, env.command("functionapp", "delete", "-g", rg, "-n", app))

	var shown struct {
		LinuxFxVersion string `json:"linuxFxVersion"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "config", "show", "-g", rg, "-n", app, "-o", "json")), &shown)
	assert.Equal(t, "Node|22", shown.LinuxFxVersion)

	runCLI(t, env.command("functionapp", "config", "appsettings", "set", "-g", rg, "-n", app,
		"--settings", "AzureWebJobsSecretStorageType=files", "-o", "json"))
	pkgURL := stage3ServeZip(t, stage3Zip(t, map[string]string{
		"host.json": `{"version":"2.0"}`,
		"hello/function.json": `{"bindings":[` +
			`{"type":"httpTrigger","direction":"in","name":"req","authLevel":"function","methods":["get"]},` +
			`{"type":"http","direction":"out","name":"res"}]}`,
		"hello/index.js": `module.exports = async function (context, req) {` +
			` context.res = { body: "hello from " + process.env.WEBSITE_SITE_NAME + " on node " + process.version }; };`,
	}))
	runCLI(t, env.command("functionapp", "deploy", "-g", rg, "-n", app,
		"--src-url", pkgURL, "--type", "zip", "-o", "json"))

	invoke := func(path string) (string, error) {
		cmd := env.command("rest", "--method", "GET", "--url", env.baseURL+path,
			"--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header")
		out, err := cmd.Output()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return strings.TrimSpace(string(exitErr.Stderr)), err
		}
		return strings.TrimSpace(string(out)), err
	}
	out, err := invoke("/api/hello")
	require.Error(t, err, "a function-level function refuses a request without a key: %s", out)
	assert.Contains(t, out, "Unauthorized")

	var functions []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "function", "list", "-g", rg, "-n", app, "-o", "json")), &functions)
	require.Len(t, functions, 1)
	assert.Equal(t, app+"/hello", functions[0].Name)

	var fnKeys map[string]string
	parseJSON(t, runCLI(t, env.command("functionapp", "function", "keys", "list", "-g", rg, "-n", app,
		"--function-name", "hello", "-o", "json")), &fnKeys)
	require.Contains(t, fnKeys, "default")
	out, err = invoke("/api/hello?code=" + fnKeys["default"])
	require.NoError(t, err, out)
	assert.Regexp(t, regexp.MustCompile(`^hello from `+app+` on node v22\.\d+\.\d+$`), out)

	runCLI(t, env.command("functionapp", "function", "keys", "set", "-g", rg, "-n", app,
		"--function-name", "hello", "--key-name", "cli", "--key-value", "cli-function-key", "-o", "json"))
	out, err = invoke("/api/hello?code=cli-function-key")
	require.NoError(t, err, out)
	runCLI(t, env.command("functionapp", "function", "keys", "delete", "-g", rg, "-n", app,
		"--function-name", "hello", "--key-name", "cli"))
	out, err = invoke("/api/hello?code=cli-function-key")
	require.Error(t, err, "a deleted key no longer opens the function: %s", out)

	var hostKeys struct {
		MasterKey    string            `json:"masterKey"`
		FunctionKeys map[string]string `json:"functionKeys"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "keys", "list", "-g", rg, "-n", app, "-o", "json")), &hostKeys)
	out, err = invoke("/api/hello?code=" + hostKeys.FunctionKeys["default"])
	require.NoError(t, err, out)

	// az functionapp keys set prints the key redacted; the list reads it.
	runCLI(t, env.command("functionapp", "keys", "set", "-g", rg, "-n", app,
		"--key-type", "functionKeys", "--key-name", "deploy", "-o", "json"))
	parseJSON(t, runCLI(t, env.command("functionapp", "keys", "list", "-g", rg, "-n", app, "-o", "json")), &hostKeys)
	require.Contains(t, hostKeys.FunctionKeys, "deploy")
	out, err = invoke("/api/hello?code=" + hostKeys.FunctionKeys["deploy"])
	require.NoError(t, err, out)
}

// TestFunctionApp_CLI_BlobSecretStoreHoldsTheKeys drives the default secret
// store with the native az commands: `az functionapp create` sets
// AzureWebJobsStorage to the storage account's connection string, and the
// keys `az functionapp keys` lists and sets live in the blob
// azure-webjobs-secrets/<app>/host.json that `az storage blob download` reads
// from that account.
func TestFunctionApp_CLI_BlobSecretStoreHoldsTheKeys(t *testing.T) {
	env := startAzLoginSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-funcblob",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-funcblob"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	rg, plan, storage, app := "funcblob-cli-rg", "funcblob-cli-plan", "funcblobclisa", "funcblob-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("storage", "account", "create", "-g", rg, "-n", storage, "-l", "eastus",
		"--sku", "Standard_LRS", "-o", "json"))
	runCLI(t, env.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "B1", "-o", "json"))
	runCLI(t, env.command("functionapp", "create", "-g", rg, "-p", plan, "-n", app,
		"--storage-account", storage, "--runtime", "node", "--runtime-version", "22",
		"--functions-version", "4", "--os-type", "Linux", "--disable-app-insights", "-o", "json"))
	defer runCLI(t, env.command("functionapp", "delete", "-g", rg, "-n", app))

	var accountKeys []struct {
		Value string `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "keys", "list",
		"-g", rg, "-n", storage, "-o", "json")), &accountKeys)
	require.NotEmpty(t, accountKeys)
	type secretKey struct {
		Name      string `json:"name"`
		Value     string `json:"value"`
		Encrypted bool   `json:"encrypted"`
	}
	var stored struct {
		MasterKey    secretKey   `json:"masterKey"`
		FunctionKeys []secretKey `json:"functionKeys"`
	}
	readHostSecrets := func() {
		t.Helper()
		dst := filepath.Join(t.TempDir(), "host.json")
		runCLI(t, env.command(azMoveStorageBlobCmd(env, storage, accountKeys[0].Value,
			"storage", "blob", "download", "--container-name", "azure-webjobs-secrets",
			"--name", app+"/host.json", "--file", dst)...))
		data, err := os.ReadFile(dst)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &stored))
	}

	var hostKeys struct {
		MasterKey    string            `json:"masterKey"`
		FunctionKeys map[string]string `json:"functionKeys"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "keys", "list", "-g", rg, "-n", app, "-o", "json")), &hostKeys)
	require.NotEmpty(t, hostKeys.MasterKey)
	readHostSecrets()
	assert.Equal(t, "master", stored.MasterKey.Name)
	assert.True(t, stored.MasterKey.Encrypted, "the host protects key values in its blob store on App Service")

	runCLI(t, env.command("functionapp", "keys", "set", "-g", rg, "-n", app,
		"--key-type", "functionKeys", "--key-name", "deploy", "--key-value", "cli-deploy-key", "-o", "json"))
	readHostSecrets()
	var names []string
	for _, k := range stored.FunctionKeys {
		names = append(names, k.Name)
	}
	assert.Contains(t, names, "deploy")
	parseJSON(t, runCLI(t, env.command("functionapp", "keys", "list", "-g", rg, "-n", app, "-o", "json")), &hostKeys)
	assert.Equal(t, "cli-deploy-key", hostKeys.FunctionKeys["deploy"])
}
