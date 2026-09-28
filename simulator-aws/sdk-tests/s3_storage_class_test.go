package aws_sdk_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireS3ErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "want %s, got %v", code, err)
	assert.Equal(t, code, apiErr.ErrorCode())
}

// An object keeps the storage class it was written in, an archived one is
// not readable or copyable until RestoreObject makes a temporary copy, and
// the restore reports its state and expiry the way Amazon S3 does.
func TestS3_StorageClassAndRestoreLifecycle(t *testing.T) {
	client := s3Client()
	bucket := "sdk-storage-class-restore"
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("archive.txt"),
		Body: strings.NewReader("archived"), StorageClass: s3types.StorageClassGlacier,
	})
	require.NoError(t, err)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("standard.txt"), Body: strings.NewReader("standard"),
	})
	require.NoError(t, err)

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("archive.txt")})
	require.NoError(t, err)
	assert.Equal(t, s3types.StorageClassGlacier, head.StorageClass)
	assert.Nil(t, head.Restore, "no restore has been requested")

	listed, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	classes := map[string]s3types.ObjectStorageClass{}
	for _, object := range listed.Contents {
		classes[aws.ToString(object.Key)] = object.StorageClass
	}
	assert.Equal(t, s3types.ObjectStorageClassGlacier, classes["archive.txt"])
	assert.Equal(t, s3types.ObjectStorageClassStandard, classes["standard.txt"])

	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("archive.txt")})
	var invalid *s3types.InvalidObjectState
	require.True(t, errors.As(err, &invalid), "an archived object is not readable: %v", err)
	assert.Equal(t, s3types.StorageClassGlacier, invalid.StorageClass)
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("copy.txt"), CopySource: aws.String(bucket + "/archive.txt"),
	})
	requireS3ErrorCode(t, err, "ObjectNotInActiveTierError")

	_, err = client.RestoreObject(ctx, &s3.RestoreObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("standard.txt"),
		RestoreRequest: &s3types.RestoreRequest{Days: aws.Int32(1)},
	})
	requireS3ErrorCode(t, err, "ObjectAlreadyInActiveTierError")
	_, err = client.RestoreObject(ctx, &s3.RestoreObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("archive.txt"), RestoreRequest: &s3types.RestoreRequest{},
	})
	requireS3ErrorCode(t, err, "MalformedXML")

	_, err = client.RestoreObject(ctx, &s3.RestoreObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("archive.txt"),
		RestoreRequest: &s3types.RestoreRequest{
			Days: aws.Int32(2), GlacierJobParameters: &s3types.GlacierJobParameters{Tier: s3types.TierBulk},
		},
	})
	require.NoError(t, err)
	head, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("archive.txt")})
	require.NoError(t, err)
	restore := aws.ToString(head.Restore)
	require.Contains(t, restore, `ongoing-request="false"`)
	expiry := time.Now().UTC().AddDate(0, 0, 3)
	assert.Contains(t, restore, time.Date(expiry.Year(), expiry.Month(), expiry.Day(), 0, 0, 0, 0, time.UTC).Format("02 Jan 2006 00:00:00 GMT"),
		"the restored copy expires at the midnight after two days")

	got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("archive.txt")})
	require.NoError(t, err, "a restored object is readable")
	body, err := io.ReadAll(got.Body)
	require.NoError(t, got.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, "archived", string(body))

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("bad.txt"), Body: strings.NewReader("x"),
		StorageClass: s3types.StorageClass("COLD_STORAGE"),
	})
	requireS3ErrorCode(t, err, "InvalidStorageClass")

	upload, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String("multipart.txt"), StorageClass: s3types.StorageClassDeepArchive,
	})
	require.NoError(t, err)
	parts, err := client.ListParts(ctx, &s3.ListPartsInput{
		Bucket: aws.String(bucket), Key: aws.String("multipart.txt"), UploadId: upload.UploadId,
	})
	require.NoError(t, err)
	assert.Equal(t, s3types.StorageClassDeepArchive, parts.StorageClass)
	_, err = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String("multipart.txt"), UploadId: upload.UploadId,
	})
	require.NoError(t, err)
}
