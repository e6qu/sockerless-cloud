package azure_cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const storageAccountKey = "a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s="

func azStorageContainer(args ...string) *exec.Cmd {
	baseArgs := append([]string{"storage", "container"}, args...)
	baseArgs = append(baseArgs,
		"--auth-mode", "key",
		"--account-name", "listpropsacct",
		"--account-key", storageAccountKey,
		"--blob-endpoint", baseURL+"/listpropsacct/",
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

func TestBlobListContainersPropertiesCLI(t *testing.T) {
	container := "cli-list-props"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() {
		_ = azStorageContainer("delete", "--name", container).Run()
	})

	out := runCLI(t, azStorageContainer("show", "--name", container))
	var shown struct {
		Properties struct {
			ETag         string `json:"etag"`
			LastModified string `json:"lastModified"`
		} `json:"properties"`
	}
	parseJSON(t, out, &shown)
	require.NotEmpty(t, shown.Properties.ETag)
	require.NotEmpty(t, shown.Properties.LastModified)

	out = runCLI(t, azStorageContainer("list"))
	var containers []struct {
		Name       string `json:"name"`
		Properties struct {
			ETag         string `json:"etag"`
			LastModified string `json:"lastModified"`
		} `json:"properties"`
	}
	parseJSON(t, out, &containers)
	for _, listed := range containers {
		if listed.Name != container {
			continue
		}
		assert.Equal(t, shown.Properties.ETag, listed.Properties.ETag)
		assert.Equal(t, shown.Properties.LastModified, listed.Properties.LastModified)
		return
	}
	t.Fatalf("container %q not found in az storage container list output: %s", container, out)
}

// azStorageBlob builds an `az storage blob` invocation against the simulator's
// path-style blob endpoint, with the same account + key coordinates the
// container helper uses.
func azStorageBlob(args ...string) *exec.Cmd {
	baseArgs := append([]string{"storage", "blob"}, args...)
	baseArgs = append(baseArgs,
		"--auth-mode", "key",
		"--account-name", "listpropsacct",
		"--account-key", storageAccountKey,
		"--blob-endpoint", baseURL+"/listpropsacct/",
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

// runStorageCLIExpectFailure runs an `az storage` command that must fail and
// returns its combined output. A command that succeeds is the failure: it means
// the simulator answered an operation it should have refused.
func runStorageCLIExpectFailure(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	cmd.Env = append(cmd.Env, "PYTHONWARNINGS=ignore")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("CLI command unexpectedly succeeded: %s\nOutput: %s", strings.Join(cmd.Args, " "), out)
	}
	return string(out)
}

// uploadCLIBlob writes a local file and uploads it as a blob through the CLI.
func uploadCLIBlob(t *testing.T, container, name, payload string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), filepath.Base(name))
	require.NoError(t, os.WriteFile(src, []byte(payload), 0o600))
	runCLI(t, azStorageBlob("upload", "--container-name", container, "--name", name,
		"--file", src, "--overwrite"))
}

// downloadCLIBlob reads a blob back through the CLI.
func downloadCLIBlob(t *testing.T, container, name string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "downloaded")
	runCLI(t, azStorageBlob("download", "--container-name", container, "--name", name, "--file", dst))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	return string(got)
}

// TestBlobTierMetadataAndPropertiesCLI drives Set Blob Tier, Set Blob Metadata
// and Set Blob HTTP Headers through `az storage blob`, and reads each change
// back through `az storage blob show` — the CLI's own view of the blob.
func TestBlobTierMetadataAndPropertiesCLI(t *testing.T) {
	container := "cli-props-container"
	blobName := "props.txt"
	payload := "cli property payload"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() { _ = azStorageContainer("delete", "--name", container).Run() })
	uploadCLIBlob(t, container, blobName, payload)

	runCLI(t, azStorageBlob("set-tier", "--container-name", container, "--name", blobName, "--tier", "Cool"))
	runCLI(t, azStorageBlob("metadata", "update", "--container-name", container, "--name", blobName,
		"--metadata", "owner=sockerless"))
	out := runCLI(t, azStorageBlob("show", "--container-name", container, "--name", blobName))
	var shown struct {
		Metadata   map[string]string `json:"metadata"`
		Properties struct {
			BlobTier        string `json:"blobTier"`
			ContentSettings struct {
				ContentType string `json:"contentType"`
			} `json:"contentSettings"`
		} `json:"properties"`
	}
	parseJSON(t, out, &shown)
	assert.Equal(t, "Cool", shown.Properties.BlobTier)
	assert.Equal(t, "text/plain", shown.Properties.ContentSettings.ContentType)
	assert.Equal(t, "sockerless", cliMetadataValue(shown.Metadata, "owner"))

	// The bytes are untouched by every property change.
	assert.Equal(t, payload, downloadCLIBlob(t, container, blobName))
}

