package azure_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for Resources_List. `az resource list` reaches every scoping it
// offers through one route — the subscription-wide
// `GET /subscriptions/{subscriptionId}/resources` — by translating its options
// into an OData `$filter`: `-g` becomes `resourceGroup eq '<name>'`,
// `--resource-type` becomes `resourceType eq '<ns>/<type>'`, `--name` becomes
// `name eq '<name>'`, and `--location` becomes `location eq '<region>'`. A
// simulator that ignored the filter would answer every scoping with the whole
// subscription, which reads as a result rather than as the wrong answer it is.

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

// TestResourceListCLI proves `az resource list -g <rg>` answers with that
// group's resources only, and with every provider's resources in it.
func TestResourceListCLI(t *testing.T) {
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

	// --resource-type composes with -g, as the CLI's filter builder composes
	// the two clauses with `and`.
	var typed []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list", "-g", mine,
		"--resource-type", "Microsoft.Network/virtualNetworks", "-o", "json")), &typed)
	require.Len(t, typed, 1)
	assert.Equal(t, "cli-reslist-vnet", typed[0].Name)

	// --name selects one resource across the subscription.
	var named []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list",
		"--name", "clireslistother", "-o", "json")), &named)
	require.Len(t, named, 1)
	assert.Contains(t, strings.ToLower(named[0].ID), strings.ToLower("/resourcegroups/"+theirs+"/"))

	// The unscoped subscription list holds both groups.
	var all []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list", "-o", "json")), &all)
	groups := map[string]bool{}
	for _, row := range all {
		groups[strings.ToLower(row.ResourceGroup)] = true
	}
	assert.True(t, groups[mine])
	assert.True(t, groups[theirs])

	// --location narrows by region; a region nothing sits in is empty.
	var elsewhere []azResourceListRow
	parseJSON(t, runCLI(t, env.command("resource", "list",
		"--location", "antarctica-south", "-o", "json")), &elsewhere)
	assert.Empty(t, elsewhere)
}
