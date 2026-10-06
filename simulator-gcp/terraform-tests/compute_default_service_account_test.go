package gcp_tf_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformComputeDefaultServiceAccount reads the google_compute_default_service_account
// data source: the provider takes the email from the project's Compute Engine
// resource and reads the account back from IAM, which holds it once the
// project exists.
func TestTerraformComputeDefaultServiceAccount(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "compute-default-service-account")
	cleanTerraformFixture(t, fixtureDir)

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	const email = "735298346210-compute@developer.gserviceaccount.com"
	assert.Equal(t, email, outputs.must(t, "email"))
	assert.Equal(t, "projects/test-project/serviceAccounts/"+email, outputs.must(t, "name"))
	assert.Equal(t, "Compute Engine default service account", outputs.must(t, "display_name"))
	assert.Equal(t, "serviceAccount:"+email, outputs.must(t, "member"))
	assert.NotEmpty(t, outputs.must(t, "unique_id"))
}
