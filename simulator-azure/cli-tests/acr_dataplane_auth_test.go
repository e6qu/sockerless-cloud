package azure_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the Azure Container Registry data plane's credential
// contract. The admin credential `az acr credential show` serves — the same
// pair the azurerm provider exposes as admin_username/admin_password — is the
// one `docker login <loginServer>` presents, and the registry's token service
// trades it for the scoped access token the /v2/ data plane accepts. A caller
// with no credential, with the wrong password, or with a token scoped to
// another repository is refused. Routes exercised:
//
//	GET /oauth2/token (the Docker Registry v2 token endpoint)
//	GET /v2/ and PUT|GET /v2/{name}/manifests/{reference}
//	GET /acr/v1/_catalog and GET /acr/v1/{name}/_tags
//	POST …/providers/Microsoft.ContainerRegistry/registries/{name}/listcredentials

// acrCLICredentials reads a registry's admin credential through the ARM action
// `az acr credential show` wraps.
func acrCLICredentials(t *testing.T, credentialsURL string) (string, []string) {
	t.Helper()
	out := runCLI(t, azRest("POST", credentialsURL, ""))
	var creds struct {
		Username  string `json:"username"`
		Passwords []struct {
			Value string `json:"value"`
		} `json:"passwords"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &creds))
	values := make([]string, 0, len(creds.Passwords))
	for _, p := range creds.Passwords {
		values = append(values, p.Value)
	}
	return creds.Username, values
}

func acrCLIBasic(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func TestACRCLI_DataPlaneCredentialContract(t *testing.T) {
	const registryName = "cliauthregistry"
	registryURL := acrURL("registries/" + registryName)
	out := runCLI(t, azRest("PUT", registryURL,
		`{"location":"eastus","sku":{"name":"Standard"},"properties":{"adminUserEnabled":true}}`))
	var registry struct {
		ID         string `json:"id"`
		Properties struct {
			LoginServer string `json:"loginServer"`
		} `json:"properties"`
	}
	parseJSON(t, out, &registry)
	require.NotEmpty(t, registry.Properties.LoginServer)
	t.Cleanup(func() { runCLI(t, azRest("DELETE", registryURL, "")) })

	username, passwords := acrCLICredentials(t,
		baseURL+registry.ID+"/listcredentials?api-version="+acrAPIVersion)
	require.Len(t, passwords, 2)
	password := passwords[0]

	// The registry's data plane is Host-routed to its login server; the CLI
	// dials the loopback base URL and carries that host, which avoids
	// depending on `*.localhost` resolving (Linux does, macOS does not).
	loginServer := registry.Properties.LoginServer
	dataPlane := func(headers ...string) []string {
		return append([]string{"--headers", "Host=" + loginServer}, headers...)
	}

	// No credential at all: the registry refuses with the Docker Registry v2
	// UNAUTHORIZED error.
	failure := runStorageCLIExpectFailure(t, azRest("GET", baseURL+"/v2/", "", dataPlane()...))
	assert.Contains(t, failure, "authentication required")

	// The wrong password buys no token from the registry's token service.
	tokenURL := baseURL + "/oauth2/token?service=" + url.QueryEscape(loginServer) +
		"&scope=" + url.QueryEscape("repository:cli/app:pull,push")
	failure = runStorageCLIExpectFailure(t, azRest("GET", tokenURL, "",
		dataPlane("Authorization="+acrCLIBasic(username, "not-the-password"))...))
	assert.Contains(t, failure, "not valid for this registry")

	// The admin credential does: the token endpoint returns the scoped ACR
	// access token, which the data plane accepts for that repository.
	tokenOut := runCLI(t, azRest("GET", tokenURL, "",
		dataPlane("Authorization="+acrCLIBasic(username, password))...))
	var token struct {
		AccessToken string `json:"access_token"`
	}
	parseJSON(t, tokenOut, &token)
	require.NotEmpty(t, token.AccessToken)

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`
	runCLI(t, azRest("PUT", baseURL+"/v2/cli/app/manifests/v1", manifest,
		dataPlane("Authorization=Bearer "+token.AccessToken,
			"Content-Type=application/vnd.docker.distribution.manifest.v2+json")...))

	// The same token does not reach another repository of the registry.
	failure = runStorageCLIExpectFailure(t, azRest("PUT", baseURL+"/v2/cli/other/manifests/v1", manifest,
		dataPlane("Authorization=Bearer "+token.AccessToken,
			"Content-Type=application/vnd.docker.distribution.manifest.v2+json")...))
	assert.Contains(t, failure, "authentication required")

	// The admin credential also authenticates the convenience API the
	// `az acr repository` commands wrap.
	catalogOut := runCLI(t, azRest("GET", baseURL+"/acr/v1/_catalog", "",
		dataPlane("Authorization="+acrCLIBasic(username, password))...))
	var catalog struct {
		Repositories []string `json:"repositories"`
	}
	parseJSON(t, catalogOut, &catalog)
	assert.Contains(t, catalog.Repositories, "cli/app")

	tagsOut := runCLI(t, azRest("GET", baseURL+"/acr/v1/cli/app/_tags", "",
		dataPlane("Authorization="+acrCLIBasic(username, password))...))
	var tags struct {
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}
	parseJSON(t, tagsOut, &tags)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "v1", tags.Tags[0].Name)

	// Regenerating the admin password revokes the credential and the tokens
	// derived from it.
	runCLI(t, azRest("POST", baseURL+registry.ID+"/regenerateCredential?api-version="+acrAPIVersion,
		`{"name":"password"}`))
	failure = runStorageCLIExpectFailure(t, azRest("GET", baseURL+"/acr/v1/_catalog", "",
		dataPlane("Authorization="+acrCLIBasic(username, password))...))
	assert.Contains(t, failure, "authentication required")
	failure = runStorageCLIExpectFailure(t, azRest("GET", baseURL+"/v2/cli/app/manifests/v1", "",
		dataPlane("Authorization=Bearer "+token.AccessToken)...))
	assert.Contains(t, failure, "authentication required")
}

