package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliNICIPConfig struct {
	Name             string `json:"name"`
	Primary          bool   `json:"primary"`
	PrivateIPAddress string `json:"privateIPAddress"`
}

func cliNICAddresses(t *testing.T, env azLoginEnv, rg, nic string) map[string]string {
	t.Helper()
	var shown struct {
		IPConfigurations []cliNICIPConfig `json:"ipConfigurations"`
	}
	parseJSON(t, runCLI(t, env.command("network", "nic", "show", "-g", rg, "-n", nic, "-o", "json")), &shown)
	out := map[string]string{}
	for _, ipcfg := range shown.IPConfigurations {
		out[ipcfg.Name] = ipcfg.PrivateIPAddress
	}
	return out
}

// TestNetwork_NICSecondaryIPConfigurationsCLI adds secondary IP configurations
// to a network interface with `az network nic ip-config create`, which reads
// the interface and writes it back whole, and checks that each configuration
// holds an address of its own and that deleting one returns its address to
// the subnet.
func TestNetwork_NICSecondaryIPConfigurationsCLI(t *testing.T) {
	requireNetworkHost(t)
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-nic-ipconfig")

	const rg, vnet, subnet, nic = "cli-ipcfg-rg", "cli-ipcfg-vnet", "cli-ipcfg-subnet", "cli-ipcfg-nic"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("network", "vnet", "create", "-g", rg, "-n", vnet, "-l", "eastus",
		"--address-prefix", "10.93.0.0/16", "--subnet-name", subnet, "--subnet-prefix", "10.93.1.0/24", "-o", "json"))
	runCLI(t, env.command("network", "nic", "create", "-g", rg, "-n", nic, "-l", "eastus",
		"--vnet-name", vnet, "--subnet", subnet, "-o", "json"))
	runCLI(t, env.command("network", "nic", "ip-config", "create", "-g", rg, "--nic-name", nic,
		"-n", "secondary", "-o", "json"))
	runCLI(t, env.command("network", "nic", "ip-config", "create", "-g", rg, "--nic-name", nic,
		"-n", "pinned", "--private-ip-address", "10.93.1.30", "-o", "json"))

	addresses := cliNICAddresses(t, env, rg, nic)
	require.Len(t, addresses, 3)
	assert.Equal(t, "10.93.1.4", addresses["ipconfig1"], "the primary configuration keeps the first tenant address")
	assert.Equal(t, "10.93.1.5", addresses["secondary"], "a dynamic secondary takes the next free address")
	assert.Equal(t, "10.93.1.30", addresses["pinned"], "a static secondary holds the address it asked for")

	var primary cliNICIPConfig
	parseJSON(t, runCLI(t, env.command("network", "nic", "ip-config", "show", "-g", rg, "--nic-name", nic,
		"-n", "ipconfig1", "-o", "json")), &primary)
	assert.True(t, primary.Primary, "the interface's first configuration is its primary")

	runCLI(t, env.command("network", "nic", "ip-config", "delete", "-g", rg, "--nic-name", nic, "-n", "secondary"))
	runCLI(t, env.command("network", "nic", "ip-config", "create", "-g", rg, "--nic-name", nic,
		"-n", "replacement", "-o", "json"))
	addresses = cliNICAddresses(t, env, rg, nic)
	assert.Equal(t, map[string]string{"ipconfig1": "10.93.1.4", "pinned": "10.93.1.30", "replacement": "10.93.1.5"}, addresses,
		"deleting a configuration returns its address to the subnet")

	runCLI(t, env.command("network", "nic", "delete", "-g", rg, "-n", nic))
}
