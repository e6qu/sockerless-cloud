package azure_cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the Microsoft.Storage cross-resource-group move: the
// native `az resource move` re-homes a storage account, and the Blob data
// plane — addressed by the account's globally unique name, which the move
// never changes — serves the bytes uploaded before the move. Routes
// exercised:
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources
//	POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/listKeys

// azMoveStorageBlobCmd builds an `az storage <group> ...` invocation against
// the TLS simulator's path-style blob endpoint, with the account key the ARM
// plane served.
func azMoveStorageBlobCmd(env azLoginEnv, account, accountKey string, args ...string) []string {
	return append(args,
		"--auth-mode", "key",
		"--account-name", account,
		"--account-key", accountKey,
		"--blob-endpoint", env.baseURL+"/"+account+"/",
		"--only-show-errors",
		"--output", "json",
	)
}

// TestResourceMoveStorageAccountCLI moves a storage account between resource
// groups with `az resource move` and proves the move kept the account
// coherent: `az storage account show` answers under the destination group
// only, `az storage account keys list` serves the same key material, and
// `az storage blob download` reads back the bytes uploaded before the move.
func TestResourceMoveStorageAccountCLI(t *testing.T) {
	env := startAzLoginSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-move",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-move"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	srcRG, dstRG, account := "cli-move-storage-src-rg", "cli-move-storage-dst-rg", "climoveacct"
	runCLI(t, env.command("group", "create", "-n", srcRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", dstRG, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("storage", "account", "create", "-n", account, "-g", srcRG,
		"-l", "eastus", "--sku", "Standard_LRS", "-o", "json"))

	var keysBefore []struct {
		KeyName string `json:"keyName"`
		Value   string `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "keys", "list",
		"-g", srcRG, "-n", account, "-o", "json")), &keysBefore)
	require.Len(t, keysBefore, 2)
	accountKey := keysBefore[0].Value
	require.NotEmpty(t, accountKey)

	// Real blob content written before the move, through the CLI's own
	// data-plane commands with the key the ARM plane served.
	const container = "cli-move-container"
	const blobName = "moved.txt"
	const payload = "cli bytes written before the move"
	runCLI(t, env.command(azMoveStorageBlobCmd(env, account, accountKey,
		"storage", "container", "create", "--name", container)...))
	src := filepath.Join(t.TempDir(), blobName)
	require.NoError(t, os.WriteFile(src, []byte(payload), 0o600))
	runCLI(t, env.command(azMoveStorageBlobCmd(env, account, accountKey,
		"storage", "blob", "upload", "--container-name", container, "--name", blobName,
		"--file", src, "--overwrite")...))

	// The move itself, by resource ID.
	acctID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s",
		subscriptionID, srcRG, account)
	runCLI(t, env.command("resource", "move", "--ids", acctID, "--destination-group", dstRG))

	// The ARM record relocated.
	var moved struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "show",
		"-g", dstRG, "-n", account, "-o", "json")), &moved)
	assert.Contains(t, moved.ID, "/resourceGroups/"+dstRG+"/",
		"the account moved into the destination group")
	failure := runStorageCLIExpectFailure(t, env.command("storage", "account", "show",
		"-g", srcRG, "-n", account, "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound",
		"the moved account must no longer resolve under the source group")

	// The keys the account held survive the move.
	var keysAfter []struct {
		KeyName string `json:"keyName"`
		Value   string `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "keys", "list",
		"-g", dstRG, "-n", account, "-o", "json")), &keysAfter)
	require.Len(t, keysAfter, 2)
	assert.Equal(t, keysBefore, keysAfter, "a cross-group move must not rotate the account keys")

	// The data plane still serves the bytes, under the same account
	// coordinates as before the move.
	dst := filepath.Join(t.TempDir(), "downloaded.txt")
	runCLI(t, env.command(azMoveStorageBlobCmd(env, account, accountKey,
		"storage", "blob", "download", "--container-name", container, "--name", blobName,
		"--file", dst)...))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, payload, string(got), "blob bytes must survive the account move")
}
