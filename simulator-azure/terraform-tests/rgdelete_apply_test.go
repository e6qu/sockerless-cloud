package azure_tf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformResourceGroupDeleteTakesItsResources destroys a resource group
// still holding an identity Terraform no longer tracks; azurerm waits on the
// delete's long-running operation, after which neither can be imported,
// because Azure Resource Manager deleted the identity with its group.
func TestTerraformResourceGroupDeleteTakesItsResources(t *testing.T) {
	dir := tfWorkspaceFrom(t, mustAbs("rgdelete"))
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	outputs := readOutputs(t, dir)
	identityID := outputs.must(t, "identity_id")
	groupID := outputs.must(t, "group_id")

	out, err = runTimed(t, "terraform state rm", terraformCmd(dir, "state", "rm", "azurerm_user_assigned_identity.left_behind"))
	require.NoError(t, err, "terraform state rm failed:\n%s", out)

	out, err = runTimed(t, "terraform destroy", terraformCmd(dir, "destroy", "-auto-approve"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
	require.Contains(t, string(out), "Destroy complete! Resources: 1 destroyed.")

	for address, id := range map[string]string{
		"azurerm_user_assigned_identity.left_behind": identityID,
		"azurerm_resource_group.rgdelete":            groupID,
	} {
		out, err = runTimed(t, "terraform import", terraformCmd(dir, "import", address, id))
		require.Error(t, err, "importing %s after its group's delete succeeded:\n%s", id, out)
		require.True(t, strings.Contains(string(out), "Cannot import non-existent remote object"),
			"importing %s after its group's delete failed for another reason:\n%s", id, out)
	}
}
