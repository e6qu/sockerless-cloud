package azure_tf_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformSubscriptionApplyDestroy covers both azurerm_subscription
// creation modes, under a billing scope and by adopting an existing
// subscription; destroy deletes the aliases and cancels the subscriptions,
// waiting for Disabled. It runs as its own CI shard: each create and cancel
// sits through the provider's fixed 60s StateChangeConf delay and four 10s
// polls.
func TestTerraformSubscriptionApplyDestroy(t *testing.T) {
	dir := tfSubscriptionWorkspace(t)
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	// A second plan must show no drift: -detailed-exitcode exits 2 on drift, which
	// runTimed surfaces as an error.
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

func tfSubscriptionWorkspace(t *testing.T) string {
	t.Helper()
	return tfWorkspaceFrom(t, tfSubscriptionDir())
}
