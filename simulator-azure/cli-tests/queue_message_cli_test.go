package azure_cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQueueMessageUpdateCLI drives `az storage message update`, the command a
// consumer still working on a message runs to keep it invisible to everyone
// else, and then the pop-receipt rules that make the update the dequeuer's
// alone.
func TestQueueMessageUpdateCLI(t *testing.T) {
	const queue = "cli-update-queue"

	runCLI(t, azStorageQueue("queue", "create", "--name", queue))
	t.Cleanup(func() {
		_ = azStorageQueue("queue", "delete", "--name", queue).Run()
	})

	runCLI(t, azStorageQueue("message", "put", "--queue-name", queue, "--content", "work item"))

	out := runCLI(t, azStorageQueue("message", "get", "--queue-name", queue, "--visibility-timeout", "1"))
	var dequeued []struct {
		ID         string `json:"id"`
		PopReceipt string `json:"popReceipt"`
	}
	parseJSON(t, out, &dequeued)
	require.Len(t, dequeued, 1, "az storage message get must hand out one message: %s", out)
	require.NotEmpty(t, dequeued[0].PopReceipt, "the dequeue must carry a pop receipt: %s", out)

	// A pop receipt the service never issued updates nothing.
	err := azStorageQueue("message", "update", "--queue-name", queue,
		"--id", dequeued[0].ID, "--pop-receipt", "not-the-receipt",
		"--visibility-timeout", "600", "--content", "work item").Run()
	require.Error(t, err, "az storage message update must reject a pop receipt the service did not issue")

	out = runCLI(t, azStorageQueue("message", "update", "--queue-name", queue,
		"--id", dequeued[0].ID, "--pop-receipt", dequeued[0].PopReceipt,
		"--visibility-timeout", "600", "--content", "work item, still running"))
	var updated struct {
		PopReceipt      string `json:"popReceipt"`
		TimeNextVisible string `json:"timeNextVisible"`
	}
	parseJSON(t, out, &updated)
	require.NotEmpty(t, updated.PopReceipt, "az storage message update must return a fresh pop receipt: %s", out)
	assert.NotEqual(t, dequeued[0].PopReceipt, updated.PopReceipt,
		"the fresh pop receipt must supersede the one the dequeue handed out: %s", out)
	assert.NotEmpty(t, updated.TimeNextVisible,
		"az storage message update must report when the message reappears: %s", out)

	// The extension holds: the message is not handed to another consumer.
	out = runCLI(t, azStorageQueue("message", "peek", "--queue-name", queue))
	var peeked []struct {
		Content string `json:"content"`
	}
	parseJSON(t, out, &peeked)
	assert.Empty(t, peeked, "the message must stay invisible for the timeout the update set: %s", out)

	// The superseded receipt can no longer delete the message; the fresh one can.
	err = azStorageQueue("message", "delete", "--queue-name", queue,
		"--id", dequeued[0].ID, "--pop-receipt", dequeued[0].PopReceipt).Run()
	require.Error(t, err, "the superseded pop receipt must not delete the message")
	runCLI(t, azStorageQueue("message", "delete", "--queue-name", queue,
		"--id", dequeued[0].ID, "--pop-receipt", updated.PopReceipt))
}

// TestQueueAccessPolicyCLI drives the queue's stored access policies through
// `az storage queue policy`, the signed identifiers a shared access signature
// is issued against.
func TestQueueAccessPolicyCLI(t *testing.T) {
	const queue = "cli-acl-queue"

	runCLI(t, azStorageQueue("queue", "create", "--name", queue))
	t.Cleanup(func() {
		_ = azStorageQueue("queue", "delete", "--name", queue).Run()
	})

	runCLI(t, azStorageQueuePolicy("create", "--queue-name", queue,
		"--name", "cli-consumer", "--permissions", "rap"))

	out := runCLI(t, azStorageQueuePolicy("list", "--queue-name", queue))
	var policies map[string]struct {
		Permission string `json:"permission"`
	}
	parseJSON(t, out, &policies)
	policy, ok := policies["cli-consumer"]
	require.True(t, ok, "az storage queue policy list must name the stored policy: %s", out)
	assert.Equal(t, "rap", policy.Permission,
		"az storage queue policy list must read back the stored permission: %s", out)

	runCLI(t, azStorageQueuePolicy("delete", "--queue-name", queue, "--name", "cli-consumer"))
	out = runCLI(t, azStorageQueuePolicy("list", "--queue-name", queue))
	var afterDelete map[string]any
	parseJSON(t, out, &afterDelete)
	assert.Empty(t, afterDelete, "the deleted policy must be gone: %s", out)
}

// azStorageQueuePolicy builds an `az storage queue policy` invocation. The
// stored-access-policy commands authorize with the account key alone and reject
// --auth-mode, so they are spelled out here rather than through azStorageQueue.
func azStorageQueuePolicy(args ...string) *exec.Cmd {
	baseArgs := append([]string{"storage", "queue", "policy"}, args...)
	baseArgs = append(baseArgs,
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
