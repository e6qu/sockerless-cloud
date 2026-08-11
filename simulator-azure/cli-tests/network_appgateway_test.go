package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure Application Gateway driven through the Azure CLI. The gateway's own
// resource needs the subnet its instances are deployed into, so the round trip
// is gated on the real-execution network host; the subscription-scoped catalogs
// describe the product rather than a deployment and are exercised everywhere.

const applicationGatewayAPIVersion = "2025-03-01"

func subscriptionURL(provider, resourcePath, apiVersion string) string {
	return fmt.Sprintf("%s/subscriptions/%s/providers/%s/%s?api-version=%s",
		baseURL, subscriptionID, provider, resourcePath, apiVersion)
}

// TestNetworkApplicationGatewayCatalogsCLI covers the six subscription-scoped
// catalogs a rewrite rule set and an SSL policy are written against.
func TestNetworkApplicationGatewayCatalogsCLI(t *testing.T) {
	out := runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network",
		"applicationGatewayAvailableServerVariables", applicationGatewayAPIVersion), ""))
	var serverVariables []string
	parseJSON(t, out, &serverVariables)
	assert.Contains(t, serverVariables, "request_uri")

	out = runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network",
		"applicationGatewayAvailableRequestHeaders", applicationGatewayAPIVersion), ""))
	var requestHeaders []string
	parseJSON(t, out, &requestHeaders)
	assert.Contains(t, requestHeaders, "X-Forwarded-For")

	out = runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network",
		"applicationGatewayAvailableResponseHeaders", applicationGatewayAPIVersion), ""))
	var responseHeaders []string
	parseJSON(t, out, &responseHeaders)
	assert.Contains(t, responseHeaders, "Set-Cookie")

	out = runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network",
		"applicationGatewayAvailableSslOptions/default", applicationGatewayAPIVersion), ""))
	var options struct {
		Name       string `json:"name"`
		Properties struct {
			DefaultPolicy         string   `json:"defaultPolicy"`
			AvailableProtocols    []string `json:"availableProtocols"`
			AvailableCipherSuites []string `json:"availableCipherSuites"`
			PredefinedPolicies    []struct {
				ID string `json:"id"`
			} `json:"predefinedPolicies"`
		} `json:"properties"`
	}
	parseJSON(t, out, &options)
	assert.Equal(t, "default", options.Name)
	assert.Equal(t, "AppGwSslPolicy20150501", options.Properties.DefaultPolicy)
	assert.Contains(t, options.Properties.AvailableProtocols, "TLSv1_3")
	assert.NotEmpty(t, options.Properties.AvailableCipherSuites)
	require.Len(t, options.Properties.PredefinedPolicies, 5)

	out = runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network",
		"applicationGatewayAvailableSslOptions/default/predefinedPolicies", applicationGatewayAPIVersion), ""))
	var policies struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, out, &policies)
	names := make([]string, 0, len(policies.Value))
	for _, policy := range policies.Value {
		names = append(names, policy.Name)
	}
	assert.Contains(t, names, "AppGwSslPolicy20220101S")

	out = runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network",
		"applicationGatewayAvailableSslOptions/default/predefinedPolicies/AppGwSslPolicy20170401S",
		applicationGatewayAPIVersion), ""))
	var policy struct {
		Name       string `json:"name"`
		Properties struct {
			MinProtocolVersion string   `json:"minProtocolVersion"`
			CipherSuites       []string `json:"cipherSuites"`
		} `json:"properties"`
	}
	parseJSON(t, out, &policy)
	assert.Equal(t, "AppGwSslPolicy20170401S", policy.Name)
	assert.Equal(t, "TLSv1_2", policy.Properties.MinProtocolVersion)
	assert.NotEmpty(t, policy.Properties.CipherSuites)

	failure := runNetworkCLIExpectFailure(t, azRest("GET", subscriptionURL("Microsoft.Network",
		"applicationGatewayAvailableSslOptions/default/predefinedPolicies/AppGwSslPolicyNeverPublished",
		applicationGatewayAPIVersion), ""))
	assert.Contains(t, failure, "ResourceNotFound")
}

