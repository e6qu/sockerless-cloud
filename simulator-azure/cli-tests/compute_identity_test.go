package azure_cli_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliVMIdentity is the VirtualMachineIdentity `az vm identity show` prints.
type cliVMIdentity struct {
	Type                   string `json:"type"`
	PrincipalID            string `json:"principalId"`
	TenantID               string `json:"tenantId"`
	UserAssignedIdentities map[string]struct {
		ClientID    string `json:"clientId"`
		PrincipalID string `json:"principalId"`
	} `json:"userAssignedIdentities"`
}

// A machine's managed identity through the native CLI: `az vm identity show`
// reads the identity the machine was created with, `az vm identity assign`
// attaches a user-assigned identity beside it, and `az vm identity remove`
// takes each away again.
func TestComputeVirtualMachineIdentityCLI(t *testing.T) {
	t.Parallel()
	env := startAzTLSSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-vm-identity",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-vm-identity"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, env.command("logout")) })

	rg, vmName := "cli-vm-identity-rg", "cli-vm-identity"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	rest := func(method, url, body string, extra ...string) *exec.Cmd {
		args := []string{"rest", "--method", method, "--url", url, "-o", "json"}
		if body != "" {
			args = append(args, "--body", body)
		}
		return env.command(append(args, extra...)...)
	}
	arm := func(resourcePath, apiVersion string) string {
		return fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/%s?api-version=%s",
			env.baseURL, subscriptionID, rg, resourcePath, apiVersion)
	}
	runCLI(t, rest("PUT", arm("Microsoft.Network/virtualNetworks/cli-vm-identity-vnet", "2024-05-01"),
		`{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.97.0.0/16"]}}}`))
	subnetID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/cli-vm-identity-vnet/subnets/default", subscriptionID, rg)
	runCLI(t, rest("PUT", arm("Microsoft.Network/virtualNetworks/cli-vm-identity-vnet/subnets/default", "2024-05-01"),
		`{"properties":{"addressPrefix":"10.97.1.0/24"}}`))
	nicID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkInterfaces/cli-vm-identity-nic", subscriptionID, rg)
	runCLI(t, rest("PUT", arm("Microsoft.Network/networkInterfaces/cli-vm-identity-nic", "2024-05-01"), fmt.Sprintf(
		`{"location":"eastus","properties":{"ipConfigurations":[{"name":"ipconfig1","properties":{"subnet":{"id":%q},"privateIPAllocationMethod":"Dynamic","privateIPAddressVersion":"IPv4","primary":true}}]}}`, subnetID)))

	vmURL := arm("Microsoft.Compute/virtualMachines/"+vmName, "2024-07-01")
	azLongRunning(t, rest, "PUT", vmURL, fmt.Sprintf(`{"location":"eastus","identity":{"type":"SystemAssigned"},"properties":{"hardwareProfile":{"vmSize":"Standard_B1s"},"storageProfile":{"imageReference":{"publisher":"Debian","offer":"debian-12","sku":"12","version":"latest"},"osDisk":{"name":"%s-osdisk","createOption":"FromImage","caching":"ReadWrite","deleteOption":"Delete"}},"osProfile":{"computerName":%q,"adminUsername":"azureuser","adminPassword":"Str0ng-password-12345!"},"networkProfile":{"networkInterfaces":[{"id":%q,"properties":{"primary":true}}]}}}`,
		vmName, vmName, nicID))
	t.Cleanup(func() { azLongRunning(t, rest, "DELETE", vmURL, "") })

	show := func() cliVMIdentity {
		t.Helper()
		var shown cliVMIdentity
		parseJSON(t, runCLI(t, env.command("vm", "identity", "show", "-g", rg, "-n", vmName, "-o", "json")), &shown)
		return shown
	}
	shown := show()
	assert.Equal(t, "SystemAssigned", shown.Type)
	require.NotEmpty(t, shown.PrincipalID, "the system-assigned identity has a principal")
	assert.Equal(t, azLoginTenantID, shown.TenantID)
	systemPrincipal := shown.PrincipalID

	var uai struct {
		ID          string `json:"id"`
		ClientID    string `json:"clientId"`
		PrincipalID string `json:"principalId"`
	}
	parseJSON(t, runCLI(t, env.command("identity", "create", "-g", rg, "-n", "cli-vm-identity-uai", "-o", "json")), &uai)
	require.NotEmpty(t, uai.ID)

	var assigned struct {
		SystemAssignedIdentity string `json:"systemAssignedIdentity"`
		UserAssignedIdentities map[string]struct {
			PrincipalID string `json:"principalId"`
		} `json:"userAssignedIdentities"`
	}
	parseJSON(t, runCLI(t, env.command("vm", "identity", "assign", "-g", rg, "-n", vmName, "--identities", uai.ID, "-o", "json")), &assigned)
	assert.Equal(t, systemPrincipal, assigned.SystemAssignedIdentity, "assigning a user-assigned identity keeps the system-assigned one")
	require.Contains(t, assigned.UserAssignedIdentities, uai.ID)
	assert.Equal(t, uai.PrincipalID, assigned.UserAssignedIdentities[uai.ID].PrincipalID)

	shown = show()
	assert.Equal(t, "SystemAssigned, UserAssigned", shown.Type)
	assert.Equal(t, systemPrincipal, shown.PrincipalID)
	require.Contains(t, shown.UserAssignedIdentities, uai.ID)
	assert.Equal(t, uai.ClientID, shown.UserAssignedIdentities[uai.ID].ClientID)

	runCLI(t, env.command("vm", "identity", "remove", "-g", rg, "-n", vmName, "--identities", uai.ID, "-o", "json"))
	shown = show()
	assert.Equal(t, "SystemAssigned", shown.Type)
	assert.Equal(t, systemPrincipal, shown.PrincipalID, "removing the user-assigned identity keeps the system-assigned principal")
	assert.Empty(t, shown.UserAssignedIdentities)

	runCLI(t, env.command("vm", "identity", "remove", "-g", rg, "-n", vmName, "-o", "json"))
	assert.Empty(t, strings.TrimSpace(runCLI(t, env.command("vm", "identity", "show", "-g", rg, "-n", vmName, "-o", "json"))),
		"removing the system-assigned identity leaves the machine with none, which az vm identity show prints as nothing")
}