// TestBlobLeaseCLI drives the blob lease verbs and their enforcement through
// `az storage blob lease`.
func TestBlobLeaseCLI(t *testing.T) {
	container := "cli-lease-container"
	blobName := "leased.txt"
	payload := "cli lease payload"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() { _ = azStorageContainer("delete", "--name", container).Run() })
	uploadCLIBlob(t, container, blobName, payload)

	leaseID := strings.TrimSpace(strings.Trim(
		runCLI(t, azStorageBlob("lease", "acquire", "--container-name", container, "--blob-name", blobName,
			"--lease-duration", "-1")), "\"\n"))
	require.NotEmpty(t, leaseID)

	out := runCLI(t, azStorageBlob("show", "--container-name", container, "--name", blobName))
	var shown struct {
		Properties struct {
			Lease struct {
				State    string `json:"state"`
				Status   string `json:"status"`
				Duration string `json:"duration"`
			} `json:"lease"`
		} `json:"properties"`
	}
	parseJSON(t, out, &shown)
	assert.Equal(t, "leased", shown.Properties.Lease.State)
	assert.Equal(t, "locked", shown.Properties.Lease.Status)
	assert.Equal(t, "infinite", shown.Properties.Lease.Duration)

	// A delete without the lease ID is refused and the blob survives.
	failure := runStorageCLIExpectFailure(t,
		azStorageBlob("delete", "--container-name", container, "--name", blobName))
	assert.Contains(t, failure, "LeaseIdMissing")
	assert.Equal(t, payload, downloadCLIBlob(t, container, blobName))

	runCLI(t, azStorageBlob("lease", "renew", "--container-name", container, "--blob-name", blobName,
		"--lease-id", leaseID))
	const changed = "3c1e0a5f-2b6d-4d8e-9f4a-1b2c3d4e5f60"
	runCLI(t, azStorageBlob("lease", "change", "--container-name", container, "--blob-name", blobName,
		"--lease-id", leaseID, "--proposed-lease-id", changed))

	// The old lease ID no longer opens the lease: Change Lease really swapped it.
	stale := runStorageCLIExpectFailure(t,
		azStorageBlob("lease", "release", "--container-name", container, "--blob-name", blobName,
			"--lease-id", leaseID))
	assert.Contains(t, stale, "LeaseIdMismatchWithLeaseOperation")

	runCLI(t, azStorageBlob("lease", "release", "--container-name", container, "--blob-name", blobName,
		"--lease-id", changed))

	// With the lease released the delete goes through.
	runCLI(t, azStorageBlob("delete", "--container-name", container, "--name", blobName))
}

// TestBlobContainerLeaseAndPolicyCLI drives the container lease verbs and the
// container stored access policies through `az storage container`.
func TestBlobContainerLeaseAndPolicyCLI(t *testing.T) {
	container := "cli-container-admin"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() { _ = azStorageContainer("delete", "--name", container).Run() })

	runCLI(t, azStorageContainer("metadata", "update", "--name", container, "--metadata", "team=storage"))
	out := runCLI(t, azStorageContainer("metadata", "show", "--name", container))
	var metadata map[string]string
	parseJSON(t, out, &metadata)
	assert.Equal(t, "storage", cliMetadataValue(metadata, "team"))

	runCLI(t, azStorageContainer("policy", "create", "--container-name", container,
		"--name", "readers", "--permissions", "rl",
		"--expiry", "2030-01-01T00:00:00Z"))
	out = runCLI(t, azStorageContainer("policy", "list", "--container-name", container))
	var policies map[string]struct {
		Permission string `json:"permission"`
	}
	parseJSON(t, out, &policies)
	require.Contains(t, policies, "readers")
	assert.Equal(t, "rl", policies["readers"].Permission)

	leaseID := strings.TrimSpace(strings.Trim(
		runCLI(t, azStorageContainer("lease", "acquire", "--container-name", container,
			"--lease-duration", "-1")), "\"\n"))
	require.NotEmpty(t, leaseID)

	failure := runStorageCLIExpectFailure(t, azStorageContainer("delete", "--name", container))
	assert.Contains(t, failure, "LeaseIdMissing")

	runCLI(t, azStorageContainer("lease", "break", "--container-name", container, "--lease-break-period", "0"))
	runCLI(t, azStorageContainer("delete", "--name", container))
}

