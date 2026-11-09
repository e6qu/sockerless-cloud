package gcp_tf_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A google_sql_database_instance created on POSTGRES_15 whose
// database_version moves to POSTGRES_16 upgrades in place: the apply changes
// the instance without replacing it, and the new version reads back without
// drift.
func TestTerraformCloudSQLMajorVersionUpgrade(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "sql-version-upgrade")
	cleanTerraformFixture(t, fixtureDir)
	version := func(v string) []string { return []string{"-var", "database_version=" + v} }

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, append([]string{"apply", "-auto-approve"}, version("POSTGRES_15")...)...))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, append([]string{"destroy", "-auto-approve"}, version("POSTGRES_16")...)...))
		assert.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	assert.Equal(t, "POSTGRES_15", readOutputsInDir(t, fixtureDir).must(t, "database_version"))

	out, err = runTimed(t, "terraform apply (upgrade)", terraformCmdInDir(fixtureDir, append([]string{"apply", "-auto-approve", "-no-color"}, version("POSTGRES_16")...)...))
	require.NoError(t, err, "terraform apply with POSTGRES_16 failed:\n%s", out)
	assert.Contains(t, string(out), "0 added, 1 changed, 0 destroyed", "the upgrade happens in place")
	assert.Equal(t, "POSTGRES_16", readOutputsInDir(t, fixtureDir).must(t, "database_version"))

	out, err = runTimed(t, "terraform plan", terraformCmdInDir(fixtureDir, append([]string{"plan", "-detailed-exitcode"}, version("POSTGRES_16")...)...))
	require.NoError(t, err, "the upgraded instance plans no change:\n%s", out)
}
