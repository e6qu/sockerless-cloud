package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const networkMoreAPIVersion = "2024-05-01"

// TestNetwork_PublicIPDdosStatus exercises the public-IP DDoS protection status
// operation through `az rest`.
func TestNetwork_PublicIPDdosStatus(t *testing.T) {
	pipURL := armURL("Microsoft.Network", "publicIPAddresses/cli-ddos-pip", networkMoreAPIVersion)
	runCLI(t, azRest("PUT", pipURL, `{"location":"eastus","sku":{"name":"Standard"},"properties":{"publicIPAllocationMethod":"Dynamic"}}`))
	defer runCLI(t, azRest("DELETE", pipURL, ""))

	statusURL := armURL("Microsoft.Network", "publicIPAddresses/cli-ddos-pip/ddosProtectionStatus", networkMoreAPIVersion)
	out := runCLI(t, azRest("POST", statusURL, ""))
	var status struct {
		PublicIPAddressID   string `json:"publicIpAddressId"`
		IsWorkloadProtected string `json:"isWorkloadProtected"`
	}
	parseJSON(t, out, &status)
	assert.Contains(t, status.PublicIPAddressID, "cli-ddos-pip")
	assert.Equal(t, "False", status.IsWorkloadProtected)
}

// TestNetwork_LoadBalancerExtraOps exercises the load-balancer operations
// beyond CRUD through `az rest`: the associated network-interface list, the
// inbound-NAT port-mapping query, the per-rule health, and the NIC→IP-based
// pool migration.
func TestNetwork_LoadBalancerExtraOps(t *testing.T) {
	pipURL := armURL("Microsoft.Network", "publicIPAddresses/cli-lbx-pip", networkMoreAPIVersion)
	runCLI(t, azRest("PUT", pipURL, `{"location":"eastus","sku":{"name":"Standard"},"properties":{"publicIPAllocationMethod":"Dynamic"}}`))
	defer runCLI(t, azRest("DELETE", pipURL, ""))
	pipID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/publicIPAddresses/cli-lbx-pip", subscriptionID, resourceGroup)

	lbURL := armURL("Microsoft.Network", "loadBalancers/cli-lbx", networkMoreAPIVersion)
	lbID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/cli-lbx", subscriptionID, resourceGroup)
	body := fmt.Sprintf(`{"location":"eastus","sku":{"name":"Standard"},"properties":{
		"frontendIPConfigurations":[{"name":"fe","properties":{"publicIPAddress":{"id":%q}}}],
		"backendAddressPools":[{"name":"pool"}],
		"probes":[{"name":"probe","properties":{"protocol":"Tcp","port":80}}],
		"loadBalancingRules":[{"name":"rule","properties":{"protocol":"Tcp","frontendPort":80,"backendPort":8080,
			"frontendIPConfiguration":{"id":%q},"backendAddressPool":{"id":%q},"probe":{"id":%q}}}]}}`,
		pipID, lbID+"/frontendIPConfigurations/fe", lbID+"/backendAddressPools/pool", lbID+"/probes/probe")
	runCLI(t, azRest("PUT", lbURL, body))
	defer runCLI(t, azRest("DELETE", lbURL, ""))

	// LoadBalancerNetworkInterfaces_List — no NICs reference the pool.
	out := runCLI(t, azRest("GET", armURL("Microsoft.Network", "loadBalancers/cli-lbx/networkInterfaces", networkMoreAPIVersion), ""))
	var nics struct {
		Value []any `json:"value"`
	}
	parseJSON(t, out, &nics)
	assert.Empty(t, nics.Value)

	// LoadBalancers_ListInboundNatRulePortMappings — no inbound NAT rules.
	out = runCLI(t, azRest("POST",
		armURL("Microsoft.Network", "loadBalancers/cli-lbx/backendAddressPools/pool/queryInboundNatRulePortMapping", networkMoreAPIVersion),
		`{"ipAddress":"10.0.0.7"}`))
	var mappings struct {
		InboundNatRulePortMappings []any `json:"inboundNatRulePortMappings"`
	}
	parseJSON(t, out, &mappings)
	assert.Empty(t, mappings.InboundNatRulePortMappings)

	// LoadBalancerLoadBalancingRules_Health — zero backends → 0 up / 0 down.
	out = runCLI(t, azRest("POST",
		armURL("Microsoft.Network", "loadBalancers/cli-lbx/loadBalancingRules/rule/health", networkMoreAPIVersion), ""))
	var health struct {
		Up   int `json:"up"`
		Down int `json:"down"`
	}
	parseJSON(t, out, &health)
	assert.Equal(t, 0, health.Up)
	assert.Equal(t, 0, health.Down)

	// LoadBalancers_MigrateToIpBased — the named pool exists, so it migrates.
	out = runCLI(t, azRest("POST",
		armURL("Microsoft.Network", "loadBalancers/cli-lbx/migrateToIpBased", networkMoreAPIVersion),
		`{"pools":["pool"]}`))
	var migrated struct {
		MigratedPools []string `json:"migratedPools"`
	}
	parseJSON(t, out, &migrated)
	require.Len(t, migrated.MigratedPools, 1)
	assert.Equal(t, "pool", migrated.MigratedPools[0])
}

// TestNetwork_SubnetLinksAndVnetDdos exercises the subnet resource/service
// association link reads and the virtual-network DDoS protection status through
// `az rest` (all empty without delegated services or attached public IPs).
func TestNetwork_SubnetLinksAndVnetDdos(t *testing.T) {
	navURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-links-subnet/ResourceNavigationLinks", networkMoreAPIVersion)
	out := runCLI(t, azRest("GET", navURL, ""))
	var nav struct {
		Value []any `json:"value"`
	}
	parseJSON(t, out, &nav)
	assert.Empty(t, nav.Value)

	svcURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-links-subnet/ServiceAssociationLinks", networkMoreAPIVersion)
	out = runCLI(t, azRest("GET", svcURL, ""))
	var svc struct {
		Value []any `json:"value"`
	}
	parseJSON(t, out, &svc)
	assert.Empty(t, svc.Value)

	ddosURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/ddosProtectionStatus", networkMoreAPIVersion)
	out = runCLI(t, azRest("POST", ddosURL, ""))
	var ddos struct {
		Value []any `json:"value"`
	}
	parseJSON(t, out, &ddos)
	assert.Empty(t, ddos.Value)
}
