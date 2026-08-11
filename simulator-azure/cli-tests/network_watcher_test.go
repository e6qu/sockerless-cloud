package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure Network Watcher through the Azure CLI: the watcher resource, its flow
// log and connection monitor children, and the diagnostics it answers about the
// network the test builds. The diagnostics that read an interface need the
// real-execution network host, because that is what allocates the interface an
// address in its subnet.

const networkWatcherAPIVersion = "2025-03-01"

func TestNetworkWatcherConfigurationCLI(t *testing.T) {
	nsgURL := armURL("Microsoft.Network", "networkSecurityGroups/cli-watch-nsg", networkWatcherAPIVersion)
	runCLI(t, azRest("PUT", nsgURL, `{"location":"eastus","properties":{"securityRules":[]}}`))
	nsgID := networkResourceID("networkSecurityGroups/cli-watch-nsg")

	watcherURL := armURL("Microsoft.Network", "networkWatchers/cli-watcher", networkWatcherAPIVersion)
	out := runCLI(t, azRest("PUT", watcherURL, `{"location":"eastus","tags":{"team":"network"},"properties":{}}`))
	var watcher struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, out, &watcher)
	assert.Equal(t, "cli-watcher", watcher.Name)
	assert.Equal(t, "Microsoft.Network/networkWatchers", watcher.Type)
	assert.Equal(t, "Succeeded", watcher.Properties.ProvisioningState)

	out = runCLI(t, azRest("PATCH", watcherURL, `{"tags":{"team":"network","env":"cli"}}`))
	assert.Contains(t, out, `"env"`)
	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "networkWatchers", networkWatcherAPIVersion), ""))
	assert.Contains(t, out, "cli-watcher")
	out = runCLI(t, azRest("GET", subscriptionURL("Microsoft.Network", "networkWatchers", networkWatcherAPIVersion), ""))
	assert.Contains(t, out, "cli-watcher")

	storageID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/cliflowlogs",
		subscriptionID, resourceGroup)
	flowLogURL := armURL("Microsoft.Network", "networkWatchers/cli-watcher/flowLogs/nsg-flows", networkWatcherAPIVersion)
	out = runCLI(t, azRest("PUT", flowLogURL, fmt.Sprintf(
		`{"location":"eastus","properties":{"targetResourceId":%q,"storageId":%q,"enabled":true,"retentionPolicy":{"days":7,"enabled":true}}}`,
		nsgID, storageID)))
	var flowLog struct {
		Name       string `json:"name"`
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
			Format            struct {
				Type    string `json:"type"`
				Version int    `json:"version"`
			} `json:"format"`
		} `json:"properties"`
	}
	parseJSON(t, out, &flowLog)
	assert.Equal(t, "nsg-flows", flowLog.Name)
	assert.Equal(t, "Succeeded", flowLog.Properties.ProvisioningState)
	assert.Equal(t, "JSON", flowLog.Properties.Format.Type)

	// The target-addressed query reads the record the flow log resource wrote.
	statusURL := strings.Replace(watcherURL, "?api-version=", "/queryFlowLogStatus?api-version=", 1)
	out = runCLI(t, azRest("POST", statusURL, fmt.Sprintf(`{"targetResourceId":%q}`, nsgID)))
	assert.Contains(t, out, storageID)

	// And the target-addressed write lands on the same record.
	configureURL := strings.Replace(watcherURL, "?api-version=", "/configureFlowLog?api-version=", 1)
	runCLI(t, azRest("POST", configureURL, fmt.Sprintf(
		`{"targetResourceId":%q,"properties":{"storageId":%q,"enabled":false}}`, nsgID, storageID)))
	out = runCLI(t, azRest("GET", flowLogURL, ""))
	assert.Contains(t, out, `"enabled": false`)

	out = runCLI(t, azRest("PATCH", flowLogURL, `{"tags":{"retention":"short"}}`))
	assert.Contains(t, out, "short")
	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "networkWatchers/cli-watcher/flowLogs", networkWatcherAPIVersion), ""))
	assert.Contains(t, out, "nsg-flows")

	monitorURL := armURL("Microsoft.Network", "networkWatchers/cli-watcher/connectionMonitors/cli-monitor", networkWatcherAPIVersion)
	out = runCLI(t, azRest("PUT", monitorURL, `{
      "location": "eastus",
      "properties": {
        "endpoints": [{"name": "source", "address": "10.90.1.4"}, {"name": "target", "address": "example.internal"}],
        "testConfigurations": [{"name": "tcp-443", "protocol": "Tcp", "testFrequencySec": 30, "tcpConfiguration": {"port": 443}}],
        "testGroups": [{"name": "group1", "sources": ["source"], "destinations": ["target"], "testConfigurations": ["tcp-443"]}]
      }
    }`))
	var monitor struct {
		Name       string `json:"name"`
		Properties struct {
			MonitoringStatus      string `json:"monitoringStatus"`
			ConnectionMonitorType string `json:"connectionMonitorType"`
			StartTime             string `json:"startTime"`
		} `json:"properties"`
	}
	parseJSON(t, out, &monitor)
	assert.Equal(t, "cli-monitor", monitor.Name)
	assert.Equal(t, "Running", monitor.Properties.MonitoringStatus)
	assert.Equal(t, "MultiEndpoint", monitor.Properties.ConnectionMonitorType)
	require.NotEmpty(t, monitor.Properties.StartTime)

	runCLI(t, azRest("POST", strings.Replace(monitorURL, "?api-version=", "/stop?api-version=", 1), ""))
	out = runCLI(t, azRest("GET", monitorURL, ""))
	assert.Contains(t, out, `"Stopped"`)
	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "networkWatchers/cli-watcher/connectionMonitors", networkWatcherAPIVersion), ""))
	assert.Contains(t, out, "cli-monitor")

	// The measurements Azure collects with its own internet-measurement fleet
	// are not this deployment's to report, and it says so rather than naming
	// providers it knows nothing about.
	providersURL := strings.Replace(watcherURL, "?api-version=", "/availableProvidersList?api-version=", 1)
	out = runCLI(t, azRest("POST", providersURL, `{"country":"United States"}`))
	var providers struct {
		Countries []any `json:"countries"`
	}
	parseJSON(t, out, &providers)
	assert.Empty(t, providers.Countries)

	reportURL := strings.Replace(watcherURL, "?api-version=", "/azureReachabilityReport?api-version=", 1)
	out = runCLI(t, azRest("POST", reportURL,
		`{"providerLocation":{"country":"United States","state":"washington"},"startTime":"2026-07-01T00:00:00Z","endTime":"2026-07-02T00:00:00Z"}`))
	var report struct {
		AggregationLevel   string `json:"aggregationLevel"`
		ReachabilityReport []any  `json:"reachabilityReport"`
	}
	parseJSON(t, out, &report)
	assert.Equal(t, "State", report.AggregationLevel)
	assert.Empty(t, report.ReachabilityReport)

	// Troubleshooting names a virtual network gateway, a resource type this
	// deployment holds none of, so the watcher reports the target absent.
	troubleshootURL := strings.Replace(watcherURL, "?api-version=", "/troubleshoot?api-version=", 1)
	failure := runNetworkCLIExpectFailure(t, azRest("POST", troubleshootURL, fmt.Sprintf(
		`{"targetResourceId":"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworkGateways/vpn","properties":{"storageId":%q,"storagePath":"https://cliflowlogs.blob.core.windows.net/logs"}}`,
		subscriptionID, resourceGroup, storageID)))
	assert.Contains(t, failure, "ResourceNotFound")

	runCLI(t, azRest("DELETE", monitorURL, ""))
	runCLI(t, azRest("DELETE", flowLogURL, ""))
	runCLI(t, azRest("DELETE", watcherURL, ""))
	failure = runNetworkCLIExpectFailure(t, azRest("GET", watcherURL, ""))
	assert.Contains(t, failure, "ResourceNotFound")
	runCLI(t, azRest("DELETE", nsgURL, ""))
}

