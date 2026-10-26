package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSiteContainers_CLI_CRUD drives the App Service sitecontainers surface:
// a main and a sidecar on a site, read, listed and deleted.
func TestSiteContainers_CLI_CRUD(t *testing.T) {
	t.Parallel()
	const apiVersion = "2024-11-01"
	scURL := func(path string) string {
		return armURL("Microsoft.Web", path, apiVersion)
	}

	siteURL := scURL("sites/cli-sc-app")
	runCLI(t, azRest("PUT", siteURL, `{
		"location": "eastus",
		"kind": "functionapp,linux,container",
		"properties": {"siteConfig": {}}
	}`))
	defer runCLI(t, azRest("DELETE", siteURL, ""))

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

	sideURL := scURL("sites/cli-sc-app/sitecontainers/redis")
	runCLI(t, azRest("PUT", sideURL, `{
		"properties": {"image": "mirror.gcr.io/library/redis:7-alpine", "isMain": false, "targetPort": "6379"}
	}`))

	out = runCLI(t, azRest("GET", mainURL, ""))
	parseJSON(t, out, &sc)
	assert.Equal(t, "myapp:latest", sc.Properties.Image)

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

	runCLI(t, azRest("DELETE", sideURL, ""))
	out = runCLI(t, azRest("GET", scURL("sites/cli-sc-app/sitecontainers"), ""))
	parseJSON(t, out, &list)
	for _, c := range list.Value {
		assert.NotEqual(t, "redis", c.Name, "redis sidecar should be deleted")
	}

	// A deployment slot's sitecontainers are its own records, invisible on the
	// production site.
	runCLI(t, azRest("PUT", scURL("sites/cli-sc-app/slots/staging"), `{
		"location": "eastus",
		"kind": "functionapp,linux,container"
	}`))
	defer runCLI(t, azRest("DELETE", scURL("sites/cli-sc-app/slots/staging"), ""))

	slotURL := scURL("sites/cli-sc-app/slots/staging/sitecontainers/slotmain")
	out = runCLI(t, azRest("PUT", slotURL, `{
		"properties": {"image": "myapp:staging", "isMain": true}
	}`))
	var slotSC struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Properties struct {
			Image string `json:"image"`
		} `json:"properties"`
	}
	parseJSON(t, out, &slotSC)
	assert.Equal(t, "slotmain", slotSC.Name)
	assert.Equal(t, "Microsoft.Web/sites/slots/sitecontainers", slotSC.Type)
	assert.Equal(t, "myapp:staging", slotSC.Properties.Image)

	out = runCLI(t, azRest("GET", slotURL, ""))
	parseJSON(t, out, &slotSC)
	assert.Equal(t, "myapp:staging", slotSC.Properties.Image)

	out = runCLI(t, azRest("GET", scURL("sites/cli-sc-app/slots/staging/sitecontainers"), ""))
	parseJSON(t, out, &list)
	require.Len(t, list.Value, 1)
	assert.Equal(t, "slotmain", list.Value[0].Name)

	out = runCLI(t, azRest("GET", scURL("sites/cli-sc-app/sitecontainers"), ""))
	parseJSON(t, out, &list)
	for _, c := range list.Value {
		assert.NotEqual(t, "slotmain", c.Name, "slot sitecontainer must not leak into the production list")
	}

	runCLI(t, azRest("DELETE", slotURL, ""))
	out = runCLI(t, azRest("GET", scURL("sites/cli-sc-app/slots/staging/sitecontainers"), ""))
	parseJSON(t, out, &list)
	assert.Empty(t, list.Value, "deleted slot sitecontainer must disappear from the slot list")
}
