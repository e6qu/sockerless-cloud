package aws_cli_test

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// s3PutVersionCLI writes body to bucket/key through `aws s3api put-object` and
// returns the version id S3 reports.
func s3PutVersionCLI(t *testing.T, bucket, key, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "body")
	require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
	var out struct {
		VersionId string `json:"VersionId"`
	}
	parseJSON(t, runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", key, "--body", file)), &out)
	return out.VersionId
}

// s3GetVersionCLI reads one version of bucket/key ("" for the current object).
func s3GetVersionCLI(t *testing.T, bucket, key, version string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "out")
	args := []string{"s3api", "get-object", "--bucket", bucket, "--key", key}
	if version != "" {
		args = append(args, "--version-id", version)
	}
	runCLI(t, awsCLI(append(args, file)...))
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	return string(data)
}

type s3VersionListingCLI struct {
	Versions []struct {
		Key       string `json:"Key"`
		VersionId string `json:"VersionId"`
		IsLatest  bool   `json:"IsLatest"`
	} `json:"Versions"`
	DeleteMarkers []struct {
		Key       string `json:"Key"`
		VersionId string `json:"VersionId"`
		IsLatest  bool   `json:"IsLatest"`
	} `json:"DeleteMarkers"`
}

// TestS3_ObjectVersioningCLI drives a versioning-enabled bucket through the
// aws CLI: versions accumulate, a delete leaves a delete marker, version ids
// address reads, copies and deletes, and a suspended bucket writes the null
// version.
func TestS3_ObjectVersioningCLI(t *testing.T) {
	const bucket = "cli-object-versioning"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("s3api", "put-bucket-versioning", "--bucket", bucket,
		"--versioning-configuration", "Status=Enabled"))

	first := s3PutVersionCLI(t, bucket, "doc", "first")
	second := s3PutVersionCLI(t, bucket, "doc", "second")
	require.NotEmpty(t, first)
	assert.NotEqual(t, first, second)
	assert.Equal(t, "second", s3GetVersionCLI(t, bucket, "doc", ""))
	assert.Equal(t, "first", s3GetVersionCLI(t, bucket, "doc", first))

	var deleted struct {
		DeleteMarker bool   `json:"DeleteMarker"`
		VersionId    string `json:"VersionId"`
	}
	parseJSON(t, runCLI(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", "doc")), &deleted)
	assert.True(t, deleted.DeleteMarker)
	require.NotEmpty(t, deleted.VersionId)
	assert.Contains(t, runCLIExpectError(t, awsCLI("s3api", "head-object", "--bucket", bucket, "--key", "doc")), "404")

	var listing s3VersionListingCLI
	parseJSON(t, runCLI(t, awsCLI("s3api", "list-object-versions", "--bucket", bucket)), &listing)
	require.Len(t, listing.Versions, 2)
	assert.Equal(t, second, listing.Versions[0].VersionId)
	assert.False(t, listing.Versions[0].IsLatest)
	require.Len(t, listing.DeleteMarkers, 1)
	assert.Equal(t, deleted.VersionId, listing.DeleteMarkers[0].VersionId)
	assert.True(t, listing.DeleteMarkers[0].IsLatest)

	runCLI(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", "doc", "--version-id", deleted.VersionId))
	assert.Equal(t, "second", s3GetVersionCLI(t, bucket, "doc", ""), "removing the delete marker restores the key")

	var copied struct {
		CopySourceVersionId string `json:"CopySourceVersionId"`
		VersionId           string `json:"VersionId"`
	}
	parseJSON(t, runCLI(t, awsCLI("s3api", "copy-object", "--bucket", bucket, "--key", "restored",
		"--copy-source", bucket+"/doc?versionId="+first)), &copied)
	assert.Equal(t, first, copied.CopySourceVersionId)
	assert.NotEmpty(t, copied.VersionId)
	assert.Equal(t, "first", s3GetVersionCLI(t, bucket, "restored", ""))

	var batch struct {
		Deleted []struct {
			Key       string `json:"Key"`
			VersionId string `json:"VersionId"`
		} `json:"Deleted"`
	}
	parseJSON(t, runCLI(t, awsCLI("s3api", "delete-objects", "--bucket", bucket, "--delete",
		`{"Objects":[{"Key":"doc","VersionId":"`+second+`"}]}`)), &batch)
	require.Len(t, batch.Deleted, 1)
	assert.Equal(t, second, batch.Deleted[0].VersionId)
	assert.Equal(t, "first", s3GetVersionCLI(t, bucket, "doc", ""), "removing the current version makes the previous one current")

	runCLI(t, awsCLI("s3api", "put-bucket-versioning", "--bucket", bucket,
		"--versioning-configuration", "Status=Suspended"))
	assert.Equal(t, "null", s3PutVersionCLI(t, bucket, "doc", "suspended"))
	assert.Equal(t, "suspended", s3GetVersionCLI(t, bucket, "doc", "null"))
	assert.Equal(t, "first", s3GetVersionCLI(t, bucket, "doc", first))
}

// TestS3_DeleteObjectsRefusesEntriesCLI deletes two keys as a caller that may
// delete one: delete-objects succeeds and reports the other as AccessDenied.
func TestS3_DeleteObjectsRefusesEntriesCLI(t *testing.T) {
	const bucket = "cli-delete-objects-partial"
	const user = "cli-s3-delete-scratch"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	s3PutVersionCLI(t, bucket, "scratch/tmp", "x")
	s3PutVersionCLI(t, bucket, "records/keep", "y")
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user, "--policy-name", "scratch",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:DeleteObject",`+
			`"Resource":"arn:aws:s3:::`+bucket+`/scratch/*"}]}`))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user-policy", "--user-name", user, "--policy-name", "scratch").Run() })
	var key struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user)), &key)
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-access-key", "--user-name", user, "--access-key-id", key.AccessKey.AccessKeyId).Run()
	})

	cmd := awsCLI("s3api", "delete-objects", "--bucket", bucket, "--delete",
		`{"Objects":[{"Key":"scratch/tmp"},{"Key":"records/keep"}]}`)
	cmd.Env = withCreds(cmd.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
	var result struct {
		Deleted []struct {
			Key string `json:"Key"`
		} `json:"Deleted"`
		Errors []struct {
			Key  string `json:"Key"`
			Code string `json:"Code"`
		} `json:"Errors"`
	}
	parseJSON(t, runCLI(t, cmd), &result)
	require.Len(t, result.Deleted, 1)
	assert.Equal(t, "scratch/tmp", result.Deleted[0].Key)
	require.Len(t, result.Errors, 1)
	assert.Equal(t, "records/keep", result.Errors[0].Key)
	assert.Equal(t, "AccessDenied", result.Errors[0].Code)
	assert.Equal(t, "y", s3GetVersionCLI(t, bucket, "records/keep", ""))
}

