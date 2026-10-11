package azure_cli_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loginForwardedHeadersCLI registers the TLS simulator as a cloud, logs the CLI
// in to it, and returns the environment.
func loginForwardedHeadersCLI(t *testing.T, cloud string) azLoginEnv {
	t.Helper()
	env := startAzTLSSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", cloud,
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", cloud))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, env.command("logout")) })
	return env
}

// frontEndEchoedHeaders sends `az rest` to the simulator carrying host and an
// X-Forwarded-For of its own, the way a client resolving the platform hostname
// does, and returns the headers the echoing workload received.
func frontEndEchoedHeaders(t *testing.T, env azLoginEnv, host string) http.Header {
	t.Helper()
	out := runCLI(t, env.command("rest", "--method", "GET", "--url", env.baseURL+"/",
		"--headers", "Host="+host, "X-Forwarded-For=203.0.113.7", "X-Forwarded-Proto=spoofed",
		"--skip-authorization-header"))
	var got http.Header
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	return got
}

// TestWebApp_CLI_ForwardedRequestHeaders runs a custom container web app set
// up with `az webapp config set --startup-file` and `az webapp config
// appsettings set`: the App Service front end appends the client's address
// and port to X-Forwarded-For and names the client's protocol in
// X-Forwarded-Proto and X-AppService-Proto.
func TestWebApp_CLI_ForwardedRequestHeaders(t *testing.T) {
	env := loginForwardedHeadersCLI(t, "sockerless-web-xff")
	rg, app := "web-xff-cli-rg", "web-xff-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	arm := func(resourcePath string) string {
		return fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/%s?api-version=%s",
			env.baseURL, subscriptionID, rg, resourcePath, stage4CLIAPIVersion)
	}
	runCLI(t, env.command("appservice", "plan", "create", "-g", rg, "-n", "web-xff-cli-plan",
		"--is-linux", "--sku", "B1", "-o", "json"))
	planID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/web-xff-cli-plan", subscriptionID, rg)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", arm("Microsoft.Web/sites/"+app), "--body", fmt.Sprintf(
		`{"location":"eastus","kind":"app,linux,container","properties":{"serverFarmId":%q,"reserved":true,"siteConfig":{"linuxFxVersion":"DOCKER|%s"}}}`,
		planID, httpProbeImageName), "-o", "json"))
	t.Cleanup(func() { runCLI(t, env.command("webapp", "delete", "-g", rg, "-n", app)) })
	runCLI(t, env.command("webapp", "config", "set", "-g", rg, "-n", app,
		"--startup-file", "echo-headers", "-o", "json"))
	runCLI(t, env.command("webapp", "config", "appsettings", "set", "-g", rg, "-n", app,
		"--settings", "WEBSITES_PORT=8080", "-o", "json"))

	got := frontEndEchoedHeaders(t, env, app+".azurewebsites.net")
	forwardedFor := strings.Split(got.Get("X-Forwarded-For"), ", ")
	require.Len(t, forwardedFor, 2, "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.Equal(t, "203.0.113.7", forwardedFor[0])
	clientIP, clientPort, err := net.SplitHostPort(forwardedFor[1])
	require.NoError(t, err, "the client's address carries its port: %q", forwardedFor[1])
	assert.True(t, net.ParseIP(clientIP).IsLoopback(), "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.NotEmpty(t, clientPort)
	assert.Equal(t, "https", got.Get("X-Forwarded-Proto"))
	assert.Equal(t, "https", got.Get("X-AppService-Proto"))
}

// TestContainerAppsCLI_IngressForwardedRequestHeaders creates a container app
// with `az rest` and reaches it at its ingress FQDN: ingress appends the
// client's IP to X-Forwarded-For and overwrites X-Forwarded-Proto with the
// protocol the client used.
func TestContainerAppsCLI_IngressForwardedRequestHeaders(t *testing.T) {
	env := loginForwardedHeadersCLI(t, "sockerless-aca-xff")
	rg, app := "aca-xff-cli-rg", "aca-xff-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	rest := func(method, url, body string, extra ...string) *exec.Cmd {
		args := []string{"rest", "--method", method, "--url", url, "-o", "json"}
		if body != "" {
			args = append(args, "--body", body)
		}
		return env.command(append(args, extra...)...)
	}
	appURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s?api-version=%s",
		env.baseURL, subscriptionID, rg, app, acaAPIVersion)
	azLongRunning(t, rest, "PUT", appURL, fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"configuration": {"ingress": {"external": true, "targetPort": 8080}},
			"template": {
				"containers": [{"name": "main", "image": %q, "args": ["echo-headers"]}],
				"scale": {"minReplicas": 1, "maxReplicas": 1}
			}
		}
	}`, httpProbeImageName))
	t.Cleanup(func() { azLongRunning(t, rest, "DELETE", appURL, "") })
	var created struct {
		Properties struct {
			Configuration struct {
				Ingress struct {
					Fqdn string `json:"fqdn"`
				} `json:"ingress"`
			} `json:"configuration"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, rest("GET", appURL, "")), &created)
	require.NotEmpty(t, created.Properties.Configuration.Ingress.Fqdn)

	got := frontEndEchoedHeaders(t, env, created.Properties.Configuration.Ingress.Fqdn)
	forwardedFor := strings.Split(got.Get("X-Forwarded-For"), ", ")
	require.Len(t, forwardedFor, 2, "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.Equal(t, "203.0.113.7", forwardedFor[0])
	client := net.ParseIP(forwardedFor[1])
	require.NotNil(t, client, "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.True(t, client.IsLoopback(), "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.Equal(t, "https", got.Get("X-Forwarded-Proto"))
}
