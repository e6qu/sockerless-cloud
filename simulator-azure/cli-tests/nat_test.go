package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestNetworkNatGatewayPublicIPPrefixCLI(t *testing.T) {
	requireNetworkHost(t)
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
}
