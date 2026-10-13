package azure_tf_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformMissingResourceGroupRefusesApply applies an
// azurerm_user_assigned_identity whose resource group was never created. Azure
// Resource Manager refuses the create with ResourceGroupNotFound, so the apply
// fails and the identity never reaches the state.
func TestTerraformMissingResourceGroupRefusesApply(t *testing.T) {
	dir := tfWorkspaceFrom(t, mustAbs("rgmissing"))
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	var exited *exec.ExitError
	require.ErrorAs(t, err, &exited, "applying an identity in a missing resource group succeeded:\n%s", out)
	require.True(t, strings.Contains(string(out), "ResourceGroupNotFound"),
		"the apply failed for another reason than ResourceGroupNotFound:\n%s", out)

	out, err = runTimed(t, "terraform state list", terraformCmd(dir, "state", "list"))
	require.NoError(t, err, "terraform state list failed:\n%s", out)
	require.Empty(t, strings.TrimSpace(string(out)), "the refused identity reached the state")
}
