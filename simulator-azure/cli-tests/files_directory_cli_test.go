package azure_cli_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFilesNestedDirectoryTreeCLI drives the nested-path story an Azure
// Container Apps or Azure Functions volume depends on through the `az storage
// directory` and `az storage file` command groups: a directory tree is created,
// a file is uploaded into it, the tree is listed at depth, and everything is
// removed — and the share's backing directory, which a mounting workload sees,
// agrees with the CLI at each step.
func TestFilesNestedDirectoryTreeCLI(t *testing.T) {
	const share = "cli-nested-share"
	payload := "bytes uploaded into a nested directory"

	runCLI(t, azStorageFile("share", "create", "--name", share))
	t.Cleanup(func() {
		_ = azStorageFile("share", "delete", "--name", share).Run()
	})

	runCLI(t, azStorageFile("directory", "create", "--share-name", share, "--name", "build"))
	runCLI(t, azStorageFile("directory", "create", "--share-name", share, "--name", "build/artifacts"))

	out := runCLI(t, azStorageFile("directory", "exists", "--share-name", share, "--name", "build/artifacts"))
	var exists struct {
		Exists bool `json:"exists"`
	}
	parseJSON(t, out, &exists)
	assert.True(t, exists.Exists, "az storage directory exists must find the nested directory: %s", out)

	source := filepath.Join(t.TempDir(), "bundle.tar")
	require.NoError(t, os.WriteFile(source, []byte(payload), 0o600))
	runCLI(t, azStorageFile("file", "upload", "--share-name", share,
		"--source", source, "--path", "build/artifacts/bundle.tar"))

	// The upload landed in the real nested directory a mounting workload walks.
	mounted := filepath.Join(azureFilesDataDir, filesAccountName, share, "build", "artifacts", "bundle.tar")
	got, err := os.ReadFile(mounted)
	require.NoError(t, err, "the nested upload must materialize at %s", mounted)
	assert.Equal(t, payload, string(got))

	out = runCLI(t, azStorageFile("file", "list", "--share-name", share, "--path", "build/artifacts"))
	var listed []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	parseJSON(t, out, &listed)
	require.Len(t, listed, 1, "the listing at depth must hold only the uploaded file: %s", out)
	assert.Equal(t, "bundle.tar", listed[0].Name)
	assert.Equal(t, "file", listed[0].Type)

	// The parent lists the subdirectory, not the file inside it.
	out = runCLI(t, azStorageFile("file", "list", "--share-name", share, "--path", "build"))
	var parent []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	parseJSON(t, out, &parent)
	require.Len(t, parent, 1, "the parent must list the subdirectory only: %s", out)
	assert.Equal(t, "artifacts", parent[0].Name)
	assert.Equal(t, "dir", parent[0].Type)

	// A directory with anything in it cannot be deleted, so the file goes first.
	runCLI(t, azStorageFile("file", "delete", "--share-name", share, "--path", "build/artifacts/bundle.tar"))
	runCLI(t, azStorageFile("directory", "delete", "--share-name", share, "--name", "build/artifacts"))
	runCLI(t, azStorageFile("directory", "delete", "--share-name", share, "--name", "build"))

	out = runCLI(t, azStorageFile("directory", "exists", "--share-name", share, "--name", "build"))
	parseJSON(t, out, &exists)
	assert.False(t, exists.Exists, "the deleted directory must be gone: %s", out)
	if _, err := os.Stat(filepath.Join(azureFilesDataDir, filesAccountName, share, "build")); !os.IsNotExist(err) {
		t.Fatalf("the deleted directory is still on the host: %v", err)
	}
}

// TestFilesDirectoryMetadataAndShareOperationsCLI drives the directory metadata
// round trip and the share-level operations `az storage share` exposes:
// snapshots, quota, usage statistics and stored access policies.
func TestFilesDirectoryMetadataAndShareOperationsCLI(t *testing.T) {
	const share = "cli-shareops-share"
	payload := "bytes counted by az storage share stats"

	runCLI(t, azStorageFile("share", "create", "--name", share))
	t.Cleanup(func() {
		_ = azStorageFile("share", "delete", "--name", share).Run()
	})

	runCLI(t, azStorageFile("directory", "create", "--share-name", share, "--name", "logs"))
	runCLI(t, azStorageFile("directory", "metadata", "update", "--share-name", share, "--name", "logs",
		"--metadata", "stage=collect"))
	out := runCLI(t, azStorageFile("directory", "metadata", "show", "--share-name", share, "--name", "logs"))
	var metadata map[string]string
	parseJSON(t, out, &metadata)
	// az reports metadata keyed by the response header name, which Python's
	// HTTP stack title-cases, so the lookup is case-insensitive.
	assert.Equal(t, "collect", metadataValue(metadata, "stage"),
		"az storage directory metadata show must read back what update wrote: %s", out)

	source := filepath.Join(t.TempDir(), "run.log")
	require.NoError(t, os.WriteFile(source, []byte(payload), 0o600))
	runCLI(t, azStorageFile("file", "upload", "--share-name", share, "--source", source, "--path", "logs/run.log"))

	// az reports the share usage rounded up to whole gibibytes, so the exact
	// byte count the service returns is asserted through the SDK instead; what
	// the CLI proves here is that Get Share Statistics answers it a usage at all.
	out = runCLI(t, azStorageFile("share", "stats", "--name", share))
	var usage string
	parseJSON(t, out, &usage)
	_, err := strconv.Atoi(usage)
	require.NoError(t, err, "az storage share stats must report a numeric usage: %s", out)

	runCLI(t, azStorageFile("share", "update", "--name", share, "--quota", "64"))
	out = runCLI(t, azStorageFile("share", "show", "--name", share))
	var shown struct {
		Properties struct {
			Quota int `json:"quota"`
		} `json:"properties"`
	}
	parseJSON(t, out, &shown)
	assert.Equal(t, 64, shown.Properties.Quota, "az storage share update must change the quota: %s", out)

	// A snapshot freezes the share's contents.
	out = runCLI(t, azStorageFile("share", "snapshot", "--name", share))
	var snapshot struct {
		Snapshot string `json:"snapshot"`
	}
	parseJSON(t, out, &snapshot)
	require.NotEmpty(t, snapshot.Snapshot, "az storage share snapshot must return the snapshot identifier: %s", out)

	out = runCLI(t, azStorageFile("share", "list", "--include-snapshots"))
	var shares []struct {
		Name     string `json:"name"`
		Snapshot string `json:"snapshot"`
	}
	parseJSON(t, out, &shares)
	found := false
	for _, entry := range shares {
		if entry.Name == share && entry.Snapshot == snapshot.Snapshot {
			found = true
		}
	}
	assert.True(t, found, "az storage share list --include-snapshots must name the snapshot: %s", out)

	// Stored access policies round-trip through the share ACL.
	runCLI(t, azStorageFile("share", "policy", "create", "--share-name", share,
		"--name", "cli-policy", "--permissions", "rl"))
	out = runCLI(t, azStorageFile("share", "policy", "show", "--share-name", share, "--name", "cli-policy"))
	var policy struct {
		Permission string `json:"permission"`
	}
	parseJSON(t, out, &policy)
	assert.Equal(t, "rl", policy.Permission,
		"az storage share policy show must read back the stored permission: %s", out)
}

// metadataValue reads one metadata entry regardless of the case az reports its
// key in: az surfaces metadata keyed by the x-ms-meta-* response header name,
// which Python's HTTP stack title-cases.
func metadataValue(metadata map[string]string, name string) string {
	for k, v := range metadata {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
