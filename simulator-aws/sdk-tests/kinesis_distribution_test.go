package aws_sdk_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/stretchr/testify/require"
)

// kinesisRecordsPerShard puts records under one partition key and counts
// where they landed.
func kinesisRecordsPerShard(t *testing.T, client *kinesis.Client, streamName string, records int) map[string]int {
	t.Helper()
	perShard := map[string]int{}
	for i := range records {
		out, err := client.PutRecord(ctx, &kinesis.PutRecordInput{
			StreamName:   aws.String(streamName),
			PartitionKey: aws.String("one-key"),
			Data:         []byte(fmt.Sprint(i)),
		})
		require.NoError(t, err)
		perShard[aws.ToString(out.ShardId)]++
	}
	return perShard
}

// TestKinesisSDK_RecordDistributionStrategy holds the strategy to what it
// means: USER_PARTITION_KEY, the default, keeps one partition key on one
// shard; AUTO spreads records across every open shard whatever their key.
func TestKinesisSDK_RecordDistributionStrategy(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-distribution"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName:        aws.String(streamName),
		StreamModeDetails: &ktypes.StreamModeDetails{StreamMode: ktypes.StreamModeOnDemand},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(streamName)})
	require.NoError(t, err)
	streamARN := summary.StreamDescriptionSummary.StreamARN
	require.Equal(t, ktypes.RecordDistributionStrategyUserPartitionKey, summary.StreamDescriptionSummary.RecordDistributionStrategy)
	openShards := int(aws.ToInt32(summary.StreamDescriptionSummary.OpenShardCount))
	require.Greater(t, openShards, 1)

	keyed := kinesisRecordsPerShard(t, client, streamName, 8)
	require.Len(t, keyed, 1, "one partition key lands on one shard")

	_, err = client.UpdateStreamRecordDistributionStrategy(ctx, &kinesis.UpdateStreamRecordDistributionStrategyInput{
		StreamARN:                  streamARN,
		RecordDistributionStrategy: ktypes.RecordDistributionStrategyAuto,
	})
	require.NoError(t, err)
	summary, err = client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(streamName)})
	require.NoError(t, err)
	require.Equal(t, ktypes.RecordDistributionStrategyAuto, summary.StreamDescriptionSummary.RecordDistributionStrategy)

	// AUTO ignores the partition key and evens the shards out, starting with
	// the ones the keyed records left behind.
	totals := kinesisRecordsPerShard(t, client, streamName, 8*openShards)
	for shard, count := range keyed {
		totals[shard] += count
	}
	require.Len(t, totals, openShards, "AUTO uses every open shard")
	least, most := -1, 0
	for _, count := range totals {
		if least < 0 || count < least {
			least = count
		}
		most = max(most, count)
	}
	require.LessOrEqual(t, most-least, 1, "records per shard after AUTO: %v", totals)

	// A provisioned stream has no AUTO strategy, at creation or later.
	provisioned := "sdk-kinesis-distribution-provisioned"
	_, err = client.CreateStream(ctx, &kinesis.CreateStreamInput{StreamName: aws.String(provisioned), ShardCount: aws.Int32(2)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(provisioned)})
	})
	provisionedSummary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(provisioned)})
	require.NoError(t, err)
	_, err = client.UpdateStreamRecordDistributionStrategy(ctx, &kinesis.UpdateStreamRecordDistributionStrategyInput{
		StreamARN:                  provisionedSummary.StreamDescriptionSummary.StreamARN,
		RecordDistributionStrategy: ktypes.RecordDistributionStrategyAuto,
	})
	var invalid *ktypes.InvalidArgumentException
	require.True(t, errors.As(err, &invalid), "AUTO on a provisioned stream: %v", err)

	_, err = client.UpdateStreamRecordDistributionStrategy(ctx, &kinesis.UpdateStreamRecordDistributionStrategyInput{
		StreamARN:                  aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/absent"),
		RecordDistributionStrategy: ktypes.RecordDistributionStrategyAuto,
	})
	var notFound *ktypes.ResourceNotFoundException
	require.True(t, errors.As(err, &notFound), "an absent stream: %v", err)
}
