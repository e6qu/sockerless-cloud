package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure Virtual Network Manager through the Azure CLI: the manager resource,
// the commit that deploys to a set of regions, and the deployment status that
// commit produces.

const networkManagerAPIVersion = "2025-03-01"

func TestNetworkManagerCLI(t *testing.T) {
	managerURL := armURL("Microsoft.Network", "networkManagers/cli-manager", networkManagerAPIVersion)
	body := fmt.Sprintf(`{
      "location": "eastus",
      "tags": {"owner": "platform"},
      "properties": {
        "description": "cli network manager",
        "networkManagerScopes": {"subscriptions": ["/subscriptions/%s"]},
        "networkManagerScopeAccesses": ["Connectivity"]
      }
    }`, subscriptionID)

	out := runCLI(t, azRest("PUT", managerURL, body))
	var manager struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		SystemData struct {
			CreatedAt string `json:"createdAt"`
		} `json:"systemData"`
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
			ResourceGUID      string `json:"resourceGuid"`
			Description       string `json:"description"`
		} `json:"properties"`
	}
	parseJSON(t, out, &manager)
	assert.Equal(t, "cli-manager", manager.Name)
	assert.Equal(t, "Microsoft.Network/networkManagers", manager.Type)
	assert.Equal(t, "Succeeded", manager.Properties.ProvisioningState)
	assert.Equal(t, "cli network manager", manager.Properties.Description)
	require.NotEmpty(t, manager.Properties.ResourceGUID)
	require.NotEmpty(t, manager.SystemData.CreatedAt)

	out = runCLI(t, azRest("GET", managerURL, ""))
	assert.Contains(t, out, "networkManagerScopes")

	out = runCLI(t, azRest("PATCH", managerURL, `{"tags":{"owner":"platform","env":"cli"}}`))
	assert.Contains(t, out, `"env"`)

	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "networkManagers", networkManagerAPIVersion), ""))
	assert.Contains(t, out, "cli-manager")
	out = runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network", "networkManagers", networkManagerAPIVersion), ""))
	assert.Contains(t, out, "cli-manager")

	commitURL := strings.Replace(managerURL, "?api-version=", "/commit?api-version=", 1)
	out = runCLI(t, azRest("POST", commitURL,
		`{"commitType":"Connectivity","targetLocations":["eastus","westeurope"]}`))
	var commit struct {
		CommitID   string   `json:"commitId"`
		CommitType string   `json:"commitType"`
		Targets    []string `json:"targetLocations"`
	}
	parseJSON(t, out, &commit)
	require.NotEmpty(t, commit.CommitID)
	assert.Equal(t, "Connectivity", commit.CommitType)
	assert.Len(t, commit.Targets, 2)

	statusURL := strings.Replace(managerURL, "?api-version=", "/listDeploymentStatus?api-version=", 1)
	out = runCLI(t, azRest("POST", statusURL, `{}`))
	var status struct {
		Value []struct {
			Region           string `json:"region"`
			DeploymentStatus string `json:"deploymentStatus"`
			DeploymentType   string `json:"deploymentType"`
			CommitTime       string `json:"commitTime"`
		} `json:"value"`
	}
	parseJSON(t, out, &status)
	require.Len(t, status.Value, 2)
	for _, entry := range status.Value {
		assert.Equal(t, "Deployed", entry.DeploymentStatus)
		assert.Equal(t, "Connectivity", entry.DeploymentType)
		require.NotEmpty(t, entry.CommitTime)
	}

	out = runCLI(t, azRest("POST", statusURL, `{"regions":["westeurope"]}`))
	parseJSON(t, out, &status)
	require.Len(t, status.Value, 1)
	assert.Equal(t, "westeurope", status.Value[0].Region)

	// A commit type the manager was never given access to is refused.
	failure := runNetworkCLIExpectFailure(t, azRest("POST", commitURL,
		`{"commitType":"SecurityAdmin","targetLocations":["eastus"]}`))
	assert.Contains(t, failure, "scope access")

	runCLI(t, azRest("DELETE", managerURL, ""))
	failure = runNetworkCLIExpectFailure(t, azRest("GET", managerURL, ""))
	assert.Contains(t, failure, "ResourceNotFound")
}
