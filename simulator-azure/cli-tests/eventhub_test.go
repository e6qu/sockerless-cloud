package azure_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventHubsCLI_ARMResources(t *testing.T) {
	ns := "cli-eventhub-ns"
	hub := "clihub"
	group := "clicg"

	nsURL := armURL("Microsoft.EventHub", "namespaces/"+ns, "2024-01-01")
	nsOut := runCLI(t, azRest("PUT", nsURL, `{
		"location":"eastus",
		"sku":{"name":"Standard","tier":"Standard","capacity":1},
		"tags":{"env":"cli"}
	}`))
	var createdNS struct {
		Name       string `json:"name"`
		Properties struct {
			ProvisioningState  string `json:"provisioningState"`
			ServiceBusEndpoint string `json:"serviceBusEndpoint"`
		} `json:"properties"`
	}
	parseJSON(t, nsOut, &createdNS)
	assert.Equal(t, ns, createdNS.Name)
	assert.Equal(t, "Creating", createdNS.Properties.ProvisioningState)
	assert.Contains(t, createdNS.Properties.ServiceBusEndpoint, ns+".servicebus.")
	t.Cleanup(func() {
		_ = azRest("DELETE", nsURL, "").Run()
	})

	nsOut = waitForCLIJSON(t, nsURL, func(data string) bool {
		return strings.Contains(data, `"provisioningState": "Succeeded"`)
	})
	parseJSON(t, nsOut, &createdNS)
	assert.Equal(t, "Succeeded", createdNS.Properties.ProvisioningState)

	hubURL := armURL("Microsoft.EventHub", "namespaces/"+ns+"/eventhubs/"+hub, "2024-01-01")
	hubOut := runCLI(t, azRest("PUT", hubURL, `{
		"properties":{"partitionCount":2,"messageRetentionInDays":1}
	}`))
	var createdHub struct {
		Name       string `json:"name"`
		Properties struct {
			PartitionCount int      `json:"partitionCount"`
			PartitionIDs   []string `json:"partitionIds"`
		} `json:"properties"`
	}
	parseJSON(t, hubOut, &createdHub)
	assert.Equal(t, hub, createdHub.Name)
	assert.Equal(t, 2, createdHub.Properties.PartitionCount)
	assert.ElementsMatch(t, []string{"0", "1"}, createdHub.Properties.PartitionIDs)

	groupURL := armURL("Microsoft.EventHub", "namespaces/"+ns+"/eventhubs/"+hub+"/consumerGroups/"+group, "2024-01-01")
	groupOut := runCLI(t, azRest("PUT", groupURL, `{
		"properties":{"userMetadata":"cli group"}
	}`))
	var createdGroup struct {
		Name       string `json:"name"`
		Properties struct {
			UserMetadata string `json:"userMetadata"`
		} `json:"properties"`
	}
	parseJSON(t, groupOut, &createdGroup)
	assert.Equal(t, group, createdGroup.Name)
	assert.Equal(t, "cli group", createdGroup.Properties.UserMetadata)

	keysURL := armURL("Microsoft.EventHub", "namespaces/"+ns+"/authorizationRules/RootManageSharedAccessKey/listKeys", "2024-01-01")
	keysOut := runCLI(t, azRest("POST", keysURL, `{}`))
	var keys struct {
		KeyName                 string `json:"keyName"`
		PrimaryConnectionString string `json:"primaryConnectionString"`
	}
	parseJSON(t, keysOut, &keys)
	require.Equal(t, "RootManageSharedAccessKey", keys.KeyName)
	assert.Contains(t, keys.PrimaryConnectionString, "Endpoint=sb://"+ns+".servicebus.")
}
