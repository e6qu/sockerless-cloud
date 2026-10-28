package gcp_tf_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformIAMEnforcement applies through a second provider configured
// with a service account's own key: holding roles/storage.objectViewer on the
// bucket it reads an object but cannot create one, and once it holds
// roles/storage.objectAdmin it can.
func TestTerraformIAMEnforcement(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "iam-enforcement")
	cleanTerraformFixture(t, fixtureDir)

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	raw, err := base64.StdEncoding.DecodeString(outputs.must(t, "reader_key"))
	require.NoError(t, err)
	var keyFile map[string]any
	require.NoError(t, json.Unmarshal(raw, &keyFile))
	keyFile["token_uri"] = tfEndpoint + "/token"
	readerKey, err := json.Marshal(keyFile)
	require.NoError(t, err)
	readerFile := filepath.Join(t.TempDir(), "reader-key.json")
	require.NoError(t, os.WriteFile(readerFile, readerKey, 0o600))
	reader := "-var=reader_credentials=" + readerFile

	t.Cleanup(func() {
		out, err := runTimed(t, "terraform apply (remove the reader's object)", terraformCmdInDir(fixtureDir,
			"apply", "-auto-approve", reader, "-var=grant_admin=true"))
		require.NoError(t, err, "terraform apply failed:\n%s", out)
		out, err = runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve", reader))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})

	out, err = runTimed(t, "terraform apply (read as the viewer)", terraformCmdInDir(fixtureDir,
		"apply", "-auto-approve", reader, "-var=read_object=true"))
	require.NoError(t, err, "an object viewer reads the object:\n%s", out)
	require.Equal(t, "seeded", readOutputsInDir(t, fixtureDir).must(t, "read_content"))

	out, err = runTimed(t, "terraform apply (write as the viewer)", terraformCmdInDir(fixtureDir,
		"apply", "-auto-approve", reader, "-var=write_object=true"))
	require.Error(t, err, "an object viewer creates an object:\n%s", out)
	require.Contains(t, string(out), "does not have storage.objects.create access to the Google Cloud Storage bucket")

	out, err = runTimed(t, "terraform apply (write as an object admin)", terraformCmdInDir(fixtureDir,
		"apply", "-auto-approve", reader, "-var=write_object=true", "-var=grant_admin=true"))
	require.NoError(t, err, "an object admin creates an object:\n%s", out)
}
