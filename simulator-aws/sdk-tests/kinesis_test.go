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

func kinesisClient() *kinesis.Client {
	return kinesis.NewFromConfig(sdkConfig(), func(o *kinesis.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestKinesisSDK_StreamLifecycleAndRecords(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-stream"

	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(2),
		Tags: map[string]string{
			"env": "sdk",
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{
			StreamName: aws.String(streamName),
		})
	})

	desc, err := client.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	require.NotNil(t, desc.StreamDescription)
	assert.Equal(t, streamName, aws.ToString(desc.StreamDescription.StreamName))
	assert.Equal(t, ktypes.StreamStatusActive, desc.StreamDescription.StreamStatus)
	require.Len(t, desc.StreamDescription.Shards, 2)

	summary, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	require.NotNil(t, summary.StreamDescriptionSummary)
	assert.Equal(t, int32(2), aws.ToInt32(summary.StreamDescriptionSummary.OpenShardCount))

	list, err := client.ListStreams(ctx, &kinesis.ListStreamsInput{})
	require.NoError(t, err)
	assert.Contains(t, list.StreamNames, streamName)
	// StreamSummaries entries are the StreamSummary shape — identity +
	// status + mode only (retention/shards/monitoring ride the describes).
	var streamSummary *ktypes.StreamSummary
	for i := range list.StreamSummaries {
		if aws.ToString(list.StreamSummaries[i].StreamName) == streamName {
			streamSummary = &list.StreamSummaries[i]
		}
	}
	require.NotNil(t, streamSummary, "ListStreams must include a StreamSummary for %s", streamName)
	assert.Contains(t, aws.ToString(streamSummary.StreamARN), ":stream/"+streamName)
	assert.Equal(t, ktypes.StreamStatusActive, streamSummary.StreamStatus)
	require.NotNil(t, streamSummary.StreamModeDetails)
	assert.Equal(t, ktypes.StreamModeProvisioned, streamSummary.StreamModeDetails.StreamMode)
	assert.NotNil(t, streamSummary.StreamCreationTimestamp)

	_, err = client.AddTagsToStream(ctx, &kinesis.AddTagsToStreamInput{
		StreamName: aws.String(streamName),
		Tags:       map[string]string{"team": "platform"},
	})
	require.NoError(t, err)
	tags, err := client.ListTagsForStream(ctx, &kinesis.ListTagsForStreamInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	tagMap := map[string]string{}
	for _, tag := range tags.Tags {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, map[string]string{"env": "sdk", "team": "platform"}, tagMap)

	_, err = client.RemoveTagsFromStream(ctx, &kinesis.RemoveTagsFromStreamInput{
		StreamName: aws.String(streamName),
		TagKeys:    []string{"team"},
	})
	require.NoError(t, err)

	_, err = client.IncreaseStreamRetentionPeriod(ctx, &kinesis.IncreaseStreamRetentionPeriodInput{
		StreamName:           aws.String(streamName),
		RetentionPeriodHours: aws.Int32(48),
	})
	require.NoError(t, err)
	_, err = client.DecreaseStreamRetentionPeriod(ctx, &kinesis.DecreaseStreamRetentionPeriodInput{
		StreamName:           aws.String(streamName),
		RetentionPeriodHours: aws.Int32(24),
	})
	require.NoError(t, err)

	enabled, err := client.EnableEnhancedMonitoring(ctx, &kinesis.EnableEnhancedMonitoringInput{
		StreamName:        aws.String(streamName),
		ShardLevelMetrics: []ktypes.MetricsName{ktypes.MetricsNameIncomingBytes},
	})
	require.NoError(t, err)
	assert.Contains(t, enabled.CurrentShardLevelMetrics, ktypes.MetricsNameIncomingBytes)
	disabled, err := client.DisableEnhancedMonitoring(ctx, &kinesis.DisableEnhancedMonitoringInput{
		StreamName:        aws.String(streamName),
		ShardLevelMetrics: []ktypes.MetricsName{ktypes.MetricsNameIncomingBytes},
	})
	require.NoError(t, err)
	assert.NotContains(t, disabled.CurrentShardLevelMetrics, ktypes.MetricsNameIncomingBytes)

	_, err = client.StartStreamEncryption(ctx, &kinesis.StartStreamEncryptionInput{
		StreamName:     aws.String(streamName),
		EncryptionType: ktypes.EncryptionTypeKms,
		KeyId:          aws.String("alias/aws/kinesis"),
	})
	require.NoError(t, err)
	_, err = client.StopStreamEncryption(ctx, &kinesis.StopStreamEncryptionInput{
		StreamName:     aws.String(streamName),
		EncryptionType: ktypes.EncryptionTypeKms,
		KeyId:          aws.String("alias/aws/kinesis"),
	})
	require.NoError(t, err)

	update, err := client.UpdateShardCount(ctx, &kinesis.UpdateShardCountInput{
		StreamName:       aws.String(streamName),
		TargetShardCount: aws.Int32(3),
		ScalingType:      ktypes.ScalingTypeUniformScaling,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), aws.ToInt32(update.TargetShardCount))

	limits, err := client.DescribeLimits(ctx, &kinesis.DescribeLimitsInput{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, aws.ToInt32(limits.OpenShardCount), int32(3))
	assert.Greater(t, aws.ToInt32(limits.ShardLimit), int32(0))

	put, err := client.PutRecord(ctx, &kinesis.PutRecordInput{
		StreamName:   aws.String(streamName),
		PartitionKey: aws.String("pk-1"),
		Data:         []byte("one"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(put.ShardId))
	require.NotEmpty(t, aws.ToString(put.SequenceNumber))

	putMany, err := client.PutRecords(ctx, &kinesis.PutRecordsInput{
		StreamName: aws.String(streamName),
		Records: []ktypes.PutRecordsRequestEntry{
			{PartitionKey: aws.String("pk-1"), Data: []byte("two")},
			{PartitionKey: aws.String("pk-2"), Data: []byte("three")},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), aws.ToInt32(putMany.FailedRecordCount))
	require.Len(t, putMany.Records, 2)

	shards, err := client.ListShards(ctx, &kinesis.ListShardsInput{
		StreamName: aws.String(streamName),
	})
	require.NoError(t, err)
	require.NotEmpty(t, shards.Shards)
	bodies := map[string]bool{}
	require.Eventually(t, func() bool {
		for _, shard := range shards.Shards {
			iter, err := client.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
				StreamName:        aws.String(streamName),
				ShardId:           shard.ShardId,
				ShardIteratorType: ktypes.ShardIteratorTypeTrimHorizon,
			})
			require.NoError(t, err)
			require.NotEmpty(t, aws.ToString(iter.ShardIterator))
			out, err := client.GetRecords(ctx, &kinesis.GetRecordsInput{
				ShardIterator: iter.ShardIterator,
				Limit:         aws.Int32(10),
			})
			require.NoError(t, err)
			for _, record := range out.Records {
				bodies[string(record.Data)] = true
			}
		}
		return len(bodies) == 3
	}, 2*time.Second, 50*time.Millisecond)
	assert.Equal(t, map[string]bool{"one": true, "two": true, "three": true}, bodies)
}

// TestKinesisSDK_ListShardsPagination drives ListShards across multiple pages
// via MaxResults + NextToken, asserting the page caps, the truncation token,
// the strictly-after resume, and that the union covers every shard exactly once.
func TestKinesisSDK_ListShardsPagination(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-shard-paging"

	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(5),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})

	// First page: cap at 2, expect a NextToken because 5 > 2.
	page1, err := client.ListShards(ctx, &kinesis.ListShardsInput{
		StreamName: aws.String(streamName),
		MaxResults: aws.Int32(2),
	})
	require.NoError(t, err)
	require.Len(t, page1.Shards, 2)
	require.NotEmpty(t, aws.ToString(page1.NextToken), "ListShards must return NextToken when truncated")

	// Resume strictly after the first page; per the SDK contract, NextToken is
	// specified WITHOUT StreamName.
	page2, err := client.ListShards(ctx, &kinesis.ListShardsInput{
		MaxResults: aws.Int32(2),
		NextToken:  page1.NextToken,
	})
	require.NoError(t, err)
	require.Len(t, page2.Shards, 2)
	require.NotEmpty(t, aws.ToString(page2.NextToken))

	page3, err := client.ListShards(ctx, &kinesis.ListShardsInput{
		MaxResults: aws.Int32(2),
		NextToken:  page2.NextToken,
	})
	require.NoError(t, err)
	require.Len(t, page3.Shards, 1, "final page holds the remaining shard")
	assert.Empty(t, aws.ToString(page3.NextToken), "no NextToken on the last page")

	seen := map[string]int{}
	for _, p := range [][]ktypes.Shard{page1.Shards, page2.Shards, page3.Shards} {
		for _, s := range p {
			seen[aws.ToString(s.ShardId)]++
		}
	}
	assert.Len(t, seen, 5, "every shard appears exactly once across pages")
	for id, n := range seen {
		assert.Equal(t, 1, n, "shard %s must not repeat across pages", id)
	}

	// ExclusiveStartShardId resumes after a given shard id (legacy cursor).
	exclusive, err := client.ListShards(ctx, &kinesis.ListShardsInput{
		StreamName:            aws.String(streamName),
		ExclusiveStartShardId: page1.Shards[1].ShardId,
	})
	require.NoError(t, err)
	require.Len(t, exclusive.Shards, 3)
	assert.Greater(t, aws.ToString(exclusive.Shards[0].ShardId), aws.ToString(page1.Shards[1].ShardId))
}

// TestKinesisSDK_ListStreamsPagination drives ListStreams across pages via
// Limit + NextToken and asserts HasMoreStreams + the cursor advance.
func TestKinesisSDK_ListStreamsPagination(t *testing.T) {
	client := kinesisClient()
	names := []string{
		"sdk-paging-stream-aaa",
		"sdk-paging-stream-bbb",
		"sdk-paging-stream-ccc",
	}
	for _, n := range names {
		_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
			StreamName: aws.String(n),
			ShardCount: aws.Int32(1),
		})
		require.NoError(t, err)
		nm := n
		t.Cleanup(func() {
			_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(nm)})
		})
	}

	// Page through ALL streams two-at-a-time, collecting only our three names
	// (other tests' streams may coexist). Verify our trio appears once each and
	// that pagination terminates.
	collected := map[string]int{}
	var token *string
	pages := 0
	for {
		out, err := client.ListStreams(ctx, &kinesis.ListStreamsInput{
			Limit:     aws.Int32(2),
			NextToken: token,
		})
		require.NoError(t, err)
		assert.LessOrEqual(t, len(out.StreamNames), 2, "Limit caps the page")
		for _, n := range out.StreamNames {
			collected[n]++
		}
		if !aws.ToBool(out.HasMoreStreams) {
			assert.Empty(t, aws.ToString(out.NextToken), "no NextToken once HasMoreStreams is false")
			break
		}
		require.NotEmpty(t, aws.ToString(out.NextToken), "HasMoreStreams=true must carry a NextToken")
		token = out.NextToken
		pages++
		require.Less(t, pages, 100, "pagination must terminate")
	}
	for _, n := range names {
		assert.Equal(t, 1, collected[n], "stream %s must appear exactly once across pages", n)
	}
}

// TestKinesisSDK_DescribeStreamShardPagination pins that DescribeStream honors
// Limit + ExclusiveStartShardId and sets HasMoreShards (it previously returned
// every shard with HasMoreShards hardcoded false).
func TestKinesisSDK_DescribeStreamShardPagination(t *testing.T) {
	client := kinesisClient()
	streamName := "sdk-kinesis-describe-paging"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(3),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})

	page1, err := client.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName: aws.String(streamName),
		Limit:      aws.Int32(2),
	})
	require.NoError(t, err)
	require.Len(t, page1.StreamDescription.Shards, 2)
	require.True(t, aws.ToBool(page1.StreamDescription.HasMoreShards), "HasMoreShards true when more remain")

	last := page1.StreamDescription.Shards[1].ShardId
	page2, err := client.DescribeStream(ctx, &kinesis.DescribeStreamInput{
		StreamName:            aws.String(streamName),
		ExclusiveStartShardId: last,
	})
	require.NoError(t, err)
	require.Len(t, page2.StreamDescription.Shards, 1, "resume after the cursor")
	assert.False(t, aws.ToBool(page2.StreamDescription.HasMoreShards))
}
