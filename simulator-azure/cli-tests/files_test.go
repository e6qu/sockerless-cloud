package azure_cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const filesAccountName = "clifileacct"

// azStorageFile builds an `az storage file` / `az storage share` invocation
// against the simulator's path-style Files endpoint. The account, the key and
// the endpoint are the only coordinates that differ from a real Azure
// invocation.
func azStorageFile(group string, args ...string) *exec.Cmd {
	baseArgs := append([]string{"storage", group}, args...)
	// Azure Files has no Microsoft Entra data-plane authorization, so `az
	// storage file` / `az storage share` take an account key and reject
	// --auth-mode.
	baseArgs = append(baseArgs,
		"--account-name", filesAccountName,
		"--account-key", storageAccountKey,
		"--file-endpoint", baseURL+"/file/"+filesAccountName+"/",
		"--only-show-errors",
		"--output", "json",
	)
	cmd := exec.Command("az", baseArgs...)
	cmd.Env = append(os.Environ(),
		"AZURE_CONFIG_DIR="+filepath.Join(tmpDir, "azure-config"),
		"AZURE_CORE_NO_COLOR=1",
	)
	return cmd
}

// TestFilesUploadLandsInTheContainerAppsMountCLI drives the Azure Files data
// plane with the az CLI and then looks at the directory a Container Apps
// Volume{StorageType: AzureFile} bind-mounts. `az storage file upload` issues
// Create File followed by Upload Range, so a share is only useful as a mounted
// volume if those two land the bytes on the host path the executor binds.
func TestFilesUploadLandsInTheContainerAppsMountCLI(t *testing.T) {
	const share = "cli-mount-share"
	const fileName = "uploaded.txt"
	payload := "bytes uploaded with the az CLI"

	runCLI(t, azStorageFile("share", "create", "--name", share))
	t.Cleanup(func() {
		_ = azStorageFile("share", "delete", "--name", share).Run()
	})

	source := filepath.Join(tmpDir, "cli-files-source.txt")
	require.NoError(t, os.WriteFile(source, []byte(payload), 0o600))

	runCLI(t, azStorageFile("file", "upload", "--share-name", share, "--source", source, "--path", fileName))

	// The share materializes at <files data dir>/<account>/<share>: the exact
	// host path the simulator's Container Apps executor bind-mounts, so this is
	// what a container reads at <mountPath>/uploaded.txt.
	mounted := filepath.Join(azureFilesDataDir, filesAccountName, share, fileName)
	onDisk, err := os.ReadFile(mounted)
	require.NoError(t, err, "the uploaded file must exist in the directory a volume mount binds: %s", mounted)
	assert.Equal(t, payload, string(onDisk), "the mounted share must hold the uploaded bytes")

	// The same bytes come back through the data plane.
	destination := filepath.Join(tmpDir, "cli-files-download.txt")
	runCLI(t, azStorageFile("file", "download", "--share-name", share, "--path", fileName, "--dest", destination))
	downloaded, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, payload, string(downloaded), "az storage file download must return the uploaded bytes")

	out := runCLI(t, azStorageFile("file", "list", "--share-name", share))
	var listed []struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &listed)
	found := false
	for _, entry := range listed {
		if entry.Name == fileName {
			found = true
		}
	}
	assert.True(t, found, "az storage file list must name the uploaded file: %s", out)

	out = runCLI(t, azStorageFile("share", "list"))
	var shares []struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &shares)
	found = false
	for _, entry := range shares {
		if entry.Name == share {
			found = true
		}
	}
	assert.True(t, found, "az storage share list must name the created share: %s", out)
}

// TestFilesShareDeleteRemovesTheMountedContentsCLI: deleting the share takes
// its files with it, so a container that mounts a share of the same name later
// never sees the previous share's data.
func TestFilesShareDeleteRemovesTheMountedContentsCLI(t *testing.T) {
	const share = "cli-deleted-share"
	const fileName = "gone.txt"

	runCLI(t, azStorageFile("share", "create", "--name", share))
	source := filepath.Join(tmpDir, "cli-files-deleted-source.txt")
	require.NoError(t, os.WriteFile(source, []byte("temporary"), 0o600))
	runCLI(t, azStorageFile("file", "upload", "--share-name", share, "--source", source, "--path", fileName))

	shareDir := filepath.Join(azureFilesDataDir, filesAccountName, share)
	require.DirExists(t, shareDir)

	runCLI(t, azStorageFile("share", "delete", "--name", share))
	_, err := os.Stat(shareDir)
	assert.True(t, os.IsNotExist(err), "the share directory must be gone after az storage share delete: %v", err)
}

func TestFilesShareStoredAccessPolicyCLI(t *testing.T) {
	const (
		share  = "cli-policy-share"
		policy = "cli-policy"
	)
	runCLI(t, azStorageFile("share", "create", "--name", share))
	t.Cleanup(func() {
		_ = azStorageFile("share", "delete", "--name", share).Run()
	})

	runCLI(t, azStorageFile("share", "policy", "create",
		"--share-name", share,
		"--name", policy,
		"--permissions", "rwdl",
		"--start", "2026-07-28T10:00:00Z",
		"--expiry", "2026-07-29T10:00:00Z"))

	out := runCLI(t, azStorageFile("share", "policy", "show",
		"--share-name", share,
		"--name", policy))
	var stored struct {
		Permission string `json:"permission"`
		Start      string `json:"start"`
		Expiry     string `json:"expiry"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &stored))
	assert.Equal(t, "rwdl", stored.Permission, out)
	assert.Equal(t, "2026-07-28T10:00:00Z+00:00", stored.Start, out)
	assert.Equal(t, "2026-07-29T10:00:00Z+00:00", stored.Expiry, out)
}
