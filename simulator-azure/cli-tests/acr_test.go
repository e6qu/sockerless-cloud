package azure_cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const acrAPIVersion = "2023-01-01-preview"

func acrURL(path string) string {
	return armURL("Microsoft.ContainerRegistry", path, acrAPIVersion)
}

func TestACR_CreateAndShow(t *testing.T) {
	url := acrURL("registries/clitestregistry")

	out := runCLI(t, azRest("PUT", url,
		`{"location":"eastus","sku":{"name":"Basic"},"properties":{"adminUserEnabled":false}}`))

	var registry struct {
		Name     string `json:"name"`
		Location string `json:"location"`
		Sku      struct {
			Name string `json:"name"`
		} `json:"sku"`
		Properties struct {
			ProvisioningState  string `json:"provisioningState"`
			LoginServer        string `json:"loginServer"`
			RoleAssignmentMode string `json:"roleAssignmentMode"`
		} `json:"properties"`
	}
	parseJSON(t, out, &registry)
	assert.Equal(t, "clitestregistry", registry.Name)
	assert.Equal(t, "eastus", registry.Location)
	assert.NotEmpty(t, registry.Properties.LoginServer)
	assert.Equal(t, "LegacyRegistryPermissions", registry.Properties.RoleAssignmentMode)

	// The advertised loginServer is a simulator coordinate (the request host,
	// SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON's "cli-shim.localhost"
	// template applied here) — never the unreachable real-cloud
	// "<name>.azurecr.io" host a browser or client couldn't resolve against
	// the simulator (BUG-2643).
	assert.False(t, strings.HasSuffix(registry.Properties.LoginServer, ".azurecr.io"),
		"loginServer must not be the real-cloud host, got %q", registry.Properties.LoginServer)
	assert.Contains(t, registry.Properties.LoginServer, "clitestregistry.azurecr.cli-shim.localhost")

	// GET
	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &registry)
	assert.Equal(t, "clitestregistry", registry.Name)

	// The advertised loginServer must actually resolve to this simulator: a
	// direct HTTP request to the loopback base URL carrying that host in the
	// Host header must reach the ACR data plane and see the same catalog a
	// request without the Host-header trick would (the OCI /v2/ and
	// /acr/v1/ routes are mounted without a host component, so any Host
	// reaches them — but the value must not be the never-resolvable
	// "*.azurecr.io" real-cloud host it used to be).
	req, err := http.NewRequest(http.MethodGet, baseURL+"/acr/v1/_catalog", nil)
	if err != nil {
		t.Fatalf("build catalog request: %v", err)
	}
	req.Host = registry.Properties.LoginServer
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET catalog via loginServer coordinate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("catalog via loginServer coordinate returned %d", resp.StatusCode)
	}

	// Cleanup
	runCLI(t, azRest("DELETE", url, ""))
}

// TestACR_CacheRuleCRUD drives the `cacheRules` sub-resource
// through the `az rest` CLI. `az acr cache` commands are higher-level
// but route through the same ARM endpoints, so exercising via az rest
// covers both paths.
func TestACR_CacheRuleCRUD(t *testing.T) {
	regURL := acrURL("registries/cliregforcache")
	runCLI(t, azRest("PUT", regURL,
		`{"location":"eastus","sku":{"name":"Basic"},"properties":{}}`))
	defer runCLI(t, azRest("DELETE", regURL, ""))

	ruleURL := acrURL("registries/cliregforcache/cacheRules/docker-hub")

	// Create.
	body := `{"properties":{"sourceRepository":"docker.io/library/*","targetRepository":"docker-hub/library/*"}}`
	out := runCLI(t, azRest("PUT", ruleURL, body))
	var rule struct {
		Name       string `json:"name"`
		Properties struct {
			SourceRepository  string `json:"sourceRepository"`
			TargetRepository  string `json:"targetRepository"`
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, out, &rule)
	assert.Equal(t, "docker-hub", rule.Name)
	assert.Equal(t, "docker.io/library/*", rule.Properties.SourceRepository)
	assert.Equal(t, "docker-hub/library/*", rule.Properties.TargetRepository)
	assert.Equal(t, "Succeeded", rule.Properties.ProvisioningState)

	// Get.
	out = runCLI(t, azRest("GET", ruleURL, ""))
	parseJSON(t, out, &rule)
	assert.Equal(t, "docker-hub", rule.Name)

	// List.
	listURL := acrURL("registries/cliregforcache/cacheRules")
	out = runCLI(t, azRest("GET", listURL, ""))
	var listResp struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &listResp)
	var found bool
	for _, r := range listResp.Value {
		if r.Name == "docker-hub" {
			found = true
		}
	}
	assert.True(t, found, "expected List to return the created cache rule")

	// Delete.
	runCLI(t, azRest("DELETE", ruleURL, ""))
}

// TestACR_ImageCatalogAndTags pushes a manifest via the OCI distribution
// protocol (raw HTTP, same path that az acr and docker CLI use), then
// verifies the ACR data-plane catalog and tags endpoints that
// `az acr repository list` and `az acr repository show-tags` wrap.
func TestACR_ImageCatalogAndTags(t *testing.T) {
	const (
		registryName = "catalogtestregistry"
		imageName    = "cli-catalog-image"
		imageTag     = "stable"
	)

	// Create the ACR registry via ARM
	regURL := acrURL("registries/" + registryName)
	runCLI(t, azRest("PUT", regURL, `{"location":"eastus","sku":{"name":"Basic"},"properties":{}}`))
	defer runCLI(t, azRest("DELETE", regURL, ""))

	// Push a minimal manifest via OCI distribution PUT
	manifestJSON := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":7,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`
	putPath := fmt.Sprintf("/v2/%s/manifests/%s", imageName, imageTag)
	req, err := http.NewRequest(http.MethodPut, baseURL+putPath, bytes.NewBufferString(manifestJSON))
	if err != nil {
		t.Fatalf("build PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT manifest: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from PUT manifest, got %d", resp.StatusCode)
	}

	// GET /acr/v1/_catalog — list repositories, must include imageName
	catResp, err := http.Get(baseURL + "/acr/v1/_catalog")
	if err != nil {
		t.Fatalf("GET catalog: %v", err)
	}
	defer catResp.Body.Close()
	if catResp.StatusCode != http.StatusOK {
		t.Fatalf("catalog returned %d", catResp.StatusCode)
	}
	var catalog struct {
		Repositories []string `json:"repositories"`
	}
	parseJSON(t, readBody(t, catResp), &catalog)
	found := false
	for _, r := range catalog.Repositories {
		if r == imageName {
			found = true
		}
	}
	assert.True(t, found, "expected catalog to include %q, got %v", imageName, catalog.Repositories)

	// GET /acr/v1/{name}/_tags — list tags for imageName
	tagsResp, err := http.Get(fmt.Sprintf("%s/acr/v1/%s/_tags", baseURL, imageName))
	if err != nil {
		t.Fatalf("GET tags: %v", err)
	}
	defer tagsResp.Body.Close()
	if tagsResp.StatusCode != http.StatusOK {
		t.Fatalf("tags returned %d", tagsResp.StatusCode)
	}
	var tagList struct {
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}
	parseJSON(t, readBody(t, tagsResp), &tagList)
	foundTag := false
	for _, tag := range tagList.Tags {
		if tag.Name == imageTag {
			foundTag = true
		}
	}
	assert.True(t, foundTag, "expected tags list to include %q, got %v", imageTag, tagList.Tags)
}

// readBody reads and returns the body of an HTTP response as a string.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return buf.String()
}
