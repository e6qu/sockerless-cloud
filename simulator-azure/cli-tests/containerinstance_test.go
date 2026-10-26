package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const aciAPIVersion = "2021-10-01"

// TestACILocationUsages_CLI exercises Location_ListUsage, the per-region Azure
// Container Instances usage endpoint.
func TestACILocationUsages_CLI(t *testing.T) {
	t.Parallel()
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

// TestACISubnetServiceAssociationLinkDelete deletes a subnet's service
// association link on a subnet that exists, and checks the refusal for one
// that does not: an exit status alone cannot tell this from a handler that
// answers 204 to any URL of that shape.
func TestACISubnetServiceAssociationLinkDelete(t *testing.T) {
	t.Parallel()
	const networkAPIVersion = "2024-05-01"
	vnetURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/aci-vnet?api-version=%s",
		baseURL, subscriptionID, resourceGroup, networkAPIVersion)
	runCLI(t, azRest("PUT", vnetURL,
		`{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.94.0.0/16"]}}}`))
	subnetURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/aci-vnet/subnets/aci-subnet?api-version=%s",
		baseURL, subscriptionID, resourceGroup, networkAPIVersion)
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.94.1.0/24"}}`))

	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/aci-vnet/subnets/aci-subnet/providers/Microsoft.ContainerInstance/serviceAssociationLinks/default?api-version=%s",
		baseURL, subscriptionID, resourceGroup, aciAPIVersion)
	assert.Empty(t, strings.TrimSpace(runCLI(t, azRest("DELETE", url, ""))),
		"a 204 No Content delete carries no body")

	missing := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/aci-vnet/subnets/aci-no-such-subnet/providers/Microsoft.ContainerInstance/serviceAssociationLinks/default?api-version=%s",
		baseURL, subscriptionID, resourceGroup, aciAPIVersion)
	assert.Contains(t, runNetworkCLIExpectFailure(t, azRest("DELETE", missing, "")), "ResourceNotFound",
		"deleting the link on a subnet that does not exist must be refused, not reported as a delete that succeeded")
}
