package aws_sdk_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func s3CreateBucket(t *testing.T, c *s3.Client, bucket string) {
	t.Helper()
	_, err := c.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err)
}

// TestS3_Multipart exercises the canonical multipart upload flow:
// Initiate → UploadPart × N → CompleteMultipartUpload → GetObject
// asserts the assembled bytes equal the concatenation of the parts.
func TestS3_Multipart(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "mp-bucket"
	key := "mp-key"
	s3CreateBucket(t, c, bucket)

	initOut, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String("text/plain"),
	})
	require.NoError(t, err)
	uploadID := aws.ToString(initOut.UploadId)
	require.NotEmpty(t, uploadID)

	parts := []string{"hello ", "world ", "from multipart"}
	var completedParts []types.CompletedPart
	for i, body := range parts {
		out, err := c.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(key),
			UploadId:   aws.String(uploadID),
			PartNumber: aws.Int32(int32(i + 1)),
			Body:       bytes.NewReader([]byte(body)),
		})
		require.NoError(t, err)
		completedParts = append(completedParts, types.CompletedPart{
			ETag:       out.ETag,
			PartNumber: aws.Int32(int32(i + 1)),
		})
	}

	_, err = c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completedParts},
	})
	require.NoError(t, err)

	get, err := c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	defer get.Body.Close()
	body, _ := io.ReadAll(get.Body)
	assert.Equal(t, strings.Join(parts, ""), string(body))
}

// TestS3_AbortMultipart confirms AbortMultipartUpload cleanly cancels
// an in-flight upload.
func TestS3_AbortMultipart(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "abort-bucket"
	key := "abort-key"
	s3CreateBucket(t, c, bucket)

	initOut, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	uploadID := aws.ToString(initOut.UploadId)

	_, err = c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	require.NoError(t, err)

	// Subsequent Complete must 404 (upload no longer exists).
	_, err = c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{},
	})
	require.Error(t, err, "Complete after Abort must fail")
}

// TestS3_ObjectTagging exercises PutObjectTagging → GetObjectTagging
// → DeleteObjectTagging on a regular (single-shot) object.
func TestS3_ObjectTagging(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "tag-bucket"
	key := "tag-key"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte("body")),
	})
	require.NoError(t, err)

	_, err = c.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Tagging: &types.Tagging{TagSet: []types.Tag{
			{Key: aws.String("env"), Value: aws.String("prod")},
			{Key: aws.String("team"), Value: aws.String("platform")},
		}},
	})
	require.NoError(t, err)

	get, err := c.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	tags := map[string]string{}
	for _, t := range get.TagSet {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	assert.Equal(t, "prod", tags["env"])
	assert.Equal(t, "platform", tags["team"])

	_, err = c.DeleteObjectTagging(ctx, &s3.DeleteObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	get2, err := c.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	assert.Empty(t, get2.TagSet)
}

// TestS3_CopyObject exercises the x-amz-copy-source header flow.
func TestS3_CopyObject(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "copy-bucket"
	src := "source-key"
	dst := "dest-key"
	s3CreateBucket(t, c, bucket)

	payload := []byte("copy me")
	_, err := c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(src),
		Body:   bytes.NewReader(payload),
	})
	require.NoError(t, err)

	_, err = c.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(dst),
		CopySource: aws.String(bucket + "/" + src),
	})
	require.NoError(t, err)

	get, err := c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(dst),
	})
	require.NoError(t, err)
	defer get.Body.Close()
	got, _ := io.ReadAll(get.Body)
	assert.Equal(t, payload, got, "copied object must have the same bytes")

	list, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(dst),
	})
	require.NoError(t, err)
	require.Len(t, list.Contents, 1)
	assert.Equal(t, dst, aws.ToString(list.Contents[0].Key), "copied object must be visible to ListObjectsV2")
}
