package azure_cli_test

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

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
//
// The virtual network and subnet are created first. Reading a child collection
// of a subnet that was never created and asserting the collection is empty
// proves nothing about the collection — a handler answering {"value":[]} to any
// URL of that shape satisfies it — and it pins the simulator answering 200
// where the real service answers ResourceNotFound. The refusal for a subnet
// that does not exist is asserted alongside, so "empty" and "absent" stay
// distinguishable.
//
// Creating the subnet builds real Linux network fabric, so this joins the rest
// of the Microsoft.Network suite behind the host-capability gate and runs for
// real on the CI Linux cell — a better trade than the version it replaces,
// which ran everywhere and proved nothing because its subject existed nowhere.
func TestNetwork_SubnetLinksAndVnetDdos(t *testing.T) {
	requireNetworkHost(t)
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

	// The discriminator: these collections belong to the subnet, so a subnet
	// that does not exist has no collection to read.
	missingNav := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-no-such-subnet/ResourceNavigationLinks", networkMoreAPIVersion)
	assert.Contains(t, runNetworkCLIExpectFailure(t, azRest("GET", missingNav, "")), "ResourceNotFound",
		"resource navigation links under a subnet that does not exist must be refused as ResourceNotFound")
	missingSvc := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/subnets/cli-no-such-subnet/ServiceAssociationLinks", networkMoreAPIVersion)
	assert.Contains(t, runNetworkCLIExpectFailure(t, azRest("GET", missingSvc, "")), "ResourceNotFound",
		"service association links under a subnet that does not exist must be refused as ResourceNotFound")

	// Listing the DDoS protection status is a long-running operation whose
	// final state comes via Location, so the addresses are read from the
	// Location poll rather than from the 202 that starts it. `az rest` prints
	// response headers only under --debug, which is where the poll URL comes
	// from; a client that ignored the header would see the empty 202 body and
	// conclude the network has no addresses at all.
	ddosURL := armURL("Microsoft.Network", "virtualNetworks/cli-links-vnet/ddosProtectionStatus", networkMoreAPIVersion)
	location := azRestResponseHeader(t, azRest("POST", ddosURL, "", "--debug"), "Location")
	require.NotEmpty(t, location,
		"the DDoS protection status operation must answer 202 with the Location its result is read from")
	var ddos struct {
		Value []any `json:"value"`
	}
	require.Eventually(t, func() bool {
		out = runCLI(t, azRest("GET", location, ""))
		return strings.Contains(out, "\"value\"")
	}, 30*time.Second, 250*time.Millisecond,
		"the Location poll never produced the operation's result")
	parseJSON(t, out, &ddos)
	assert.Empty(t, ddos.Value, "no public IP is attached to this network, so no address has a DDoS protection status")
}

// azRestResponseHeader runs an `az rest --debug` command and returns the named
// response header the client printed. Response headers are how an Azure
// long-running operation hands back the URL its result is read from, and the
// Azure CLI shows them only in its debug log.
func azRestResponseHeader(t *testing.T, cmd *exec.Cmd, header string) string {
	t.Helper()
	// az prints response headers only in its --debug log, which it writes to
	// stderr; stdout carries the response body. Both are searched, because
	// which stream a given az version logs to is not part of its contract.
	stdout, stderr := runCLIStreams(t, cmd)
	out := stderr + "\n" + stdout
	match := regexp.MustCompile(`'` + regexp.QuoteMeta(header) + `':\s*'([^']+)'`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("the response carried no %s header:\n%s", header, out)
	}
	return match[1]
}
