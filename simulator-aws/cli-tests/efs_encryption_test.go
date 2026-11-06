package aws_cli_test

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEFS_CLIFileSystemEncryption expects `aws efs create-file-system
// --encrypted` to protect the file system with the AWS managed key
// aws/elasticfilesystem, and a reused creation token to be refused.
func TestEFS_CLIFileSystemEncryption(t *testing.T) {
	t.Parallel()
	token := uniquename.New("cli-efs-encrypted")
	var fs struct {
		FileSystemId string `json:"FileSystemId"`
		Encrypted    bool   `json:"Encrypted"`
		KmsKeyId     string `json:"KmsKeyId"`
	}
	parseJSON(t, runCLI(t, awsCLI("efs", "create-file-system",
		"--creation-token", token, "--encrypted", "--output", "json")), &fs)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("efs", "delete-file-system", "--file-system-id", fs.FileSystemId))
	})
	require.True(t, fs.Encrypted)

	var key struct {
		KeyMetadata struct {
			Arn        string `json:"Arn"`
			KeyManager string `json:"KeyManager"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, runCLI(t, awsCLI("kms", "describe-key",
		"--key-id", "alias/aws/elasticfilesystem", "--output", "json")), &key)
	assert.Equal(t, "AWS", key.KeyMetadata.KeyManager)
	assert.Equal(t, key.KeyMetadata.Arn, fs.KmsKeyId)

	out := runCLIExpectError(t, awsCLI("efs", "create-file-system", "--creation-token", token))
	assert.Contains(t, out, "FileSystemAlreadyExists")

	out = runCLIExpectError(t, awsCLI("efs", "create-file-system",
		"--creation-token", uniquename.New("cli-efs-key-only"), "--kms-key-id", key.KeyMetadata.Arn))
	assert.Contains(t, out, "BadRequest")
}
