package azure_cli_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure Private Link and the Microsoft.Network resources that describe how a
// workload attaches to a virtual network, driven through the Azure CLI.

const privateLinkAPIVersion = "2025-03-01"

// runNetworkCLIExpectFailure runs a CLI command that must fail and returns its
// combined output. `az rest` exits non-zero on any 4xx, so an operation the
// simulator is expected to refuse — a read of a deleted resource, a write the
// resource provider rejects — is asserted here rather than through runCLI,
// which treats a non-zero exit as a test failure.
func runNetworkCLIExpectFailure(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	cmd.Env = append(cmd.Env, "PYTHONWARNINGS=ignore")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("CLI command unexpectedly succeeded: %s\nOutput: %s", strings.Join(cmd.Args, " "), out)
	}
	return string(out)
}

func networkResourceID(path string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/%s",
		subscriptionID, resourceGroup, path)
}

// TestNetworkApplicationSecurityGroupCLI covers the application security group
// surface: create, show, retag, both lists, delete.
func TestNetworkApplicationSecurityGroupCLI(t *testing.T) {
	url := armURL("Microsoft.Network", "applicationSecurityGroups/cli-asg", privateLinkAPIVersion)
	out := runCLI(t, azRest("PUT", url, `{"location":"eastus","tags":{"tier":"web"}}`))
	var asg struct {
		Name       string            `json:"name"`
		Type       string            `json:"type"`
		Etag       string            `json:"etag"`
		Tags       map[string]string `json:"tags"`
		Properties struct {
			ResourceGUID      string `json:"resourceGuid"`
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, out, &asg)
	assert.Equal(t, "cli-asg", asg.Name)
	assert.Equal(t, "Microsoft.Network/applicationSecurityGroups", asg.Type)
	assert.Equal(t, "Succeeded", asg.Properties.ProvisioningState)
	require.NotEmpty(t, asg.Properties.ResourceGUID)
	require.NotEmpty(t, asg.Etag)
	guid := asg.Properties.ResourceGUID

	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &asg)
	assert.Equal(t, guid, asg.Properties.ResourceGUID)

	out = runCLI(t, azRest("PATCH", url, `{"tags":{"tier":"web","env":"cli"}}`))
	parseJSON(t, out, &asg)
	assert.Equal(t, "cli", asg.Tags["env"])
	assert.Equal(t, guid, asg.Properties.ResourceGUID)

	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "applicationSecurityGroups", privateLinkAPIVersion), ""))
	assert.Contains(t, out, "cli-asg")

	subURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/applicationSecurityGroups?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", subURL, ""))
	assert.Contains(t, out, "cli-asg")

	runCLI(t, azRest("DELETE", url, ""))
	out = runNetworkCLIExpectFailure(t, azRest("GET", url, ""))
	assert.Contains(t, out, "ResourceNotFound")
}

