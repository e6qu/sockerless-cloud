package aws_sdk_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_BucketMetadataConfiguration round-trips the V2 S3 Metadata
// configuration feature (?metadataConfiguration) plus the
// inventory/journal table sub-config updates, and the legacy V1 metadata
// table configuration feature (?metadataTable).
func TestS3_BucketMetadataConfiguration(t *testing.T) {
	client := s3Client()
	bucket := "metadata-config-bucket"
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	// The regional service root lists the bucket via ListBuckets (the plain
	// <ListAllMyBucketsResult> envelope — S3 Express ListDirectoryBuckets is a
	// dedicated-endpoint op the regional surface does not serve).
	lb, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)
	found := false
	for _, b := range lb.Buckets {
		if aws.ToString(b.Name) == bucket {
			found = true
		}
	}
	require.True(t, found, "created bucket should appear in ListBuckets")
	t.Cleanup(func() {
		_, _ = client.DeleteBucketMetadataConfiguration(ctx, &s3.DeleteBucketMetadataConfigurationInput{Bucket: aws.String(bucket)})
		_, _ = client.DeleteBucketMetadataTableConfiguration(ctx, &s3.DeleteBucketMetadataTableConfigurationInput{Bucket: aws.String(bucket)})
	})

	// CreateBucketMetadataConfiguration (V2).
	_, err = client.CreateBucketMetadataConfiguration(ctx, &s3.CreateBucketMetadataConfigurationInput{
		Bucket: aws.String(bucket),
		MetadataConfiguration: &s3types.MetadataConfiguration{
			JournalTableConfiguration: &s3types.JournalTableConfiguration{
				RecordExpiration: &s3types.RecordExpiration{
					Expiration: s3types.ExpirationStateEnabled,
					Days:       aws.Int32(30),
				},
			},
			InventoryTableConfiguration: &s3types.InventoryTableConfiguration{
				ConfigurationState: s3types.InventoryConfigurationStateEnabled,
			},
		},
	})
	require.NoError(t, err)

	// GetBucketMetadataConfiguration echoes back the destination + table state.
	getOut, err := client.GetBucketMetadataConfiguration(ctx, &s3.GetBucketMetadataConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.NotNil(t, getOut.GetBucketMetadataConfigurationResult)
	res := getOut.GetBucketMetadataConfigurationResult.MetadataConfigurationResult
	require.NotNil(t, res)
	require.NotNil(t, res.DestinationResult)
	assert.Equal(t, "aws_s3_metadata", aws.ToString(res.DestinationResult.TableNamespace))
	require.NotNil(t, res.JournalTableConfigurationResult)
	assert.Equal(t, "ACTIVE", aws.ToString(res.JournalTableConfigurationResult.TableStatus))
	require.NotNil(t, res.InventoryTableConfigurationResult)
	assert.Equal(t, s3types.InventoryConfigurationStateEnabled, res.InventoryTableConfigurationResult.ConfigurationState)

	// UpdateBucketMetadataInventoryTableConfiguration disables the inventory table.
	_, err = client.UpdateBucketMetadataInventoryTableConfiguration(ctx, &s3.UpdateBucketMetadataInventoryTableConfigurationInput{
		Bucket: aws.String(bucket),
		InventoryTableConfiguration: &s3types.InventoryTableConfigurationUpdates{
			ConfigurationState: s3types.InventoryConfigurationStateDisabled,
		},
	})
	require.NoError(t, err)

	// UpdateBucketMetadataJournalTableConfiguration changes record expiration.
	_, err = client.UpdateBucketMetadataJournalTableConfiguration(ctx, &s3.UpdateBucketMetadataJournalTableConfigurationInput{
		Bucket: aws.String(bucket),
		JournalTableConfiguration: &s3types.JournalTableConfigurationUpdates{
			RecordExpiration: &s3types.RecordExpiration{
				Expiration: s3types.ExpirationStateEnabled,
				Days:       aws.Int32(90),
			},
		},
	})
	require.NoError(t, err)

	getOut2, err := client.GetBucketMetadataConfiguration(ctx, &s3.GetBucketMetadataConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	res2 := getOut2.GetBucketMetadataConfigurationResult.MetadataConfigurationResult
	require.NotNil(t, res2.JournalTableConfigurationResult.RecordExpiration)
	assert.Equal(t, int32(90), aws.ToInt32(res2.JournalTableConfigurationResult.RecordExpiration.Days))

	// DeleteBucketMetadataConfiguration removes it; a subsequent Get 404s.
	_, err = client.DeleteBucketMetadataConfiguration(ctx, &s3.DeleteBucketMetadataConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = client.GetBucketMetadataConfiguration(ctx, &s3.GetBucketMetadataConfigurationInput{Bucket: aws.String(bucket)})
	require.Error(t, err)

	// CreateBucketMetadataTableConfiguration (legacy V1).
	tableBucketArn := "arn:aws:s3tables:us-east-1:000000000000:bucket/my-table-bucket"
	_, err = client.CreateBucketMetadataTableConfiguration(ctx, &s3.CreateBucketMetadataTableConfigurationInput{
		Bucket: aws.String(bucket),
		MetadataTableConfiguration: &s3types.MetadataTableConfiguration{
			S3TablesDestination: &s3types.S3TablesDestination{
				TableBucketArn: aws.String(tableBucketArn),
				TableName:      aws.String("my-metadata-table"),
			},
		},
	})
	require.NoError(t, err)

	tableGet, err := client.GetBucketMetadataTableConfiguration(ctx, &s3.GetBucketMetadataTableConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.NotNil(t, tableGet.GetBucketMetadataTableConfigurationResult)
	dest := tableGet.GetBucketMetadataTableConfigurationResult.MetadataTableConfigurationResult.S3TablesDestinationResult
	require.NotNil(t, dest)
	assert.Equal(t, tableBucketArn, aws.ToString(dest.TableBucketArn))
	assert.Equal(t, "my-metadata-table", aws.ToString(dest.TableName))

	_, err = client.DeleteBucketMetadataTableConfiguration(ctx, &s3.DeleteBucketMetadataTableConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
}

// TestS3_BucketAbac round-trips the attribute-based-access-control status
// (?abac) for a general purpose bucket.
func TestS3_BucketAbac(t *testing.T) {
	client := s3Client()
	bucket := "abac-bucket"
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	// Default (never set) → Disabled.
	getOut, err := client.GetBucketAbac(ctx, &s3.GetBucketAbacInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.NotNil(t, getOut.AbacStatus)
	assert.Equal(t, s3types.BucketAbacStatusDisabled, getOut.AbacStatus.Status)

	// Enable ABAC, read back.
	_, err = client.PutBucketAbac(ctx, &s3.PutBucketAbacInput{
		Bucket:     aws.String(bucket),
		AbacStatus: &s3types.AbacStatus{Status: s3types.BucketAbacStatusEnabled},
	})
	require.NoError(t, err)

	getOut2, err := client.GetBucketAbac(ctx, &s3.GetBucketAbacInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Equal(t, s3types.BucketAbacStatusEnabled, getOut2.AbacStatus.Status)
}

// TestS3_RenameObject moves a stored object key→key within a bucket
// (?renameObject + x-amz-rename-source).
func TestS3_RenameObject(t *testing.T) {
	client := s3Client()
	bucket := "rename-bucket"
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	body := []byte("rename me")
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("old/name.txt"),
		Body:   bytes.NewReader(body),
	})
	require.NoError(t, err)

	_, err = client.RenameObject(ctx, &s3.RenameObjectInput{
		Bucket:       aws.String(bucket),
		Key:          aws.String("new/name.txt"),
		RenameSource: aws.String("/" + bucket + "/old/name.txt"),
	})
	require.NoError(t, err)

	// The new key exists with the original content.
	getOut, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("new/name.txt"),
	})
	require.NoError(t, err)
	got, err := io.ReadAll(getOut.Body)
	require.NoError(t, err)
	_ = getOut.Body.Close()
	assert.Equal(t, body, got)

	// The old key is gone.
	_, err = client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("old/name.txt"),
	})
	require.Error(t, err)
}

// TestS3_ObjectEncryption updates a stored object's SSE settings
// (object ?encryption) and verifies the encryption headers round-trip.
func TestS3_ObjectEncryption(t *testing.T) {
	client := s3Client()
	bucket := "obj-encryption-bucket"
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("secret.txt"),
		Body:   bytes.NewReader([]byte("data")),
	})
	require.NoError(t, err)

	kmsArn := "arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	_, err = client.UpdateObjectEncryption(ctx, &s3.UpdateObjectEncryptionInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("secret.txt"),
		ObjectEncryption: &s3types.ObjectEncryptionMemberSSEKMS{
			Value: s3types.SSEKMSEncryption{KMSKeyArn: aws.String(kmsArn)},
		},
	})
	require.NoError(t, err)

	// The object now reports SSE-KMS on read.
	headOut, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("secret.txt"),
	})
	require.NoError(t, err)
	assert.Equal(t, s3types.ServerSideEncryptionAwsKms, headOut.ServerSideEncryption)
	assert.Equal(t, kmsArn, aws.ToString(headOut.SSEKMSKeyId))
}
