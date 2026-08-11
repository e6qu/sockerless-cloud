package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestNetworkLoadBalancerCLI(t *testing.T) {
	pipURL := armURL("Microsoft.Network", "publicIPAddresses/cli-lb-pip", "2024-05-01")
	pipBody := `{"location":"eastus","sku":{"name":"Standard"},"properties":{"publicIPAllocationMethod":"Static","publicIPAddressVersion":"IPv4"}}`
	out := runCLI(t, azRest("PUT", pipURL, pipBody))
	if !strings.Contains(out, "cli-lb-pip") {
		t.Fatalf("expected public IP response, got %s", out)
	}

	pipID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/publicIPAddresses/cli-lb-pip", subscriptionID, resourceGroup)
	lbID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/cli-lb", subscriptionID, resourceGroup)
	feID := lbID + "/frontendIPConfigurations/frontend"
	beID := lbID + "/backendAddressPools/backend"
	probeID := lbID + "/probes/tcp-probe"

	lbURL := armURL("Microsoft.Network", "loadBalancers/cli-lb", "2024-05-01")
	lbBody := fmt.Sprintf(`{"location":"eastus","sku":{"name":"Standard"},"properties":{"frontendIPConfigurations":[{"name":"frontend","properties":{"publicIPAddress":{"id":%q}}}],"backendAddressPools":[{"name":"backend","properties":{}}],"probes":[{"name":"tcp-probe","properties":{"protocol":"Tcp","port":80}}],"loadBalancingRules":[{"name":"http-rule","properties":{"protocol":"Tcp","frontendPort":80,"backendPort":80,"frontendIPConfiguration":{"id":%q},"backendAddressPool":{"id":%q},"probe":{"id":%q}}}]}}`, pipID, feID, beID, probeID)
	out = runCLI(t, azRest("PUT", lbURL, lbBody))
	if !strings.Contains(out, "http-rule") {
		t.Fatalf("expected load-balancing rule in load balancer response, got %s", out)
	}

	out = runCLI(t, azRest("GET", lbURL, ""))
	if !strings.Contains(out, "frontendIPConfigurations") || !strings.Contains(out, "backendAddressPools") {
		t.Fatalf("expected load balancer child resources, got %s", out)
	}

	runCLI(t, azRest("DELETE", lbURL, ""))
	runCLI(t, azRest("DELETE", pipURL, ""))
}
