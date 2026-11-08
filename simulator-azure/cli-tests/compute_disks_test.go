package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliDisk is the Disk `az disk` prints.
type cliDisk struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	ManagedBy         string `json:"managedBy"`
	DiskSizeGB        int    `json:"diskSizeGB"`
	DiskState         string `json:"diskState"`
	ProvisioningState string `json:"provisioningState"`
	Sku               struct {
		Name string `json:"name"`
	} `json:"sku"`
}

// Managed disks through the native CLI: `az disk create/update/show/list/
// delete`, and `az vm disk attach/detach` both through the machine's PUT (a
// disk named with --name) and through attachDetachDataDisks (--disk-ids). What
// the guest writes to a disk comes back with it on the next attach.
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/disks/{diskName}
//	PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/disks/{diskName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/disks/{diskName}
//	DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/disks/{diskName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/disks
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/disks
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/virtualMachines/{vmName}/attachDetachDataDisks
func TestComputeManagedDisksCLI(t *testing.T) {
	t.Parallel()
	env := startAzTLSSimulator(t)
	tagCLILogin(t, env, "sockerless-managed-disks")

	const rg, vnet, nic, vm, disk = "cli-disks-rg", "cli-disks-vnet", "cli-disks-nic", "cli-disks-vm", "cli-disk-a"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	show := func() cliDisk {
		t.Helper()
		var shown cliDisk
		parseJSON(t, runCLI(t, env.command("disk", "show", "-g", rg, "-n", disk, "-o", "json")), &shown)
		return shown
	}

	var created cliDisk
	rawCreated := runCLI(t, env.command("disk", "create", "-g", rg, "-n", disk, "-l", "eastus",
		"--size-gb", "4", "--sku", "Standard_LRS", "-o", "json"))
	parseJSON(t, rawCreated, &created)
	assert.Equal(t, "Succeeded", created.ProvisioningState)
	assert.Equal(t, "Unattached", created.DiskState)
	assert.Equal(t, "Standard_LRS", created.Sku.Name)

	var grown cliDisk
	parseJSON(t, runCLI(t, env.command("disk", "update", "-g", rg, "-n", disk, "--size-gb", "8", "-o", "json")), &grown)
	assert.Equal(t, 8, grown.DiskSizeGB)
	assert.Equal(t, 8, show().DiskSizeGB)

	var listed []cliDisk
	parseJSON(t, runCLI(t, env.command("disk", "list", "-g", rg, "-o", "json")), &listed)
	require.Len(t, listed, 1)
	assert.Equal(t, disk, listed[0].Name)
	// `az disk list` asks for a resource group, so read the subscription's list
	// through az rest.
	var subscriptionWide struct {
		Value []cliDisk `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("rest", "--method", "GET", "--url",
		fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Compute/disks?api-version=2023-04-02", env.baseURL, subscriptionID), "-o", "json")), &subscriptionWide)
	require.Len(t, subscriptionWide.Value, 1, "the subscription holds this simulator's one disk")
	assert.Equal(t, created.ID, subscriptionWide.Value[0].ID)

	runCLI(t, env.command("network", "vnet", "create", "-g", rg, "-n", vnet, "-l", "eastus",
		"--address-prefixes", "10.120.0.0/16", "--subnet-name", "default", "--subnet-prefixes", "10.120.1.0/24", "-o", "json"))
	runCLI(t, env.command("network", "nic", "create", "-g", rg, "-n", nic, "-l", "eastus",
		"--vnet-name", vnet, "--subnet", "default", "-o", "json"))
	// `az vm create` deploys an ARM template through
	// Microsoft.Resources/deployments, which the simulator does not serve, so write
	// the machine as the template would.
	nicID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/%s", subscriptionID, rg, nic)
	vmURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s?api-version=2024-07-01",
		env.baseURL, subscriptionID, rg, vm)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", vmURL, "--body", fmt.Sprintf(`{
		"location": "eastus",
		"properties": {
			"hardwareProfile": {"vmSize": "Standard_B1s"},
			"storageProfile": {
				"imageReference": {"publisher": "Debian", "offer": "debian-12", "sku": "12", "version": "latest"},
				"osDisk": {"name": "%s-osdisk", "createOption": "FromImage", "managedDisk": {"storageAccountType": "Standard_LRS"}}
			},
			"osProfile": {"computerName": "%s", "adminUsername": "azureuser", "adminPassword": "Sockerless!Pass1"},
			"networkProfile": {"networkInterfaces": [{"id": %q}]}
		}
	}`, vm, vm, nicID), "-o", "json"))
	t.Cleanup(func() { _, _, _ = runCLIStreamsResult(env.command("rest", "--method", "DELETE", "--url", vmURL)) })
	runCLI(t, env.command("vm", "wait", "-g", rg, "-n", vm, "--created", "--timeout", "300"))

	inGuest := func(command string) string {
		t.Helper()
		var ran struct {
			InstanceView struct {
				Statuses []struct {
					Message string `json:"message"`
				} `json:"statuses"`
			} `json:"instanceView"`
		}
		parseJSON(t, runCLI(t, env.command("vm", "extension", "set", "-g", rg, "--vm-name", vm, "-n", "CustomScript",
			"--publisher", "Microsoft.Azure.Extensions", "--version", "2.1",
			"--settings", fmt.Sprintf(`{"commandToExecute": %q}`, command), "-o", "json")), &ran)
		var messages []string
		for _, status := range ran.InstanceView.Statuses {
			messages = append(messages, status.Message)
		}
		return strings.Join(messages, "\n")
	}

	runCLI(t, env.command("vm", "disk", "attach", "-g", rg, "--vm-name", vm, "--name", disk, "--lun", "0", "-o", "json"))
	attached := show()
	assert.Equal(t, "Attached", attached.DiskState)
	assert.True(t, strings.HasSuffix(strings.ToLower(attached.ManagedBy), "/virtualmachines/"+vm), "managedBy names the machine; got %q", attached.ManagedBy)

	_, stderr, err := runCLIStreamsResult(env.command("disk", "delete", "-g", rg, "-n", disk, "--yes"))
	require.Error(t, err, "an attached disk cannot be deleted")
	assert.Contains(t, stderr, "OperationNotAllowed")

	inGuest("echo written-by-cli > /dev/disk/azure/scsi1/lun0/proof")
	runCLI(t, env.command("vm", "disk", "detach", "-g", rg, "--vm-name", vm, "--name", disk, "-o", "json"))
	assert.Equal(t, "Unattached", show().DiskState)
	assert.Contains(t, inGuest("test -e /dev/disk/azure/scsi1/lun0 || echo lun0-detached"), "lun0-detached")

	runCLI(t, env.command("vm", "disk", "attach", "-g", rg, "--vm-name", vm, "--disk-ids", attached.ID, "--lun", "2", "-o", "json"))
	assert.Equal(t, "Attached", show().DiskState)
	assert.Contains(t, inGuest("cat /dev/disk/azure/scsi1/lun2/proof"), "written-by-cli",
		"the disk came back without what the guest wrote to it")

	runCLI(t, env.command("vm", "disk", "detach", "-g", rg, "--vm-name", vm, "--disk-ids", attached.ID, "-o", "json"))
	assert.Equal(t, "Unattached", show().DiskState)

	runCLI(t, env.command("disk", "delete", "-g", rg, "-n", disk, "--yes"))
	parseJSON(t, runCLI(t, env.command("disk", "list", "-g", rg, "-o", "json")), &listed)
	assert.Empty(t, listed)
}
