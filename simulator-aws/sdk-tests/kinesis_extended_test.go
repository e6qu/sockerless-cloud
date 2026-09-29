package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKinesisSDK_Consumers drives the enhanced fan-out consumer lifecycle:
// Register -> Describe -> List -> Deregister, asserting the ARN shape and the
// status transitions a real client reads back.
func TestKinesisSDK_Consumers(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-consumers"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})

	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	streamARN := aws.ToString(summary.StreamDescriptionSummary.StreamARN)
	require.NotEmpty(t, streamARN)

	reg, err := client.RegisterStreamConsumer(ctx, &kinesis.RegisterStreamConsumerInput{
		StreamARN:    aws.String(streamARN),
		ConsumerName: aws.String("sdk-consumer"),
	})
	require.NoError(t, err)
	require.NotNil(t, reg.Consumer)
	assert.Equal(t, "sdk-consumer", aws.ToString(reg.Consumer.ConsumerName))
	consumerARN := aws.ToString(reg.Consumer.ConsumerARN)
	assert.Contains(t, consumerARN, ":stream/"+streamName+"/consumer/sdk-consumer:")
	assert.NotNil(t, reg.Consumer.ConsumerCreationTimestamp)

	// Re-registering the same name fails while the consumer exists.
	_, err = client.RegisterStreamConsumer(ctx, &kinesis.RegisterStreamConsumerInput{
		StreamARN:    aws.String(streamARN),
		ConsumerName: aws.String("sdk-consumer"),
	})
	require.Error(t, err)

	descByName, err := client.DescribeStreamConsumer(ctx, &kinesis.DescribeStreamConsumerInput{
		StreamARN:    aws.String(streamARN),
		ConsumerName: aws.String("sdk-consumer"),
	})
	require.NoError(t, err)
	assert.Equal(t, consumerARN, aws.ToString(descByName.ConsumerDescription.ConsumerARN))
	assert.Equal(t, streamARN, aws.ToString(descByName.ConsumerDescription.StreamARN))
	assert.Equal(t, ktypes.ConsumerStatusActive, descByName.ConsumerDescription.ConsumerStatus)

	descByARN, err := client.DescribeStreamConsumer(ctx, &kinesis.DescribeStreamConsumerInput{
		ConsumerARN: aws.String(consumerARN),
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk-consumer", aws.ToString(descByARN.ConsumerDescription.ConsumerName))

	list, err := client.ListStreamConsumers(ctx, &kinesis.ListStreamConsumersInput{
		StreamARN: aws.String(streamARN),
	})
	require.NoError(t, err)
	require.Len(t, list.Consumers, 1)
	assert.Equal(t, consumerARN, aws.ToString(list.Consumers[0].ConsumerARN))

	_, err = client.DeregisterStreamConsumer(ctx, &kinesis.DeregisterStreamConsumerInput{
		ConsumerARN: aws.String(consumerARN),
	})
	require.NoError(t, err)

	_, err = client.DescribeStreamConsumer(ctx, &kinesis.DescribeStreamConsumerInput{
		ConsumerARN: aws.String(consumerARN),
	})
	require.Error(t, err)
}

// TestKinesisSDK_ResourcePolicy round-trips a stream resource policy through
// Put/Get/Delete.
func TestKinesisSDK_ResourcePolicy(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-resource-policy"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	streamARN := aws.ToString(summary.StreamDescriptionSummary.StreamARN)

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"s","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"kinesis:GetRecords","Resource":"` + streamARN + `"}]}`
	_, err = client.PutResourcePolicy(ctx, &kinesis.PutResourcePolicyInput{
		ResourceARN: aws.String(streamARN),
		Policy:      aws.String(policy),
	})
	require.NoError(t, err)

	got, err := client.GetResourcePolicy(ctx, &kinesis.GetResourcePolicyInput{
		ResourceARN: aws.String(streamARN),
	})
	require.NoError(t, err)
	assert.JSONEq(t, policy, aws.ToString(got.Policy))

	_, err = client.DeleteResourcePolicy(ctx, &kinesis.DeleteResourcePolicyInput{
		ResourceARN: aws.String(streamARN),
	})
	require.NoError(t, err)

	_, err = client.GetResourcePolicy(ctx, &kinesis.GetResourcePolicyInput{
		ResourceARN: aws.String(streamARN),
	})
	requireAWSErrorCode(t, err, "ResourceNotFoundException")
}

// TestKinesisSDK_MergeAndSplitShards exercises SplitShard then MergeShards,
// asserting the shard list mutates faithfully (split parent closes, two open
// children appear; an adjacent merge collapses two open shards into one child).
func TestKinesisSDK_MergeAndSplitShards(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-merge-split"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})

	desc, err := client.DescribeStream(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(streamName)})
	require.NoError(t, err)
	require.Len(t, desc.StreamDescription.Shards, 1)
	parent := desc.StreamDescription.Shards[0]

	// Split at the midpoint of the parent's hash-key range.
	mid := "170141183460469231731687303715884105728" // 2^127
	_, err = client.SplitShard(ctx, &kinesis.SplitShardInput{
		StreamName:         aws.String(streamName),
		ShardToSplit:       parent.ShardId,
		NewStartingHashKey: aws.String(mid),
	})
	require.NoError(t, err)

	afterSplit, err := client.DescribeStream(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(streamName)})
	require.NoError(t, err)
	open := openShards(afterSplit.StreamDescription.Shards)
	require.Len(t, open, 2, "split yields two open children")
	// Parent is now closed (has an EndingSequenceNumber).
	assert.Equal(t, 3, len(afterSplit.StreamDescription.Shards), "parent + two children")

	// Merge the two open children back together.
	_, err = client.MergeShards(ctx, &kinesis.MergeShardsInput{
		StreamName:           aws.String(streamName),
		ShardToMerge:         open[0].ShardId,
		AdjacentShardToMerge: open[1].ShardId,
	})
	require.NoError(t, err)

	afterMerge, err := client.DescribeStream(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(streamName)})
	require.NoError(t, err)
	require.Len(t, openShards(afterMerge.StreamDescription.Shards), 1, "merge collapses to one open shard")
}

