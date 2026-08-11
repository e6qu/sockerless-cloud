package azure_cli_test

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	queueCLIAccountName = "cliqueueacct"
	tableCLIAccountName = "clitableacct"
)

// azStorageQueue builds an `az storage queue` / `az storage message`
// invocation against the simulator's path-style Queue endpoint. The account,
// the key and the endpoint are the only coordinates that differ from a real
// Azure invocation.
func azStorageQueue(group string, args ...string) *exec.Cmd {
	baseArgs := append([]string{"storage", group}, args...)
	baseArgs = append(baseArgs,
		"--auth-mode", "key",
		"--account-name", queueCLIAccountName,
		"--account-key", storageAccountKey,
		"--queue-endpoint", baseURL+"/queue/"+queueCLIAccountName+"/",
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

// azStorageTable builds an `az storage table` / `az storage entity`
// invocation against the simulator's path-style Table endpoint.
func azStorageTable(group string, args ...string) *exec.Cmd {
	baseArgs := append([]string{"storage", group}, args...)
	baseArgs = append(baseArgs,
		"--auth-mode", "key",
		"--account-name", tableCLIAccountName,
		"--account-key", storageAccountKey,
		"--table-endpoint", baseURL+"/table/"+tableCLIAccountName+"/",
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

// TestBlobCopyCLI drives Copy Blob through `az storage blob copy start` with
// a path-style source URI, then reads the destination back.
func TestBlobCopyCLI(t *testing.T) {
	container := "cli-copy-container"
	srcName := "copy-source.txt"
	dstName := "copy-dest.txt"
	payload := "copied through az storage blob copy start"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() {
		_ = azStorageContainer("delete", "--name", container).Run()
	})

	src := filepath.Join(t.TempDir(), srcName)
	require.NoError(t, os.WriteFile(src, []byte(payload), 0o600))
	runCLI(t, azStorageBlob("upload", "--container-name", container, "--name", srcName, "--file", src))

	out := runCLI(t, azStorageBlob("copy", "start",
		"--destination-container", container,
		"--destination-blob", dstName,
		"--source-uri", baseURL+"/listpropsacct/"+container+"/"+srcName))
	var started struct {
		CopyID     string `json:"copy_id"`
		CopyStatus string `json:"copy_status"`
	}
	parseJSON(t, out, &started)
	require.NotEmpty(t, started.CopyID, "Copy Blob must return a copy ID: %s", out)
	require.Equal(t, "success", started.CopyStatus, "the simulator completes the copy synchronously: %s", out)

	dst := filepath.Join(t.TempDir(), dstName)
	runCLI(t, azStorageBlob("download", "--container-name", container, "--name", dstName, "--file", dst))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, payload, string(got), "the copied blob must hold the source bytes")
}

// TestBlobBlockStagingCLI drives Put Block, Get Block List and Put Block List
// through `az rest` against the path-style Blob endpoint — the same wire
// requests the SDK's block upload issues.
func TestBlobBlockStagingCLI(t *testing.T) {
	container := "cli-block-container"
	blobName := "staged.txt"

	runCLI(t, azStorageContainer("create", "--name", container))
	t.Cleanup(func() {
		_ = azStorageContainer("delete", "--name", container).Run()
	})

	blobURL := baseURL + "/listpropsacct/" + container + "/" + blobName
	blockA := base64.StdEncoding.EncodeToString([]byte("block-000001"))
	blockB := base64.StdEncoding.EncodeToString([]byte("block-000002"))

	// Stage two blocks.
	runCLI(t, azRest("PUT", blobURL+"?comp=block&blockid="+blockA, "hello ",
		"--headers", "x-ms-version=2023-11-03"))
	runCLI(t, azRest("PUT", blobURL+"?comp=block&blockid="+blockB, "from blocks",
		"--headers", "x-ms-version=2023-11-03"))

	// The uncommitted block list names both blocks.
	out := runCLI(t, azRest("GET", blobURL+"?comp=blocklist&blocklisttype=uncommitted", "",
		"--headers", "x-ms-version=2023-11-03"))
	assert.Contains(t, out, blockA, "Get Block List must name the first uncommitted block")
	assert.Contains(t, out, blockB, "Get Block List must name the second uncommitted block")

	// Commit both blocks in order.
	blockList := `<?xml version="1.0" encoding="utf-8"?><BlockList><Latest>` + blockA + `</Latest><Latest>` + blockB + `</Latest></BlockList>`
	runCLI(t, azRest("PUT", blobURL+"?comp=blocklist", blockList,
		"--headers", "x-ms-version=2023-11-03", "Content-Type=application/xml"))

	// The committed block list now names them; the uncommitted list is empty.
	out = runCLI(t, azRest("GET", blobURL+"?comp=blocklist&blocklisttype=committed", "",
		"--headers", "x-ms-version=2023-11-03"))
	assert.Contains(t, out, blockA, "the committed block list must name the first block")
	assert.Contains(t, out, blockB, "the committed block list must name the second block")

	// The committed blob holds the concatenated block bytes.
	dst := filepath.Join(t.TempDir(), blobName)
	runCLI(t, azStorageBlob("download", "--container-name", container, "--name", blobName, "--file", dst))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "hello from blocks", string(got))
}

// TestFilesShareListCLI drives List Shares through `az storage share list`.
func TestFilesShareListCLI(t *testing.T) {
	const share = "cli-share-list"
	runCLI(t, azStorageFile("share", "create", "--name", share))
	t.Cleanup(func() {
		_ = azStorageFile("share", "delete", "--name", share).Run()
	})

	out := runCLI(t, azStorageFile("share", "list"))
	var shares []struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &shares)
	found := false
	for _, entry := range shares {
		if entry.Name == share {
			found = true
		}
	}
	assert.True(t, found, "az storage share list must name the created share: %s", out)
}

