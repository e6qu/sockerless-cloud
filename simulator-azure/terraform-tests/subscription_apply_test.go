package azure_tf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformSubscriptionApplyDestroy provisions subscriptions through the
// Microsoft.Subscription alias API with `azurerm_subscription`, covering both
// creation modes the resource supports: creation under a billing scope (the
// alias PUT + provisioning-state poll, then the provider's wait for the
// subscription to settle Enabled) and adoption of an existing subscription
// id. Destroy deletes the aliases and cancels the subscriptions, waiting for
// the Disabled state.
//
// This runs against the standalone subscription/ configuration as its own CI
// shard (`tf (azure subscription)`): each create and cancel sits through the
// provider's fixed 60s StateChangeConf delay plus four 10s confirmation
// polls, which does not fit the shared stack's budget under the repo-wide
// 15-minute job ceiling. Unlike the shared stack, none of its resources need
// real-execution network capabilities, so it runs wherever the simulator and
// the HTTPS gateway do.
func TestTerraformSubscriptionApplyDestroy(t *testing.T) {
	dir := tfSubscriptionWorkspace(t)
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	// Idempotency: a second plan must show no drift. -detailed-exitcode makes
	// terraform exit 2 (non-zero) on any drift, which runTimed surfaces as an
	// error — so a clean plan (exit 0) is the only pass.
	out, err = runTimed(t, "terraform plan", terraformCmd(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "terraform plan showed drift after apply (not idempotent):\n%s", out)

	outputs := readOutputs(t, dir)

	subAliasID := outputs.must(t, "azrm_subscription_created_alias_id")
	require.Equal(t, "/providers/Microsoft.Subscription/aliases/tf-test-sub-alias", subAliasID,
		"azurerm_subscription id must be the canonical alias resource path; got %s", subAliasID)

	subCreatedID := outputs.must(t, "azrm_subscription_created_subscription_id")
	require.NotEmpty(t, subCreatedID, "created subscription must expose the minted subscription id")
	require.NotEqual(t, "00000000-0000-0000-0000-000000000001", subCreatedID,
		"a created subscription must get its own id, not the harness default")

	subTenantID := outputs.must(t, "azrm_subscription_created_tenant_id")
	require.Equal(t, "11111111-1111-1111-1111-111111111111", subTenantID,
		"created subscription must report the simulator tenant; got %s", subTenantID)

	subAdoptedAliasID := outputs.must(t, "azrm_subscription_adopted_alias_id")
	require.Equal(t, "/providers/Microsoft.Subscription/aliases/tf-test-sub-alias-adopted", subAdoptedAliasID,
		"adopted azurerm_subscription id must be the canonical alias resource path; got %s", subAdoptedAliasID)

	subAdoptedID := outputs.must(t, "azrm_subscription_adopted_subscription_id")
	require.Equal(t, "00000000-0000-0000-0000-0000000000ad", subAdoptedID,
		"adoption must keep the pre-existing subscription id; got %s", subAdoptedID)

	out, err = runTimed(t, "terraform destroy", terraformCmd(dir, "destroy", "-auto-approve"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
}

// tfSubscriptionWorkspace copies the standalone Microsoft.Subscription
// configuration into a working directory private to this run.
//
// Terraform keeps its working state — terraform.tfstate, the state lock, the
// .terraform plugin directory, errored.tfstate — next to the configuration it
// runs, so running in the checked-in configuration directory makes that
// directory shared mutable state between every terraform process on the
// machine. Two overlapping runs of this round trip (the CI shard alongside a
// local run, or two local runs) then corrupt each other: the second run wipes
// the working directory while the first is mid-apply, and the first run's
// subsequent plan reads an empty state and reports every resource as a fresh
// create, which reads as a simulator idempotency failure. A per-run directory
// keeps the round trip hermetic, and takes the state files out of the
// checked-in tree entirely.
func tfSubscriptionWorkspace(t *testing.T) string {
	t.Helper()
	src := tfSubscriptionDir()
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	require.NoError(t, err, "read the Microsoft.Subscription configuration at %s", src)
	copied := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".tf" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		require.NoError(t, err, "read %s", entry.Name())
		require.NoError(t, os.WriteFile(filepath.Join(dst, entry.Name()), data, 0o600), "write %s", entry.Name())
		copied++
	}
	require.NotZero(t, copied, "no .tf files found in the Microsoft.Subscription configuration at %s", src)
	return dst
}
