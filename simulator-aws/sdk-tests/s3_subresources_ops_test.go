package aws_sdk_test

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// putTestObject creates a bucket (idempotent) and stores a small object,
// returning the client. Shared by the new-op tests below.
func putTestObject(t *testing.T, bucket, key string, body []byte) *s3.Client {
	t.Helper()
	client := s3Client()
	_, _ = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	require.NoError(t, err)
	return client
}

func TestS3_ObjectAcl(t *testing.T) {
	bucket, key := "obj-acl-bucket", "acl-obj.txt"
	client := putTestObject(t, bucket, key, []byte("acl body"))

	// GET the default ACL (owner FULL_CONTROL).
	getOut, err := client.GetObjectAcl(ctx, &s3.GetObjectAclInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	require.NoError(t, err)
	require.NotNil(t, getOut.Owner)
	require.NotEmpty(t, getOut.Grants)
	assert.Equal(t, s3types.PermissionFullControl, getOut.Grants[0].Permission)

	// PUT a custom ACL grant set, then read it back.
	_, err = client.PutObjectAcl(ctx, &s3.PutObjectAclInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		AccessControlPolicy: &s3types.AccessControlPolicy{
			Owner: &s3types.Owner{ID: aws.String("0123456789")},
			Grants: []s3types.Grant{{
				Grantee: &s3types.Grantee{
					Type: s3types.TypeGroup,
					URI:  aws.String("http://acs.amazonaws.com/groups/global/AllUsers"),
				},
				Permission: s3types.PermissionRead,
			}},
		},
	})
	require.NoError(t, err)

	readBack, err := client.GetObjectAcl(ctx, &s3.GetObjectAclInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	require.NoError(t, err)
	require.NotEmpty(t, readBack.Grants)
	assert.Equal(t, s3types.PermissionRead, readBack.Grants[0].Permission)

	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
}

func TestS3_ObjectAttributes(t *testing.T) {
	bucket, key := "obj-attrs-bucket", "attrs-obj.txt"
	body := []byte("attributes payload")
	client := putTestObject(t, bucket, key, body)

	out, err := client.GetObjectAttributes(ctx, &s3.GetObjectAttributesInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		ObjectAttributes: []s3types.ObjectAttributes{
			s3types.ObjectAttributesEtag,
			s3types.ObjectAttributesObjectSize,
			s3types.ObjectAttributesStorageClass,
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(out.ETag))
	assert.Equal(t, int64(len(body)), aws.ToInt64(out.ObjectSize))
	assert.Equal(t, s3types.StorageClassStandard, out.StorageClass)

	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
}

func TestS3_ObjectLegalHold(t *testing.T) {
	bucket, key := "obj-legalhold-bucket", "lh-obj.txt"
	client := s3Client()
	_, _ = client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket:                     aws.String(bucket),
		ObjectLockEnabledForBucket: aws.Bool(true),
	})
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader([]byte("lh")),
	})
	require.NoError(t, err)

	_, err = client.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		LegalHold: &s3types.ObjectLockLegalHold{Status: s3types.ObjectLockLegalHoldStatusOn},
	})
	require.NoError(t, err)

	out, err := client.GetObjectLegalHold(ctx, &s3.GetObjectLegalHoldInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	require.NoError(t, err)
	require.NotNil(t, out.LegalHold)
	assert.Equal(t, s3types.ObjectLockLegalHoldStatusOn, out.LegalHold.Status)

	// Turn it off so the object can be deleted.
	_, _ = client.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		LegalHold: &s3types.ObjectLockLegalHold{Status: s3types.ObjectLockLegalHoldStatusOff},
	})
	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
}

func TestS3_ObjectRetention(t *testing.T) {
	bucket, key := "obj-retention-bucket", "ret-obj.txt"
	client := s3Client()
	_, _ = client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket:                     aws.String(bucket),
		ObjectLockEnabledForBucket: aws.Bool(true),
	})
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader([]byte("ret")),
	})
	require.NoError(t, err)

	until := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	_, err = client.PutObjectRetention(ctx, &s3.PutObjectRetentionInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Retention: &s3types.ObjectLockRetention{
			Mode:            s3types.ObjectLockRetentionModeGovernance,
			RetainUntilDate: aws.Time(until),
		},
	})
	require.NoError(t, err)

	out, err := client.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	require.NoError(t, err)
	require.NotNil(t, out.Retention)
	assert.Equal(t, s3types.ObjectLockRetentionModeGovernance, out.Retention.Mode)
	require.NotNil(t, out.Retention.RetainUntilDate)
	assert.True(t, until.Equal(out.Retention.RetainUntilDate.UTC()))

	// Object Lock-protected objects need bypass to delete; tolerant cleanup.
	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		BypassGovernanceRetention: aws.Bool(true),
	})
}

