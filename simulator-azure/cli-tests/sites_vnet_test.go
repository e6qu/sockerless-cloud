package azure_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSiteVNetIntegration_CLI_RoundTrip: a site joins a Microsoft.Web-delegated
// subnet through regional virtual network integration (the "swift"
// connection), which round-trips on GET and deletes.
func TestSiteVNetIntegration_CLI_RoundTrip(t *testing.T) {
	t.Parallel()
	// A site that is not Always On and gets no request runs no container, so the
	// integration only records the connection.
	url := funcURL("sites/cli-sitevnet-app")
	body := `{
		"location": "eastus",
		"kind": "functionapp,linux,container",
		"properties": {
			"siteConfig": {
				"linuxFxVersion": "DOCKER|registry.example/functions/azf:test"
			}
		}
	}`
	runCLI(t, azRest("PUT", url, body))

	// As on Azure, the integration resolves the subnet to its network, so the
	// subnet must exist.
	runCLI(t, azRest("PUT", armURL("Microsoft.Network", "virtualNetworks/cli-sitevnet", "2024-05-01"),
		`{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.72.0.0/16"]}}}`))
	runCLI(t, azRest("PUT", armURL("Microsoft.Network", "virtualNetworks/cli-sitevnet/subnets/appservice", "2024-05-01"),
		`{"properties":{"addressPrefix":"10.72.1.0/24","delegations":[{"name":"appservice","properties":{"serviceName":"Microsoft.Web/serverFarms"}}]}}`))
	subnetID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.Network/virtualNetworks/cli-sitevnet/subnets/appservice"

	swiftURL := funcURL("sites/cli-sitevnet-app/networkConfig/virtualNetwork")
	swiftBody := `{"properties":{"subnetResourceId":"` + subnetID + `","swiftSupported":true}}`

	var conn struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties struct {
			SubnetResourceID string `json:"subnetResourceId"`
			SwiftSupported   bool   `json:"swiftSupported"`
		} `json:"properties"`
	}

	out := runCLI(t, azRest("PUT", swiftURL, swiftBody))
	parseJSON(t, out, &conn)
	assert.Equal(t, subnetID, conn.Properties.SubnetResourceID)
	assert.True(t, conn.Properties.SwiftSupported)
	// The operation path is .../networkConfig/virtualNetwork; the canonical ID is
	// .../config/virtualNetwork.
	assert.True(t, strings.HasSuffix(conn.ID, "/config/virtualNetwork"),
		"swift connection id must be the canonical config sub-resource; got %s", conn.ID)

	out = runCLI(t, azRest("GET", swiftURL, ""))
	parseJSON(t, out, &conn)
	assert.Equal(t, subnetID, conn.Properties.SubnetResourceID)
	assert.True(t, strings.HasSuffix(conn.ID, "/config/virtualNetwork"),
		"swift connection id must be the canonical config sub-resource; got %s", conn.ID)

	runCLI(t, azRest("DELETE", swiftURL, ""))
	runCLI(t, azRest("DELETE", url, ""))
}
