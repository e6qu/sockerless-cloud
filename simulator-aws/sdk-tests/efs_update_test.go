package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEFS_UpdateFileSystem switches an existing file system to provisioned
// throughput and back, asserting the FileSystemDescription round-trips the
// updated ThroughputMode + ProvisionedThroughputInMibps.
func TestEFS_UpdateFileSystem(t *testing.T) {
	client := efsClient()

	createOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String("efs-update-token"),
	})
	require.NoError(t, err)
	fsID := *createOut.FileSystemId
	defer client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(fsID)})

	updOut, err := client.UpdateFileSystem(ctx, &efs.UpdateFileSystemInput{
		FileSystemId:                 aws.String(fsID),
		ThroughputMode:               efstypes.ThroughputModeProvisioned,
		ProvisionedThroughputInMibps: aws.Float64(64),
	})
	require.NoError(t, err)
	assert.Equal(t, efstypes.ThroughputModeProvisioned, updOut.ThroughputMode)
	require.NotNil(t, updOut.ProvisionedThroughputInMibps)
	assert.Equal(t, 64.0, *updOut.ProvisionedThroughputInMibps)
	assert.Equal(t, fsID, aws.ToString(updOut.FileSystemId))

	// The change is durable: DescribeFileSystems reflects provisioned mode.
	desc, err := client.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{FileSystemId: aws.String(fsID)})
	require.NoError(t, err)
	require.Len(t, desc.FileSystems, 1)
	assert.Equal(t, efstypes.ThroughputModeProvisioned, desc.FileSystems[0].ThroughputMode)

	// Returning to bursting clears the provisioned value.
	backOut, err := client.UpdateFileSystem(ctx, &efs.UpdateFileSystemInput{
		FileSystemId:   aws.String(fsID),
		ThroughputMode: efstypes.ThroughputModeBursting,
	})
	require.NoError(t, err)
	assert.Equal(t, efstypes.ThroughputModeBursting, backOut.ThroughputMode)
	assert.Nil(t, backOut.ProvisionedThroughputInMibps)
}

// TestEFS_UpdateFileSystemProtection toggles the file system's
// ReplicationOverwriteProtection and asserts the returned
// FileSystemProtectionDescription carries the new status.
func TestEFS_UpdateFileSystemProtection(t *testing.T) {
	client := efsClient()

	createOut, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String("efs-protection-token"),
	})
	require.NoError(t, err)
	fsID := *createOut.FileSystemId
	defer client.DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: aws.String(fsID)})

	protOut, err := client.UpdateFileSystemProtection(ctx, &efs.UpdateFileSystemProtectionInput{
		FileSystemId:                   aws.String(fsID),
		ReplicationOverwriteProtection: efstypes.ReplicationOverwriteProtectionDisabled,
	})
	require.NoError(t, err)
	assert.Equal(t, efstypes.ReplicationOverwriteProtectionDisabled, protOut.ReplicationOverwriteProtection)

	// Re-enabling protection round-trips too.
	protOut2, err := client.UpdateFileSystemProtection(ctx, &efs.UpdateFileSystemProtectionInput{
		FileSystemId:                   aws.String(fsID),
		ReplicationOverwriteProtection: efstypes.ReplicationOverwriteProtectionEnabled,
	})
	require.NoError(t, err)
	assert.Equal(t, efstypes.ReplicationOverwriteProtectionEnabled, protOut2.ReplicationOverwriteProtection)
}
