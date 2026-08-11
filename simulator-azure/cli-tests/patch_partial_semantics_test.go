package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// patch_partial_semantics_test.go drives the ARM PATCH (update) handlers behind
// the Azure console's edit flows through the real az CLI: the
// Microsoft.ContainerRegistry registry PATCH, the Microsoft.App job PATCH, and
// the Microsoft.Web site PATCH.
//
// ARM PATCH is a merge — a property the client omits keeps its stored value —
// so every case here sends a deliberately partial body and asserts the
// properties it did not send survived.
//
// These go through `az rest`, the harness's control-plane convention: the CLI
// tests authenticate to the simulator with a simulator-minted ARM bearer rather
// than an `az login` session, so `az rest --method PATCH` is how the real az
// binary reaches an ARM control-plane endpoint here. It is the same HTTP request
// `az acr update` / `az containerapp job update` / `az webapp update` emit.

const (
	webPatchAPIVersion = "2023-01-01"
	jobPatchAPIVersion = "2023-05-01"
)

// TestCLI_ACRRegistryUpdatePartial covers PATCH
// .../Microsoft.ContainerRegistry/registries/{registryName} — the request
// `az acr update` sends.
func TestCLI_ACRRegistryUpdatePartial(t *testing.T) {
	url := acrURL("registries/clipatchregistry")

	type registryShape struct {
		Name string            `json:"name"`
		Tags map[string]string `json:"tags"`
		Sku  struct {
			Name string `json:"name"`
			Tier string `json:"tier"`
		} `json:"sku"`
		Properties struct {
			AdminUserEnabled    bool   `json:"adminUserEnabled"`
			PublicNetworkAccess string `json:"publicNetworkAccess"`
			LoginServer         string `json:"loginServer"`
		} `json:"properties"`
	}

	out := runCLI(t, azRest("PUT", url,
		`{"location":"eastus","tags":{"env":"dev"},"sku":{"name":"Basic"},"properties":{"adminUserEnabled":true}}`))
	var created registryShape
	parseJSON(t, out, &created)
	require.Equal(t, "clipatchregistry", created.Name)
	require.True(t, created.Properties.AdminUserEnabled)
	defer runCLI(t, azRest("DELETE", url, ""))

	// PATCH tags only — adminUserEnabled and the SKU must survive.
	out = runCLI(t, azRest("PATCH", url, `{"tags":{"env":"prod","owner":"sockerless"}}`))
	var patched registryShape
	parseJSON(t, out, &patched)
	assert.Equal(t, "prod", patched.Tags["env"])
	assert.Equal(t, "sockerless", patched.Tags["owner"])
	assert.True(t, patched.Properties.AdminUserEnabled,
		"tags-only PATCH must not clear adminUserEnabled")
	assert.Equal(t, "Basic", patched.Sku.Name, "tags-only PATCH must not clear the SKU")

	// PATCH a property only — the tags must survive.
	out = runCLI(t, azRest("PATCH", url, `{"properties":{"adminUserEnabled":false}}`))
	var propPatched registryShape
	parseJSON(t, out, &propPatched)
	assert.False(t, propPatched.Properties.AdminUserEnabled,
		"an explicit adminUserEnabled=false must be applied")
	assert.Equal(t, "sockerless", propPatched.Tags["owner"],
		"tags must survive a properties-only PATCH")

	// Read back through GET so the stored row is verified, not just the
	// PATCH response.
	out = runCLI(t, azRest("GET", url, ""))
	var got registryShape
	parseJSON(t, out, &got)
	assert.False(t, got.Properties.AdminUserEnabled)
	assert.Equal(t, "sockerless", got.Tags["owner"])
	assert.Equal(t, "Basic", got.Sku.Name)
}