// TestNetworkVirtualNetworkTapCLI covers the virtual network tap surface,
// including the rejection of a tap that names no collector destination.
func TestNetworkVirtualNetworkTapCLI(t *testing.T) {
	url := armURL("Microsoft.Network", "virtualNetworkTaps/cli-tap", privateLinkAPIVersion)

	out := runNetworkCLIExpectFailure(t, azRest("PUT", url, `{"location":"eastus","properties":{}}`))
	assert.Contains(t, out, "VirtualNetworkTapDestinationRequired")

	collector := networkResourceID("networkInterfaces/cli-collector/ipConfigurations/ipconfig1")
	body := fmt.Sprintf(`{"location":"eastus","properties":{"destinationNetworkInterfaceIPConfiguration":{"id":%q},"destinationPort":4789}}`, collector)
	out = runCLI(t, azRest("PUT", url, body))
	var tap struct {
		Name       string `json:"name"`
		Properties struct {
			ProvisioningState                 string `json:"provisioningState"`
			DestinationPort                   int    `json:"destinationPort"`
			NetworkInterfaceTapConfigurations []any  `json:"networkInterfaceTapConfigurations"`
		} `json:"properties"`
	}
	parseJSON(t, out, &tap)
	assert.Equal(t, "cli-tap", tap.Name)
	assert.Equal(t, "Succeeded", tap.Properties.ProvisioningState)
	assert.Equal(t, 4789, tap.Properties.DestinationPort)
	assert.Empty(t, tap.Properties.NetworkInterfaceTapConfigurations)

	out = runCLI(t, azRest("PATCH", url, `{"tags":{"purpose":"mirror"}}`))
	assert.Contains(t, out, "mirror")

	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "virtualNetworkTaps", privateLinkAPIVersion), ""))
	assert.Contains(t, out, "cli-tap")

	subURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/virtualNetworkTaps?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", subURL, ""))
	assert.Contains(t, out, "cli-tap")

	runCLI(t, azRest("DELETE", url, ""))
	out = runNetworkCLIExpectFailure(t, azRest("GET", url, ""))
	assert.Contains(t, out, "ResourceNotFound")
}

// TestNetworkServiceEndpointPolicyCLI covers the service endpoint policy and
// its definition sub-resource, and proves a definition written through the
// sub-resource path is reported by the policy's own read.
func TestNetworkServiceEndpointPolicyCLI(t *testing.T) {
	url := armURL("Microsoft.Network", "serviceEndpointPolicies/cli-sep", privateLinkAPIVersion)
	account := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/cliallowed",
		subscriptionID, resourceGroup)
	body := fmt.Sprintf(`{"location":"eastus","properties":{"serviceEndpointPolicyDefinitions":[{"name":"allow-one","properties":{"service":"Microsoft.Storage","serviceResources":[%q]}}]}}`, account)
	out := runCLI(t, azRest("PUT", url, body))
	var policy struct {
		Name       string `json:"name"`
		Properties struct {
			ProvisioningState                string `json:"provisioningState"`
			ServiceEndpointPolicyDefinitions []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Type       string `json:"type"`
				Properties struct {
					Service string `json:"service"`
				} `json:"properties"`
			} `json:"serviceEndpointPolicyDefinitions"`
			Subnets []any `json:"subnets"`
		} `json:"properties"`
	}
	parseJSON(t, out, &policy)
	assert.Equal(t, "cli-sep", policy.Name)
	assert.Equal(t, "Succeeded", policy.Properties.ProvisioningState)
	require.Len(t, policy.Properties.ServiceEndpointPolicyDefinitions, 1)
	assert.Equal(t, "Microsoft.Storage", policy.Properties.ServiceEndpointPolicyDefinitions[0].Properties.Service)
	assert.Equal(t, "Microsoft.Network/serviceEndpointPolicies/serviceEndpointPolicyDefinitions",
		policy.Properties.ServiceEndpointPolicyDefinitions[0].Type)
	assert.Empty(t, policy.Properties.Subnets)

	defURL := armURL("Microsoft.Network",
		"serviceEndpointPolicies/cli-sep/serviceEndpointPolicyDefinitions/allow-two", privateLinkAPIVersion)
	defBody := fmt.Sprintf(`{"properties":{"description":"second","service":"Microsoft.Storage","serviceResources":[%q]}}`, account+"2")
	out = runCLI(t, azRest("PUT", defURL, defBody))
	assert.Contains(t, out, "allow-two")

	out = runCLI(t, azRest("GET", defURL, ""))
	assert.Contains(t, out, "second")

	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &policy)
	assert.Len(t, policy.Properties.ServiceEndpointPolicyDefinitions, 2,
		"the policy must report the definition written through the sub-resource path")

	listDefURL := armURL("Microsoft.Network",
		"serviceEndpointPolicies/cli-sep/serviceEndpointPolicyDefinitions", privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", listDefURL, ""))
	assert.Contains(t, out, "allow-one")
	assert.Contains(t, out, "allow-two")

	out = runCLI(t, azRest("PATCH", url, `{"tags":{"owner":"platform"}}`))
	assert.Contains(t, out, "platform")

	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "serviceEndpointPolicies", privateLinkAPIVersion), ""))
	assert.Contains(t, out, "cli-sep")

	// Microsoft.Network spells the subscription-wide collection with a capital S.
	subURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/ServiceEndpointPolicies?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", subURL, ""))
	assert.Contains(t, out, "cli-sep")

	runCLI(t, azRest("DELETE", defURL, ""))
	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &policy)
	assert.Len(t, policy.Properties.ServiceEndpointPolicyDefinitions, 1)

	runCLI(t, azRest("DELETE", url, ""))
	out = runNetworkCLIExpectFailure(t, azRest("GET", url, ""))
	assert.Contains(t, out, "ResourceNotFound")
}

