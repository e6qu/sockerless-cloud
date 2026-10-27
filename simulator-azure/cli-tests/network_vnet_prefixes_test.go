package azure_cli_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetwork_VirtualNetworkWithTwoAddressPrefixesCLI creates a virtual network
// with two address prefixes (`az network vnet create --address-prefixes`), a
// subnet in each, and a machine in the second; the machine takes its address
// there and reads it back from the Instance Metadata Service. A subnet outside
// both prefixes is refused.
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/virtualNetworks/{virtualNetworkName}
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/virtualNetworks/{virtualNetworkName}/subnets/{subnetName}
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/networkInterfaces/{networkInterfaceName}
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/virtualMachines/{vmName}
func TestNetwork_VirtualNetworkWithTwoAddressPrefixesCLI(t *testing.T) {
	t.Parallel()
	// The machine runs, so use the full workload runtime.
	env := startAzTLSSimulator(t)
	tagCLILogin(t, env, "sockerless-vnet-prefixes")

	const rg, vnet, nic, vm = "cli-prefixes-rg", "cli-prefixes-vnet", "cli-prefixes-nic", "cli-prefixes-vm"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	var created struct {
		NewVNet struct {
			AddressSpace struct {
				AddressPrefixes []string `json:"addressPrefixes"`
			} `json:"addressSpace"`
		} `json:"newVNet"`
	}
	parseJSON(t, runCLI(t, env.command("network", "vnet", "create", "-g", rg, "-n", vnet, "-l", "eastus",
		"--address-prefixes", "10.109.0.0/16", "10.110.0.0/16", "-o", "json")), &created)
	assert.Equal(t, []string{"10.109.0.0/16", "10.110.0.0/16"}, created.NewVNet.AddressSpace.AddressPrefixes)
	runCLI(t, env.command("network", "vnet", "subnet", "create", "-g", rg, "--vnet-name", vnet, "-n", "first",
		"--address-prefixes", "10.109.1.0/24", "-o", "json"))
	runCLI(t, env.command("network", "vnet", "subnet", "create", "-g", rg, "--vnet-name", vnet, "-n", "second",
		"--address-prefixes", "10.110.1.0/24", "-o", "json"))
	_, stderr, err := runCLIStreamsResult(env.command("network", "vnet", "subnet", "create", "-g", rg, "--vnet-name", vnet, "-n", "outside",
		"--address-prefixes", "10.111.1.0/24", "-o", "json"))
	require.Error(t, err, "a subnet outside every address prefix is refused")
	assert.Contains(t, stderr, "10.111.1.0/24")

	runCLI(t, env.command("network", "nic", "create", "-g", rg, "-n", nic, "-l", "eastus",
		"--vnet-name", vnet, "--subnet", "second", "-o", "json"))
	addresses := cliNICAddresses(t, env, rg, nic)
	require.True(t, strings.HasPrefix(addresses["ipconfig1"], "10.110.1."),
		"the interface in the second prefix's subnet takes its address there; got %s", addresses["ipconfig1"])

	// `az vm create` deploys an ARM template through
	// Microsoft.Resources/deployments, which the simulator does not serve, so write
	// the machine as the template would.
	nicID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/%s", subscriptionID, rg, nic)
	vmURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s?api-version=2024-07-01",
		env.baseURL, subscriptionID, rg, vm)
	customData := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nhostname -I > /var/tmp/addresses\n"))
	runCLI(t, env.command("rest", "--method", "PUT", "--url", vmURL, "--body", fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"hardwareProfile": {"vmSize": "Standard_B1s"},
			"storageProfile": {
				"imageReference": {"publisher": "Debian", "offer": "debian-12", "sku": "12", "version": "latest"},
				"osDisk": {"name": "%s-osdisk", "createOption": "FromImage", "managedDisk": {"storageAccountType": "Standard_LRS"}}
			},
			"osProfile": {"computerName": "%s", "adminUsername": "azureuser", "adminPassword": "Sockerless!Pass1", "customData": %q},
			"networkProfile": {"networkInterfaces": [{"id": %q}]}
		}
	}`, vm, vm, customData, nicID), "-o", "json"))
	t.Cleanup(func() { _, _, _ = runCLIStreamsResult(env.command("rest", "--method", "DELETE", "--url", vmURL)) })
	runCLI(t, env.command("vm", "wait", "-g", rg, "-n", vm, "--created", "--timeout", "300"))

	var ran struct {
		InstanceView struct {
			Statuses []struct {
				Message string `json:"message"`
			} `json:"statuses"`
		} `json:"instanceView"`
	}
	parseJSON(t, runCLI(t, env.command("vm", "extension", "set", "-g", rg, "--vm-name", vm, "-n", "CustomScript",
		"--publisher", "Microsoft.Azure.Extensions", "--version", "2.1",
		"--settings", `{"commandToExecute": "cat /var/tmp/addresses"}`, "-o", "json")), &ran)
	var messages []string
	for _, status := range ran.InstanceView.Statuses {
		messages = append(messages, status.Message)
	}
	assert.Contains(t, strings.Join(messages, "\n"), addresses["ipconfig1"], "the machine holds the interface's address in the second prefix")
}
