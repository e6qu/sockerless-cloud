package aws_sdk_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/firehose"
	firehosetypes "github.com/aws/aws-sdk-go-v2/service/firehose/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/golang/snappy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func firehoseClient() *firehose.Client {
	return firehose.NewFromConfig(sdkConfig(), func(o *firehose.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func createFirehoseServiceRole(t *testing.T, name, service string, actions []string, resources []string) string {
	t.Helper()
	client := iamClient()
	trust, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect": "Allow", "Principal": map[string]string{"Service": service}, "Action": "sts:AssumeRole",
		}},
	})
	require.NoError(t, err)
	created, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(string(trust)),
	})
	require.NoError(t, err)
	policy, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect": "Allow", "Action": actions, "Resource": resources,
		}},
	})
	require.NoError(t, err)
	_, err = client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(name), PolicyName: aws.String("delivery"), PolicyDocument: aws.String(string(policy)),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName: aws.String(name), PolicyName: aws.String("delivery"),
		})
		_, _ = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})
	return aws.ToString(created.Role.Arn)
}

func createImmediateFirehose(t *testing.T, name string) (string, string) {
	t.Helper()
	bucket := strings.ToLower(name) + "-bucket"
	s3c := s3Client()
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		objects, _ := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		for _, object := range objects.Contents {
			_, _ = s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: object.Key})
		}
		_, _ = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})

	bucketARN := "arn:aws:s3:::" + bucket
	roleARN := createFirehoseServiceRole(t, name+"-delivery", "firehose.amazonaws.com",
		[]string{"s3:GetBucketLocation", "s3:ListBucket", "s3:PutObject"},
		[]string{bucketARN, bucketARN + "/*"})
	client := firehoseClient()
	created, err := client.CreateDeliveryStream(ctx, &firehose.CreateDeliveryStreamInput{
		DeliveryStreamName: aws.String(name),
		ExtendedS3DestinationConfiguration: &firehosetypes.ExtendedS3DestinationConfiguration{
			BucketARN: aws.String(bucketARN),
			RoleARN:   aws.String(roleARN),
			Prefix:    aws.String("initial/"),
			BufferingHints: &firehosetypes.BufferingHints{
				IntervalInSeconds: aws.Int32(0),
				SizeInMBs:         aws.Int32(1),
			},
		},
		Tags: []firehosetypes.Tag{{Key: aws.String("environment"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(created.DeliveryStreamARN))
	t.Cleanup(func() {
		_, _ = client.DeleteDeliveryStream(ctx, &firehose.DeleteDeliveryStreamInput{
			DeliveryStreamName: aws.String(name), AllowForceDelete: aws.Bool(true),
		})
	})
	return aws.ToString(created.DeliveryStreamARN), bucket
}

func readOnlyS3ObjectWithPrefix(t *testing.T, bucket, prefix string) string {
	t.Helper()
	s3c := s3Client()
	out, err := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	require.NoError(t, err)
	require.Len(t, out.Contents, 1, "expected one delivered object under %q", prefix)
	object, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: out.Contents[0].Key})
	require.NoError(t, err)
	defer object.Body.Close()
	body, err := io.ReadAll(object.Body)
	require.NoError(t, err)
	return string(body)
}

func TestFirehose_CompleteLifecycleAndS3Delivery(t *testing.T) {
	const name = "sdk-firehose-lifecycle"
	arn, bucket := createImmediateFirehose(t, name)
	client := firehoseClient()

	description, err := client.DescribeDeliveryStream(ctx, &firehose.DescribeDeliveryStreamInput{
		DeliveryStreamName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(description.DeliveryStreamDescription.DeliveryStreamARN))
	assert.Equal(t, firehosetypes.DeliveryStreamStatusActive, description.DeliveryStreamDescription.DeliveryStreamStatus)
	require.Len(t, description.DeliveryStreamDescription.Destinations, 1)

	listed, err := client.ListDeliveryStreams(ctx, &firehose.ListDeliveryStreamsInput{})
	require.NoError(t, err)
	assert.Contains(t, listed.DeliveryStreamNames, name)

	tags, err := client.ListTagsForDeliveryStream(ctx, &firehose.ListTagsForDeliveryStreamInput{
		DeliveryStreamName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "environment", aws.ToString(tags.Tags[0].Key))

	_, err = client.TagDeliveryStream(ctx, &firehose.TagDeliveryStreamInput{
		DeliveryStreamName: aws.String(name),
		Tags:               []firehosetypes.Tag{{Key: aws.String("owner"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)
	_, err = client.UntagDeliveryStream(ctx, &firehose.UntagDeliveryStreamInput{
		DeliveryStreamName: aws.String(name), TagKeys: []string{"environment"},
	})
	require.NoError(t, err)

	put, err := client.PutRecord(ctx, &firehose.PutRecordInput{
		DeliveryStreamName: aws.String(name),
		Record:             &firehosetypes.Record{Data: []byte("one\n")},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(put.RecordId))
	assert.Equal(t, "one\n", readOnlyS3ObjectWithPrefix(t, bucket, "initial/"))

	batch, err := client.PutRecordBatch(ctx, &firehose.PutRecordBatchInput{
		DeliveryStreamName: aws.String(name),
		Records: []firehosetypes.Record{
			{Data: []byte("two\n")},
			{Data: []byte("three\n")},
		},
	})
	require.NoError(t, err)
	assert.Zero(t, aws.ToInt32(batch.FailedPutCount))
	require.Len(t, batch.RequestResponses, 2)

	_, err = client.StartDeliveryStreamEncryption(ctx, &firehose.StartDeliveryStreamEncryptionInput{
		DeliveryStreamName: aws.String(name),
		DeliveryStreamEncryptionConfigurationInput: &firehosetypes.DeliveryStreamEncryptionConfigurationInput{
			KeyType: firehosetypes.KeyTypeAwsOwnedCmk,
		},
	})
	require.NoError(t, err)
	_, err = client.StopDeliveryStreamEncryption(ctx, &firehose.StopDeliveryStreamEncryptionInput{
		DeliveryStreamName: aws.String(name),
	})
	require.NoError(t, err)

	kmsc := kmsClient()
	key, err := kmsc.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("Firehose stream encryption")})
	require.NoError(t, err)
	keyARN := aws.ToString(key.KeyMetadata.Arn)
	_, err = client.StartDeliveryStreamEncryption(ctx, &firehose.StartDeliveryStreamEncryptionInput{
		DeliveryStreamName: aws.String(name),
		DeliveryStreamEncryptionConfigurationInput: &firehosetypes.DeliveryStreamEncryptionConfigurationInput{
			KeyType: firehosetypes.KeyTypeCustomerManagedCmk,
			KeyARN:  aws.String(keyARN),
		},
	})
	require.NoError(t, err)
	encryptedDescription, err := client.DescribeDeliveryStream(ctx, &firehose.DescribeDeliveryStreamInput{
		DeliveryStreamName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, keyARN,
		aws.ToString(encryptedDescription.DeliveryStreamDescription.DeliveryStreamEncryptionConfiguration.KeyARN))
	_, err = kmsc.DisableKey(ctx, &kms.DisableKeyInput{KeyId: key.KeyMetadata.KeyId})
	require.NoError(t, err)
	_, err = client.PutRecord(ctx, &firehose.PutRecordInput{
		DeliveryStreamName: aws.String(name), Record: &firehosetypes.Record{Data: []byte("blocked\n")},
	})
	require.Error(t, err, "a disabled customer-managed KMS key must stop encrypted record ingestion")
	_, err = kmsc.EnableKey(ctx, &kms.EnableKeyInput{KeyId: key.KeyMetadata.KeyId})
	require.NoError(t, err)
	_, err = client.StopDeliveryStreamEncryption(ctx, &firehose.StopDeliveryStreamEncryptionInput{
		DeliveryStreamName: aws.String(name),
	})
	require.NoError(t, err)

	description, err = client.DescribeDeliveryStream(ctx, &firehose.DescribeDeliveryStreamInput{
		DeliveryStreamName: aws.String(name),
	})
	require.NoError(t, err)
	_, err = client.UpdateDestination(ctx, &firehose.UpdateDestinationInput{
		DeliveryStreamName:             aws.String(name),
		CurrentDeliveryStreamVersionId: description.DeliveryStreamDescription.VersionId,
		DestinationId:                  description.DeliveryStreamDescription.Destinations[0].DestinationId,
		ExtendedS3DestinationUpdate: &firehosetypes.ExtendedS3DestinationUpdate{
			Prefix: aws.String("updated/"),
		},
	})
	require.NoError(t, err)
	_, err = client.PutRecord(ctx, &firehose.PutRecordInput{
		DeliveryStreamName: aws.String(name),
		Record:             &firehosetypes.Record{Data: []byte("updated\n")},
	})
	require.NoError(t, err)
	assert.Equal(t, "updated\n", readOnlyS3ObjectWithPrefix(t, bucket, "updated/"))
}

func TestFirehose_SNSAndCloudWatchDeliverThroughPublicAPIs(t *testing.T) {
	const name = "sdk-firehose-services"
	arn, bucket := createImmediateFirehose(t, name)

	snsc := snsClient()
	topic, err := snsc.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("firehose-source-topic")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsc.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })
	snsRoleARN := createFirehoseServiceRole(t, "sdk-sns-firehose", "sns.amazonaws.com",
		[]string{"firehose:PutRecord"}, []string{arn})
	_, err = snsc.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn,
		Protocol: aws.String("firehose"),
		Endpoint: aws.String(arn),
		Attributes: map[string]string{
			"SubscriptionRoleArn": snsRoleARN,
		},
	})
	require.NoError(t, err)
	_, err = snsc.Publish(ctx, &sns.PublishInput{TopicArn: topic.TopicArn, Message: aws.String("from-sns")})
	require.NoError(t, err)
	assert.Contains(t, readOnlyS3ObjectWithPrefix(t, bucket, "initial/"), "from-sns")

	cwc := cloudwatchClient()
	streamName := "firehose-metric-stream"
	cloudWatchRoleARN := createFirehoseServiceRole(t, "sdk-cloudwatch-firehose",
		"streams.metrics.cloudwatch.amazonaws.com", []string{"firehose:PutRecord"}, []string{arn})
	_, err = cwc.PutMetricStream(ctx, &cloudwatch.PutMetricStreamInput{
		Name:         aws.String(streamName),
		FirehoseArn:  aws.String(arn),
		RoleArn:      aws.String(cloudWatchRoleARN),
		OutputFormat: cwtypes.MetricStreamOutputFormatJson,
		IncludeFilters: []cwtypes.MetricStreamFilter{{
			Namespace: aws.String("Custom/Firehose"),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cwc.DeleteMetricStream(ctx, &cloudwatch.DeleteMetricStreamInput{Name: aws.String(streamName)})
	})
	_, err = cwc.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Custom/Firehose"),
		MetricData: []cwtypes.MetricDatum{{
			MetricName: aws.String("Delivered"), Value: aws.Float64(7),
			Timestamp: aws.Time(time.Unix(1234, 0).UTC()), Unit: cwtypes.StandardUnitCount,
		}},
	})
	require.NoError(t, err)

	s3c := s3Client()
	objects, err := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("initial/")})
	require.NoError(t, err)
	require.Len(t, objects.Contents, 2)
	metricObject, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: objects.Contents[1].Key})
	require.NoError(t, err)
	defer metricObject.Body.Close()
	data, err := io.ReadAll(metricObject.Body)
	require.NoError(t, err)
	var metric map[string]any
	require.NoError(t, json.Unmarshal(data, &metric))
	assert.Equal(t, streamName, metric["metric_stream_name"])
	assert.Equal(t, "Custom/Firehose", metric["namespace"])
	assert.Equal(t, "Delivered", metric["metric_name"])
	assert.Equal(t, float64(1234000), metric["timestamp"])
}