// TestNetworkPrivateLinkCLI drives the whole Private Link round trip through
// the CLI: a private link service, a private endpoint that connects to it, the
// owner's approval on the service, and the endpoint reading that decision back.
func TestNetworkPrivateLinkCLI(t *testing.T) {
	requireNetworkHost(t)

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-pl-vnet", privateLinkAPIVersion)
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.94.0.0/16"]}}}`))
	natSubnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-pl-vnet/subnets/cli-nat-subnet", privateLinkAPIVersion)
	runCLI(t, azRest("PUT", natSubnetURL, `{"properties":{"addressPrefix":"10.94.1.0/24"}}`))
	peSubnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-pl-vnet/subnets/cli-pe-subnet", privateLinkAPIVersion)
	runCLI(t, azRest("PUT", peSubnetURL, `{"properties":{"addressPrefix":"10.94.2.0/24"}}`))

	natSubnetID := networkResourceID("virtualNetworks/cli-pl-vnet/subnets/cli-nat-subnet")
	peSubnetID := networkResourceID("virtualNetworks/cli-pl-vnet/subnets/cli-pe-subnet")

	plsURL := armURL("Microsoft.Network", "privateLinkServices/cli-pls", privateLinkAPIVersion)
	plsBody := fmt.Sprintf(`{"location":"eastus","properties":{"ipConfigurations":[{"name":"natcfg1","properties":{"subnet":{"id":%q},"privateIPAllocationMethod":"Dynamic"}}]}}`, natSubnetID)
	out := runCLI(t, azRest("PUT", plsURL, plsBody))
	var pls struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties struct {
			Alias             string `json:"alias"`
			AccessMode        string `json:"accessMode"`
			ProvisioningState string `json:"provisioningState"`
			IPConfigurations  []struct {
				Name       string `json:"name"`
				Properties struct {
					PrivateIPAddress string `json:"privateIPAddress"`
				} `json:"properties"`
			} `json:"ipConfigurations"`
			NetworkInterfaces []struct {
				ID string `json:"id"`
			} `json:"networkInterfaces"`
			PrivateEndpointConnections []any `json:"privateEndpointConnections"`
		} `json:"properties"`
	}
	parseJSON(t, out, &pls)
	assert.Equal(t, "cli-pls", pls.Name)
	assert.Equal(t, "Default", pls.Properties.AccessMode)
	require.NotEmpty(t, pls.Properties.Alias)
	require.Len(t, pls.Properties.IPConfigurations, 1)
	assert.NotEmpty(t, pls.Properties.IPConfigurations[0].Properties.PrivateIPAddress,
		"the NAT configuration must take a real address from its subnet")
	require.Len(t, pls.Properties.NetworkInterfaces, 1)
	assert.Empty(t, pls.Properties.PrivateEndpointConnections)

	peURL := armURL("Microsoft.Network", "privateEndpoints/cli-pe", privateLinkAPIVersion)
	peBody := fmt.Sprintf(`{"location":"eastus","properties":{"subnet":{"id":%q},"manualPrivateLinkServiceConnections":[{"name":"cli-pe","properties":{"privateLinkServiceId":%q,"requestMessage":"cli asks"}}]}}`,
		peSubnetID, pls.ID)
	out = runCLI(t, azRest("PUT", peURL, peBody))
	var pe struct {
		ID         string `json:"id"`
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
			NetworkInterfaces []struct {
				ID string `json:"id"`
			} `json:"networkInterfaces"`
			ManualPrivateLinkServiceConnections []struct {
				Name       string `json:"name"`
				Properties struct {
					PrivateLinkServiceConnectionState struct {
						Status      string `json:"status"`
						Description string `json:"description"`
					} `json:"privateLinkServiceConnectionState"`
				} `json:"properties"`
			} `json:"manualPrivateLinkServiceConnections"`
		} `json:"properties"`
	}
	parseJSON(t, out, &pe)
	assert.Equal(t, "Succeeded", pe.Properties.ProvisioningState)
	require.Len(t, pe.Properties.NetworkInterfaces, 1)
	require.Len(t, pe.Properties.ManualPrivateLinkServiceConnections, 1)
	assert.Equal(t, "Pending",
		pe.Properties.ManualPrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Status)

	// The endpoint's interface is an ordinary network interface holding a real
	// address from the endpoint's own subnet.
	nicURL := fmt.Sprintf("%s%s?api-version=%s", baseURL, pe.Properties.NetworkInterfaces[0].ID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", nicURL, ""))
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
	assert.NotEmpty(t, nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress)

	// The service lists the connection the endpoint opened.
	connListURL := armURL("Microsoft.Network", "privateLinkServices/cli-pls/privateEndpointConnections", privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", connListURL, ""))
	assert.Contains(t, out, "cli-pe")

	connURL := armURL("Microsoft.Network", "privateLinkServices/cli-pls/privateEndpointConnections/cli-pe", privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", connURL, ""))
	assert.Contains(t, out, "Pending")
	assert.Contains(t, out, pe.ID)

	// The owner approves on the service; the endpoint reads the decision back.
	out = runCLI(t, azRest("PUT", connURL,
		`{"properties":{"privateLinkServiceConnectionState":{"status":"Approved","description":"cli approves"}}}`))
	assert.Contains(t, out, "Approved")

	out = runCLI(t, azRest("GET", peURL, ""))
	parseJSON(t, out, &pe)
	assert.Equal(t, "Approved",
		pe.Properties.ManualPrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Status)
	assert.Equal(t, "cli approves",
		pe.Properties.ManualPrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Description)

	// The service's own read reports the connection too.
	out = runCLI(t, azRest("GET", plsURL, ""))
	assert.Contains(t, out, "cli-pe")

	// Visibility and auto-approval discovery.
	visURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/locations/eastus/checkPrivateLinkServiceVisibility?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("POST", visURL, fmt.Sprintf(`{"privateLinkServiceAlias":%q}`, pls.Properties.Alias)))
	assert.Contains(t, out, "true")

	visRGURL := armURL("Microsoft.Network", "locations/eastus/checkPrivateLinkServiceVisibility", privateLinkAPIVersion)
	out = runCLI(t, azRest("POST", visRGURL, `{"privateLinkServiceAlias":"missing.0.eastus.azure.privatelinkservice"}`))
	assert.Contains(t, out, "false")

	autoURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/locations/eastus/autoApprovedPrivateLinkServices?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", autoURL, ""))
	assert.NotContains(t, out, "cli-pls",
		"a service with no auto-approval list must not appear as auto-approved")

	autoRGURL := armURL("Microsoft.Network", "locations/eastus/autoApprovedPrivateLinkServices", privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", autoRGURL, ""))
	assert.Contains(t, out, "value")

	// Endpoint lists and the private-endpoint type catalog.
	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "privateEndpoints", privateLinkAPIVersion), ""))
	assert.Contains(t, out, "cli-pe")
	peSubListURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/privateEndpoints?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", peSubListURL, ""))
	assert.Contains(t, out, "cli-pe")
	plsSubListURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/privateLinkServices?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", plsSubListURL, ""))
	assert.Contains(t, out, "cli-pls")
	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "privateLinkServices", privateLinkAPIVersion), ""))
	assert.Contains(t, out, "cli-pls")

	typesURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/locations/eastus/availablePrivateEndpointTypes?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", typesURL, ""))
	assert.Contains(t, out, "Microsoft.KeyVault/vaults")
	typesRGURL := armURL("Microsoft.Network", "locations/eastus/availablePrivateEndpointTypes", privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", typesRGURL, ""))
	assert.Contains(t, out, "Microsoft.Network/privateLinkServices")

	// Removing the connection on the service disconnects the endpoint.
	runCLI(t, azRest("DELETE", connURL, ""))
	out = runCLI(t, azRest("GET", peURL, ""))
	assert.Contains(t, out, "Disconnected")

	runCLI(t, azRest("DELETE", peURL, ""))
	runCLI(t, azRest("DELETE", plsURL, ""))
	out = runNetworkCLIExpectFailure(t, azRest("GET", plsURL, ""))
	assert.Contains(t, out, "ResourceNotFound")
}

