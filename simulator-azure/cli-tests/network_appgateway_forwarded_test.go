package azure_cli_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkApplicationGatewayForwardedHeadersCLI deploys an Application
// Gateway through the Azure CLI in front of a backend that echoes the headers
// it received, and sends a request at the gateway's frontend address. The
// backend sees the six headers the gateway inserts into every request.
func TestNetworkApplicationGatewayForwardedHeadersCLI(t *testing.T) {
	requireNetworkHost(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(r.Header)
	}))
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)
	backendIP, backendPort, err := net.SplitHostPort(backendURL.Host)
	require.NoError(t, err)

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-xff-vnet", applicationGatewayAPIVersion)
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.81.0.0/16"]}}}`))
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-xff-vnet/subnets/appgw", applicationGatewayAPIVersion)
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.81.1.0/24"}}`))
	subnetID := networkResourceID("virtualNetworks/cli-xff-vnet/subnets/appgw")

	pipURL := armURL("Microsoft.Network", "publicIPAddresses/cli-xff-pip", applicationGatewayAPIVersion)
	var pip struct {
		Properties struct {
			IPAddress string `json:"ipAddress"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", pipURL, `{"location":"eastus","sku":{"name":"Standard"},"properties":{"publicIPAllocationMethod":"Static"}}`)), &pip)
	frontendAddress := pip.Properties.IPAddress
	require.NotEmpty(t, frontendAddress)
	pipID := networkResourceID("publicIPAddresses/cli-xff-pip")

	gatewayID := networkResourceID("applicationGateways/cli-xff-appgw")
	body := fmt.Sprintf(`{
      "location": "eastus",
      "properties": {
        "sku": {"name": "Standard_v2", "capacity": 1},
        "gatewayIPConfigurations": [{"name": "gw-ipcfg", "properties": {"subnet": {"id": %q}}}],
        "frontendIPConfigurations": [{"name": "public", "properties": {"publicIPAddress": {"id": %q}}}],
        "frontendPorts": [{"name": "port-80", "properties": {"port": 80}}],
        "backendAddressPools": [{"name": "echo", "properties": {"backendAddresses": [{"ipAddress": %q}]}}],
        "probes": [{"name": "fast", "properties": {"protocol": "Http", "host": %q, "path": "/", "interval": 1, "timeout": 1, "unhealthyThreshold": 1}}],
        "backendHttpSettingsCollection": [{"name": "http", "properties": {"port": %s, "protocol": "Http", "probe": {"id": %q}}}],
        "httpListeners": [{"name": "listener", "properties": {"frontendIPConfiguration": {"id": %q}, "frontendPort": {"id": %q}, "protocol": "Http"}}],
        "requestRoutingRules": [{"name": "rule", "properties": {"ruleType": "Basic", "priority": 100, "httpListener": {"id": %q}, "backendAddressPool": {"id": %q}, "backendHttpSettings": {"id": %q}}}]
      }
    }`,
		subnetID, pipID, backendIP, backendIP, backendPort,
		gatewayID+"/probes/fast",
		gatewayID+"/frontendIPConfigurations/public",
		gatewayID+"/frontendPorts/port-80",
		gatewayID+"/httpListeners/listener",
		gatewayID+"/backendAddressPools/echo",
		gatewayID+"/backendHttpSettingsCollection/http")
	gatewayURL := armURL("Microsoft.Network", "applicationGateways/cli-xff-appgw", applicationGatewayAPIVersion)
	runCLI(t, azRest("PUT", gatewayURL, body))
	t.Cleanup(func() {
		runCLI(t, azRest("DELETE", gatewayURL, ""))
		runCLI(t, azRest("DELETE", pipURL, ""))
		runCLI(t, azRest("DELETE", subnetURL, ""))
		runCLI(t, azRest("DELETE", vnetURL, ""))
	})

	request := func() *http.Response {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/orders?page=2", nil)
		require.NoError(t, err)
		req.Host = frontendAddress
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return resp
	}
	// The gateway forwards only once its probe has found the backend Up.
	var resp *http.Response
	require.Eventually(t, func() bool {
		resp = request()
		if resp.StatusCode == http.StatusOK {
			return true
		}
		resp.Body.Close()
		return false
	}, 60*time.Second, time.Second, "the gateway never forwarded to its backend")
	defer resp.Body.Close()
	var seen http.Header
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&seen))
	assert.Regexp(t, `^203\.0\.113\.9, 127\.0\.0\.1:\d+$`, seen.Get("X-Forwarded-For"))
	assert.Equal(t, "http", seen.Get("X-Forwarded-Proto"))
	assert.Equal(t, "80", seen.Get("X-Forwarded-Port"))
	assert.Equal(t, frontendAddress, seen.Get("X-Original-Host"))
	assert.Equal(t, "/orders?page=2", seen.Get("X-Original-URL"))
	assert.Regexp(t, `^[0-9a-f]{32}$`, seen.Get("X-AppGW-Trace-Id"))
}
