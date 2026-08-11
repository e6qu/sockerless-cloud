package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const aspAPIVersion = "2022-09-01"

func aspURL(path string) string {
	return armURL("Microsoft.Web", path, aspAPIVersion)
}

func TestAppServicePlan_CreateAndShow(t *testing.T) {
	url := aspURL("serverfarms/cli-test-plan")

	out := runCLI(t, azRest("PUT", url,
		`{"location":"eastus","sku":{"name":"B1","tier":"Basic"}}`))

	var plan struct {
		Name     string `json:"name"`
		Location string `json:"location"`
		Sku      struct {
			Name string `json:"name"`
			Tier string `json:"tier"`
		} `json:"sku"`
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, out, &plan)
	assert.Equal(t, "cli-test-plan", plan.Name)
	assert.Equal(t, "eastus", plan.Location)
	assert.Equal(t, "B1", plan.Sku.Name)
	assert.Equal(t, "Succeeded", plan.Properties.ProvisioningState)

	// GET
	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &plan)
	assert.Equal(t, "cli-test-plan", plan.Name)

	// Cleanup
	runCLI(t, azRest("DELETE", url, ""))
}

func TestAppServicePlan_Delete(t *testing.T) {
	url := aspURL("serverfarms/delete-test-plan")
	runCLI(t, azRest("PUT", url, `{"location":"eastus"}`))
	runCLI(t, azRest("DELETE", url, ""))

	cmd := azRest("GET", url, "")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "Expected GET to fail after deletion")
}

// TestAppServicePlan_ServerFarmsCasing covers the capital-F "serverFarms"
// spelling of the App Service plan routes. Microsoft.Web is the one ARM
// provider whose own clients disagree on the segment's casing: go-azure-sdk
// (which terraform-provider-azurerm is built on) requests "serverFarms" while
// azure-sdk-for-go and the ARM reference use "serverfarms". Both spellings are
// mounted, and the resource IDs they return are normalized to the lowercase
// form, so a client that writes through one casing reads a canonical ID back.
func TestAppServicePlan_ServerFarmsCasing(t *testing.T) {
	upperURL := aspURL("serverFarms/cli-casing-plan")
	lowerURL := aspURL("serverfarms/cli-casing-plan")

	var plan struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Location string `json:"location"`
		Sku      struct {
			Name string `json:"name"`
		} `json:"sku"`
	}

	// Write through the capital-F spelling.
	out := runCLI(t, azRest("PUT", upperURL,
		`{"location":"eastus","sku":{"name":"B1","tier":"Basic"}}`))
	parseJSON(t, out, &plan)
	assert.Equal(t, "cli-casing-plan", plan.Name)
	assert.Contains(t, plan.ID, "/providers/Microsoft.Web/serverfarms/cli-casing-plan",
		"resource IDs must be normalized to the lowercase serverfarms spelling")

	// Read it back through the capital-F spelling.
	out = runCLI(t, azRest("GET", upperURL, ""))
	parseJSON(t, out, &plan)
	assert.Equal(t, "cli-casing-plan", plan.Name)
	assert.Equal(t, "B1", plan.Sku.Name)

	// The same plan is readable through the lowercase spelling — both routes
	// resolve to one stored resource, not two.
	out = runCLI(t, azRest("GET", lowerURL, ""))
	parseJSON(t, out, &plan)
	assert.Equal(t, "cli-casing-plan", plan.Name)

	// List-by-resource-group through the capital-F spelling. This is the
	// existence probe go-azure-sdk issues before a create.
	out = runCLI(t, azRest("GET", aspURL("serverFarms"), ""))
	var list struct {
		Value []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"value"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, p := range list.Value {
		if p.Name == "cli-casing-plan" {
			found = true
			assert.Contains(t, p.ID, "/providers/Microsoft.Web/serverfarms/cli-casing-plan")
		}
	}
	assert.True(t, found, "serverFarms list must include the plan created above")

	// Delete through the capital-F spelling; the lowercase read must 404.
	runCLI(t, azRest("DELETE", upperURL, ""))
	cmd := azRest("GET", lowerURL, "")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "GET after DELETE must fail through either casing")
}