// TestNetworkPrivateEndpointKeyVaultAndDNSCLI proves the cross-provider
// integration through the CLI: an endpoint on a Key Vault opens its connection
// in the vault's own collection, and a private DNS zone group publishes real A
// records the private DNS record surface then serves.
func TestNetworkPrivateEndpointKeyVaultAndDNSCLI(t *testing.T) {
	requireNetworkHost(t)

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-kvpe-vnet", privateLinkAPIVersion)
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.95.0.0/16"]}}}`))
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-kvpe-vnet/subnets/cli-kvpe-subnet", privateLinkAPIVersion)
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.95.1.0/24"}}`))
	subnetID := networkResourceID("virtualNetworks/cli-kvpe-vnet/subnets/cli-kvpe-subnet")

	vaultURL := armURL("Microsoft.KeyVault", "vaults/clipelinkvault", "2023-07-01")
	runCLI(t, azRest("PUT", vaultURL, fmt.Sprintf(
		`{"location":"eastus","properties":{"tenantId":%q,"sku":{"family":"A","name":"standard"}}}`, simTenantID)))
	vaultID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/clipelinkvault",
		subscriptionID, resourceGroup)

	zoneURL := armURL("Microsoft.Network", "privateDnsZones/privatelink.vaultcore.azure.net", "2024-06-01")
	runCLI(t, azRest("PUT", zoneURL, `{"location":"global"}`))
	zoneID := networkResourceID("privateDnsZones/privatelink.vaultcore.azure.net")

	peURL := armURL("Microsoft.Network", "privateEndpoints/cli-kv-pe", privateLinkAPIVersion)
	peBody := fmt.Sprintf(`{"location":"eastus","properties":{"subnet":{"id":%q},"privateLinkServiceConnections":[{"name":"cli-kv-pe","properties":{"privateLinkServiceId":%q,"groupIds":["vault"]}}]}}`,
		subnetID, vaultID)
	out := runCLI(t, azRest("PUT", peURL, peBody))
	var pe struct {
		Properties struct {
			CustomDNSConfigs []struct {
				Fqdn        string   `json:"fqdn"`
				IPAddresses []string `json:"ipAddresses"`
			} `json:"customDnsConfigs"`
			PrivateLinkServiceConnections []struct {
				Properties struct {
					PrivateLinkServiceConnectionState struct {
						Status string `json:"status"`
					} `json:"privateLinkServiceConnectionState"`
				} `json:"properties"`
			} `json:"privateLinkServiceConnections"`
		} `json:"properties"`
	}
	parseJSON(t, out, &pe)
	require.Len(t, pe.Properties.PrivateLinkServiceConnections, 1)
	assert.Equal(t, "Approved",
		pe.Properties.PrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Status)
	require.Len(t, pe.Properties.CustomDNSConfigs, 1)
	assert.Equal(t, "clipelinkvault.vault.azure.net", pe.Properties.CustomDNSConfigs[0].Fqdn)
	require.Len(t, pe.Properties.CustomDNSConfigs[0].IPAddresses, 1)
	peAddress := pe.Properties.CustomDNSConfigs[0].IPAddresses[0]
	require.NotEmpty(t, peAddress)

	// The vault's own connection surface lists the same connection.
	vaultConnURL := armURL("Microsoft.KeyVault", "vaults/clipelinkvault/privateEndpointConnections", "2023-07-01")
	out = runCLI(t, azRest("GET", vaultConnURL, ""))
	assert.Contains(t, out, "cli-kv-pe")
	assert.Contains(t, out, "Approved")

	// The vault owner rejects it there; the endpoint reads the rejection back.
	vaultConnItemURL := armURL("Microsoft.KeyVault", "vaults/clipelinkvault/privateEndpointConnections/cli-kv-pe", "2023-07-01")
	runCLI(t, azRest("PUT", vaultConnItemURL,
		`{"properties":{"privateLinkServiceConnectionState":{"status":"Rejected","description":"cli rejects"}}}`))
	out = runCLI(t, azRest("GET", peURL, ""))
	parseJSON(t, out, &pe)
	assert.Equal(t, "Rejected",
		pe.Properties.PrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Status)

	// A private DNS zone group publishes real A records.
	groupURL := armURL("Microsoft.Network", "privateEndpoints/cli-kv-pe/privateDnsZoneGroups/default", privateLinkAPIVersion)
	groupBody := fmt.Sprintf(`{"properties":{"privateDnsZoneConfigs":[{"name":"vault-config","properties":{"privateDnsZoneId":%q}}]}}`, zoneID)
	out = runCLI(t, azRest("PUT", groupURL, groupBody))
	var group struct {
		Properties struct {
			ProvisioningState     string `json:"provisioningState"`
			PrivateDNSZoneConfigs []struct {
				Properties struct {
					RecordSets []struct {
						RecordType    string   `json:"recordType"`
						RecordSetName string   `json:"recordSetName"`
						TTL           int      `json:"ttl"`
						IPAddresses   []string `json:"ipAddresses"`
					} `json:"recordSets"`
				} `json:"properties"`
			} `json:"privateDnsZoneConfigs"`
		} `json:"properties"`
	}
	parseJSON(t, out, &group)
	assert.Equal(t, "Succeeded", group.Properties.ProvisioningState)
	require.Len(t, group.Properties.PrivateDNSZoneConfigs, 1)
	require.Len(t, group.Properties.PrivateDNSZoneConfigs[0].Properties.RecordSets, 1)
	record := group.Properties.PrivateDNSZoneConfigs[0].Properties.RecordSets[0]
	assert.Equal(t, "A", record.RecordType)
	assert.Equal(t, "clipelinkvault", record.RecordSetName)
	assert.Equal(t, []string{peAddress}, record.IPAddresses)

	// The record the group reports is a real record set in the zone.
	recordURL := armURL("Microsoft.Network",
		"privateDnsZones/privatelink.vaultcore.azure.net/A/clipelinkvault", "2024-06-01")
	out = runCLI(t, azRest("GET", recordURL, ""))
	assert.Contains(t, out, peAddress)

	out = runCLI(t, azRest("GET", groupURL, ""))
	assert.Contains(t, out, "vault-config")
	groupListURL := armURL("Microsoft.Network", "privateEndpoints/cli-kv-pe/privateDnsZoneGroups", privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", groupListURL, ""))
	assert.Contains(t, out, "default")

	runCLI(t, azRest("DELETE", groupURL, ""))
	out = runNetworkCLIExpectFailure(t, azRest("GET", recordURL, ""))
	assert.NotContains(t, out, peAddress,
		"removing the zone group must remove the records it published")

	// Deleting the endpoint closes the connection on the vault.
	runCLI(t, azRest("DELETE", peURL, ""))
	out = runNetworkCLIExpectFailure(t, azRest("GET", vaultConnItemURL, ""))
	assert.Contains(t, out, "ResourceNotFound")
}

