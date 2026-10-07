package azure_cli_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for how the Key Vault data plane authorizes a request, driven
// by the permission and network settings the Azure CLI writes:
//
//	az keyvault create (with and without --enable-rbac-authorization)
//	az keyvault set-policy
//	az keyvault update --default-action / --public-network-access
//	az keyvault network-rule add
//	az role assignment create
//	GET+PUT https://{vault}.vault.{suffix}/secrets/{name}

var kvClientAddress = regexp.MustCompile(`Client address: ([0-9a-fA-F:.]+)`)

func TestKeyVaultCLI_DataPlaneAuthorization(t *testing.T) {
	env := startAzLoginSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-kv-auth",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-kv-auth"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	rg, vault := "cli-kv-auth-rg", "clikvauthpolicy"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))

	// An access-policy vault: the CLI grants its creator a policy.
	runCLI(t, env.command("keyvault", "create", "-n", vault, "-g", rg, "-l", "eastus",
		"--sku", "standard", "--enable-rbac-authorization", "false", "-o", "json"))
	runCLI(t, kvMoveDataPlane(env, vault, "PUT", "/secrets/cli-auth", `{"value":"v1"}`))

	failure := runStorageCLIExpectFailure(t, env.command("rest", "--method", "GET",
		"--url", env.baseURL+"/secrets/cli-auth?api-version=7.4",
		"--headers", "Host="+vault+".vault."+strings.TrimPrefix(env.baseURL, "https://")))
	assert.Contains(t, failure, "Unauthorized", "a Resource Manager token is not a Key Vault token")
	assert.Contains(t, failure, "AKV10022")

	var sp struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("ad", "sp", "show", "--id", "test-client-id", "-o", "json")), &sp)
	runCLI(t, env.command("keyvault", "set-policy", "-n", vault, "-g", rg,
		"--object-id", sp.ID, "--secret-permissions", "get", "-o", "json"))
	failure = runStorageCLIExpectFailure(t, kvMoveDataPlane(env, vault, "PUT", "/secrets/cli-auth", `{"value":"v2"}`))
	assert.Contains(t, failure, "Forbidden")
	assert.Contains(t, failure, "AccessDenied")
	assert.Contains(t, failure, "does not have secrets set permission")
	var secret struct {
		Value string `json:"value"`
	}
	parseJSON(t, runCLI(t, kvMoveDataPlane(env, vault, "GET", "/secrets/cli-auth", "")), &secret)
	assert.Equal(t, "v1", secret.Value)

	// The firewall refuses an address no rule names, and names the address it
	// saw, which is the address an operator adds.
	runCLI(t, env.command("keyvault", "update", "-n", vault, "-g", rg, "--default-action", "Deny", "-o", "json"))
	failure = runStorageCLIExpectFailure(t, kvMoveDataPlane(env, vault, "GET", "/secrets/cli-auth", ""))
	assert.Contains(t, failure, "ForbiddenByFirewall")
	match := kvClientAddress.FindStringSubmatch(failure)
	require.Len(t, match, 2, "the refusal names the client address: %s", failure)
	runCLI(t, env.command("keyvault", "network-rule", "add", "-n", vault, "-g", rg, "--ip-address", match[1], "-o", "json"))
	parseJSON(t, runCLI(t, kvMoveDataPlane(env, vault, "GET", "/secrets/cli-auth", "")), &secret)
	assert.Equal(t, "v1", secret.Value)

	runCLI(t, env.command("keyvault", "update", "-n", vault, "-g", rg, "--public-network-access", "Disabled", "-o", "json"))
	failure = runStorageCLIExpectFailure(t, kvMoveDataPlane(env, vault, "GET", "/secrets/cli-auth", ""))
	assert.Contains(t, failure, "ForbiddenByConnection")

	// An Azure RBAC vault grants nothing until a role assignment does.
	rbacVault := "clikvauthrbac"
	var created struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("keyvault", "create", "-n", rbacVault, "-g", rg, "-l", "eastus",
		"--sku", "standard", "--enable-rbac-authorization", "true", "--no-self-perms", "-o", "json")), &created)
	failure = runStorageCLIExpectFailure(t, kvMoveDataPlane(env, rbacVault, "PUT", "/secrets/cli-auth", `{"value":"v1"}`))
	assert.Contains(t, failure, "ForbiddenByRbac")
	assert.Contains(t, failure, "Microsoft.KeyVault/vaults/secrets/setSecret/action")
	kvGrantCaller(t, env.command, "Key Vault Secrets Officer", created.ID)
	runCLI(t, kvMoveDataPlane(env, rbacVault, "PUT", "/secrets/cli-auth", `{"value":"v1"}`))
}
