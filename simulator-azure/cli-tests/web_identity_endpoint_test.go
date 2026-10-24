package azure_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the managed identity endpoint of an App Service app: the
// app's code gets tokens for exactly the identities its site resource names.
// The simulator terminates TLS, as a deployment the CLI trusts through
// REQUESTS_CA_BUNDLE does, and the app still reaches its IDENTITY_ENDPOINT:
// the simulator serves the workload-facing endpoints plain beside the TLS
// listener.
//
//	PUT  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ManagedIdentity/userAssignedIdentities/{resourceName}
//	PUT  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}
//	PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}
//	GET {IDENTITY_ENDPOINT}?api-version=2019-08-01&resource={resource}[&client_id]

func TestWebAppIdentityEndpoint_MintsTheAssignedIdentities(t *testing.T) {
	t.Parallel()
	env := startAzTLSSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-identity-endpoint",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-identity-endpoint"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	const rg, app = "identity-endpoint-cli-rg", "identity-endpoint-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	arm := func(resourcePath, apiVersion string) string {
		return fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/%s?api-version=%s",
			env.baseURL, subscriptionID, rg, resourcePath, apiVersion)
	}
	rest := func(method, url, body string) string {
		args := []string{"rest", "--method", method, "--url", url, "-o", "json"}
		if body != "" {
			args = append(args, "--body", body)
		}
		return runCLI(t, env.command(args...))
	}
	rest("PUT", arm("Microsoft.Web/serverfarms/identity-endpoint-cli-plan", stage4CLIAPIVersion),
		`{"location":"eastus","kind":"linux","sku":{"name":"S1","tier":"Standard"},"properties":{"reserved":true}}`)
	type uai struct {
		ID         string `json:"id"`
		Properties struct {
			ClientID    string `json:"clientId"`
			PrincipalID string `json:"principalId"`
		} `json:"properties"`
	}
	createIdentity := func(name string) uai {
		var out uai
		parseJSON(t, rest("PUT", arm("Microsoft.ManagedIdentity/userAssignedIdentities/"+name, "2023-01-31"), `{"location":"eastus"}`), &out)
		return out
	}
	attached := createIdentity("identity-endpoint-cli-attached")
	other := createIdentity("identity-endpoint-cli-other")

	siteURL := arm("Microsoft.Web/sites/"+app, stage4CLIAPIVersion)
	rest("PUT", siteURL, fmt.Sprintf(`{
		"location": "eastus",
		"kind": "app,linux,container",
		"properties": {
			"serverFarmId": "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/identity-endpoint-cli-plan",
			"reserved": true,
			"siteConfig": {"linuxFxVersion": "DOCKER|%s", "appCommandLine": "files-http 80 /home/site/wwwroot"}
		}
	}`, subscriptionID, rg, commandImageName))
	defer rest("DELETE", siteURL, "")

	// The app's hostname is routed by Host, so the request carries it to the
	// simulator's TLS endpoint.
	read := func(path string) (string, error) {
		cmd := env.command("rest", "--method", "GET", "--url", env.baseURL+path,
			"--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header")
		out, stderr, err := runCLIStreamsResult(cmd)
		if err != nil {
			return "", fmt.Errorf("%w: %s", err, stderr)
		}
		return strings.TrimSpace(out), nil
	}
	_, err := read("/env/IDENTITY_ENDPOINT")
	require.Error(t, err, "an app without an identity has no IDENTITY_ENDPOINT")

	var assigned struct {
		Identity struct {
			PrincipalID string `json:"principalId"`
		} `json:"identity"`
	}
	parseJSON(t, rest("PATCH", siteURL, fmt.Sprintf(
		`{"identity":{"type":"SystemAssigned, UserAssigned","userAssignedIdentities":{%q:{}}}}`, attached.ID)), &assigned)
	require.NotEmpty(t, assigned.Identity.PrincipalID)
	rest("POST", arm("Microsoft.Web/sites/"+app+"/restart", stage4CLIAPIVersion), "")

	endpoint, err := read("/env/IDENTITY_ENDPOINT")
	require.NoError(t, err, "an app with an identity has an IDENTITY_ENDPOINT")
	assert.True(t, strings.HasPrefix(strings.Trim(endpoint, `"`), "http://"), "IDENTITY_ENDPOINT is a plain-HTTP URL: %s", endpoint)

	token := func(query url.Values) (map[string]any, error) {
		t.Helper()
		query.Set("resource", "https://vault.azure.net")
		out, err := read("/identity-token?" + query.Encode())
		if err != nil {
			return nil, err
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &body), out)
		parts := strings.Split(body.AccessToken, ".")
		require.Len(t, parts, 3, out)
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		require.NoError(t, err)
		var claims map[string]any
		require.NoError(t, json.Unmarshal(payload, &claims))
		return claims, nil
	}
	claims, err := token(url.Values{})
	require.NoError(t, err)
	assert.Equal(t, assigned.Identity.PrincipalID, claims["oid"], "the token is the system-assigned identity's")
	claims, err = token(url.Values{"client_id": {attached.Properties.ClientID}})
	require.NoError(t, err)
	assert.Equal(t, attached.Properties.PrincipalID, claims["oid"], "the token is the named user-assigned identity's")
	_, err = token(url.Values{"client_id": {other.Properties.ClientID}})
	require.Error(t, err, "the app does not have the other identity")
	assert.Contains(t, err.Error(), "Unable to load the proper Managed Identity.")
	_, err = token(url.Values{"identity-header": {"not-this-apps-secret"}})
	require.Error(t, err, "a request without the app's IDENTITY_HEADER is refused")

	rest("PATCH", siteURL, `{"identity":{"type":"None"}}`)
	rest("POST", arm("Microsoft.Web/sites/"+app+"/restart", stage4CLIAPIVersion), "")
	_, err = read("/env/IDENTITY_ENDPOINT")
	require.Error(t, err, "an app whose identities are removed has no IDENTITY_ENDPOINT")
}
