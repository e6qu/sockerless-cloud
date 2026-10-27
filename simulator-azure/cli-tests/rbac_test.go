package azure_cli_test

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for Azure RBAC as Azure Resource Manager and the Blob service
// evaluate it, driven by the commands an operator grants access with:
//
//	az ad sp create-for-rbac --role Reader --scopes <group>
//	az role assignment create
//	az role definition create
//	az storage account update --allow-shared-key-access false
//	az storage blob download / upload --auth-mode login
//	az storage blob generate-sas --as-user

// azRBACCloud registers the TLS simulator as a cloud in env's configuration
// directory, selects it and signs in as the service principal named.
func azRBACCloud(t *testing.T, env azLoginEnv, appID, secret string) {
	t.Helper()
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-rbac",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-rbac"))
	// The joined form keeps a secret that starts with '-' from reading as a flag.
	runCLI(t, env.command("login", "--service-principal", "-u", appID, "--password="+secret,
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
}

// The CLI reports Storage's AuthorizationPermissionMismatch and
// AuthenticationFailed in its own words.
const (
	azStoragePermissionRefusal     = "You do not have the required permissions needed to perform this operation."
	azStorageAuthenticationRefusal = "Authentication failure."
)

// cliRBACGUID names a new role assignment, which Azure requires to be a GUID.
func cliRBACGUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type azRBACPrincipal struct {
	AppID    string `json:"appId"`
	Password string `json:"password"`
}

// azRBACSignIn creates a service principal with `az ad sp create-for-rbac`
// as the signed-in operator and returns a second CLI configuration signed in
// as it.
func azRBACSignIn(t *testing.T, operator azLoginEnv, name string, extra ...string) (azRBACPrincipal, azLoginEnv) {
	t.Helper()
	var principal azRBACPrincipal
	parseJSON(t, runCLI(t, operator.command(append([]string{"ad", "sp", "create-for-rbac", "-n", name, "-o", "json"}, extra...)...)), &principal)
	require.NotEmpty(t, principal.AppID)
	require.NotEmpty(t, principal.Password)
	as := operator
	as.configDir = filepath.Join(t.TempDir(), "azure-config")
	azRBACCloud(t, as, principal.AppID, principal.Password)
	return principal, as
}

func TestRBAC_CLI_RoleAssignmentsGateResourceManager(t *testing.T) {
	t.Parallel()
	env := startAzLoginSimulator(t)
	azRBACCloud(t, env, "test-client-id", "test-client-secret")
	defer runCLI(t, env.command("logout"))

	const group, other = "cli-rbac-rg", "cli-rbac-other-rg"
	runCLI(t, env.command("group", "create", "-n", group, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("group", "create", "-n", other, "-l", "eastus", "-o", "json"))
	groupID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", subscriptionID, group)

	principal, sp := azRBACSignIn(t, env, "cli-rbac-reader", "--role", "Reader", "--scopes", groupID)
	runCLI(t, sp.command("group", "show", "-n", group, "-o", "json"))

	failure := runStorageCLIExpectFailure(t, sp.command("identity", "create", "-g", group, "-n", "cli-rbac-id", "-o", "json"))
	assert.Contains(t, failure, "AuthorizationFailed")
	assert.Contains(t, failure, "Microsoft.ManagedIdentity/userAssignedIdentities/write")
	failure = runStorageCLIExpectFailure(t, sp.command("group", "show", "-n", other, "-o", "json"))
	assert.Contains(t, failure, "AuthorizationFailed")

	runCLI(t, env.command("role", "assignment", "create", "--assignee", principal.AppID,
		"--role", "Contributor", "--scope", groupID, "-o", "json"))
	runCLI(t, sp.command("identity", "create", "-g", group, "-n", "cli-rbac-id", "-o", "json"))

	// A custom role's notActions take the delete away from its actions.
	definition := fmt.Sprintf(`{"Name":"CLI RBAC no identity delete","IsCustom":true,"Description":"Everything but deleting identities.","Actions":["*"],"NotActions":["Microsoft.ManagedIdentity/userAssignedIdentities/delete"],"AssignableScopes":["/subscriptions/%s"]}`, subscriptionID)
	runCLI(t, env.command("role", "definition", "create", "--role-definition", definition, "-o", "json"))
	_, limitedSP := azRBACSignIn(t, env, "cli-rbac-notactions",
		"--role", "CLI RBAC no identity delete", "--scopes", groupID)
	runCLI(t, limitedSP.command("identity", "create", "-g", group, "-n", "cli-rbac-kept", "-o", "json"))
	failure = runStorageCLIExpectFailure(t, limitedSP.command("identity", "delete", "-g", group, "-n", "cli-rbac-kept"))
	assert.Contains(t, failure, "AuthorizationFailed")
	assert.Contains(t, failure, "Microsoft.ManagedIdentity/userAssignedIdentities/delete")

	// An assignment at the subscription reaches every group beneath it.
	runCLI(t, env.command("role", "assignment", "create", "--assignee", principal.AppID,
		"--role", "Reader", "--scope", "/subscriptions/"+subscriptionID, "-o", "json"))
	runCLI(t, sp.command("group", "show", "-n", other, "-o", "json"))
}

func TestRBAC_CLI_StorageDataRolesAndSharedKeyAccess(t *testing.T) {
	t.Parallel()
	env := startAzLoginSimulator(t)
	azRBACCloud(t, env, "test-client-id", "test-client-secret")
	defer runCLI(t, env.command("logout"))

	const group, account, container = "cli-rbac-storage-rg", "clirbacstorage", "data"
	runCLI(t, env.command("group", "create", "-n", group, "-l", "eastus", "-o", "json"))
	var created struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "create", "-n", account, "-g", group,
		"-l", "eastus", "--sku", "Standard_LRS", "-o", "json")), &created)
	var keys []struct {
		Value string `json:"value"`
	}
	parseJSON(t, runCLI(t, env.command("storage", "account", "keys", "list", "-g", group, "-n", account, "-o", "json")), &keys)
	require.NotEmpty(t, keys)
	runCLI(t, env.command(azMoveStorageBlobCmd(env, account, keys[0].Value,
		"storage", "container", "create", "--name", container)...))
	src := filepath.Join(t.TempDir(), "hello.txt")
	require.NoError(t, os.WriteFile(src, []byte("hello"), 0o600))
	runCLI(t, env.command(azMoveStorageBlobCmd(env, account, keys[0].Value,
		"storage", "blob", "upload", "--container-name", container, "--name", "hello.txt", "--file", src, "--overwrite")...))

	principal, sp := azRBACSignIn(t, env, "cli-rbac-blob-reader")
	login := func(args ...string) []string {
		return append(args, "--auth-mode", "login", "--account-name", account,
			"--blob-endpoint", env.baseURL+"/"+account+"/", "--only-show-errors", "-o", "json")
	}
	dst := filepath.Join(t.TempDir(), "downloaded.txt")
	failure := runStorageCLIExpectFailure(t, sp.command(login("storage", "blob", "download",
		"--container-name", container, "--name", "hello.txt", "--file", dst)...))
	assert.Contains(t, failure, azStoragePermissionRefusal)

	runCLI(t, env.command("role", "assignment", "create", "--assignee", principal.AppID,
		"--role", "Storage Blob Data Reader", "--scope", created.ID, "-o", "json"))
	runCLI(t, sp.command(login("storage", "blob", "download",
		"--container-name", container, "--name", "hello.txt", "--file", dst)...))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
	failure = runStorageCLIExpectFailure(t, sp.command(login("storage", "blob", "upload",
		"--container-name", container, "--name", "written.txt", "--file", src, "--overwrite")...))
	assert.Contains(t, failure, azStoragePermissionRefusal)

	// A user delegation signature verifies against the key it was signed with.
	var token string
	parseJSON(t, runCLI(t, sp.command(login("storage", "blob", "generate-sas", "--as-user",
		"--container-name", container, "--name", "hello.txt", "--permissions", "r",
		"--expiry", time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04Z"))...)), &token)
	query, err := url.ParseQuery(token)
	require.NoError(t, err)
	sasCmd := func(sas string) []string {
		return []string{"storage", "blob", "download", "--container-name", container, "--name", "hello.txt",
			"--file", filepath.Join(t.TempDir(), "sas.txt"), "--sas-token", sas, "--account-name", account,
			"--blob-endpoint", env.baseURL + "/" + account + "/", "--only-show-errors", "-o", "json"}
	}
	runCLI(t, env.command(sasCmd(token)...))
	query.Set("sig", "AAAA"+query.Get("sig")[4:])
	failure = runStorageCLIExpectFailure(t, env.command(sasCmd(query.Encode())...))
	assert.Contains(t, failure, azStorageAuthenticationRefusal)

	// An account that refuses its keys refuses Shared Key.
	runCLI(t, env.command("storage", "account", "update", "-n", account, "-g", group,
		"--allow-shared-key-access", "false", "-o", "json"))
	failure = runStorageCLIExpectFailure(t, env.command(azMoveStorageBlobCmd(env, account, keys[0].Value,
		"storage", "blob", "download", "--container-name", container, "--name", "hello.txt", "--file", dst)...))
	assert.Contains(t, failure, "KeyBasedAuthenticationNotPermitted")
	runCLI(t, sp.command(login("storage", "blob", "download",
		"--container-name", container, "--name", "hello.txt", "--file", dst)...))
}
