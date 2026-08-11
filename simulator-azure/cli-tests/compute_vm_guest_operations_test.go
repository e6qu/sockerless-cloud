package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

// The in-guest operations over the real az CLI. Each runs against a machine
// that really booted, and asserts on what the guest produced.
func TestComputeVirtualMachineGuestOperationsCLI(t *testing.T) {
	requireNetworkHost(t)

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-guest-vnet", "2024-05-01")
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.98.0.0/16"]}}}`))

	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-guest-vnet/subnets/cli-guest-subnet", "2024-05-01")
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.98.1.0/24"}}`))

	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/cli-guest-vnet/subnets/cli-guest-subnet",
		subscriptionID, resourceGroup)
	nicID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/cli-guest-nic",
		subscriptionID, resourceGroup)
	nicURL := armURL("Microsoft.Network", "networkInterfaces/cli-guest-nic", "2024-05-01")
	runCLI(t, azRest("PUT", nicURL, fmt.Sprintf(
		`{"location":"eastus","properties":{"ipConfigurations":[{"name":"ipconfig1","properties":{"subnet":{"id":%q},"privateIPAllocationMethod":"Dynamic","privateIPAddressVersion":"IPv4","primary":true}}]}}`,
		subnetID)))

	vmPath := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/cli-guest",
		baseURL, subscriptionID, resourceGroup)
	runCLI(t, azRest("PUT", vmPath+"?api-version=2022-03-01", fmt.Sprintf(
		`{"location":"eastus","properties":{"hardwareProfile":{"vmSize":"Standard_B1s"},`+
			`"storageProfile":{"osDisk":{"name":"cli-guest-osdisk","createOption":"FromImage","managedDisk":{"storageAccountType":"Standard_LRS"}}},`+
			`"osProfile":{"computerName":"cli-guest","adminUsername":"azureuser","adminPassword":"Str0ng-password-12345!"},`+
			`"diagnosticsProfile":{"bootDiagnostics":{"enabled":true,"storageUri":"https://simbootdiag.blob.core.windows.net/"}},`+
			`"networkProfile":{"networkInterfaces":[{"id":%q,"properties":{"primary":true}}]}}}`, nicID)))

	t.Run("a Custom Script extension runs its command in the guest", func(t *testing.T) {
		const marker = "cli-ran-in-the-guest"
		extURL := vmPath + "/extensions/script?api-version=2022-03-01"
		out := runCLI(t, azRest("PUT", extURL, `{"location":"eastus","properties":{`+
			`"publisher":"Microsoft.Azure.Extensions","type":"CustomScript","typeHandlerVersion":"2.1",`+
			`"settings":{"commandToExecute":"echo `+marker+`"}}}`))
		if !strings.Contains(out, marker) {
			t.Fatalf("the extension's status does not carry the command's output, so nothing ran: %s", out)
		}
		if !strings.Contains(out, "Succeeded") {
			t.Fatalf("expected the extension to provision: %s", out)
		}

		if list := runCLI(t, azRest("GET", vmPath+"/extensions?api-version=2022-03-01", "")); !strings.Contains(list, "script") {
			t.Fatalf("the extension is missing from the list: %s", list)
		}
		runCLI(t, azRest("DELETE", extURL, ""))
	})

	t.Run("assessPatches reads the guest's own package manager", func(t *testing.T) {
		out := runCLI(t, azRest("POST", vmPath+"/assessPatches?api-version=2022-03-01", ""))
		if !strings.Contains(out, "Succeeded") {
			t.Fatalf("expected a completed assessment: %s", out)
		}
		if !strings.Contains(out, "availablePatches") {
			t.Fatalf("the assessment carries no package list: %s", out)
		}
	})

	runCLI(t, azRest("DELETE", vmPath+"?api-version=2022-03-01", ""))
}
