package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const aciAPIVersion = "2021-10-01"

// TestACILocationUsages_CLI exercises the per-region Azure Container Instances
// usage endpoint (Location_ListUsage) through the az-rest CLI.
func TestACILocationUsages_CLI(t *testing.T) {
	url := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.ContainerInstance/locations/eastus/usages?api-version=%s",
		baseURL, subscriptionID, aciAPIVersion)
	out := runCLI(t, azRest("GET", url, ""))

	var result struct {
		Value []struct {
			Name struct {
				Value string `json:"value"`
			} `json:"name"`
			Limit int `json:"limit"`
		} `json:"value"`
	}
	parseJSON(t, out, &result)
	require.NotEmpty(t, result.Value)
	names := map[string]bool{}
	for _, u := range result.Value {
		names[u.Name.Value] = true
	}
	assert.True(t, names["ContainerGroups"], "ContainerGroups usage must be reported")
}

// TestACISubnetServiceAssociationLinkDelete exercises
// SubnetServiceAssociationLink_Delete: removing the subnet<->container-group
// service association link succeeds (the simulator holds no such link).
func TestACISubnetServiceAssociationLinkDelete(t *testing.T) {
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/aci-vnet/subnets/aci-subnet/providers/Microsoft.ContainerInstance/serviceAssociationLinks/default?api-version=%s",
		baseURL, subscriptionID, resourceGroup, aciAPIVersion)
	// A 204 No Content delete returns no body and exits 0.
	runCLI(t, azRest("DELETE", url, ""))
}
