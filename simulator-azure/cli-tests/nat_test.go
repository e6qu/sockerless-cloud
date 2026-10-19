package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestNetworkNatGatewayPublicIPPrefixCLI(t *testing.T) {
	t.Parallel()
	prefixURL := armURL("Microsoft.Network", "publicIPPrefixes/cli-nat-prefix", "2024-05-01")
	prefixBody := `{"location":"eastus","sku":{"name":"Standard"},"properties":{"prefixLength":28,"publicIPAddressVersion":"IPv4"}}`
	out := runCLI(t, azRest("PUT", prefixURL, prefixBody))
	if !strings.Contains(out, "cli-nat-prefix") || !strings.Contains(out, "ipPrefix") {
		t.Fatalf("expected public IP prefix response, got %s", out)
	}

	prefixID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/publicIPPrefixes/cli-nat-prefix", subscriptionID, resourceGroup)
	natURL := armURL("Microsoft.Network", "natGateways/cli-nat", "2024-05-01")
	natBody := fmt.Sprintf(`{"location":"eastus","sku":{"name":"Standard"},"properties":{"idleTimeoutInMinutes":10,"publicIpPrefixes":[{"id":%q}]}}`, prefixID)
	out = runCLI(t, azRest("PUT", natURL, natBody))
	if !strings.Contains(out, "cli-nat") || !strings.Contains(out, "publicIpPrefixes") {
		t.Fatalf("expected NAT gateway response, got %s", out)
	}

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-nat-vnet", "2024-05-01")
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.92.0.0/16"]}}}`))

	natID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/natGateways/cli-nat", subscriptionID, resourceGroup)
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-nat-vnet/subnets/cli-nat-subnet", "2024-05-01")
	subnetBody := fmt.Sprintf(`{"properties":{"addressPrefix":"10.92.1.0/24","natGateway":{"id":%q}}}`, natID)
	out = runCLI(t, azRest("PUT", subnetURL, subnetBody))
	if !strings.Contains(out, "natGateway") {
		t.Fatalf("expected subnet NAT association, got %s", out)
	}

	out = runCLI(t, azRest("GET", natURL, ""))
	if !strings.Contains(out, "subnets") || !strings.Contains(out, "cli-nat-subnet") {
		t.Fatalf("expected NAT gateway to list associated subnet, got %s", out)
	}

	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "natGateways", "2024-05-01"), ""))
	if !strings.Contains(out, "cli-nat") {
		t.Fatalf("expected NAT gateway list response, got %s", out)
	}

	// The gateway's Azure Monitor metrics are listed by name, with the
	// definitions the gateway's namespace publishes.
	out = runCLI(t, azRest("GET", baseURL+natID+"/providers/Microsoft.Insights/metricDefinitions?api-version=2024-02-01", ""))
	for _, metric := range []string{"ByteCount", "SNATConnectionCount", "TotalConnectionCount", "DatapathAvailability"} {
		if !strings.Contains(out, `"value": "`+metric+`"`) {
			t.Fatalf("expected the metric definitions to name %s, got %s", metric, out)
		}
	}
	out = runCLI(t, azRest("GET", baseURL+natID+"/providers/Microsoft.Insights/metrics?api-version=2024-02-01&metricnames=TotalConnectionCount&aggregation=Total&interval=PT1M", ""))
	if !strings.Contains(out, `"value": "TotalConnectionCount"`) || !strings.Contains(out, `"namespace": "Microsoft.Network/natGateways"`) {
		t.Fatalf("expected the gateway's TotalConnectionCount series, got %s", out)
	}
	out = runCLI(t, azRest("GET", baseURL+natID+"/providers/microsoft.insights/metricNamespaces?api-version=2024-02-01", ""))
	if !strings.Contains(out, `"metricNamespaceName": "Microsoft.Network/natGateways"`) {
		t.Fatalf("expected the gateway's platform metric namespace, got %s", out)
	}

	// A subscription-scoped read covers every gateway of the region, one
	// series per gateway, named by its resource ID.
	subscriptionInsights := baseURL + "/subscriptions/" + subscriptionID + "/providers/Microsoft.Insights"
	out = runCLI(t, azRest("GET", subscriptionInsights+"/metricDefinitions?api-version=2024-02-01&region=eastus&metricnamespace=Microsoft.Network/natGateways", ""))
	if !strings.Contains(out, `"value": "SNATConnectionCount"`) {
		t.Fatalf("expected the subscription's NAT gateway metric definitions, got %s", out)
	}
	out = runCLI(t, azRest("GET", subscriptionInsights+"/metrics?api-version=2024-02-01&region=eastus&metricnamespace=Microsoft.Network/natGateways&metricnames=ByteCount&aggregation=Total", ""))
	if !strings.Contains(out, `"value": "`+natID+`"`) {
		t.Fatalf("expected a ByteCount series for %s, got %s", natID, out)
	}
	out = runCLI(t, azRest("POST", subscriptionInsights+"/metrics?api-version=2024-02-01&region=eastus",
		`{"metricNamespace":"Microsoft.Network/natGateways","metricNames":"PacketCount","aggregation":"Total","interval":"PT5M"}`))
	if !strings.Contains(out, `"value": "PacketCount"`) || !strings.Contains(out, `"interval": "PT5M"`) || !strings.Contains(out, `"value": "`+natID+`"`) {
		t.Fatalf("expected the POSTed query's PacketCount series for %s, got %s", natID, out)
	}
}
