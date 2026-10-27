package azure_cli_test

import (
	"fmt"
	"os/exec"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const networkMoreAPIVersion = "2024-05-01"

func TestNetwork_PublicIPDdosStatus(t *testing.T) {
	t.Parallel()
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

// TestNetwork_LoadBalancerExtraOps exercises the load balancer operations
// beyond CRUD: the associated network-interface list, the inbound NAT
// port-mapping query, per-rule health, and the migration to an IP-based pool.
func TestNetwork_LoadBalancerExtraOps(t *testing.T) {
	t.Parallel()
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

	// No NICs reference the pool.
	out := runCLI(t, azRest("GET", armURL("Microsoft.Network", "loadBalancers/cli-lbx/networkInterfaces", networkMoreAPIVersion), ""))
	var nics struct {
		Value []any `json:"value"`
	}
	parseJSON(t, out, &nics)
	assert.Empty(t, nics.Value)

	// The load balancer has no inbound NAT rules.
	out = runCLI(t, azRest("POST",
		armURL("Microsoft.Network", "loadBalancers/cli-lbx/backendAddressPools/pool/queryInboundNatRulePortMapping", networkMoreAPIVersion),
		`{"ipAddress":"10.0.0.7"}`))
	var mappings struct {
		InboundNatRulePortMappings []any `json:"inboundNatRulePortMappings"`
	}
	parseJSON(t, out, &mappings)
	assert.Empty(t, mappings.InboundNatRulePortMappings)

	// Zero backends: 0 up, 0 down.
	out = runCLI(t, azRest("POST",
		armURL("Microsoft.Network", "loadBalancers/cli-lbx/loadBalancingRules/rule/health", networkMoreAPIVersion), ""))
	var health struct {
		Up   int `json:"up"`
		Down int `json:"down"`
	}
	parseJSON(t, out, &health)
	assert.Equal(t, 0, health.Up)
	assert.Equal(t, 0, health.Down)

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

// TestNetwork_SubnetLinksAndVnetDdos reads the subnet resource and service
// association links and the virtual network DDoS protection status, all empty
// without delegated services or attached public IPs. It also checks that a
// missing subnet answers ResourceNotFound: a handler answering {"value":[]}
// to any URL of that shape would otherwise pass.
func TestNetwork_SubnetLinksAndVnetDdos(t *testing.T) {
	t.Parallel()
	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet", networkMoreAPIVersion)
	runCLI(t, azRest("PUT", vnetURL,
		`{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.93.0.0/16"]}}}`))
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-links-subnet", networkMoreAPIVersion)
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.93.1.0/24"}}`))

	navURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-links-subnet/ResourceNavigationLinks", networkMoreAPIVersion)
	out := runCLI(t, azRest("GET", navURL, ""))
	var nav struct {
		Value []any `json:"value"`
	}
	parseJSON(t, out, &nav)
	assert.Empty(t, nav.Value, "a subnet no service has delegated carries no resource navigation links")

	svcURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-links-subnet/ServiceAssociationLinks", networkMoreAPIVersion)
	out = runCLI(t, azRest("GET", svcURL, ""))
	var svc struct {
		Value []any `json:"value"`
	}
	parseJSON(t, out, &svc)
	assert.Empty(t, svc.Value, "a subnet no service has delegated carries no service association links")

	// These collections belong to the subnet, so a missing subnet has none.
	missingNav := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-no-such-subnet/ResourceNavigationLinks", networkMoreAPIVersion)
	assert.Contains(t, runNetworkCLIExpectFailure(t, azRest("GET", missingNav, "")), "ResourceNotFound",
		"resource navigation links under a subnet that does not exist must be refused as ResourceNotFound")
	missingSvc := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-no-such-subnet/ServiceAssociationLinks", networkMoreAPIVersion)
	assert.Contains(t, runNetworkCLIExpectFailure(t, azRest("GET", missingSvc, "")), "ResourceNotFound",
		"service association links under a subnet that does not exist must be refused as ResourceNotFound")

	// The DDoS protection status list is a long-running operation whose result
	// comes from the Location poll, not the empty 202 that starts it. `az rest`
	// prints response headers only under --debug.
	ddosURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/ddosProtectionStatus", networkMoreAPIVersion)
	location := azRestResponseHeader(t, azRest("POST", ddosURL, "", "--debug"), "Location")
	require.NotEmpty(t, location,
		"the DDoS protection status operation must answer 202 with the Location its result is read from")
	var ddos struct {
		Value []any `json:"value"`
	}
	parseJSON(t, azLocationResult(t, location), &ddos)
	assert.Empty(t, ddos.Value, "no public IP is attached to this network, so no address has a DDoS protection status")
}

// azRestResponseHeader runs an `az rest --debug` command and returns the named
// response header, which the Azure CLI shows only in its debug log.
func azRestResponseHeader(t *testing.T, cmd *exec.Cmd, header string) string {
	t.Helper()
	// Search both streams: which one a given az version logs --debug to is not
	// part of its contract.
	stdout, stderr := runCLIStreams(t, cmd)
	out := stderr + "\n" + stdout
	match := regexp.MustCompile(`'` + regexp.QuoteMeta(header) + `':\s*'([^']+)'`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("the response carried no %s header:\n%s", header, out)
	}
	return match[1]
}
