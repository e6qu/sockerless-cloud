package gcp_tf_test

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A google_sql_database_instance whose disk_size drops shrinks its storage in
// place, and the shrunk size reads back without drift.
func TestTerraformCloudSQLStorageShrink(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "sql-storage-shrink")
	cleanTerraformFixture(t, fixtureDir)
	size := func(gb int) []string { return []string{"-var", "disk_size=" + strconv.Itoa(gb)} }

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, append([]string{"apply", "-auto-approve"}, size(20)...)...))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, append([]string{"destroy", "-auto-approve"}, size(12)...)...))
		assert.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	assert.Equal(t, float64(20), readOutputsInDir(t, fixtureDir).mustNumber(t, "disk_size"))

	out, err = runTimed(t, "terraform apply (shrink)", terraformCmdInDir(fixtureDir, append([]string{"apply", "-auto-approve"}, size(12)...)...))
	require.NoError(t, err, "terraform apply with a smaller disk_size failed:\n%s", out)
	assert.Equal(t, float64(12), readOutputsInDir(t, fixtureDir).mustNumber(t, "disk_size"))

	out, err = runTimed(t, "terraform plan", terraformCmdInDir(fixtureDir, append([]string{"plan", "-detailed-exitcode"}, size(12)...)...))
	require.NoError(t, err, "the shrunk instance plans no change:\n%s", out)
}