// TestKinesisSDK_UpdateShardCountReshardsByLineage scales a stream through
// UpdateShardCount and reads a closed parent to its end: the parent's last
// GetRecords carries no NextShardIterator and names the children to read next.
func TestKinesisSDK_UpdateShardCountReshardsByLineage(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-reshard-lineage"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(2),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	put, err := client.PutRecord(ctx, &kinesis.PutRecordInput{
		StreamName: aws.String(streamName), PartitionKey: aws.String("before-scaling"), Data: []byte("parent-record"),
	})
	require.NoError(t, err)

	updated, err := client.UpdateShardCount(ctx, &kinesis.UpdateShardCountInput{
		StreamName:       aws.String(streamName),
		TargetShardCount: aws.Int32(3),
		ScalingType:      ktypes.ScalingTypeUniformScaling,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), aws.ToInt32(updated.CurrentShardCount))

	listed, err := client.ListShards(ctx, &kinesis.ListShardsInput{StreamName: aws.String(streamName)})
	require.NoError(t, err)
	byID := map[string]ktypes.Shard{}
	for _, shard := range listed.Shards {
		byID[aws.ToString(shard.ShardId)] = shard
	}
	open := openShards(listed.Shards)
	require.Len(t, open, 3)
	for _, shard := range open {
		parent, ok := byID[aws.ToString(shard.ParentShardId)]
		require.True(t, ok, "open shard %s names a listed parent", aws.ToString(shard.ShardId))
		require.NotNil(t, parent.SequenceNumberRange.EndingSequenceNumber, "parent %s is closed", aws.ToString(parent.ShardId))
	}

	parent := byID[aws.ToString(put.ShardId)]
	require.NotNil(t, parent.SequenceNumberRange.EndingSequenceNumber)
	assert.Equal(t, aws.ToString(put.SequenceNumber), aws.ToString(parent.SequenceNumberRange.EndingSequenceNumber))
	iterator, err := client.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName:        aws.String(streamName),
		ShardId:           put.ShardId,
		ShardIteratorType: ktypes.ShardIteratorTypeTrimHorizon,
	})
	require.NoError(t, err)
	records, err := client.GetRecords(ctx, &kinesis.GetRecordsInput{ShardIterator: iterator.ShardIterator})
	require.NoError(t, err)
	require.Len(t, records.Records, 1)
	assert.Equal(t, []byte("parent-record"), records.Records[0].Data)
	assert.Nil(t, records.NextShardIterator, "a drained closed shard has no next iterator")
	require.NotEmpty(t, records.ChildShards)
	for _, child := range records.ChildShards {
		assert.Contains(t, child.ParentShards, aws.ToString(put.ShardId))
		_, listedChild := byID[aws.ToString(child.ShardId)]
		assert.True(t, listedChild, "child %s is a listed shard", aws.ToString(child.ShardId))
	}
}

func openShards(shards []ktypes.Shard) []ktypes.Shard {
	var out []ktypes.Shard
	for _, s := range shards {
		if s.SequenceNumberRange == nil || s.SequenceNumberRange.EndingSequenceNumber == nil {
			out = append(out, s)
		}
	}
	return out
}

