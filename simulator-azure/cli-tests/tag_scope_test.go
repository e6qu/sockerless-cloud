package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for Microsoft.Resources/tags/default: the native `az tag`
// commands read and write the same set of tags the tagged thing's own commands
// report, at a resource scope and at a resource-group scope alike. Routes
// exercised:
//
//	GET+PUT+PATCH+DELETE /{resourceScope}/providers/Microsoft.Resources/tags/default
//	GET+PUT+PATCH+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Resources/tags/default

// tagCLILogin registers the simulator as a cloud and logs in, so the native
// `az tag` commands (which have no `az rest` spelling) reach it.
func tagCLILogin(t *testing.T, env azLoginEnv, cloudName string) {
	t.Helper()
	runCLI(t, env.command("cloud", "register", "-n", cloudName,
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", cloudName))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	t.Cleanup(func() { runCLI(t, env.command("logout")) })
}

// sbNamespaceCLITags reads the tags `az servicebus namespace show` reports.
// Each call decodes into a fresh value: encoding/json merges into an existing
// map rather than replacing it, so a reused destination would accumulate the
// tags of every earlier read and hide exactly the divergence under test.
func sbNamespaceCLITags(t *testing.T, env azLoginEnv, rg, namespace string) map[string]string {
	t.Helper()
	var shown struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, runCLI(t, env.command("servicebus", "namespace", "show",
		"-n", namespace, "-g", rg, "-o", "json")), &shown)
	if shown.Tags == nil {
		return map[string]string{}
	}
	return shown.Tags
}

// tagCLIList reads the tags `az tag list` reports at a scope.
func tagCLIList(t *testing.T, env azLoginEnv, scope string) map[string]string {
	t.Helper()
	var listed struct {
		Properties struct {
			Tags map[string]string `json:"tags"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, env.command("tag", "list", "--resource-id", scope, "-o", "json")), &listed)
	if listed.Properties.Tags == nil {
		return map[string]string{}
	}
	return listed.Properties.Tags
}

// TestTagAtScopeCLI proves `az tag` and the tagged resource's own commands are
// two spellings of one set of tags: a tag created through `az tag create` is
// reported by `az servicebus namespace show` and `az resource list`, a tag
// written by the resource's own create is reported by `az tag list`, and
// `az tag delete` clears the resource's own tags.
func TestTagAtScopeCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	tagCLILogin(t, env, "sockerless-tag-scope")

	rg, namespace := "cli-tag-scope-rg", "clitagscopens"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus",
		"--tags", "origin=group-create", "-o", "json"))
	runCLI(t, env.command("servicebus", "namespace", "create", "-n", namespace, "-g", rg,
		"-l", "eastus", "--sku", "Standard", "--tags", "origin=resource-create", "-o", "json"))

	namespaceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ServiceBus/namespaces/%s",
		subscriptionID, rg, namespace)
	rgID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", subscriptionID, rg)

	// The tag the resource's own create wrote is what `az tag list` reports.
	assert.Equal(t, map[string]string{"origin": "resource-create"}, tagCLIList(t, env, namespaceID),
		"az tag list must report the tags the resource itself holds")
	assert.Equal(t, map[string]string{"origin": "group-create"}, tagCLIList(t, env, rgID),
		"az tag list must report the tags the resource group itself holds")

	// `az tag update --operation Merge` is visible on the resource's own show.
	runCLI(t, env.command("tag", "update", "--resource-id", namespaceID,
		"--operation", "Merge", "--tags", "team=platform", "-o", "json"))
	assert.Equal(t, map[string]string{"origin": "resource-create", "team": "platform"},
		sbNamespaceCLITags(t, env, rg, namespace),
		"az servicebus namespace show must report the tag az tag update merged")

	// `az resource list` reads the same set.
	var listed []struct {
		ID   string            `json:"id"`
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, runCLI(t, env.command("resource", "list", "-g", rg, "-o", "json")), &listed)
	found := false
	for _, row := range listed {
		if row.ID != namespaceID {
			continue
		}
		found = true
		assert.Equal(t, map[string]string{"origin": "resource-create", "team": "platform"}, row.Tags,
			"az resource list must report the tag az tag update merged")
	}
	require.True(t, found, "az resource list did not report the namespace: %v", listed)

	// `az tag create` replaces the whole set — the CLI documents it as an init.
	runCLI(t, env.command("tag", "create", "--resource-id", namespaceID,
		"--tags", "env=prod", "-o", "json"))
	assert.Equal(t, map[string]string{"env": "prod"}, tagCLIList(t, env, namespaceID))
	assert.Equal(t, map[string]string{"env": "prod"}, sbNamespaceCLITags(t, env, rg, namespace))

	// A retag through the resource's own command is visible to `az tag list`.
	runCLI(t, env.command("servicebus", "namespace", "update", "-g", rg, "-n", namespace,
		"--set", "tags.env=staging", "-o", "json"))
	assert.Equal(t, map[string]string{"env": "staging"}, tagCLIList(t, env, namespaceID),
		"az tag list must report a retag written through the resource's own command")

	// `az tag delete` clears the resource's own tags.
	runCLI(t, env.command("tag", "delete", "--resource-id", namespaceID, "-y"))
	assert.Empty(t, sbNamespaceCLITags(t, env, rg, namespace),
		"az tag delete must clear the resource's own tags")

	// At the group scope, `az tag` writes what `az group show` reports.
	runCLI(t, env.command("tag", "update", "--resource-id", rgID,
		"--operation", "Replace", "--tags", "cost=42", "-o", "json"))
	var group struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, runCLI(t, env.command("group", "show", "-n", rg, "-o", "json")), &group)
	assert.Equal(t, map[string]string{"cost": "42"}, group.Tags,
		"az group show must report the tags az tag wrote at the group scope")

	// A scope naming no resource is refused rather than accepting a write
	// nothing else can read.
	missing := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ServiceBus/namespaces/clitagnevercreated",
		subscriptionID, rg)
	failure := runStorageCLIExpectFailure(t, env.command("tag", "create",
		"--resource-id", missing, "--tags", "a=b", "-o", "json"))
	assert.Contains(t, failure, "ResourceNotFound",
		"tagging a scope that holds no resource must fail")
}
