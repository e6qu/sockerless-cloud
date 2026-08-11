package aws_sdk_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_Multipart_ListParts verifies that the multipart part-list
// route returns the canonical paged ListParts shape instead of falling
// through to object reads.
func TestS3_Multipart_ListParts(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "lp-bucket"
	key := "lp-key"
	s3CreateBucket(t, c, bucket)

	initOut, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	uploadID := aws.ToString(initOut.UploadId)

	// Upload 3 parts.
	parts := []string{"alpha ", "beta ", "gamma"}
	for i, body := range parts {
		_, err := c.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(key),
			UploadId:   aws.String(uploadID),
			PartNumber: aws.Int32(int32(i + 1)),
			Body:       bytes.NewReader([]byte(body)),
		})
		require.NoError(t, err)
	}

	pager := s3.NewListPartsPaginator(c, &s3.ListPartsInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	}, func(o *s3.ListPartsPaginatorOptions) {
		o.Limit = 2
	})
	var listed []s3typesCompletedPart
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err, "ListParts paginator must succeed")
		listed = append(listed, toCompletedPartRows(page.Parts)...)
	}
	require.Len(t, listed, 3, "ListParts must return one entry per uploaded part")
	for i, p := range listed {
		assert.Equal(t, int32(i+1), aws.ToInt32(p.PartNumber))
		etag := aws.ToString(p.ETag)
		assert.True(t, strings.HasPrefix(etag, `"`) && strings.HasSuffix(etag, `"`),
			"ETag must be canonical quoted form; got %q", etag)
		assert.Equal(t, int64(len(parts[i])), aws.ToInt64(p.Size))
	}

	_, err = c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	require.NoError(t, err)
}

func TestS3_Multipart_ListMultipartUploadsPaginator(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "lmu-bucket"
	s3CreateBucket(t, c, bucket)

	keys := []string{"alpha", "beta", "gamma"}
	uploadIDs := make(map[string]string, len(keys))
	for _, key := range keys {
		out, err := c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		require.NoError(t, err)
		uploadIDs[key] = aws.ToString(out.UploadId)
	}

	pager := s3.NewListMultipartUploadsPaginator(c, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(bucket),
	}, func(o *s3.ListMultipartUploadsPaginatorOptions) {
		o.Limit = 2
	})
	var listed []string
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err, "ListMultipartUploads paginator must succeed")
		for _, upload := range page.Uploads {
			listed = append(listed, aws.ToString(upload.Key))
		}
	}
	assert.Equal(t, keys, listed)

	for key, uploadID := range uploadIDs {
		_, err := c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(bucket),
			Key:      aws.String(key),
			UploadId: aws.String(uploadID),
		})
		require.NoError(t, err)
	}
}

type s3typesCompletedPart struct {
	PartNumber *int32
	ETag       *string
	Size       *int64
}

func toCompletedPartRows(parts []types.Part) []s3typesCompletedPart {
	rows := make([]s3typesCompletedPart, 0, len(parts))
	for _, part := range parts {
		rows = append(rows, s3typesCompletedPart{
			PartNumber: part.PartNumber,
			ETag:       part.ETag,
			Size:       part.Size,
		})
	}
	return rows
}
