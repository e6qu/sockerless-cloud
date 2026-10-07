package azure_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the managed identity endpoint of an App Service app: the
// app's code gets tokens for exactly the identities its site resource names.
//
//	PUT  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ManagedIdentity/userAssignedIdentities/{resourceName}
//	PUT  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}
//	PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}
//	GET {IDENTITY_ENDPOINT}?api-version=2019-08-01&resource={resource}[&client_id]

func TestWebAppIdentityEndpoint_MintsTheAssignedIdentities(t *testing.T) {
	const app = "identity-endpoint-cli-app"
	runCLI(t, azRest("PUT", aspURL("serverfarms/identity-endpoint-cli-plan"),
		`{"location":"eastus","kind":"linux","sku":{"name":"S1","tier":"Standard"},"properties":{"reserved":true}}`))
	type uai struct {
		ID         string `json:"id"`
		Properties struct {
			ClientID    string `json:"clientId"`
			PrincipalID string `json:"principalId"`
		} `json:"properties"`
	}
	createIdentity := func(name string) uai {
		var out uai
		identityURL := armURL("Microsoft.ManagedIdentity", "userAssignedIdentities/"+name, "2023-01-31")
		parseJSON(t, runCLI(t, azRest("PUT", identityURL, `{"location":"eastus"}`)), &out)
		t.Cleanup(func() { _ = azRest("DELETE", identityURL, "").Run() })
		return out
	}
	attached := createIdentity("identity-endpoint-cli-attached")
	other := createIdentity("identity-endpoint-cli-other")

	siteURL := armURL("Microsoft.Web", "sites/"+app, "2024-04-01")
	runCLI(t, azRest("PUT", siteURL, fmt.Sprintf(`{
		"location": "eastus",
		"kind": "app,linux,container",
		"properties": {
			"serverFarmId": "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/identity-endpoint-cli-plan",
			"reserved": true,
			"siteConfig": {"linuxFxVersion": "DOCKER|%s", "appCommandLine": "files-http 80 /home/site/wwwroot"}
		}
	}`, subscriptionID, resourceGroup, commandImageName)))
	t.Cleanup(func() { _ = azRest("DELETE", siteURL, "").Run() })

	read := func(path string) (string, error) {
		cmd := exec.Command("az", "rest", "--method", "GET", "--url", baseURL+path,
			"--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header")
		cmd.Env = append(os.Environ(), "AZURE_CONFIG_DIR="+filepath.Join(tmpDir, "azure-config"), "AZURE_CORE_NO_COLOR=1")
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
	parseJSON(t, runCLI(t, azRest("PATCH", siteURL, fmt.Sprintf(
		`{"identity":{"type":"SystemAssigned, UserAssigned","userAssignedIdentities":{%q:{}}}}`, attached.ID))), &assigned)
	require.NotEmpty(t, assigned.Identity.PrincipalID)
	runCLI(t, azRest("POST", armURL("Microsoft.Web", "sites/"+app+"/restart", "2024-04-01"), ""))

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

	runCLI(t, azRest("PATCH", siteURL, `{"identity":{"type":"None"}}`))
	runCLI(t, azRest("POST", armURL("Microsoft.Web", "sites/"+app+"/restart", "2024-04-01"), ""))
	_, err = read("/env/IDENTITY_ENDPOINT")
	require.Error(t, err, "an app whose identities are removed has no IDENTITY_ENDPOINT")
}