func TestFirehose_DocumentedS3CompressionFormats(t *testing.T) {
	const bucket = "sdk-firehose-compression-bucket"
	s3c := s3Client()
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		objects, _ := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		for _, object := range objects.Contents {
			_, _ = s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: object.Key})
		}
		_, _ = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})
	bucketARN := "arn:aws:s3:::" + bucket
	roleARN := createFirehoseServiceRole(t, "sdk-firehose-compression-role", "firehose.amazonaws.com",
		[]string{"s3:GetBucketLocation", "s3:ListBucket", "s3:PutObject"},
		[]string{bucketARN, bucketARN + "/*"})
	client := firehoseClient()

	formats := []struct {
		name   string
		format firehosetypes.CompressionFormat
		decode func(*testing.T, []byte) []byte
	}{
		{name: "gzip", format: firehosetypes.CompressionFormatGzip, decode: func(t *testing.T, data []byte) []byte {
			reader, err := gzip.NewReader(bytes.NewReader(data))
			require.NoError(t, err)
			defer reader.Close()
			decoded, err := io.ReadAll(reader)
			require.NoError(t, err)
			return decoded
		}},
		{name: "zip", format: firehosetypes.CompressionFormatZip, decode: func(t *testing.T, data []byte) []byte {
			reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			require.NoError(t, err)
			require.Len(t, reader.File, 1)
			entry, err := reader.File[0].Open()
			require.NoError(t, err)
			defer entry.Close()
			decoded, err := io.ReadAll(entry)
			require.NoError(t, err)
			return decoded
		}},
		{name: "snappy", format: firehosetypes.CompressionFormatSnappy, decode: func(t *testing.T, data []byte) []byte {
			decoded, err := io.ReadAll(snappy.NewReader(bytes.NewReader(data)))
			require.NoError(t, err)
			return decoded
		}},
		{name: "hadoop-snappy", format: firehosetypes.CompressionFormatHadoopSnappy, decode: func(t *testing.T, data []byte) []byte {
			require.GreaterOrEqual(t, len(data), 8)
			uncompressedLength := binary.BigEndian.Uint32(data[:4])
			compressedLength := binary.BigEndian.Uint32(data[4:8])
			require.Equal(t, int(compressedLength)+8, len(data))
			decoded, err := snappy.Decode(nil, data[8:])
			require.NoError(t, err)
			require.Equal(t, int(uncompressedLength), len(decoded))
			return decoded
		}},
	}

	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			name := "sdk-firehose-" + format.name
			_, err := client.CreateDeliveryStream(ctx, &firehose.CreateDeliveryStreamInput{
				DeliveryStreamName: aws.String(name),
				ExtendedS3DestinationConfiguration: &firehosetypes.ExtendedS3DestinationConfiguration{
					BucketARN: aws.String(bucketARN), RoleARN: aws.String(roleARN),
					Prefix: aws.String(format.name + "/"), CompressionFormat: format.format,
					BufferingHints: &firehosetypes.BufferingHints{
						IntervalInSeconds: aws.Int32(0), SizeInMBs: aws.Int32(1),
					},
				},
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = client.DeleteDeliveryStream(ctx, &firehose.DeleteDeliveryStreamInput{
					DeliveryStreamName: aws.String(name), AllowForceDelete: aws.Bool(true),
				})
			})
			_, err = client.PutRecord(ctx, &firehose.PutRecordInput{
				DeliveryStreamName: aws.String(name),
				Record:             &firehosetypes.Record{Data: []byte("compressed-firehose-record\n")},
			})
			require.NoError(t, err)
			objects, err := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
				Bucket: aws.String(bucket), Prefix: aws.String(format.name + "/"),
			})
			require.NoError(t, err)
			require.Len(t, objects.Contents, 1)
			object, err := s3c.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(bucket), Key: objects.Contents[0].Key,
			})
			require.NoError(t, err)
			data, err := io.ReadAll(object.Body)
			require.NoError(t, err)
			require.NoError(t, object.Body.Close())
			assert.Equal(t, "compressed-firehose-record\n", string(format.decode(t, data)))
		})
	}
}