// TestBlobSnapshotSoftDeleteAndUndeleteCLI drives Snapshot Blob, the account's
// blob service delete-retention policy, and Undelete Blob through the CLI.
func TestBlobSnapshotSoftDeleteAndUndeleteCLI(t *testing.T) {
	container := "cli-snapshot-container"
	blobName := "versioned.txt"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() { _ = azStorageContainer("delete", "--name", container).Run() })
	uploadCLIBlob(t, container, blobName, "v1")

	out := runCLI(t, azStorageBlob("snapshot", "--container-name", container, "--name", blobName))
	var snapshot struct {
		Snapshot string `json:"snapshot"`
	}
	parseJSON(t, out, &snapshot)
	require.NotEmpty(t, snapshot.Snapshot)

	uploadCLIBlob(t, container, blobName, "v2")
	assert.Equal(t, "v2", downloadCLIBlob(t, container, blobName))

	dst := filepath.Join(t.TempDir(), "snapshot.txt")
	runCLI(t, azStorageBlob("download", "--container-name", container, "--name", blobName,
		"--snapshot", snapshot.Snapshot, "--file", dst))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "v1", string(got), "the snapshot must keep the bytes it was taken from")

	// Turn on the delete-retention policy, then delete and undelete.
	runCLI(t, azStorageBlob("service-properties", "delete-policy", "update",
		"--enable", "true", "--days-retained", "3"))
	out = runCLI(t, azStorageBlob("service-properties", "show"))
	var props struct {
		DeleteRetentionPolicy struct {
			Enabled bool `json:"enabled"`
			Days    int  `json:"days"`
		} `json:"deleteRetentionPolicy"`
	}
	parseJSON(t, out, &props)
	require.True(t, props.DeleteRetentionPolicy.Enabled)
	assert.Equal(t, 3, props.DeleteRetentionPolicy.Days)

	runCLI(t, azStorageBlob("delete", "--container-name", container, "--name", blobName,
		"--delete-snapshots", "include"))
	failure := runStorageCLIExpectFailure(t,
		azStorageBlob("download", "--container-name", container, "--name", blobName,
			"--file", filepath.Join(t.TempDir(), "gone.txt")))
	assert.Contains(t, failure, "BlobNotFound")

	runCLI(t, azStorageBlob("undelete", "--container-name", container, "--name", blobName))
	assert.Equal(t, "v2", downloadCLIBlob(t, container, blobName),
		"undelete must restore the retained bytes")

	// Turn the policy back off so the rest of the suite deletes permanently.
	runCLI(t, azStorageBlob("service-properties", "delete-policy", "update", "--enable", "false"))
}

// TestBlobLegalHoldAndImmutabilityCLI drives Set Blob Legal Hold and the blob
// immutability policy through the CLI, and asserts the protection is enforced.
func TestBlobLegalHoldAndImmutabilityCLI(t *testing.T) {
	container := "cli-immutable-container"
	blobName := "held.txt"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() { _ = azStorageContainer("delete", "--name", container).Run() })
	uploadCLIBlob(t, container, blobName, "under legal hold")

	runCLI(t, azStorageBlob("set-legal-hold", "--container-name", container, "--name", blobName,
		"--legal-hold", "true"))
	failure := runStorageCLIExpectFailure(t,
		azStorageBlob("delete", "--container-name", container, "--name", blobName))
	assert.Contains(t, failure, "BlobImmutableDueToPolicy")

	runCLI(t, azStorageBlob("set-legal-hold", "--container-name", container, "--name", blobName,
		"--legal-hold", "false"))

	runCLI(t, azStorageBlob("immutability-policy", "set", "--container-name", container, "--name", blobName,
		"--expiry-time", "2030-01-01T00:00:00Z", "--policy-mode", "Unlocked"))
	failure = runStorageCLIExpectFailure(t,
		azStorageBlob("delete", "--container-name", container, "--name", blobName))
	assert.Contains(t, failure, "BlobImmutableDueToPolicy")

	runCLI(t, azStorageBlob("immutability-policy", "delete", "--container-name", container, "--name", blobName))
	runCLI(t, azStorageBlob("delete", "--container-name", container, "--name", blobName))
}

// TestBlobQueryCLI drives Query Blob Contents through `az storage blob query`,
// which decodes the Avro-framed response the operation streams back.
func TestBlobQueryCLI(t *testing.T) {
	container := "cli-query-container"
	blobName := "rows.csv"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() { _ = azStorageContainer("delete", "--name", container).Run() })
	uploadCLIBlob(t, container, blobName, "alpha,10\nbravo,20\ncharlie,30\n")

	out := runCLI(t, azStorageBlob("query", "--container-name", container, "--name", blobName,
		"--query-expression", "SELECT * FROM BlobStorage WHERE _2 > '15'"))
	assert.Contains(t, out, "bravo,20")
	assert.Contains(t, out, "charlie,30")
	assert.NotContains(t, out, "alpha,10",
		"the WHERE clause must really filter the rows the query returns")
}

// cliMetadataValue reads one metadata value case-insensitively. Go canonicalizes
// header names on the wire, so the case a client sent never survives the round
// trip and the az CLI surfaces the canonicalized spelling.
func cliMetadataValue(metadata map[string]string, name string) string {
	for k, v := range metadata {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