// TestNetworkWatcherDiagnosticsCLI drives the diagnostics that read a real
// interface: IP flow verify against the rules on its subnet, and next hop
// against the route table attached to it.
func TestNetworkWatcherDiagnosticsCLI(t *testing.T) {
	requireNetworkHost(t)

	nsgURL := armURL("Microsoft.Network", "networkSecurityGroups/cli-diag-nsg", networkWatcherAPIVersion)
	runCLI(t, azRest("PUT", nsgURL, `{"location":"eastus","properties":{"securityRules":[
      {"name":"deny-ssh","properties":{"protocol":"Tcp","sourceAddressPrefix":"*","sourcePortRange":"*","destinationAddressPrefix":"*","destinationPortRange":"22","access":"Deny","priority":100,"direction":"Inbound"}},
      {"name":"allow-web","properties":{"protocol":"Tcp","sourceAddressPrefix":"*","sourcePortRange":"*","destinationAddressPrefix":"*","destinationPortRange":"80","access":"Allow","priority":200,"direction":"Inbound"}}
    ]}}`))
	nsgID := networkResourceID("networkSecurityGroups/cli-diag-nsg")

	routeURL := armURL("Microsoft.Network", "routeTables/cli-diag-rt", networkWatcherAPIVersion)
	runCLI(t, azRest("PUT", routeURL, `{"location":"eastus","properties":{"routes":[
      {"name":"appliance","properties":{"addressPrefix":"203.0.113.0/24","nextHopType":"VirtualAppliance","nextHopIpAddress":"10.90.1.9"}}
    ]}}`))
	routeTableID := networkResourceID("routeTables/cli-diag-rt")

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-diag-vnet", networkWatcherAPIVersion)
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.90.0.0/16"]}}}`))
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-diag-vnet/subnets/web", networkWatcherAPIVersion)
	runCLI(t, azRest("PUT", subnetURL, fmt.Sprintf(
		`{"properties":{"addressPrefix":"10.90.1.0/24","networkSecurityGroup":{"id":%q},"routeTable":{"id":%q}}}`, nsgID, routeTableID)))
	subnetID := networkResourceID("virtualNetworks/cli-diag-vnet/subnets/web")

	nicURL := armURL("Microsoft.Network", "networkInterfaces/cli-diag-nic", networkWatcherAPIVersion)
	out := runCLI(t, azRest("PUT", nicURL, fmt.Sprintf(
		`{"location":"eastus","properties":{"ipConfigurations":[{"name":"ipconfig1","properties":{"subnet":{"id":%q}}}]}}`, subnetID)))
	var nic struct {
		Properties struct {
			IPConfigurations []struct {
				Properties struct {
					PrivateIPAddress string `json:"privateIPAddress"`
				} `json:"properties"`
			} `json:"ipConfigurations"`
		} `json:"properties"`
	}
	parseJSON(t, out, &nic)
	require.Len(t, nic.Properties.IPConfigurations, 1)
	nicAddress := nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress
	require.NotEmpty(t, nicAddress)
	nicID := networkResourceID("networkInterfaces/cli-diag-nic")

	watcherURL := armURL("Microsoft.Network", "networkWatchers/cli-diag-watcher", networkWatcherAPIVersion)
	runCLI(t, azRest("PUT", watcherURL, `{"location":"eastus","properties":{}}`))

	verifyURL := strings.Replace(watcherURL, "?api-version=", "/ipFlowVerify?api-version=", 1)
	out = runCLI(t, azRest("POST", verifyURL, fmt.Sprintf(
		`{"targetResourceId":%q,"direction":"Inbound","protocol":"TCP","localIPAddress":%q,"localPort":"22","remoteIPAddress":"198.51.100.7","remotePort":"51000"}`,
		nicID, nicAddress)))
	var flow struct {
		Access   string `json:"access"`
		RuleName string `json:"ruleName"`
	}
	parseJSON(t, out, &flow)
	assert.Equal(t, "Deny", flow.Access)
	assert.Equal(t, "securityRules/deny-ssh", flow.RuleName)

	out = runCLI(t, azRest("POST", verifyURL, fmt.Sprintf(
		`{"targetResourceId":%q,"direction":"Inbound","protocol":"TCP","localIPAddress":%q,"localPort":"80","remoteIPAddress":"198.51.100.7","remotePort":"51000"}`,
		nicID, nicAddress)))
	parseJSON(t, out, &flow)
	assert.Equal(t, "Allow", flow.Access)
	assert.Equal(t, "securityRules/allow-web", flow.RuleName)

	nextHopURL := strings.Replace(watcherURL, "?api-version=", "/nextHop?api-version=", 1)
	out = runCLI(t, azRest("POST", nextHopURL, fmt.Sprintf(
		`{"targetResourceId":%q,"sourceIPAddress":%q,"destinationIPAddress":"203.0.113.10"}`, nicID, nicAddress)))
	var hop struct {
		NextHopType      string `json:"nextHopType"`
		NextHopIPAddress string `json:"nextHopIpAddress"`
		RouteTableID     string `json:"routeTableId"`
	}
	parseJSON(t, out, &hop)
	assert.Equal(t, "VirtualAppliance", hop.NextHopType)
	assert.Equal(t, "10.90.1.9", hop.NextHopIPAddress)
	assert.Equal(t, routeTableID, hop.RouteTableID)

	// A system route carries no route table id, so this answer is decoded into
	// its own value rather than over the previous one.
	out = runCLI(t, azRest("POST", nextHopURL, fmt.Sprintf(
		`{"targetResourceId":%q,"sourceIPAddress":%q,"destinationIPAddress":"10.90.2.4"}`, nicID, nicAddress)))
	var systemHop struct {
		NextHopType  string `json:"nextHopType"`
		RouteTableID string `json:"routeTableId"`
	}
	parseJSON(t, out, &systemHop)
	assert.Equal(t, "VnetLocal", systemHop.NextHopType)
	assert.Empty(t, systemHop.RouteTableID)

	viewURL := strings.Replace(watcherURL, "?api-version=", "/securityGroupView?api-version=", 1)
	out = runCLI(t, azRest("POST", viewURL, fmt.Sprintf(`{"targetResourceId":%q}`, nicID)))
	assert.Contains(t, out, "subnetAssociation")
	assert.Contains(t, out, "effectiveSecurityRules")

	topologyURL := strings.Replace(watcherURL, "?api-version=", "/topology?api-version=", 1)
	out = runCLI(t, azRest("POST", topologyURL, fmt.Sprintf(`{"targetResourceGroupName":%q}`, resourceGroup)))
	assert.Contains(t, out, "cli-diag-nic")

	diagnosticURL := strings.Replace(watcherURL, "?api-version=", "/networkConfigurationDiagnostic?api-version=", 1)
	out = runCLI(t, azRest("POST", diagnosticURL, fmt.Sprintf(
		`{"targetResourceId":%q,"profiles":[{"direction":"Inbound","protocol":"TCP","source":"198.51.100.7","destination":%q,"destinationPort":"22"}]}`,
		nicID, nicAddress)))
	assert.Contains(t, out, "deny-ssh")
	assert.Contains(t, out, `"securityRuleAccessResult": "Deny"`)

	runCLI(t, azRest("DELETE", watcherURL, ""))
	runCLI(t, azRest("DELETE", nicURL, ""))
	runCLI(t, azRest("DELETE", subnetURL, ""))
	runCLI(t, azRest("DELETE", vnetURL, ""))
	runCLI(t, azRest("DELETE", routeURL, ""))
	runCLI(t, azRest("DELETE", nsgURL, ""))
}
