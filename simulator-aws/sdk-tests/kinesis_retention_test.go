package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKinesisSDK_RetentionBoundsAndTimestampIterators holds the retention
// period to the range and direction each operation allows, and reads a shard
// from an AT_TIMESTAMP iterator.
func TestKinesisSDK_RetentionBoundsAndTimestampIterators(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-retention"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{StreamName: aws.String(streamName), ShardCount: aws.Int32(1)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})

	var invalid *ktypes.InvalidArgumentException
	_, err = client.DecreaseStreamRetentionPeriod(ctx, &kinesis.DecreaseStreamRetentionPeriodInput{
		StreamName: aws.String(streamName), RetentionPeriodHours: aws.Int32(12),
	})
	require.ErrorAs(t, err, &invalid, "a retention period under 24 hours is refused")
	_, err = client.IncreaseStreamRetentionPeriod(ctx, &kinesis.IncreaseStreamRetentionPeriodInput{
		StreamName: aws.String(streamName), RetentionPeriodHours: aws.Int32(72),
	})
	require.NoError(t, err)
	_, err = client.IncreaseStreamRetentionPeriod(ctx, &kinesis.IncreaseStreamRetentionPeriodInput{
		StreamName: aws.String(streamName), RetentionPeriodHours: aws.Int32(48),
	})
	require.ErrorAs(t, err, &invalid, "IncreaseStreamRetentionPeriod cannot shorten the retention period")
	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(streamName)})
	require.NoError(t, err)
	assert.Equal(t, int32(72), aws.ToInt32(summary.StreamDescriptionSummary.RetentionPeriodHours))

	_, err = client.PutRecord(ctx, &kinesis.PutRecordInput{StreamName: aws.String(streamName), Data: []byte("before"), PartitionKey: aws.String("k")})
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	between := time.Now()
	time.Sleep(20 * time.Millisecond)
	after, err := client.PutRecord(ctx, &kinesis.PutRecordInput{StreamName: aws.String(streamName), Data: []byte("after"), PartitionKey: aws.String("k")})
	require.NoError(t, err)

	it, err := client.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName: aws.String(streamName), ShardId: after.ShardId,
		ShardIteratorType: ktypes.ShardIteratorTypeAtTimestamp, Timestamp: aws.Time(between),
	})
	require.NoError(t, err)
	records, err := client.GetRecords(ctx, &kinesis.GetRecordsInput{ShardIterator: it.ShardIterator})
	require.NoError(t, err)
	require.Len(t, records.Records, 1, "AT_TIMESTAMP starts at the first record that arrived at or after the timestamp")
	assert.Equal(t, "after", string(records.Records[0].Data))
	assert.Equal(t, aws.ToString(after.SequenceNumber), aws.ToString(records.Records[0].SequenceNumber))
}