// TestLambda_CreateFunctionFromS3ObjectVersionCLI deploys a function from a
// noncurrent version of its package with `--code S3ObjectVersion=…`.
func TestLambda_CreateFunctionFromS3ObjectVersionCLI(t *testing.T) {
	const bucket = "cli-lambda-code-versions"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("s3api", "put-bucket-versioning", "--bucket", bucket,
		"--versioning-configuration", "Status=Enabled"))
	put := func(zipPath string) string {
		var out struct {
			VersionId string `json:"VersionId"`
		}
		parseJSON(t, runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "function.zip", "--body", zipPath)), &out)
		return out.VersionId
	}
	deployed := createLambdaSourceZip(t, "versioned-deployed", `exports.handler = async () => "deployed";`)
	version := put(deployed)
	put(createLambdaSourceZip(t, "versioned-superseded", `exports.handler = async () => "superseded";`))

	var created struct {
		CodeSha256 string `json:"CodeSha256"`
	}
	parseJSON(t, runCLI(t, awsCLI("lambda", "create-function", "--function-name", "cli-versioned-code",
		"--runtime", "nodejs20.x", "--role", "arn:aws:iam::123456789012:role/test-role", "--handler", "index.handler",
		"--code", "S3Bucket="+bucket+",S3Key=function.zip,S3ObjectVersion="+version)), &created)
	t.Cleanup(func() { _ = awsCLI("lambda", "delete-function", "--function-name", "cli-versioned-code").Run() })
	data, err := os.ReadFile(deployed)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	assert.Equal(t, base64.StdEncoding.EncodeToString(sum[:]), created.CodeSha256)
}
