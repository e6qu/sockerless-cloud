package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_ListBucketsPaginates pages a prefix's buckets with the SDK's
// paginator, each carrying its Region, and filters by Region.
func TestS3_ListBucketsPaginates(t *testing.T) {
	t.Parallel()
	client := s3Client()
	names := []string{"lb-page-sdk-a", "lb-page-sdk-b", "lb-page-sdk-c"}
	for _, name := range names {
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)}) })
	}

	var got []string
	pages := 0
	pager := s3.NewListBucketsPaginator(client, &s3.ListBucketsInput{Prefix: aws.String("lb-page-sdk-"), MaxBuckets: aws.Int32(2)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		pages++
		assert.Equal(t, "lb-page-sdk-", aws.ToString(page.Prefix))
		for _, b := range page.Buckets {
			got = append(got, aws.ToString(b.Name))
			assert.Equal(t, "us-east-1", aws.ToString(b.BucketRegion))
		}
	}
	assert.Equal(t, names, got)
	assert.Equal(t, 2, pages)

	other, err := client.ListBuckets(ctx, &s3.ListBucketsInput{Prefix: aws.String("lb-page-sdk-"), BucketRegion: aws.String("eu-west-1")})
	require.NoError(t, err)
	assert.Empty(t, other.Buckets)

	plain, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)
	for _, b := range plain.Buckets {
		assert.Nil(t, b.BucketRegion, "an unpaginated listing names no Region")
	}
}
