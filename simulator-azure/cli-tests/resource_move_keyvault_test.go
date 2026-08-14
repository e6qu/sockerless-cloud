package azure_cli_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the Microsoft.KeyVault cross-resource-group move: the
// native `az resource move` re-homes a vault, `az keyvault show` / `az
// keyvault list` report it under the destination group with an unchanged vault
// URI, and the secret written before the move reads back afterwards through
// the vault's data plane — which is addressed by the vault's globally unique
// name, a coordinate the move never touches. Routes exercised:
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.KeyVault/vaults/{name}
//	GET+PUT https://{vault}.vault.{suffix}/secrets/{name}

// kvMoveDataPlane dispatches a Key Vault data-plane call at the TLS
// simulator. The vault is selected by the `Host` header — the same
// `<vault>.vault.<suffix>` coordinate a client reaches on Azure — while the
// request is addressed to the simulator's own certificate name, because
// `*.vault.localhost` resolves nowhere on macOS and the certificate names only
// the simulator's host.
func kvMoveDataPlane(env azLoginEnv, vault, method, path, body string) *exec.Cmd {
	hostPort := strings.TrimPrefix(env.baseURL, "https://")
	args := []string{
		"rest", "--method", method,
		"--url", env.baseURL + path + "?api-version=7.4",
		"--headers", "Host=" + vault + ".vault." + hostPort,
		"--output", "json",
	}
	if body != "" {
		args = append(args, "--body", body)
	}
	return env.command(args...)
}

// kvMoveVaultListed reports whether `az keyvault list -g <rg>` — the
// resource-group-scoped Vaults_ListByResourceGroup read — enumerates the
// vault.
func kvMoveVaultListed(t *testing.T, env azLoginEnv, rg, vault string) bool {
	t.Helper()
	var listed []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command("keyvault", "list", "-g", rg, "-o", "json")), &listed)
	for _, v := range listed {
		if v.Name == vault {
			return true
		}
	}
	return false
}

// TestResourceMoveKeyVaultCLI moves a key vault between resource groups with
// `az resource move` and proves the move kept the vault coherent on both
// planes: `az keyvault show` answers under the destination group only and
// reports the same vault URI, `az resource list` sees the vault in its new
// group, the secret written before the move still reads back, and a type real
// Azure Resource Manager refuses to move is still refused.
func TestResourceMoveKeyVaultCLI(t *testing.T) {
	env := startAzLoginSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-kv-move",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-kv-move"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	srcRG, dstRG, vault := "cli-move-kv-src-rg", "cli-move-kv-dst-rg", "climovekv"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("keyvault", "create", "-n", vault, "-g", srcRG, "-l", "eastus",
		"--sku", "standard", "--enable-rbac-authorization", "true", "--no-self-perms", "-o", "json"))

	var before struct {
		ID         string `json:"id"`
		Properties struct {
			VaultURI string `json:"vaultUri"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, env.command("keyvault", "show", "-g", srcRG, "-n", vault, "-o", "json")), &before)
	require.NotEmpty(t, before.Properties.VaultURI)

	// Real secret material written through the vault's own data plane before
	// the move.
	const secretValue = "cli material written before the move"
	runCLI(t, kvMoveDataPlane(env, vault, "PUT", "/secrets/cli-move-secret",
		fmt.Sprintf(`{"value":%q}`, secretValue)))

	// The move itself, by resource ID.
	vaultID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s",
		subscriptionID, srcRG, vault)
	runCLI(t, env.command("resource", "move", "--ids", vaultID, "--destination-group", dstRG))

	// The ARM record relocated, and the vault URI clients hold is unchanged.
	var moved struct {
		ID         string `json:"id"`
		Properties struct {
			VaultURI string `json:"vaultUri"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, env.command("keyvault", "show", "-g", dstRG, "-n", vault, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/", "the vault moved into the destination group")
	assert.Equal(t, before.Properties.VaultURI, moved.Properties.VaultURI,
		"a cross-group move must not change the vault URI")
	failure := runStorageCLIExpectFailure(t, env.command("keyvault", "show", "-g", srcRG, "-n", vault, "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound",
		"the moved vault must no longer resolve under the source group")

	// The per-group listing agrees: the vault is enumerated under the
	// destination group and no longer under the source group.
	assert.True(t, kvMoveVaultListed(t, env, dstRG, vault),
		"az keyvault list must show the vault in the destination group")
	assert.False(t, kvMoveVaultListed(t, env, srcRG, vault),
		"az keyvault list must no longer show the vault in the source group")

	// The data plane still serves the secret, at the same vault coordinates as
	// before the move.
	var secret struct {
		Value string `json:"value"`
	}
	parseJSON(t, runCLI(t, kvMoveDataPlane(env, vault, "GET", "/secrets/cli-move-secret", "")), &secret)
	assert.Equal(t, secretValue, secret.Value, "secret material must survive the vault move")

	// A type Azure Resource Manager does not move is still refused.
	aciID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerInstance/containerGroups/cli-move-never-movable",
		subscriptionID, srcRG)
	refused := runStorageCLIExpectFailure(t, env.command("resource", "move",
		"--ids", aciID, "--destination-group", dstRG))
	assert.Contains(t, refused, "ResourceMoveNotSupported",
		"an unmovable type must still be refused after the Key Vault hook landed")
}
