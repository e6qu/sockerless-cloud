package azure_cli_test

import (
	"strings"
	"testing"
)

// TestGroupDelete_RemovesTheGroupsResources deletes a resource group with
// `az group delete`, which waits on the long-running operation the delete
// starts, and finds the group and the identity it held gone.
func TestGroupDelete_RemovesTheGroupsResources(t *testing.T) {
	t.Parallel()
	env := startAzLoginSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-group-delete",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-group-delete"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, env.command("logout")) })

	const group, identity = "cli-group-delete-rg", "cli-group-delete-uai"
	runCLI(t, env.command("group", "create", "-n", group, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("identity", "create", "-g", group, "-n", identity, "-o", "json"))

	runCLI(t, env.command("group", "delete", "-n", group, "--yes"))

	if exists := strings.TrimSpace(runCLI(t, env.command("group", "exists", "-n", group))); exists != "false" {
		t.Errorf("az group exists after the delete = %q, want false", exists)
	}
	if out := runCLIExpectFailure(t, env.command("identity", "show", "-g", group, "-n", identity)); !strings.Contains(out, "ResourceNotFound") {
		t.Errorf("the identity outlived its group: %s", out)
	}
	if out := runCLIExpectFailure(t, env.command("group", "delete", "-n", group, "--yes")); !strings.Contains(out, "ResourceGroupNotFound") {
		t.Errorf("deleting the group again did not fail with ResourceGroupNotFound: %s", out)
	}
}
