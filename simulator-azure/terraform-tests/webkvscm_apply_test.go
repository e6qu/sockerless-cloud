package azure_tf_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformWebAppKeyVaultReferenceAndSourceControl provisions a Linux web
// app whose DB app setting is a Key Vault reference it resolves through the
// user-assigned identity key_vault_reference_identity_id names, granted by an
// azurerm_key_vault_access_policy, and an azurerm_app_service_source_control
// that deploys the branch of a repository the test serves. The app answers with
// the secret's value and the page the repository's head carries.
func TestTerraformWebAppKeyVaultReferenceAndSourceControl(t *testing.T) {
	repo := startGitHTTPRepo(t, "tf-site")
	repo.commit("main", "Deployed page", map[string]string{"index.html": "page-from-the-repository"})

	dir := tfWorkspaceFrom(t, mustAbs("webkvscm"))
	run := func(name string, args ...string) {
		t.Helper()
		cmd := terraformCmd(dir, args...)
		cmd.Env = append(cmd.Env, "TF_VAR_repo_url="+repo.URL)
		out, err := runTimed(t, name, cmd)
		require.NoError(t, err, "%s failed:\n%s", name, out)
	}
	run("terraform init", "init")
	run("terraform apply", "apply", "-auto-approve")
	run("terraform plan", "plan", "-detailed-exitcode")

	outputs := readOutputs(t, dir)
	require.Equal(t, "ExternalGit", outputs.must(t, "scm_type"))
	require.Equal(t, "hunter2 page-from-the-repository", slotSiteServes(t, outputs.must(t, "app_hostname")),
		"the app sees the referenced secret's value and the repository's deployed page")

	run("terraform destroy", "destroy", "-auto-approve")
}
