package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pgAPIVersion = "2024-08-01"
const resourcesAPIVersion = "2021-04-01"
const subscriptionsAPIVersion = "2022-12-01"

// subURL builds a non-resource-group-scoped ARM URL against the simulator.
func subURL(path, apiVersion string) string {
	return fmt.Sprintf("%s/subscriptions/%s/%s?api-version=%s", baseURL, subscriptionID, path, apiVersion)
}

func tenantScopeURL(path, apiVersion string) string {
	return fmt.Sprintf("%s/%s?api-version=%s", baseURL, path, apiVersion)
}

func pgURL(path string) string {
	return armURL("Microsoft.DBforPostgreSQL", path, pgAPIVersion)
}

func TestCLI_PGFlexibleServer_Actions(t *testing.T) {
	rgURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=%s", baseURL, subscriptionID, resourceGroup, resourcesAPIVersion)
	runCLI(t, azRest("PUT", rgURL, `{"location":"eastus"}`))

	server := "cli-pg-actions"
	runCLI(t, azRest("PUT", pgURL("flexibleServers/"+server),
		`{"location":"eastus","properties":{"administratorLogin":"psqladmin","administratorLoginPassword":"Sup3rSecret!","version":"15","storage":{"storageSizeGB":32}}}`))

	// Lifecycle actions return 202 Accepted with no body.
	runCLI(t, azRest("POST", pgURL("flexibleServers/"+server+"/stop"), ""))
	runCLI(t, azRest("POST", pgURL("flexibleServers/"+server+"/start"), ""))
	runCLI(t, azRest("POST", pgURL("flexibleServers/"+server+"/restart"), ""))

	// Subscription-wide list contains the server.
	out := runCLI(t, azRest("GET", subURL("providers/Microsoft.DBforPostgreSQL/flexibleServers", pgAPIVersion), ""))
	assert.Contains(t, out, server)

	// CheckNameAvailability: the created name is taken.
	out = runCLI(t, azRest("POST", subURL("providers/Microsoft.DBforPostgreSQL/checkNameAvailability", pgAPIVersion),
		fmt.Sprintf(`{"name":"%s","type":"Microsoft.DBforPostgreSQL/flexibleServers"}`, server)))
	var cna struct {
		NameAvailable bool `json:"nameAvailable"`
	}
	parseJSON(t, out, &cna)
	assert.False(t, cna.NameAvailable)
}

func TestCLI_Resources_ProvidersAndTags(t *testing.T) {
	// Provider registration + get.
	out := runCLI(t, azRest("POST", subURL("providers/Microsoft.DBforPostgreSQL/register", resourcesAPIVersion), ""))
	assert.Contains(t, out, "Registered")

	out = runCLI(t, azRest("GET", subURL("providers/Microsoft.Insights", resourcesAPIVersion), ""))
	var prov struct {
		Namespace string `json:"namespace"`
	}
	parseJSON(t, out, &prov)
	assert.Equal(t, "Microsoft.Insights", prov.Namespace)

	// Predefined tagNames catalog.
	runCLI(t, azRest("PUT", subURL("tagNames/cliCostCenter", resourcesAPIVersion), ""))
	runCLI(t, azRest("PUT", subURL("tagNames/cliCostCenter/tagValues/finance", resourcesAPIVersion), ""))
	out = runCLI(t, azRest("GET", subURL("tagNames", resourcesAPIVersion), ""))
	assert.Contains(t, out, "cliCostCenter")
	assert.Contains(t, out, "finance")
	runCLI(t, azRest("DELETE", subURL("tagNames/cliCostCenter/tagValues/finance", resourcesAPIVersion), ""))
	runCLI(t, azRest("DELETE", subURL("tagNames/cliCostCenter", resourcesAPIVersion), ""))

	// Operations metadata list.
	out = runCLI(t, azRest("GET", tenantScopeURL("providers/Microsoft.Resources/operations", resourcesAPIVersion), ""))
	assert.Contains(t, out, "Microsoft.Resources/")
}

func TestCLI_Resources_ResourceGroupUpdate(t *testing.T) {
	rg := "cli-rg-update"
	rgURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=%s", baseURL, subscriptionID, rg, resourcesAPIVersion)
	runCLI(t, azRest("PUT", rgURL, `{"location":"eastus"}`))
	out := runCLI(t, azRest("PATCH", rgURL, `{"tags":{"team":"platform"}}`))
	var got struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, "platform", got.Tags["team"])

	// exportTemplate returns an ARM template document.
	out = runCLI(t, azRest("POST",
		fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/exportTemplate?api-version=%s", baseURL, subscriptionID, rg, resourcesAPIVersion),
		`{"resources":["*"]}`))
	assert.Contains(t, out, "contentVersion")
}

func TestCLI_Subscriptions(t *testing.T) {
	out := runCLI(t, azRest("GET", tenantScopeURL("subscriptions", subscriptionsAPIVersion), ""))
	assert.Contains(t, out, subscriptionID)

	out = runCLI(t, azRest("GET", subURL("locations", subscriptionsAPIVersion), ""))
	assert.Contains(t, out, "eastus")

	out = runCLI(t, azRest("GET", tenantScopeURL("tenants", subscriptionsAPIVersion), ""))
	assert.Contains(t, out, "tenantId")

	out = runCLI(t, azRest("POST", tenantScopeURL("providers/Microsoft.Resources/checkResourceName", subscriptionsAPIVersion),
		`{"name":"microsoft","type":"Microsoft.Storage/storageAccounts"}`))
	var crn struct {
		Status string `json:"status"`
	}
	parseJSON(t, out, &crn)
	assert.Equal(t, "Reserved", crn.Status)
}

func TestCLI_AppInsights_ListUpdateTagsPurge(t *testing.T) {
	rgURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=%s", baseURL, subscriptionID, resourceGroup, resourcesAPIVersion)
	runCLI(t, azRest("PUT", rgURL, `{"location":"eastus"}`))

	name := "cli-appinsights-more"
	runCLI(t, azRest("PUT", insightsURL("components/"+name),
		`{"location":"eastus","kind":"web","properties":{"Application_Type":"web"}}`))

	// List components in the resource group.
	out := runCLI(t, azRest("GET", insightsURL("components"), ""))
	assert.Contains(t, out, name)

	// UpdateTags (PATCH).
	out = runCLI(t, azRest("PATCH", insightsURL("components/"+name), `{"tags":{"owner":"observability"}}`))
	var comp struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, out, &comp)
	assert.Equal(t, "observability", comp.Tags["owner"])

	// Purge + status.
	out = runCLI(t, azRest("POST", insightsURL("components/"+name+"/purge"),
		`{"table":"traces","filters":[{"column":"timestamp","operator":"<","value":"2030-01-01"}]}`))
	var purge struct {
		OperationID string `json:"operationId"`
	}
	parseJSON(t, out, &purge)
	require.NotEmpty(t, purge.OperationID)

	out = runCLI(t, azRest("GET", insightsURL("components/"+name+"/operations/"+purge.OperationID), ""))
	assert.Contains(t, out, "completed")

	runCLI(t, azRest("DELETE", insightsURL("components/"+name), ""))
}
