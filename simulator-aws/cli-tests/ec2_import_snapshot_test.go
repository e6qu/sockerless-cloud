package aws_cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2CLI_ImportSnapshot imports a RAW disk image from Amazon S3 with the
// aws CLI, waits with `aws ec2 wait snapshot-imported`, and reads the task and
// the snapshot back.
func TestEC2CLI_ImportSnapshot(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	suffix := fmt.Sprint(time.Now().UnixNano())
	bucket := "cli-import-snap-" + suffix
	role := "cli-vmimport-" + suffix

	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	image := filepath.Join(t.TempDir(), "disk.raw")
	require.NoError(t, os.WriteFile(image, bytes.Repeat([]byte("cli-disk"), 1<<17), 0o644))
	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "disk.raw", "--body", image))
	runCLI(t, awsCLI("iam", "create-role", "--role-name", role, "--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"vmie.amazonaws.com"},"Action":"sts:AssumeRole"}]}`))
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", role, "--policy-name", "read-disk-images", "--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::`+bucket+`/*"}]}`))

	taskID := q("ec2", "import-snapshot", "--description", "cli-imp", "--role-name", role,
		"--disk-container", "Format=RAW,UserBucket={S3Bucket="+bucket+",S3Key=disk.raw}",
		"--query", "ImportTaskId", "--output", "text")
	require.NotEmpty(t, taskID)
	runCLI(t, awsCLI("ec2", "wait", "snapshot-imported", "--import-task-ids", taskID))

	snapshot := q("ec2", "describe-import-snapshot-tasks", "--import-task-ids", taskID,
		"--query", "ImportSnapshotTasks[0].SnapshotTaskDetail.SnapshotId", "--output", "text")
	require.True(t, strings.HasPrefix(snapshot, "snap-"), "describe-import-snapshot-tasks returned %q", snapshot)
	assert.Equal(t, "1", q("ec2", "describe-snapshots", "--snapshot-ids", snapshot,
		"--query", "Snapshots[0].VolumeSize", "--output", "text"))
}
