package aws_sdk_test

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

// TestS3_ObjectAnnotations covers the object `?annotation` subresource:
// PutObjectAnnotation stores a named payload, GetObjectAnnotation streams it
// back, ListObjectAnnotations enumerates and prefix-filters them, and
// DeleteObjectAnnotation removes one.
func TestS3_ObjectAnnotations(t *testing.T) {
	client := s3Client()
	bucket := fmt.Sprintf("sdk-annotations-%d", time.Now().UnixNano())
	key := "reports/q1.csv"

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader("a,b,c\n1,2,3\n"),
	})
	require.NoError(t, err)

	put, err := client.PutObjectAnnotation(ctx, &s3.PutObjectAnnotationInput{
		Bucket:            aws.String(bucket),
		Key:               aws.String(key),
		AnnotationName:    aws.String("classification"),
		AnnotationPayload: strings.NewReader(`{"level":"internal"}`),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(put.ETag))
	require.Equal(t, key, aws.ToString(put.Key))
	require.Equal(t, "classification", aws.ToString(put.AnnotationName))

	_, err = client.PutObjectAnnotation(ctx, &s3.PutObjectAnnotationInput{
		Bucket:            aws.String(bucket),
		Key:               aws.String(key),
		AnnotationName:    aws.String("classification-review"),
		AnnotationPayload: strings.NewReader(`{"reviewed":true}`),
	})
	require.NoError(t, err)
	_, err = client.PutObjectAnnotation(ctx, &s3.PutObjectAnnotationInput{
		Bucket:            aws.String(bucket),
		Key:               aws.String(key),
		AnnotationName:    aws.String("provenance"),
		AnnotationPayload: strings.NewReader(`{"pipeline":"etl"}`),
	})
	require.NoError(t, err)

	got, err := client.GetObjectAnnotation(ctx, &s3.GetObjectAnnotationInput{
		Bucket:         aws.String(bucket),
		Key:            aws.String(key),
		AnnotationName: aws.String("classification"),
	})
	require.NoError(t, err)
	payload, err := io.ReadAll(got.AnnotationPayload)
	require.NoError(t, err)
	require.Equal(t, `{"level":"internal"}`, string(payload))
	require.Equal(t, int64(len(payload)), aws.ToInt64(got.ContentLength))

	listed, err := client.ListObjectAnnotations(ctx, &s3.ListObjectAnnotationsInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	require.Len(t, listed.Annotations, 3)
	require.Equal(t, bucket, aws.ToString(listed.Bucket))
	require.Equal(t, key, aws.ToString(listed.Key))
	names := make([]string, 0, len(listed.Annotations))
	for _, a := range listed.Annotations {
		names = append(names, aws.ToString(a.AnnotationName))
		require.NotZero(t, aws.ToInt64(a.Size))
	}
	require.Equal(t, []string{"classification", "classification-review", "provenance"}, names)

	filtered, err := client.ListObjectAnnotations(ctx, &s3.ListObjectAnnotationsInput{
		Bucket:           aws.String(bucket),
		Key:              aws.String(key),
		AnnotationPrefix: aws.String("classification"),
	})
	require.NoError(t, err)
	require.Len(t, filtered.Annotations, 2)

	_, err = client.DeleteObjectAnnotation(ctx, &s3.DeleteObjectAnnotationInput{
		Bucket:         aws.String(bucket),
		Key:            aws.String(key),
		AnnotationName: aws.String("provenance"),
	})
	require.NoError(t, err)
	listed, err = client.ListObjectAnnotations(ctx, &s3.ListObjectAnnotationsInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	require.Len(t, listed.Annotations, 2)

	// Reading an annotation that is not there fails loudly.
	_, err = client.GetObjectAnnotation(ctx, &s3.GetObjectAnnotationInput{
		Bucket:         aws.String(bucket),
		Key:            aws.String(key),
		AnnotationName: aws.String("provenance"),
	})
	require.Error(t, err)

	// Deleting the object takes its annotations with it.
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	require.NoError(t, err)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte("recreated")),
	})
	require.NoError(t, err)
	listed, err = client.ListObjectAnnotations(ctx, &s3.ListObjectAnnotationsInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	require.NoError(t, err)
	require.Empty(t, listed.Annotations)
}

// TestS3_UpdateBucketMetadataAnnotationTableConfiguration covers the
// bucket-level `?metadataAnnotationTable` operation: the annotation table can be
// turned on for a bucket with an S3 Metadata configuration, and the state is
// read back from GetBucketMetadataConfiguration.
func TestS3_UpdateBucketMetadataAnnotationTableConfiguration(t *testing.T) {
	client := s3Client()
	bucket := fmt.Sprintf("sdk-annotation-table-%d", time.Now().UnixNano())
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	_, err = client.CreateBucketMetadataConfiguration(ctx, &s3.CreateBucketMetadataConfigurationInput{
		Bucket: aws.String(bucket),
		MetadataConfiguration: &s3types.MetadataConfiguration{
			JournalTableConfiguration: &s3types.JournalTableConfiguration{
				RecordExpiration: &s3types.RecordExpiration{
					Expiration: s3types.ExpirationStateEnabled,
					Days:       aws.Int32(7),
				},
			},
		},
	})
	require.NoError(t, err)

	roleArn := "arn:aws:iam::000000000000:role/S3MetadataAnnotationTableRole"
	_, err = client.UpdateBucketMetadataAnnotationTableConfiguration(ctx,
		&s3.UpdateBucketMetadataAnnotationTableConfigurationInput{
			Bucket: aws.String(bucket),
			AnnotationTableConfiguration: &s3types.AnnotationTableConfigurationUpdates{
				ConfigurationState: s3types.AnnotationConfigurationStateEnabled,
				Role:               aws.String(roleArn),
			},
		})
	require.NoError(t, err)

	cfg, err := client.GetBucketMetadataConfiguration(ctx, &s3.GetBucketMetadataConfigurationInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err)
	require.NotNil(t, cfg.GetBucketMetadataConfigurationResult)
	result := cfg.GetBucketMetadataConfigurationResult.MetadataConfigurationResult
	require.NotNil(t, result)
	require.NotNil(t, result.AnnotationTableConfigurationResult)
	require.Equal(t, s3types.AnnotationConfigurationStateEnabled,
		result.AnnotationTableConfigurationResult.ConfigurationState)
	require.Equal(t, roleArn, aws.ToString(result.AnnotationTableConfigurationResult.Role))
	require.Equal(t, "ACTIVE", aws.ToString(result.AnnotationTableConfigurationResult.TableStatus))
	require.Equal(t, bucket+"-annotation", aws.ToString(result.AnnotationTableConfigurationResult.TableName))
}