// TestKinesisSDK_TagResource covers the resource-ARN tagging trio.
func TestKinesisSDK_TagResource(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-tag-resource"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	streamARN := aws.ToString(summary.StreamDescriptionSummary.StreamARN)

	_, err = client.TagResource(ctx, &kinesis.TagResourceInput{
		ResourceARN: aws.String(streamARN),
		Tags:        map[string]string{"team": "data", "env": "test"},
	})
	require.NoError(t, err)

	tags, err := client.ListTagsForResource(ctx, &kinesis.ListTagsForResourceInput{
		ResourceARN: aws.String(streamARN),
	})
	require.NoError(t, err)
	got := map[string]string{}
	for _, tag := range tags.Tags {
		got[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, map[string]string{"team": "data", "env": "test"}, got)

	_, err = client.UntagResource(ctx, &kinesis.UntagResourceInput{
		ResourceARN: aws.String(streamARN),
		TagKeys:     []string{"env"},
	})
	require.NoError(t, err)
	tags, err = client.ListTagsForResource(ctx, &kinesis.ListTagsForResourceInput{
		ResourceARN: aws.String(streamARN),
	})
	require.NoError(t, err)
	got = map[string]string{}
	for _, tag := range tags.Tags {
		got[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, map[string]string{"team": "data"}, got)
}

// TestKinesisSDK_UpdateStreamMode toggles a stream between capacity modes and
// confirms the new mode reads back on the summary.
func TestKinesisSDK_UpdateStreamMode(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-stream-mode"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	streamARN := aws.ToString(summary.StreamDescriptionSummary.StreamARN)
	assert.Equal(t, ktypes.StreamModeProvisioned, summary.StreamDescriptionSummary.StreamModeDetails.StreamMode)

	_, err = client.UpdateStreamMode(ctx, &kinesis.UpdateStreamModeInput{
		StreamARN:         aws.String(streamARN),
		StreamModeDetails: &ktypes.StreamModeDetails{StreamMode: ktypes.StreamModeOnDemand},
	})
	require.NoError(t, err)

	summary, err = client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	assert.Equal(t, ktypes.StreamModeOnDemand, summary.StreamDescriptionSummary.StreamModeDetails.StreamMode)
}

// TestKinesisSDK_AccountSettings round-trips the minimum-throughput billing
// commitment via Update/Describe.
func TestKinesisSDK_AccountSettings(t *testing.T) {
	client := kinesisClient()

	upd, err := client.UpdateAccountSettings(ctx, &kinesis.UpdateAccountSettingsInput{
		MinimumThroughputBillingCommitment: &ktypes.MinimumThroughputBillingCommitmentInput{
			Status: ktypes.MinimumThroughputBillingCommitmentInputStatusEnabled,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, upd.MinimumThroughputBillingCommitment)
	assert.Equal(t, ktypes.MinimumThroughputBillingCommitmentOutputStatusEnabled, upd.MinimumThroughputBillingCommitment.Status)
	assert.NotNil(t, upd.MinimumThroughputBillingCommitment.StartedAt)

	desc, err := client.DescribeAccountSettings(ctx, &kinesis.DescribeAccountSettingsInput{})
	require.NoError(t, err)
	require.NotNil(t, desc.MinimumThroughputBillingCommitment)
	assert.Equal(t, ktypes.MinimumThroughputBillingCommitmentOutputStatusEnabled, desc.MinimumThroughputBillingCommitment.Status)

	// Reset to disabled so other tests / runs start clean.
	_, err = client.UpdateAccountSettings(ctx, &kinesis.UpdateAccountSettingsInput{
		MinimumThroughputBillingCommitment: &ktypes.MinimumThroughputBillingCommitmentInput{
			Status: ktypes.MinimumThroughputBillingCommitmentInputStatusDisabled,
		},
	})
	require.NoError(t, err)
}

// TestKinesisSDK_UpdateMaxRecordSize sets the per-record max size and rejects
// out-of-range values.
func TestKinesisSDK_UpdateMaxRecordSize(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-max-record-size"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	streamARN := aws.ToString(summary.StreamDescriptionSummary.StreamARN)

	_, err = client.UpdateMaxRecordSize(ctx, &kinesis.UpdateMaxRecordSizeInput{
		StreamARN:          aws.String(streamARN),
		MaxRecordSizeInKiB: aws.Int32(2048),
	})
	require.NoError(t, err)

	_, err = client.UpdateMaxRecordSize(ctx, &kinesis.UpdateMaxRecordSizeInput{
		StreamARN:          aws.String(streamARN),
		MaxRecordSizeInKiB: aws.Int32(99999),
	})
	requireAWSErrorCode(t, err, "ValidationException")
}

// TestKinesisSDK_UpdateStreamWarmThroughput sets a warm-throughput target and
// reads the echoed configuration back.
func TestKinesisSDK_UpdateStreamWarmThroughput(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-warm-throughput"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	streamARN := aws.ToString(summary.StreamDescriptionSummary.StreamARN)

	out, err := client.UpdateStreamWarmThroughput(ctx, &kinesis.UpdateStreamWarmThroughputInput{
		StreamARN:           aws.String(streamARN),
		WarmThroughputMiBps: aws.Int32(64),
	})
	require.NoError(t, err)
	assert.Equal(t, streamARN, aws.ToString(out.StreamARN))
	assert.Equal(t, streamName, aws.ToString(out.StreamName))
	require.NotNil(t, out.WarmThroughput)
	assert.Equal(t, int32(64), aws.ToInt32(out.WarmThroughput.TargetMiBps))
}
