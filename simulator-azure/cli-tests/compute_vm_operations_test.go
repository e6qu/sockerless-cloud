package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

// The virtual-machine operations beyond the lifecycle, driven through the real
// az CLI. The machine is created first because every one of these operates on a
// machine that exists and, for the guest-moving operations, one that is really
// running.
func TestComputeVirtualMachineOperationsCLI(t *testing.T) {
	requireNetworkHost(t)

	vnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-vmops-vnet", "2024-05-01")
	runCLI(t, azRest("PUT", vnetURL, `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.95.0.0/16"]}}}`))

	subnetURL := armURL("Microsoft.Network", "virtualNetworks/cli-vmops-vnet/subnets/cli-vmops-subnet", "2024-05-01")
	runCLI(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.95.1.0/24"}}`))

	nicID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/cli-vmops-nic",
		subscriptionID, resourceGroup)
	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/cli-vmops-vnet/subnets/cli-vmops-subnet",
		subscriptionID, resourceGroup)
	nicURL := armURL("Microsoft.Network", "networkInterfaces/cli-vmops-nic", "2024-05-01")
	runCLI(t, azRest("PUT", nicURL, fmt.Sprintf(
		`{"location":"eastus","properties":{"ipConfigurations":[{"name":"ipconfig1","properties":{"subnet":{"id":%q},"privateIPAllocationMethod":"Dynamic","privateIPAddressVersion":"IPv4","primary":true}}]}}`,
		subnetID)))

	vmPath := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/cli-vmops",
		baseURL, subscriptionID, resourceGroup)
	vmURL := vmPath + "?api-version=2022-03-01"
	runCLI(t, azRest("PUT", vmURL, fmt.Sprintf(
		`{"location":"eastus","properties":{"hardwareProfile":{"vmSize":"Standard_B1s"},`+
			`"storageProfile":{"osDisk":{"name":"cli-vmops-osdisk","createOption":"FromImage","managedDisk":{"storageAccountType":"Standard_LRS"}}},`+
			`"osProfile":{"computerName":"cli-vmops","adminUsername":"azureuser","adminPassword":"Str0ng-password-12345!"},`+
			`"diagnosticsProfile":{"bootDiagnostics":{"enabled":true,"storageUri":"https://simbootdiag.blob.core.windows.net/"}},`+
			`"networkProfile":{"networkInterfaces":[{"id":%q,"properties":{"primary":true}}]}}}`, nicID)))

	t.Run("ListAvailableSizes offers the sizes the machine can be resized to", func(t *testing.T) {
		out := runCLI(t, azRest("GET", vmPath+"/vmSizes?api-version=2022-03-01", ""))
		if !strings.Contains(out, "Standard_B1s") {
			t.Fatalf("expected the resize catalogue, got %s", out)
		}
	})

	t.Run("ListByLocation finds the machine in its location", func(t *testing.T) {
		// Built from the ARM route this exercises, so the request cannot drift
		// from the route it is meant to cover.
		const route = "/subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/virtualMachines"
		path := strings.NewReplacer(
			"{subscriptionId}", subscriptionID,
			"{location}", "eastus",
		).Replace(route)
		out := runCLI(t, azRest("GET", baseURL+path+"?api-version=2022-03-01", ""))
		if !strings.Contains(out, "cli-vmops") {
			t.Fatalf("expected the machine in the eastus listing, got %s", out)
		}
	})

	t.Run("RetrieveBootDiagnosticsData returns the guest's own console log", func(t *testing.T) {
		out := runCLI(t, azRest("POST", vmPath+"/retrieveBootDiagnosticsData?api-version=2022-03-01", ""))
		if !strings.Contains(out, "serialConsoleLogBlobUri") {
			t.Fatalf("expected a serial console log URI, got %s", out)
		}
		if !strings.Contains(out, "simbootdiag") {
			t.Fatalf("the log was not stored in the account the machine names, got %s", out)
		}
	})

	t.Run("Redeploy leaves the machine running", func(t *testing.T) {
		runCLI(t, azRest("POST", vmPath+"/redeploy?api-version=2022-03-01", ""))
		out := runCLI(t, azRest("GET", vmPath+"/instanceView?api-version=2022-03-01", ""))
		if !strings.Contains(out, "PowerState/running") {
			t.Fatalf("the machine is not running after a redeploy, got %s", out)
		}
	})

	t.Run("PerformMaintenance leaves the machine running", func(t *testing.T) {
		runCLI(t, azRest("POST", vmPath+"/performMaintenance?api-version=2022-03-01", ""))
		out := runCLI(t, azRest("GET", vmPath+"/instanceView?api-version=2022-03-01", ""))
		if !strings.Contains(out, "PowerState/running") {
			t.Fatalf("the machine is not running after maintenance, got %s", out)
		}
	})

	t.Run("Generalize is reported once the machine is stopped", func(t *testing.T) {
		runCLI(t, azRest("POST", vmPath+"/powerOff?api-version=2022-03-01", ""))
		runCLI(t, azRest("POST", vmPath+"/generalize?api-version=2022-03-01", ""))
		out := runCLI(t, azRest("GET", vmPath+"/instanceView?api-version=2022-03-01", ""))
		if !strings.Contains(out, "OSState/generalized") {
			t.Fatalf("the machine does not report itself generalized, got %s", out)
		}
	})

	t.Run("ConvertToManagedDisks succeeds on a stopped machine", func(t *testing.T) {
		runCLI(t, azRest("POST", vmPath+"/convertToManagedDisks?api-version=2022-03-01", ""))
	})

	runCLI(t, azRest("DELETE", vmURL, ""))
}
