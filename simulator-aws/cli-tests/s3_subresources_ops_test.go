package aws_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// s3PutObjectCLI creates a bucket (tolerant) and puts a body file.
func s3PutObjectCLI(t *testing.T, bucket, key, content string) {
	t.Helper()
	_ = awsCLI("s3api", "create-bucket", "--bucket", bucket).Run()
	f := filepath.Join(tmpDir, "s3op-"+key)
	require.NoError(t, os.MkdirAll(filepath.Dir(f), 0o755))
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))
	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", key, "--body", f))
}

func TestS3CLI_ObjectAcl(t *testing.T) {
	bucket, key := "cli-obj-acl-bucket", "acl.txt"
	s3PutObjectCLI(t, bucket, key, "acl body")

	out := runCLI(t, awsCLI("s3api", "get-object-acl", "--bucket", bucket, "--key", key))
	assert.Contains(t, out, "FULL_CONTROL")

	runCLI(t, awsCLI("s3api", "put-object-acl", "--bucket", bucket, "--key", key, "--acl", "private"))
	out = runCLI(t, awsCLI("s3api", "get-object-acl", "--bucket", bucket, "--key", key))
	assert.Contains(t, out, "Owner")

	_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", key).Run()
	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}

func TestS3CLI_ObjectAttributes(t *testing.T) {
	bucket, key := "cli-obj-attrs-bucket", "attrs.txt"
	s3PutObjectCLI(t, bucket, key, "attributes")

	out := runCLI(t, awsCLI("s3api", "get-object-attributes",
		"--bucket", bucket, "--key", key,
		"--object-attributes", "ETag", "ObjectSize", "StorageClass"))
	var resp struct {
		ETag         string `json:"ETag"`
		ObjectSize   int64  `json:"ObjectSize"`
		StorageClass string `json:"StorageClass"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	assert.NotEmpty(t, resp.ETag)
	assert.Equal(t, int64(len("attributes")), resp.ObjectSize)

	_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", key).Run()
	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}

func TestS3CLI_ObjectLegalHold(t *testing.T) {
	bucket, key := "cli-obj-legalhold-bucket", "lh.txt"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket, "--object-lock-enabled-for-bucket"))
	f := filepath.Join(tmpDir, "cli-lh-body")
	require.NoError(t, os.WriteFile(f, []byte("lh"), 0o644))
	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", key, "--body", f))

	runCLI(t, awsCLI("s3api", "put-object-legal-hold",
		"--bucket", bucket, "--key", key, "--legal-hold", "Status=ON"))

	out := runCLI(t, awsCLI("s3api", "get-object-legal-hold", "--bucket", bucket, "--key", key))
	var resp struct {
		LegalHold struct {
			Status string `json:"Status"`
		} `json:"LegalHold"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	assert.Equal(t, "ON", resp.LegalHold.Status)

	_ = awsCLI("s3api", "put-object-legal-hold", "--bucket", bucket, "--key", key, "--legal-hold", "Status=OFF").Run()
	_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", key).Run()
	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}

func TestS3CLI_ObjectRetention(t *testing.T) {
	bucket, key := "cli-obj-retention-bucket", "ret.txt"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket, "--object-lock-enabled-for-bucket"))
	f := filepath.Join(tmpDir, "cli-ret-body")
	require.NoError(t, os.WriteFile(f, []byte("ret"), 0o644))
	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", key, "--body", f))

	runCLI(t, awsCLI("s3api", "put-object-retention",
		"--bucket", bucket, "--key", key,
		"--retention", "Mode=GOVERNANCE,RetainUntilDate=2999-01-01T00:00:00Z"))

	out := runCLI(t, awsCLI("s3api", "get-object-retention", "--bucket", bucket, "--key", key))
	var resp struct {
		Retention struct {
			Mode            string `json:"Mode"`
			RetainUntilDate string `json:"RetainUntilDate"`
		} `json:"Retention"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	assert.Equal(t, "GOVERNANCE", resp.Retention.Mode)

	_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", key, "--bypass-governance-retention").Run()
	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}

func TestS3CLI_ObjectLockConfiguration(t *testing.T) {
	bucket := "cli-obj-lock-cfg-bucket"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket, "--object-lock-enabled-for-bucket"))

	runCLI(t, awsCLI("s3api", "put-object-lock-configuration",
		"--bucket", bucket,
		"--object-lock-configuration",
		"ObjectLockEnabled=Enabled,Rule={DefaultRetention={Mode=COMPLIANCE,Days=10}}"))

	out := runCLI(t, awsCLI("s3api", "get-object-lock-configuration", "--bucket", bucket))
	var resp struct {
		ObjectLockConfiguration struct {
			ObjectLockEnabled string `json:"ObjectLockEnabled"`
			Rule              struct {
				DefaultRetention struct {
					Mode string `json:"Mode"`
					Days int    `json:"Days"`
				} `json:"DefaultRetention"`
			} `json:"Rule"`
		} `json:"ObjectLockConfiguration"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	assert.Equal(t, "Enabled", resp.ObjectLockConfiguration.ObjectLockEnabled)
	assert.Equal(t, "COMPLIANCE", resp.ObjectLockConfiguration.Rule.DefaultRetention.Mode)
	assert.Equal(t, 10, resp.ObjectLockConfiguration.Rule.DefaultRetention.Days)

	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}

