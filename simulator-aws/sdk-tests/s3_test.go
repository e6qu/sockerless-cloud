package aws_sdk_test

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func s3Client() *s3.Client {
	return s3.NewFromConfig(sdkConfig(), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		// UsePathStyle = true is canonical config for any HTTP S3
		// endpoint that doesn't resolve `{bucket}.<host>` DNS — the
		// same option real users set against MinIO, LocalStack, or
		// any in-cluster S3 alternative addressed by IP / localhost.
		o.UsePathStyle = true
	})
}

func TestS3_CreateBucket(t *testing.T) {
	client := s3Client()
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)

	out, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("test-bucket"),
	})
	require.NoError(t, err)
	assert.NotNil(t, out)
}

func TestS3_PutAndGetObject(t *testing.T) {
	client := s3Client()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("obj-bucket"),
	})
	require.NoError(t, err)

	body := []byte("hello world")
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("obj-bucket"),
		Key:    aws.String("greeting.txt"),
		Body:   bytes.NewReader(body),
	})
	require.NoError(t, err)

	getOut, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String("obj-bucket"),
		Key:    aws.String("greeting.txt"),
	})
	require.NoError(t, err)
	defer getOut.Body.Close()

	data, err := io.ReadAll(getOut.Body)
	require.NoError(t, err)
	assert.Equal(t, body, data)
}

func TestS3_ListObjects(t *testing.T) {
	client := s3Client()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("list-bucket"),
	})
	require.NoError(t, err)

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("list-bucket"),
		Key:    aws.String("file1.txt"),
		Body:   bytes.NewReader([]byte("one")),
	})
	require.NoError(t, err)

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("list-bucket"),
		Key:    aws.String("file2.txt"),
		Body:   bytes.NewReader([]byte("two")),
	})
	require.NoError(t, err)

	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("list-bucket"),
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(out.Contents), 2)
}

func TestS3_ListObjectsV2OrderingPaginationAndDelimiter(t *testing.T) {
	client := s3Client()
	bucket := "list-v2-contract"
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	for _, key := range []string{"zebra", "apple", "mango", "dir/two", "dir/one", "banana", "cherry"} {
		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   bytes.NewReader([]byte(key)),
		})
		require.NoError(t, err)
	}

	first, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		MaxKeys: aws.Int32(2),
	})
	require.NoError(t, err)
	require.True(t, aws.ToBool(first.IsTruncated))
	require.NotNil(t, first.NextContinuationToken)
	require.Len(t, first.Contents, 2)
	assert.Equal(t, []string{"apple", "banana"}, []string{
		aws.ToString(first.Contents[0].Key),
		aws.ToString(first.Contents[1].Key),
	})

	second, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:            aws.String(bucket),
		ContinuationToken: first.NextContinuationToken,
		MaxKeys:           aws.Int32(2),
	})
	require.NoError(t, err)
	require.Len(t, second.Contents, 2)
	assert.Equal(t, []string{"cherry", "dir/one"}, []string{
		aws.ToString(second.Contents[0].Key),
		aws.ToString(second.Contents[1].Key),
	})

	after, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:     aws.String(bucket),
		StartAfter: aws.String("mango"),
	})
	require.NoError(t, err)
	keys := make([]string, 0, len(after.Contents))
	for _, obj := range after.Contents {
		keys = append(keys, aws.ToString(obj.Key))
	}
	assert.Equal(t, []string{"zebra"}, keys)

	delimited, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String(bucket),
		Delimiter: aws.String("/"),
	})
	require.NoError(t, err)
	var topKeys []string
	for _, obj := range delimited.Contents {
		topKeys = append(topKeys, aws.ToString(obj.Key))
	}
	assert.True(t, slices.IsSorted(topKeys))
	assert.NotContains(t, topKeys, "dir/one")
	require.Len(t, delimited.CommonPrefixes, 1)
	assert.Equal(t, "dir/", aws.ToString(delimited.CommonPrefixes[0].Prefix))

	delimitedPage, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:     aws.String(bucket),
		Delimiter:  aws.String("/"),
		StartAfter: aws.String("banana"),
		MaxKeys:    aws.Int32(2),
	})
	require.NoError(t, err)
	require.Len(t, delimitedPage.Contents, 1)
	require.Len(t, delimitedPage.CommonPrefixes, 1)
	assert.Equal(t, "cherry", aws.ToString(delimitedPage.Contents[0].Key))
	assert.Equal(t, "dir/", aws.ToString(delimitedPage.CommonPrefixes[0].Prefix))
	require.True(t, aws.ToBool(delimitedPage.IsTruncated))
	require.NotNil(t, delimitedPage.NextContinuationToken)

	delimitedNext, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:            aws.String(bucket),
		Delimiter:         aws.String("/"),
		ContinuationToken: delimitedPage.NextContinuationToken,
	})
	require.NoError(t, err)
	var nextKeys []string
	for _, obj := range delimitedNext.Contents {
		nextKeys = append(nextKeys, aws.ToString(obj.Key))
	}
	assert.Equal(t, []string{"mango", "zebra"}, nextKeys)
	assert.Empty(t, delimitedNext.CommonPrefixes)
}

func TestS3_DeleteObject(t *testing.T) {
	client := s3Client()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("del-bucket"),
	})
	require.NoError(t, err)

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("del-bucket"),
		Key:    aws.String("to-delete.txt"),
		Body:   bytes.NewReader([]byte("bye")),
	})
	require.NoError(t, err)

	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String("del-bucket"),
		Key:    aws.String("to-delete.txt"),
	})
	require.NoError(t, err)
}

func TestS3_GetObject_NoSuchKey_ErrorClassification(t *testing.T) {
	client := s3Client()
	bucket := "error-class-bucket"
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() { client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}) })

	_, err = client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("nonexistent-key"),
	})
	require.Error(t, err)
	var noSuchKey *s3types.NoSuchKey
	assert.True(t, errors.As(err, &noSuchKey),
		"S3 NoSuchKey must be classified by SDK errors.As; got %T: %v", err, err)
}

func TestS3_HeadBucket_NoSuchBucket_ErrorClassification(t *testing.T) {
	client := s3Client()
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String("totally-nonexistent-bucket-xyz"),
	})
	require.Error(t, err)
	var notFound *s3types.NotFound
	assert.True(t, errors.As(err, &notFound),
		"S3 NotFound (HeadBucket) must be classified by SDK errors.As; got %T: %v", err, err)
}
