package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	kinesistypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKinesis_SubscribeToShard registers an enhanced fan-out consumer, puts
// records onto a shard, then subscribes and reassembles the
// SubscribeToShardEvent frames the handler pushes over the event stream,
// asserting the stored records surface with their continuation metadata.
func TestKinesis_SubscribeToShard(t *testing.T) {
	client := kinesisClient()
	streamName := "subscribe-stream"

	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(streamName),
		ShardCount: aws.Int32(1),
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
	streamARN := aws.ToString(desc.StreamDescription.StreamARN)
	require.NotEmpty(t, desc.StreamDescription.Shards)
	shardID := aws.ToString(desc.StreamDescription.Shards[0].ShardId)

	// Put records — all on the single shard.
	payloads := [][]byte{[]byte("efo-record-0"), []byte("efo-record-1")}
	for _, p := range payloads {
		_, err = client.PutRecord(ctx, &kinesis.PutRecordInput{
			StreamName:   aws.String(streamName),
			Data:         p,
			PartitionKey: aws.String("pk"),
		})
		require.NoError(t, err)
	}

	reg, err := client.RegisterStreamConsumer(ctx, &kinesis.RegisterStreamConsumerInput{
		StreamARN:    aws.String(streamARN),
		ConsumerName: aws.String("efo-consumer"),
	})
	require.NoError(t, err)
	consumerARN := aws.ToString(reg.Consumer.ConsumerARN)
	require.NotEmpty(t, consumerARN)

	out, err := client.SubscribeToShard(ctx, &kinesis.SubscribeToShardInput{
		ConsumerARN: aws.String(consumerARN),
		ShardId:     aws.String(shardID),
		StartingPosition: &kinesistypes.StartingPosition{
			Type: kinesistypes.ShardIteratorTypeTrimHorizon,
		},
	})
	require.NoError(t, err)

	es := out.GetStream()
	defer es.Close()

	var got []string
	sawEvent := false
	for ev := range es.Events() {
		if e, ok := ev.(*kinesistypes.SubscribeToShardEventStreamMemberSubscribeToShardEvent); ok {
			sawEvent = true
			require.NotNil(t, e.Value.ContinuationSequenceNumber)
			require.NotNil(t, e.Value.MillisBehindLatest)
			assert.Equal(t, int64(0), aws.ToInt64(e.Value.MillisBehindLatest))
			for _, rec := range e.Value.Records {
				got = append(got, string(rec.Data))
			}
		}
	}
	require.NoError(t, es.Err())
	assert.True(t, sawEvent, "stream must carry a SubscribeToShardEvent")
	assert.ElementsMatch(t, []string{"efo-record-0", "efo-record-1"}, got)
}
