package azure_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSiteVNetIntegration_CLI_RoundTrip exercises App Service regional VNet
// integration (the "swift" virtual network connection) through the az CLI — the
// ARM endpoint sockerless's azure-functions backend uses for cloud-dns service
// discovery. A site joins a Microsoft.Web-delegated subnet; the connection
// round-trips on GET and is removable.
func TestSiteVNetIntegration_CLI_RoundTrip(t *testing.T) {
	// A site with a sockerless-overlay image is an HTTP function site, so VNet
	// integration only records the connection (no service container to start) —
	// keeping this an ARM round-trip test, not a workload test.
	url := funcURL("sites/cli-sitevnet-app")
	body := `{
		"location": "eastus",
		"kind": "functionapp,linux,container",
		"properties": {
			"siteConfig": {
				"linuxFxVersion": "DOCKER|registry.example/sockerless-overlay/azf:test"
			}
		}
	}`
	runCLI(t, azRest("PUT", url, body))

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
	// The operation path is .../networkConfig/virtualNetwork, but the resource's
	// canonical ARM id is the config sub-resource .../config/virtualNetwork.
	assert.True(t, strings.HasSuffix(conn.ID, "/config/virtualNetwork"),
		"swift connection id must be the canonical config sub-resource; got %s", conn.ID)

	// GET — connection round-trips.
	out = runCLI(t, azRest("GET", swiftURL, ""))
	parseJSON(t, out, &conn)
	assert.Equal(t, subnetID, conn.Properties.SubnetResourceID)
	assert.True(t, strings.HasSuffix(conn.ID, "/config/virtualNetwork"),
		"swift connection id must be the canonical config sub-resource; got %s", conn.ID)

	// DELETE — disconnect, then cleanup the site.
	runCLI(t, azRest("DELETE", swiftURL, ""))
	runCLI(t, azRest("DELETE", url, ""))
}