// TestACRCLI_AnonymousPullProperty asserts the anonymousPullEnabled property
// `az acr update --anonymous-pull-enabled` sets is what decides whether an
// unauthenticated client may pull from the registry.
func TestACRCLI_AnonymousPullProperty(t *testing.T) {
	const registryName = "clianonymousregistry"
	registryURL := acrURL("registries/" + registryName)
	out := runCLI(t, azRest("PUT", registryURL,
		`{"location":"eastus","sku":{"name":"Standard"},"properties":{"adminUserEnabled":true,"anonymousPullEnabled":true}}`))
	var registry struct {
		ID         string `json:"id"`
		Properties struct {
			LoginServer          string `json:"loginServer"`
			AnonymousPullEnabled bool   `json:"anonymousPullEnabled"`
		} `json:"properties"`
	}
	parseJSON(t, out, &registry)
	require.True(t, registry.Properties.AnonymousPullEnabled)
	t.Cleanup(func() { runCLI(t, azRest("DELETE", registryURL, "")) })

	username, passwords := acrCLICredentials(t,
		baseURL+registry.ID+"/listcredentials?api-version="+acrAPIVersion)
	dataPlane := func(headers ...string) []string {
		return append([]string{"--headers", "Host=" + registry.Properties.LoginServer}, headers...)
	}

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`
	runCLI(t, azRest("PUT", baseURL+"/v2/anon/app/manifests/v1", manifest,
		dataPlane("Authorization="+acrCLIBasic(username, passwords[0]),
			"Content-Type=application/vnd.docker.distribution.manifest.v2+json")...))

	// An unauthenticated pull is served, and an unauthenticated push is not.
	runCLI(t, azRest("GET", baseURL+"/v2/anon/app/manifests/v1", "", dataPlane()...))
	failure := runStorageCLIExpectFailure(t, azRest("PUT", baseURL+"/v2/anon/app/manifests/v2", manifest,
		dataPlane("Content-Type=application/vnd.docker.distribution.manifest.v2+json")...))
	assert.Contains(t, failure, "authentication required")
}
