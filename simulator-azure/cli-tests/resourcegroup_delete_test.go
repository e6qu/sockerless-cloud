package azure_cli_test

import (
	"strings"
	"testing"
)

// TestGroupDelete_RemovesTheGroupsResources: `az group delete` waits on the
// long-running operation, after which the group and its identity are gone.
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
	if out := runCLIExpectFailure(t, env.command("identity", "show", "-g", group, "-n", identity)); !strings.Contains(out, "ResourceGroupNotFound") {
		t.Errorf("the identity outlived its group: %s", out)
	}
	if out := runCLIExpectFailure(t, env.command("group", "delete", "-n", group, "--yes")); !strings.Contains(out, "ResourceGroupNotFound") {
		t.Errorf("deleting the group again did not fail with ResourceGroupNotFound: %s", out)
	}
}

// TestGroupMissing_RefusesItsResources: Azure Resource Manager refuses an
// identity in a group that does not exist with ResourceGroupNotFound before
// Microsoft.ManagedIdentity sees it.
func TestGroupMissing_RefusesItsResources(t *testing.T) {
	t.Parallel()
	env := startAzLoginSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-group-missing",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-group-missing"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, env.command("logout")) })

	const group = "cli-group-never-created-rg"
	out := runCLIExpectFailure(t, env.command("identity", "create", "-g", group, "-n", "cli-orphan-uai", "-l", "eastus", "-o", "json"))
	if !strings.Contains(out, "ResourceGroupNotFound") || !strings.Contains(out, "Resource group '"+group+"' could not be found.") {
		t.Fatalf("az identity create in a missing group did not fail with ResourceGroupNotFound:\n%s", out)
	}
	out = runCLIExpectFailure(t, env.command("identity", "show", "-g", group, "-n", "cli-orphan-uai", "-o", "json"))
	if !strings.Contains(out, "ResourceGroupNotFound") {
		t.Fatalf("az identity show in a missing group did not fail with ResourceGroupNotFound:\n%s", out)
	}
}
