package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEFS_UpdateFileSystem_CLI switches an existing file system to provisioned
// throughput and asserts the returned FileSystemDescription reflects it.
func TestEFS_UpdateFileSystem_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", "cli-efs-update",
		"--output", "json",
	))
	var created struct {
		FileSystemId string `json:"FileSystemId"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.FileSystemId)
	t.Cleanup(func() {
		runCLI(t, awsCLI("efs", "delete-file-system", "--file-system-id", created.FileSystemId))
	})

	out = runCLI(t, awsCLI("efs", "update-file-system",
		"--file-system-id", created.FileSystemId,
		"--throughput-mode", "provisioned",
		"--provisioned-throughput-in-mibps", "64",
		"--output", "json",
	))
	var upd struct {
		ThroughputMode               string  `json:"ThroughputMode"`
		ProvisionedThroughputInMibps float64 `json:"ProvisionedThroughputInMibps"`
	}
	parseJSON(t, out, &upd)
	assert.Equal(t, "provisioned", upd.ThroughputMode)
	assert.Equal(t, 64.0, upd.ProvisionedThroughputInMibps)
}

// TestEFS_UpdateFileSystemProtection_CLI toggles replication overwrite protection.
func TestEFS_UpdateFileSystemProtection_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", "cli-efs-protection",
		"--output", "json",
	))
	var created struct {
		FileSystemId string `json:"FileSystemId"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.FileSystemId)
	t.Cleanup(func() {
		runCLI(t, awsCLI("efs", "delete-file-system", "--file-system-id", created.FileSystemId))
	})

	out = runCLI(t, awsCLI("efs", "update-file-system-protection",
		"--file-system-id", created.FileSystemId,
		"--replication-overwrite-protection", "DISABLED",
		"--output", "json",
	))
	var prot struct {
		ReplicationOverwriteProtection string `json:"ReplicationOverwriteProtection"`
	}
	parseJSON(t, out, &prot)
	assert.Equal(t, "DISABLED", prot.ReplicationOverwriteProtection)
}
