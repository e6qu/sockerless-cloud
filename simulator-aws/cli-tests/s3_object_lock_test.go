package aws_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3CLI_ObjectLockProtectsVersions drives Amazon S3 Object Lock through the
// aws CLI: a bucket created with Object Lock keeps versioning enabled, its
// default GOVERNANCE retention protects a new version until a delete bypasses
// governance retention, COMPLIANCE retention and a legal hold refuse every
// version delete, and a bucket without Object Lock refuses the operations.
func TestS3CLI_ObjectLockProtectsVersions(t *testing.T) {
	bucket := "cli-object-lock-protects"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket, "--object-lock-enabled-for-bucket"))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-versioning", "--bucket", bucket)), `"Enabled"`)
	out := runCLIExpectError(t, awsCLI("s3api", "put-bucket-versioning", "--bucket", bucket,
		"--versioning-configuration", "Status=Suspended"))
	assert.Contains(t, out, "InvalidBucketState")

	runCLI(t, awsCLI("s3api", "put-object-lock-configuration", "--bucket", bucket, "--object-lock-configuration",
		"ObjectLockEnabled=Enabled,Rule={DefaultRetention={Mode=GOVERNANCE,Days=1}}"))
	body := filepath.Join(tmpDir, "cli-object-lock-body")
	require.NoError(t, os.WriteFile(body, []byte("locked"), 0o644))
	put := func(key string, args ...string) string {
		var resp struct {
			VersionID string `json:"VersionId"`
		}
		parseJSON(t, runCLI(t, awsCLI(append([]string{"s3api", "put-object", "--bucket", bucket, "--key", key, "--body", body}, args...)...)), &resp)
		require.NotEmpty(t, resp.VersionID)
		return resp.VersionID
	}

	governed := put("governed")
	var head struct {
		ObjectLockMode            string `json:"ObjectLockMode"`
		ObjectLockRetainUntilDate string `json:"ObjectLockRetainUntilDate"`
		ObjectLockLegalHoldStatus string `json:"ObjectLockLegalHoldStatus"`
	}
	parseJSON(t, runCLI(t, awsCLI("s3api", "head-object", "--bucket", bucket, "--key", "governed")), &head)
	assert.Equal(t, "GOVERNANCE", head.ObjectLockMode, "the version takes the bucket's default retention")
	assert.NotEmpty(t, head.ObjectLockRetainUntilDate)
	out = runCLIExpectError(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", "governed", "--version-id", governed))
	assert.Contains(t, out, "AccessDenied")
	runCLI(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", "governed", "--version-id", governed,
		"--bypass-governance-retention"))

	until := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	complied := put("complied", "--object-lock-mode", "COMPLIANCE", "--object-lock-retain-until-date", until)
	out = runCLIExpectError(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", "complied", "--version-id", complied,
		"--bypass-governance-retention"))
	assert.Contains(t, out, "AccessDenied")
	out = runCLIExpectError(t, awsCLI("s3api", "put-object-retention", "--bucket", bucket, "--key", "complied",
		"--retention", "Mode=GOVERNANCE,RetainUntilDate="+time.Now().UTC().Add(2*time.Hour).Format(time.RFC3339)))
	assert.Contains(t, out, "AccessDenied", "COMPLIANCE retention does not relax to GOVERNANCE")

	held := put("held", "--object-lock-legal-hold-status", "ON")
	parseJSON(t, runCLI(t, awsCLI("s3api", "head-object", "--bucket", bucket, "--key", "held")), &head)
	assert.Equal(t, "ON", head.ObjectLockLegalHoldStatus)
	var deleted struct {
		Errors []struct {
			Code string `json:"Code"`
		} `json:"Errors"`
	}
	parseJSON(t, runCLI(t, awsCLI("s3api", "delete-objects", "--bucket", bucket, "--bypass-governance-retention",
		"--delete", "Objects=[{Key=held,VersionId="+held+"}]")), &deleted)
	require.Len(t, deleted.Errors, 1)
	assert.Equal(t, "AccessDenied", deleted.Errors[0].Code)

	plain := "cli-object-lock-plain"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", plain))
	out = runCLIExpectError(t, awsCLI("s3api", "put-object", "--bucket", plain, "--key", "x", "--body", body,
		"--object-lock-legal-hold-status", "ON"))
	assert.Contains(t, out, "InvalidRequest")
	out = runCLIExpectError(t, awsCLI("s3api", "put-object-lock-configuration", "--bucket", plain,
		"--object-lock-configuration", "ObjectLockEnabled=Enabled"))
	assert.True(t, strings.Contains(out, "InvalidBucketState"), "Object Lock needs versioning enabled first: %s", out)
	runCLI(t, awsCLI("s3api", "delete-bucket", "--bucket", plain))
}