// TestQueueListAndClearMessagesCLI drives List Queues and Clear Messages
// through `az storage queue list` and `az storage message clear`.
func TestQueueListAndClearMessagesCLI(t *testing.T) {
	queue := "cli-clear-queue"

	runCLI(t, azStorageQueue("queue", "create", "--name", queue))
	t.Cleanup(func() {
		_ = azStorageQueue("queue", "delete", "--name", queue).Run()
	})

	out := runCLI(t, azStorageQueue("queue", "list"))
	var queues []struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &queues)
	found := false
	for _, entry := range queues {
		if entry.Name == queue {
			found = true
		}
	}
	require.True(t, found, "az storage queue list must name the created queue: %s", out)

	runCLI(t, azStorageQueue("message", "put", "--queue-name", queue, "--content", "first"))
	runCLI(t, azStorageQueue("message", "put", "--queue-name", queue, "--content", "second"))

	out = runCLI(t, azStorageQueue("message", "peek", "--queue-name", queue, "--num-messages", "2"))
	var peeked []struct {
		Content string `json:"content"`
	}
	parseJSON(t, out, &peeked)
	require.Len(t, peeked, 2, "both messages must be enqueued before the clear: %s", out)

	runCLI(t, azStorageQueue("message", "clear", "--queue-name", queue))

	out = runCLI(t, azStorageQueue("message", "peek", "--queue-name", queue))
	var afterClear []struct {
		Content string `json:"content"`
	}
	parseJSON(t, out, &afterClear)
	assert.Empty(t, afterClear, "no message may survive az storage message clear: %s", out)
}

// TestTableListAndUpsertEntityCLI drives List Tables and Upsert Entity
// (replace + merge modes) through `az storage table list`, `az storage entity
// replace` and `az storage entity merge`.
func TestTableListAndUpsertEntityCLI(t *testing.T) {
	table := "CliUpsertTable"

	runCLI(t, azStorageTable("table", "create", "--name", table))
	t.Cleanup(func() {
		_ = azStorageTable("table", "delete", "--name", table).Run()
	})

	out := runCLI(t, azStorageTable("table", "list"))
	var tables []struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &tables)
	found := false
	for _, entry := range tables {
		if entry.Name == table {
			found = true
		}
	}
	require.True(t, found, "az storage table list must name the created table: %s", out)

	runCLI(t, azStorageTable("entity", "insert", "--table-name", table,
		"--entity", "PartitionKey=p1", "RowKey=r1", "Color=red", "Size=1"))

	// Replace-mode upsert swaps the entity wholesale: Size disappears.
	runCLI(t, azStorageTable("entity", "replace", "--table-name", table,
		"--entity", "PartitionKey=p1", "RowKey=r1", "Color=blue"))
	out = runCLI(t, azStorageTable("entity", "show", "--table-name", table,
		"--partition-key", "p1", "--row-key", "r1"))
	var replaced map[string]any
	parseJSON(t, out, &replaced)
	assert.Equal(t, "blue", replaced["Color"], out)
	assert.NotContains(t, replaced, "Size", "replace-mode upsert must drop properties the new entity omits: %s", out)

	// Merge-mode upsert folds new properties into the stored entity.
	runCLI(t, azStorageTable("entity", "merge", "--table-name", table,
		"--entity", "PartitionKey=p1", "RowKey=r1", "Weight=7"))
	out = runCLI(t, azStorageTable("entity", "show", "--table-name", table,
		"--partition-key", "p1", "--row-key", "r1"))
	var merged map[string]any
	parseJSON(t, out, &merged)
	assert.Equal(t, "blue", merged["Color"], "merge-mode upsert must keep existing properties: %s", out)
	// az storage entity coerces numeric-looking values to Edm integers.
	assert.Equal(t, float64(7), merged["Weight"], "merge-mode upsert must add the new property: %s", out)
}
