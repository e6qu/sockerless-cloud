package azure_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `az resource list` reaches every scoping through the subscription-wide
// Resources_List, translating -g, --resource-type, --name and --location into
// an OData $filter. A simulator that ignored the filter would answer every
// scoping with the whole subscription.

type azResourceListRow struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	Location      string            `json:"location"`
	ResourceGroup string            `json:"resourceGroup"`
	Tags          map[string]string `json:"tags"`
}

func azResourceListTypes(rows []azResourceListRow) map[string]int {
	types := map[string]int{}
	for _, row := range rows {
		types[strings.ToLower(row.Type)]++
	}
	return types
}

// TestResourceListCLI: `az resource list -g <rg>` answers with that group's
// resources only, from every provider.
func TestResourceListCLI(t *testing.T) {
	t.Parallel()
	env := startAzLoginSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-reslist",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-reslist"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	const mine, theirs = "cli-reslist-rg", "cli-reslist-other-rg"
	runCLI(t, env.command("group", "create", "-n", mine, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", theirs, "-l", "eastus", "-o", "json"))

	// Two providers in the group under test, one in the group next door.
	runCLI(t, env.command("storage", "account", "create", "-n", "clireslistacct", "-g", mine,
		"-l", "eastus", "--sku", "Standard_LRS", "-o", "json"))
	runCLI(t, env.command("network", "vnet", "create", "-n", "cli-reslist-vnet", "-g", mine,
		"-l", "eastus", "--address-prefix", "10.92.0.0/16", "-o", "json"))
	runCLI(t, env.command("storage", "account", "create", "-n", "clireslistother", "-g", theirs,
		"-l", "eastus", "--sku", "Standard_LRS", "-o", "json"))

	var scoped []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list", "-g", mine, "-o", "json")), &scoped)
	require.NotEmpty(t, scoped)
	for _, row := range scoped {
		assert.Contains(t, strings.ToLower(row.ID), strings.ToLower("/resourcegroups/"+mine+"/"),
			"az resource list -g %s must not report resources of another group", mine)
	}
	types := azResourceListTypes(scoped)
	assert.Contains(t, types, "microsoft.storage/storageaccounts")
	assert.Contains(t, types, "microsoft.network/virtualnetworks")
	assert.GreaterOrEqual(t, len(types), 2,
		"the group's list carries every provider's resources, not one provider's")

	// The CLI composes --resource-type and -g with `and`.
	var typed []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list", "-g", mine,
		"--resource-type", "Microsoft.Network/virtualNetworks", "-o", "json")), &typed)
	require.Len(t, typed, 1)
	assert.Equal(t, "cli-reslist-vnet", typed[0].Name)

	var named []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list",
		"--name", "clireslistother", "-o", "json")), &named)
	require.Len(t, named, 1)
	assert.Contains(t, strings.ToLower(named[0].ID), strings.ToLower("/resourcegroups/"+theirs+"/"))

	var all []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list", "-o", "json")), &all)
	groups := map[string]bool{}
	for _, row := range all {
		groups[strings.ToLower(row.ResourceGroup)] = true
	}
	assert.True(t, groups[mine])
	assert.True(t, groups[theirs])

	// A region nothing sits in lists nothing.
	var elsewhere []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list",
		"--location", "antarctica-south", "-o", "json")), &elsewhere)
	assert.Empty(t, elsewhere)
}