func TestS3_ObjectLockConfiguration(t *testing.T) {
	bucket := "obj-lock-cfg-bucket"
	client := s3Client()
	_, _ = client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket:                     aws.String(bucket),
		ObjectLockEnabledForBucket: aws.Bool(true),
	})

	_, err := client.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
		ObjectLockConfiguration: &s3types.ObjectLockConfiguration{
			ObjectLockEnabled: s3types.ObjectLockEnabledEnabled,
			Rule: &s3types.ObjectLockRule{
				DefaultRetention: &s3types.DefaultRetention{
					Mode: s3types.ObjectLockRetentionModeCompliance,
					Days: aws.Int32(7),
				},
			},
		},
	})
	require.NoError(t, err)

	out, err := client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err)
	require.NotNil(t, out.ObjectLockConfiguration)
	assert.Equal(t, s3types.ObjectLockEnabledEnabled, out.ObjectLockConfiguration.ObjectLockEnabled)
	require.NotNil(t, out.ObjectLockConfiguration.Rule)
	require.NotNil(t, out.ObjectLockConfiguration.Rule.DefaultRetention)
	assert.Equal(t, int32(7), aws.ToInt32(out.ObjectLockConfiguration.Rule.DefaultRetention.Days))
}

func TestS3_ObjectTorrent(t *testing.T) {
	bucket, key := "obj-torrent-bucket", "torrent-obj.txt"
	client := putTestObject(t, bucket, key, []byte("torrent payload"))

	out, err := client.GetObjectTorrent(ctx, &s3.GetObjectTorrentInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	require.NoError(t, err)
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	// Bencoded metainfo dictionary begins with 'd'.
	require.NotEmpty(t, data)
	assert.Equal(t, byte('d'), data[0])

	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
}

func TestS3_RestoreObject(t *testing.T) {
	bucket, key := "obj-restore-bucket", "restore-obj.txt"
	client := putTestObject(t, bucket, key, []byte("restore payload"))

	_, err := client.RestoreObject(ctx, &s3.RestoreObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		RestoreRequest: &s3types.RestoreRequest{
			Days: aws.Int32(1),
			GlacierJobParameters: &s3types.GlacierJobParameters{
				Tier: s3types.TierStandard,
			},
		},
	})
	require.NoError(t, err)

	// A second restore of an already-requested object is accepted (200).
	_, err = client.RestoreObject(ctx, &s3.RestoreObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		RestoreRequest: &s3types.RestoreRequest{Days: aws.Int32(1)},
	})
	require.NoError(t, err)

	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
}

func TestS3_UploadPartCopy(t *testing.T) {
	bucket := "obj-uploadpartcopy-bucket"
	srcKey, dstKey := "source-object.txt", "multipart-target.txt"
	srcBody := bytes.Repeat([]byte("ABCDEFGH"), 16) // 128 bytes
	client := putTestObject(t, bucket, srcKey, srcBody)

	created, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(dstKey),
	})
	require.NoError(t, err)
	uploadID := aws.ToString(created.UploadId)
	require.NotEmpty(t, uploadID)

	// Copy a byte range from the source object into part 1.
	copyOut, err := client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
		Bucket:          aws.String(bucket),
		Key:             aws.String(dstKey),
		UploadId:        aws.String(uploadID),
		PartNumber:      aws.Int32(1),
		CopySource:      aws.String(bucket + "/" + srcKey),
		CopySourceRange: aws.String("bytes=0-63"),
	})
	require.NoError(t, err)
	require.NotNil(t, copyOut.CopyPartResult)
	etag := aws.ToString(copyOut.CopyPartResult.ETag)
	require.NotEmpty(t, etag)

	complete, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(dstKey), UploadId: aws.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{
			Parts: []s3types.CompletedPart{{PartNumber: aws.Int32(1), ETag: aws.String(etag)}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, complete)

	getOut, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(dstKey)})
	require.NoError(t, err)
	defer getOut.Body.Close()
	data, err := io.ReadAll(getOut.Body)
	require.NoError(t, err)
	assert.Equal(t, srcBody[:64], data)

	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(srcKey)})
	_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(dstKey)})
}

func TestS3_ListObjectsV1(t *testing.T) {
	bucket := "list-v1-bucket"
	client := s3Client()
	_, _ = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	for _, key := range []string{"apple", "banana", "cherry", "dir/one", "dir/two"} {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader([]byte(key)),
		})
		require.NoError(t, err)
	}

	// Full listing.
	out, err := client.ListObjects(ctx, &s3.ListObjectsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Len(t, out.Contents, 5)
	assert.False(t, aws.ToBool(out.IsTruncated))

	// Pagination via Marker / NextMarker with a delimiter (NextMarker is
	// populated by S3 when a delimiter is present and the list truncates).
	page, err := client.ListObjects(ctx, &s3.ListObjectsInput{
		Bucket:    aws.String(bucket),
		MaxKeys:   aws.Int32(2),
		Delimiter: aws.String("/"),
	})
	require.NoError(t, err)
	require.True(t, aws.ToBool(page.IsTruncated))
	require.NotEmpty(t, aws.ToString(page.NextMarker))

	next, err := client.ListObjects(ctx, &s3.ListObjectsInput{
		Bucket: aws.String(bucket),
		Marker: page.NextMarker,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, next.Contents)

	// Delimiter rolls dir/* up into a CommonPrefix.
	delim, err := client.ListObjects(ctx, &s3.ListObjectsInput{
		Bucket: aws.String(bucket), Delimiter: aws.String("/"),
	})
	require.NoError(t, err)
	var prefixes []string
	for _, cp := range delim.CommonPrefixes {
		prefixes = append(prefixes, aws.ToString(cp.Prefix))
	}
	assert.Contains(t, prefixes, "dir/")

	for _, key := range []string{"apple", "banana", "cherry", "dir/one", "dir/two"} {
		_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	}
}
