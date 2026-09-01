package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	kinesistypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKinesisSDK_ChannelLifecycle covers the delivery channel family: a
// channel over a real stream and a real service execution role, its
// description, an update of what an update may change, the listing and its
// stream filter, and the delete.
func TestKinesisSDK_ChannelLifecycle(t *testing.T) {
	kc := kinesisClient()
	streamName, channelName := "channel-source-stream", "delivery-channel"

	_, err := kc.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName), ShardCount: aws.Int32(1)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = kc.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	described, err := kc.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName)})
	require.NoError(t, err)
	streamARN := aws.ToString(described.StreamDescriptionSummary.StreamARN)

	ic := iam.NewFromConfig(sdkConfig(), func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) })
	role, err := ic.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String("kinesis-channel-delivery"),
		AssumeRolePolicyDocument: aws.String(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
				`"Principal":{"Service":"kinesis.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ic.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String("kinesis-channel-delivery")})
	})

	destination := &kinesistypes.S3DestinationConfiguration{
		StorageConfiguration: &kinesistypes.S3StorageConfiguration{
			BucketARN:           aws.String("arn:aws:s3:::channel-delivery-bucket"),
			ExpectedBucketOwner: aws.String("123456789012"),
			CompressionType:     kinesistypes.S3CompressionTypeGzip,
		},
	}
	created, err := kc.CreateChannel(ctx, &kinesis.CreateChannelInput{
		ChannelName:             aws.String(channelName),
		ServiceExecutionRoleARN: role.Role.Arn,
		StreamConfigurationList: []kinesistypes.ChannelStreamConfiguration{{
			StreamARN: aws.String(streamARN),
			RecordConfiguration: &kinesistypes.RecordConfiguration{
				RecordFormatType: kinesistypes.RecordFormatTypeJson,
			},
		}},
		S3DestinationConfiguration: destination,
	})
	require.NoError(t, err)
	require.NotNil(t, created.ChannelDescription)
	channelARN := aws.ToString(created.ChannelDescription.ChannelARN)
	assert.Contains(t, channelARN, ":channel/"+channelName)
	assert.Equal(t, kinesistypes.ChannelStatusActive, created.ChannelDescription.ChannelStatus)
	require.Len(t, created.ChannelDescription.StreamConfigurationList, 1)
	assert.Equal(t, streamARN,
		aws.ToString(created.ChannelDescription.StreamConfigurationList[0].StreamARN))
	t.Cleanup(func() {
		_, _ = kc.DeleteChannel(ctx, &kinesis.DeleteChannelInput{ChannelARN: aws.String(channelARN)})
	})

	// A channel over a stream that does not exist has nothing to deliver.
	_, err = kc.CreateChannel(ctx, &kinesis.CreateChannelInput{
		ChannelName:             aws.String("channel-no-stream"),
		ServiceExecutionRoleARN: role.Role.Arn,
		StreamConfigurationList: []kinesistypes.ChannelStreamConfiguration{{
			StreamARN: aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/never-created"),
			RecordConfiguration: &kinesistypes.RecordConfiguration{
				RecordFormatType: kinesistypes.RecordFormatTypeJson,
			},
		}},
		S3DestinationConfiguration: destination,
	})
	require.Error(t, err)

	got, err := kc.DescribeChannel(ctx, &kinesis.DescribeChannelInput{
		ChannelARN: aws.String(channelARN)})
	require.NoError(t, err)
	require.NotNil(t, got.ChannelDescription.S3DestinationConfiguration)
	assert.Equal(t, int32(300),
		aws.ToInt32(got.ChannelDescription.S3DestinationConfiguration.DataFreshnessInSeconds),
		"a create that set no freshness gets the service's default")

	updated, err := kc.UpdateChannel(ctx, &kinesis.UpdateChannelInput{
		ChannelARN: aws.String(channelARN),
		S3DestinationConfiguration: &kinesistypes.S3DestinationUpdateInput{
			DataFreshnessInSeconds: aws.Int32(60)},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(60),
		aws.ToInt32(updated.ChannelDescription.S3DestinationConfiguration.DataFreshnessInSeconds))

	// An update naming the destination this channel does not deliver to is
	// asking to change something that is not there.
	_, err = kc.UpdateChannel(ctx, &kinesis.UpdateChannelInput{
		ChannelARN: aws.String(channelARN),
		S3TablesDestinationConfiguration: &kinesistypes.S3TablesDestinationUpdateInput{
			DataFreshnessInSeconds: aws.Int32(60)},
	})
	require.Error(t, err)

	listed, err := kc.ListChannels(ctx, &kinesis.ListChannelsInput{})
	require.NoError(t, err)
	require.Len(t, listed.ChannelSummaries, 1)
	assert.Equal(t, kinesistypes.ChannelDestinationTypeS3,
		listed.ChannelSummaries[0].ChannelDestinationType)
	require.Len(t, listed.ChannelSummaries[0].Streams, 1)

	// The filter selects on the streams a channel reads.
	filtered, err := kc.ListChannels(ctx, &kinesis.ListChannelsInput{
		StreamFilter: []kinesistypes.StreamFilter{{
			StreamARN: aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/some-other-stream")}},
	})
	require.NoError(t, err)
	assert.Empty(t, filtered.ChannelSummaries)

	_, err = kc.DeleteChannel(ctx, &kinesis.DeleteChannelInput{ChannelARN: aws.String(channelARN)})
	require.NoError(t, err)
	_, err = kc.DescribeChannel(ctx, &kinesis.DescribeChannelInput{ChannelARN: aws.String(channelARN)})
	require.Error(t, err)
}
