package azure_cli_test

import (
	"fmt"
	"strings"
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
// service association link.
//
// The link lives on a subnet, so the subnet is created first and the delete
// runs against a subnet that exists. Driving the delete at a subnet that was
// never created, asserting nothing but the exit status, cannot tell a working
// implementation from a handler that answers 204 to any URL of that shape —
// which is what this one was. The refusal for a subnet that does not exist is
// the other half of the contract and is asserted beside it.
// Creating that subnet builds real Linux network fabric, so the test carries
// the same host-capability gate the Microsoft.Network suite does and runs for
// real on the CI Linux cell.
func TestACISubnetServiceAssociationLinkDelete(t *testing.T) {
	requireNetworkHost(t)
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
	// A 204 No Content delete returns no body and exits 0.
	assert.Empty(t, strings.TrimSpace(runCLI(t, azRest("DELETE", url, ""))),
		"a 204 No Content delete carries no body")

	missing := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/aci-vnet/subnets/aci-no-such-subnet/providers/Microsoft.ContainerInstance/serviceAssociationLinks/default?api-version=%s",
		baseURL, subscriptionID, resourceGroup, aciAPIVersion)
	assert.Contains(t, runNetworkCLIExpectFailure(t, azRest("DELETE", missing, "")), "ResourceNotFound",
		"deleting the link on a subnet that does not exist must be refused, not reported as a delete that succeeded")
}
