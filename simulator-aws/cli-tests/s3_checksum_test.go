package aws_cli_test

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// S3 keeps the checksum an upload states and returns it with
// --checksum-mode ENABLED; a checksum that does not match is BadDigest.
func TestS3CLI_AdditionalChecksums(t *testing.T) {
	t.Parallel()
	bucket := uniquename.New("cli-checksums")
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	body := []byte("checksummed by the CLI")
	file := filepath.Join(t.TempDir(), "body")
	require.NoError(t, os.WriteFile(file, body, 0o600))
	digest := sha256.Sum256(body)

	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "sha", "--body", file, "--checksum-algorithm", "SHA256"))
	head := runCLI(t, awsCLI("s3api", "head-object", "--bucket", bucket, "--key", "sha", "--checksum-mode", "ENABLED",
		"--query", "ChecksumSHA256", "--output", "text"))
	assert.Contains(t, head, base64.StdEncoding.EncodeToString(digest[:]))

	out := runCLIExpectError(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "wrong", "--body", file,
		"--checksum-crc32", "AAAAAA=="))
	assert.Contains(t, out, "BadDigest")
}