// TestNetworkApplicationGatewayCLI covers the gateway resource itself: create,
// show, retag, both lists, stop and start, backend health and delete.
func TestNetworkApplicationGatewayCLI(t *testing.T) {
	requireNetworkHost(t)

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-appgw-vnet", applicationGatewayAPIVersion)
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.80.0.0/16"]}}}`))
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-appgw-vnet/subnets/appgw", applicationGatewayAPIVersion)
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.80.1.0/24"}}`))
	subnetID := networkResourceID("virtualNetworks/cli-appgw-vnet/subnets/appgw")

	pipURL := armURL("Microsoft.Network", "publicIPAddresses/cli-appgw-pip", applicationGatewayAPIVersion)
	runCLI(t, azRest("PUT", pipURL, `{"location":"eastus","sku":{"name":"Standard"},"properties":{"publicIPAllocationMethod":"Static"}}`))
	pipID := networkResourceID("publicIPAddresses/cli-appgw-pip")

	gatewayID := networkResourceID("applicationGateways/cli-appgw")
	body := fmt.Sprintf(`{
      "location": "eastus",
      "tags": {"tier": "edge"},
      "properties": {
        "sku": {"name": "Standard_v2", "capacity": 2},
        "gatewayIPConfigurations": [{"name": "gw-ipcfg", "properties": {"subnet": {"id": %q}}}],
        "frontendIPConfigurations": [{"name": "public", "properties": {"publicIPAddress": {"id": %q}}}],
        "frontendPorts": [{"name": "port-80", "properties": {"port": 80}}],
        "backendAddressPools": [{"name": "web", "properties": {"backendAddresses": [{"ipAddress": "10.80.1.10"}]}}],
        "backendHttpSettingsCollection": [{"name": "http", "properties": {"port": 80, "protocol": "Http"}}],
        "httpListeners": [{"name": "listener", "properties": {"frontendIPConfiguration": {"id": %q}, "frontendPort": {"id": %q}, "protocol": "Http"}}],
        "requestRoutingRules": [{"name": "rule", "properties": {"ruleType": "Basic", "priority": 100, "httpListener": {"id": %q}, "backendAddressPool": {"id": %q}, "backendHttpSettings": {"id": %q}}}]
      }
    }`,
		subnetID, pipID,
		gatewayID+"/frontendIPConfigurations/public",
		gatewayID+"/frontendPorts/port-80",
		gatewayID+"/httpListeners/listener",
		gatewayID+"/backendAddressPools/web",
		gatewayID+"/backendHttpSettingsCollection/http")

	gatewayURL := armURL("Microsoft.Network", "applicationGateways/cli-appgw", applicationGatewayAPIVersion)
	out := runCLI(t, azRest("PUT", gatewayURL, body))
	var gateway struct {
		Name       string            `json:"name"`
		Type       string            `json:"type"`
		Tags       map[string]string `json:"tags"`
		Properties struct {
			ProvisioningState          string `json:"provisioningState"`
			OperationalState           string `json:"operationalState"`
			ResourceGUID               string `json:"resourceGuid"`
			DefaultPredefinedSslPolicy string `json:"defaultPredefinedSslPolicy"`
			SKU                        struct {
				Tier string `json:"tier"`
			} `json:"sku"`
			HTTPListeners []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"httpListeners"`
		} `json:"properties"`
	}
	parseJSON(t, out, &gateway)
	assert.Equal(t, "cli-appgw", gateway.Name)
	assert.Equal(t, "Microsoft.Network/applicationGateways", gateway.Type)
	assert.Equal(t, "Succeeded", gateway.Properties.ProvisioningState)
	assert.Equal(t, "Running", gateway.Properties.OperationalState)
	assert.Equal(t, "Standard_v2", gateway.Properties.SKU.Tier)
	assert.Equal(t, "AppGwSslPolicy20220101", gateway.Properties.DefaultPredefinedSslPolicy)
	require.NotEmpty(t, gateway.Properties.ResourceGUID)
	require.Len(t, gateway.Properties.HTTPListeners, 1)
	assert.Equal(t, gatewayID+"/httpListeners/listener", gateway.Properties.HTTPListeners[0].ID)

	out = runCLI(t, azRest("GET", gatewayURL, ""))
	assert.Contains(t, out, "requestRoutingRules")

	out = runCLI(t, azRest("PATCH", gatewayURL, `{"tags":{"tier":"edge","env":"cli"}}`))
	assert.Contains(t, out, `"env"`)

	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "applicationGateways", applicationGatewayAPIVersion), ""))
	assert.Contains(t, out, "cli-appgw")
	out = runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network", "applicationGateways", applicationGatewayAPIVersion), ""))
	assert.Contains(t, out, "cli-appgw")

	runCLI(t, azRest("POST", strings.Replace(gatewayURL, "?api-version=", "/stop?api-version=", 1), ""))
	out = runCLI(t, azRest("GET", gatewayURL, ""))
	assert.Contains(t, out, `"Stopped"`)
	runCLI(t, azRest("POST", strings.Replace(gatewayURL, "?api-version=", "/start?api-version=", 1), ""))
	out = runCLI(t, azRest("GET", gatewayURL, ""))
	assert.Contains(t, out, `"Running"`)

	// The backend server the pool names is not listening, so the gateway's
	// probe must report it Down rather than assume it healthy.
	out = runCLI(t, azRest("POST", strings.Replace(gatewayURL, "?api-version=", "/backendhealth?api-version=", 1), ""))
	var health struct {
		BackendAddressPools []struct {
			BackendAddressPool struct {
				Name string `json:"name"`
			} `json:"backendAddressPool"`
			BackendHTTPSettingsCollection []struct {
				Servers []struct {
					Address string `json:"address"`
					Health  string `json:"health"`
				} `json:"servers"`
			} `json:"backendHttpSettingsCollection"`
		} `json:"backendAddressPools"`
	}
	parseJSON(t, out, &health)
	require.Len(t, health.BackendAddressPools, 1)
	require.Len(t, health.BackendAddressPools[0].BackendHTTPSettingsCollection, 1)
	require.Len(t, health.BackendAddressPools[0].BackendHTTPSettingsCollection[0].Servers, 1)
	assert.Equal(t, "10.80.1.10", health.BackendAddressPools[0].BackendHTTPSettingsCollection[0].Servers[0].Address)
	assert.Equal(t, "Down", health.BackendAddressPools[0].BackendHTTPSettingsCollection[0].Servers[0].Health)

	runCLI(t, azRest("DELETE", gatewayURL, ""))
	failure := runNetworkCLIExpectFailure(t, azRest("GET", gatewayURL, ""))
	assert.Contains(t, failure, "ResourceNotFound")

	runCLI(t, azRest("DELETE", pipURL, ""))
	runCLI(t, azRest("DELETE", subnetURL, ""))
	runCLI(t, azRest("DELETE", vnetURL, ""))
}
