package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceBusCLI_ARMResources(t *testing.T) {
	ns := "cli-servicebus-ns"
	queue := "cliqueue"

	nsURL := armURL("Microsoft.ServiceBus", "namespaces/"+ns, "2024-01-01")
	nsOut := runCLI(t, azRest("PUT", nsURL, `{
		"location":"eastus",
		"sku":{"name":"Standard","tier":"Standard"},
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
	assert.Equal(t, "Succeeded", createdNS.Properties.ProvisioningState)
	assert.Contains(t, createdNS.Properties.ServiceBusEndpoint, ns+".servicebus.")
	t.Cleanup(func() {
		_ = azRest("DELETE", nsURL, "").Run()
	})

	queueURL := armURL("Microsoft.ServiceBus", "namespaces/"+ns+"/queues/"+queue, "2024-01-01")
	queueOut := runCLI(t, azRest("PUT", queueURL, `{"properties":{"maxSizeInMegabytes":1024}}`))
	var createdQueue struct {
		Name       string `json:"name"`
		Properties struct {
			MaxSizeInMegabytes int `json:"maxSizeInMegabytes"`
		} `json:"properties"`
	}
	parseJSON(t, queueOut, &createdQueue)
	assert.Equal(t, queue, createdQueue.Name)
	assert.Equal(t, 1024, createdQueue.Properties.MaxSizeInMegabytes)

	networkRulesURL := armURL("Microsoft.ServiceBus", "namespaces/"+ns+"/networkRuleSets/default", "2024-01-01")
	networkRulesOut := runCLI(t, azRest("GET", networkRulesURL, ""))
	var networkRules struct {
		Name       string `json:"name"`
		Properties struct {
			DefaultAction       string `json:"defaultAction"`
			PublicNetworkAccess string `json:"publicNetworkAccess"`
			IPRules             []any  `json:"ipRules"`
			VirtualNetworkRules []any  `json:"virtualNetworkRules"`
		} `json:"properties"`
	}
	parseJSON(t, networkRulesOut, &networkRules)
	assert.Equal(t, "default", networkRules.Name)
	assert.Equal(t, "Allow", networkRules.Properties.DefaultAction)
	assert.Equal(t, "Enabled", networkRules.Properties.PublicNetworkAccess)
	assert.Empty(t, networkRules.Properties.IPRules)
	assert.Empty(t, networkRules.Properties.VirtualNetworkRules)

	updatedRulesOut := runCLI(t, azRest("PUT", networkRulesURL, `{
		"properties":{
			"defaultAction":"Deny",
			"publicNetworkAccess":"Disabled",
			"trustedServiceAccessEnabled":true,
			"ipRules":[{"ipMask":"203.0.113.0/24","action":"Allow"}]
		}
	}`))
	parseJSON(t, updatedRulesOut, &networkRules)
	assert.Equal(t, "Deny", networkRules.Properties.DefaultAction)
	assert.Equal(t, "Disabled", networkRules.Properties.PublicNetworkAccess)
	require.Len(t, networkRules.Properties.IPRules, 1)

	disasterRecoveryOut := runCLI(t, azRest("GET", armURL("Microsoft.ServiceBus", "namespaces/"+ns+"/disasterRecoveryConfigs", "2024-01-01"), ""))
	var disasterRecovery struct {
		Value []any `json:"value"`
	}
	parseJSON(t, disasterRecoveryOut, &disasterRecovery)
	assert.Empty(t, disasterRecovery.Value)

	migrationOut := runCLI(t, azRest("GET", armURL("Microsoft.ServiceBus", "namespaces/"+ns+"/migrationConfigurations", "2024-01-01"), ""))
	var migration struct {
		Value []any `json:"value"`
	}
	parseJSON(t, migrationOut, &migration)
	assert.Empty(t, migration.Value)
}