// TestNetworkProfileCLI covers the network profile surface and the subnet
// validation its container network interface configurations are subject to.
func TestNetworkProfileCLI(t *testing.T) {
	requireNetworkHost(t)

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-np-vnet", privateLinkAPIVersion)
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.96.0.0/16"]}}}`))
	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-np-vnet/subnets/cli-np-subnet", privateLinkAPIVersion)
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.96.1.0/24"}}`))
	subnetID := networkResourceID("virtualNetworks/cli-np-vnet/subnets/cli-np-subnet")

	url := armURL("Microsoft.Network", "networkProfiles/cli-np", privateLinkAPIVersion)
	missing := fmt.Sprintf(`{"location":"eastus","properties":{"containerNetworkInterfaceConfigurations":[{"name":"cnic","properties":{"ipConfigurations":[{"name":"ipconfig1","properties":{"subnet":{"id":%q}}}]}}]}}`,
		subnetID+"-missing")
	out := runNetworkCLIExpectFailure(t, azRest("PUT", url, missing))
	assert.Contains(t, out, "ResourceNotFound")

	body := fmt.Sprintf(`{"location":"eastus","properties":{"containerNetworkInterfaceConfigurations":[{"name":"cnic","properties":{"ipConfigurations":[{"name":"ipconfig1","properties":{"subnet":{"id":%q}}}]}}]}}`,
		subnetID)
	out = runCLI(t, azRest("PUT", url, body))
	var profile struct {
		ID         string `json:"id"`
		Properties struct {
			ProvisioningState                       string `json:"provisioningState"`
			ResourceGUID                            string `json:"resourceGuid"`
			ContainerNetworkInterfaces              []any  `json:"containerNetworkInterfaces"`
			ContainerNetworkInterfaceConfigurations []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"containerNetworkInterfaceConfigurations"`
		} `json:"properties"`
	}
	parseJSON(t, out, &profile)
	assert.Equal(t, "Succeeded", profile.Properties.ProvisioningState)
	require.NotEmpty(t, profile.Properties.ResourceGUID)
	require.Len(t, profile.Properties.ContainerNetworkInterfaceConfigurations, 1)
	assert.Equal(t, "Microsoft.Network/networkProfiles/containerNetworkInterfaceConfigurations",
		profile.Properties.ContainerNetworkInterfaceConfigurations[0].Type)
	assert.Empty(t, profile.Properties.ContainerNetworkInterfaces)

	out = runCLI(t, azRest("GET", url, ""))
	assert.Contains(t, out, profile.Properties.ResourceGUID)

	out = runCLI(t, azRest("PATCH", url, `{"tags":{"workload":"aci"}}`))
	assert.Contains(t, out, "aci")

	out = runCLI(t, azRest("GET", armURL("Microsoft.Network", "networkProfiles", privateLinkAPIVersion), ""))
	assert.Contains(t, out, "cli-np")

	subListURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Network/networkProfiles?api-version=%s",
		baseURL, subscriptionID, privateLinkAPIVersion)
	out = runCLI(t, azRest("GET", subListURL, ""))
	assert.Contains(t, out, "cli-np")

	runCLI(t, azRest("DELETE", url, ""))
	out = runNetworkCLIExpectFailure(t, azRest("GET", url, ""))
	assert.Contains(t, out, "ResourceNotFound")
}
