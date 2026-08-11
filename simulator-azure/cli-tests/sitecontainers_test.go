package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSiteContainers_CLI_CRUD drives the App Service multi-container
// (sitecontainers) ARM surface with the real `az` CLI (via `az rest`,
// matching the other azure CLI tests): create a site, attach a main and a
// sidecar sitecontainer, read them back, list, and delete.
func TestSiteContainers_CLI_CRUD(t *testing.T) {
	const apiVersion = "2024-11-01"
	scURL := func(path string) string {
		return armURL("Microsoft.Web", path, apiVersion)
	}

	// Site to hold the containers.
	siteURL := scURL("sites/cli-sc-app")
	runCLI(t, azRest("PUT", siteURL, `{
		"location": "eastus",
		"kind": "functionapp,linux,container",
		"properties": {"siteConfig": {}}
	}`))
	defer runCLI(t, azRest("DELETE", siteURL, ""))

	// Main sitecontainer.
	mainURL := scURL("sites/cli-sc-app/sitecontainers/main")
	out := runCLI(t, azRest("PUT", mainURL, `{
		"properties": {"image": "myapp:latest", "isMain": true}
	}`))
	var sc struct {
		Name       string `json:"name"`
		Properties struct {
			Image  string `json:"image"`
			IsMain bool   `json:"isMain"`
		} `json:"properties"`
	}
	parseJSON(t, out, &sc)
	assert.Equal(t, "main", sc.Name)
	assert.Equal(t, "myapp:latest", sc.Properties.Image)
	assert.True(t, sc.Properties.IsMain)

	// Sidecar sitecontainer.
	sideURL := scURL("sites/cli-sc-app/sitecontainers/redis")
	runCLI(t, azRest("PUT", sideURL, `{
		"properties": {"image": "redis:7-alpine", "isMain": false, "targetPort": "6379"}
	}`))

	// GET the main back.
	out = runCLI(t, azRest("GET", mainURL, ""))
	parseJSON(t, out, &sc)
	assert.Equal(t, "myapp:latest", sc.Properties.Image)

	// LIST returns both.
	out = runCLI(t, azRest("GET", scURL("sites/cli-sc-app/sitecontainers"), ""))
	var list struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &list)
	names := map[string]bool{}
	for _, c := range list.Value {
		names[c.Name] = true
	}
	require.True(t, names["main"], "main should be listed")
	require.True(t, names["redis"], "redis sidecar should be listed")

	// DELETE the sidecar; it should disappear from the list.
	runCLI(t, azRest("DELETE", sideURL, ""))
	out = runCLI(t, azRest("GET", scURL("sites/cli-sc-app/sitecontainers"), ""))
	parseJSON(t, out, &list)
	for _, c := range list.Value {
		assert.NotEqual(t, "redis", c.Name, "redis sidecar should be deleted")
	}
}