// TestCLI_ACAJobUpdatePartial covers PATCH .../Microsoft.App/jobs/{jobName} —
// the request `az containerapp job update` sends.
func TestCLI_ACAJobUpdatePartial(t *testing.T) {
	envURL := armURL("Microsoft.App", "managedEnvironments/cli-jobpatch-env", jobPatchAPIVersion)
	runCLI(t, azRest("PUT", envURL, `{"location":"eastus","properties":{}}`))
	defer runCLI(t, azRest("DELETE", envURL, ""))

	envID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.App/managedEnvironments/cli-jobpatch-env"

	jobURL := armURL("Microsoft.App", "jobs/cli-jobpatch-job", jobPatchAPIVersion)

	type jobShape struct {
		Name       string            `json:"name"`
		Tags       map[string]string `json:"tags"`
		Properties struct {
			Configuration struct {
				TriggerType    string `json:"triggerType"`
				ReplicaTimeout int    `json:"replicaTimeout"`
			} `json:"configuration"`
			Template struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"template"`
		} `json:"properties"`
	}

	createBody := `{"location":"eastus","properties":{` +
		`"environmentId":"` + envID + `",` +
		`"configuration":{"triggerType":"Manual","replicaTimeout":1800,"replicaRetryLimit":2,` +
		`"manualTriggerConfig":{"parallelism":1,"replicaCompletionCount":1}},` +
		`"template":{"containers":[{"name":"main","image":"alpine:3.20"}]}}}`
	out := runCLI(t, azRest("PUT", jobURL, createBody))
	var created jobShape
	parseJSON(t, out, &created)
	require.Equal(t, "cli-jobpatch-job", created.Name)
	require.Equal(t, 1800, created.Properties.Configuration.ReplicaTimeout)
	defer runCLI(t, azRest("DELETE", jobURL, ""))

	// PATCH tags only — configuration and template must survive.
	runCLI(t, azRest("PATCH", jobURL, `{"tags":{"tier":"batch"}}`))

	out = runCLI(t, azRest("GET", jobURL, ""))
	var got jobShape
	parseJSON(t, out, &got)
	assert.Equal(t, "batch", got.Tags["tier"])
	assert.Equal(t, 1800, got.Properties.Configuration.ReplicaTimeout,
		"replicaTimeout must not be reset by a tags-only PATCH")
	require.Len(t, got.Properties.Template.Containers, 1,
		"template must survive a tags-only PATCH")
	assert.Equal(t, "alpine:3.20", got.Properties.Template.Containers[0].Image)

	// PATCH the configuration only — the tags must survive.
	runCLI(t, azRest("PATCH", jobURL,
		`{"properties":{"configuration":{"triggerType":"Manual","replicaTimeout":600,"replicaRetryLimit":0}}}`))
	out = runCLI(t, azRest("GET", jobURL, ""))
	var got2 jobShape
	parseJSON(t, out, &got2)
	assert.Equal(t, 600, got2.Properties.Configuration.ReplicaTimeout)
	assert.Equal(t, "batch", got2.Tags["tier"],
		"tags must survive a properties-only PATCH")
}

// TestCLI_WebAppUpdatePartialPreservesHTTPSOnly covers PATCH
// .../Microsoft.Web/sites/{name} — the request `az webapp update` sends — and
// pins the merge semantics: a body that omits httpsOnly must leave it alone.
func TestCLI_WebAppUpdatePartialPreservesHTTPSOnly(t *testing.T) {
	planURL := armURL("Microsoft.Web", "serverfarms/cli-patch-plan", webPatchAPIVersion)
	runCLI(t, azRest("PUT", planURL,
		`{"location":"eastus","kind":"functionapp","sku":{"name":"Y1","tier":"Dynamic"},"properties":{"reserved":true}}`))
	defer runCLI(t, azRest("DELETE", planURL, ""))

	planID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.Web/serverfarms/cli-patch-plan"

	siteURL := armURL("Microsoft.Web", "sites/cli-patch-site", webPatchAPIVersion)

	type siteShape struct {
		Name       string            `json:"name"`
		Kind       string            `json:"kind"`
		Tags       map[string]string `json:"tags"`
		Properties struct {
			HTTPSOnly    bool   `json:"httpsOnly"`
			ServerFarmID string `json:"serverFarmId"`
		} `json:"properties"`
	}

	runCLI(t, azRest("PUT", siteURL,
		`{"location":"eastus","kind":"functionapp,linux","properties":{"serverFarmId":"`+planID+`","httpsOnly":true}}`))
	defer runCLI(t, azRest("DELETE", siteURL, ""))

	out := runCLI(t, azRest("GET", siteURL, ""))
	var created siteShape
	parseJSON(t, out, &created)
	require.True(t, created.Properties.HTTPSOnly, "site must start with httpsOnly=true")

	// Tags-only PATCH — the body has no "properties" key at all.
	out = runCLI(t, azRest("PATCH", siteURL, `{"tags":{"owner":"sockerless"}}`))
	var tagged siteShape
	parseJSON(t, out, &tagged)
	assert.Equal(t, "sockerless", tagged.Tags["owner"])
	assert.True(t, tagged.Properties.HTTPSOnly,
		"a tags-only PATCH must not clear httpsOnly")

	// Properties-subset PATCH — "properties" present, httpsOnly absent.
	out = runCLI(t, azRest("PATCH", siteURL, `{"properties":{"clientCertMode":"Optional"}}`))
	var subset siteShape
	parseJSON(t, out, &subset)
	assert.True(t, subset.Properties.HTTPSOnly,
		"a properties body without httpsOnly must not clear it")

	// The stored row must agree, and serverFarmId must have survived too.
	out = runCLI(t, azRest("GET", siteURL, ""))
	var got siteShape
	parseJSON(t, out, &got)
	assert.True(t, got.Properties.HTTPSOnly,
		"GET after partial PATCHes must still report httpsOnly=true")
	assert.Equal(t, planID, got.Properties.ServerFarmID,
		"serverFarmId must survive partial PATCHes")
	assert.Equal(t, "sockerless", got.Tags["owner"])

	// An explicit httpsOnly=false must still be applied — presence-awareness
	// must not degrade into ignoring false.
	runCLI(t, azRest("PATCH", siteURL, `{"properties":{"httpsOnly":false}}`))
	out = runCLI(t, azRest("GET", siteURL, ""))
	var off siteShape
	parseJSON(t, out, &off)
	assert.False(t, off.Properties.HTTPSOnly,
		"an explicit httpsOnly=false must be applied")
	assert.Equal(t, "sockerless", off.Tags["owner"],
		"tags must survive a properties-only PATCH")
}