func TestS3CLI_ObjectTorrent(t *testing.T) {
	bucket, key := "cli-obj-torrent-bucket", "torrent.txt"
	s3PutObjectCLI(t, bucket, key, "torrent payload")

	outFile := filepath.Join(tmpDir, "cli-object.torrent")
	runCLI(t, awsCLI("s3api", "get-object-torrent", "--bucket", bucket, "--key", key, outFile))
	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	assert.Equal(t, byte('d'), data[0])

	_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", key).Run()
	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}

func TestS3CLI_RestoreObject(t *testing.T) {
	bucket, key := "cli-obj-restore-bucket", "restore.txt"
	s3PutObjectCLI(t, bucket, key, "restore payload")

	runCLI(t, awsCLI("s3api", "restore-object",
		"--bucket", bucket, "--key", key,
		"--restore-request", "Days=1,GlacierJobParameters={Tier=Standard}"))

	_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", key).Run()
	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}

func TestS3CLI_UploadPartCopy(t *testing.T) {
	bucket := "cli-uploadpartcopy-bucket"
	srcKey, dstKey := "src.txt", "dst.txt"
	s3PutObjectCLI(t, bucket, srcKey, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ0123")

	createOut := runCLI(t, awsCLI("s3api", "create-multipart-upload", "--bucket", bucket, "--key", dstKey))
	var created struct {
		UploadID string `json:"UploadId"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	require.NotEmpty(t, created.UploadID)

	copyOut := runCLI(t, awsCLI("s3api", "upload-part-copy",
		"--bucket", bucket, "--key", dstKey,
		"--upload-id", created.UploadID, "--part-number", "1",
		"--copy-source", bucket+"/"+srcKey,
		"--copy-source-range", "bytes=0-9"))
	var copied struct {
		CopyPartResult struct {
			ETag string `json:"ETag"`
		} `json:"CopyPartResult"`
	}
	require.NoError(t, json.Unmarshal([]byte(copyOut), &copied))
	require.NotEmpty(t, copied.CopyPartResult.ETag)

	runCLI(t, awsCLI("s3api", "complete-multipart-upload",
		"--bucket", bucket, "--key", dstKey, "--upload-id", created.UploadID,
		"--multipart-upload", "Parts=[{ETag="+copied.CopyPartResult.ETag+",PartNumber=1}]"))

	_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", srcKey).Run()
	_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", dstKey).Run()
	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}

func TestS3CLI_ListObjectsV1(t *testing.T) {
	bucket := "cli-list-v1-bucket"
	_ = awsCLI("s3api", "create-bucket", "--bucket", bucket).Run()
	for _, key := range []string{"apple", "banana", "dir/one", "dir/two"} {
		f := filepath.Join(tmpDir, "cli-v1-"+filepath.Base(key))
		require.NoError(t, os.WriteFile(f, []byte(key), 0o644))
		runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", key, "--body", f))
	}

	out := runCLI(t, awsCLI("s3api", "list-objects", "--bucket", bucket))
	var resp struct {
		Contents []struct {
			Key string `json:"Key"`
		} `json:"Contents"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	assert.Len(t, resp.Contents, 4)

	// Delimiter rolls dir/* into a common prefix.
	delimOut := runCLI(t, awsCLI("s3api", "list-objects", "--bucket", bucket, "--delimiter", "/"))
	var delimResp struct {
		CommonPrefixes []struct {
			Prefix string `json:"Prefix"`
		} `json:"CommonPrefixes"`
	}
	require.NoError(t, json.Unmarshal([]byte(delimOut), &delimResp))
	var prefixes []string
	for _, cp := range delimResp.CommonPrefixes {
		prefixes = append(prefixes, cp.Prefix)
	}
	assert.Contains(t, prefixes, "dir/")

	for _, key := range []string{"apple", "banana", "dir/one", "dir/two"} {
		_ = awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", key).Run()
	}
	_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
}
