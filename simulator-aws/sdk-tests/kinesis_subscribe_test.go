package aws_sdk_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	kinesistypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kinesisEventGuard bounds how long a test waits for an event the simulator
// owes it; the wait itself ends on the event.
const kinesisEventGuard = 30 * time.Second

type kinesisSubscribeFixture struct {
	client    *kinesis.Client
	stream    string
	streamARN string
	shardID   string
}

func newKinesisSubscribeFixture(t *testing.T, stream string) kinesisSubscribeFixture {
	t.Helper()
	client := kinesisClient()
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(stream),
		ShardCount: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{
			StreamName:              aws.String(stream),
			EnforceConsumerDeletion: aws.Bool(true),
		})
	})
	desc, err := client.DescribeStream(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(stream)})
	require.NoError(t, err)
	require.Len(t, desc.StreamDescription.Shards, 1)
	return kinesisSubscribeFixture{
		client:    client,
		stream:    stream,
		streamARN: aws.ToString(desc.StreamDescription.StreamARN),
		shardID:   aws.ToString(desc.StreamDescription.Shards[0].ShardId),
	}
}

func (f kinesisSubscribeFixture) register(t *testing.T, name string, tags map[string]string) string {
	t.Helper()
	reg, err := f.client.RegisterStreamConsumer(ctx, &kinesis.RegisterStreamConsumerInput{
		StreamARN:    aws.String(f.streamARN),
		ConsumerName: aws.String(name),
		Tags:         tags,
	})
	require.NoError(t, err)
	consumerARN := aws.ToString(reg.Consumer.ConsumerARN)
	waitForKinesisConsumerActive(t, f.client, consumerARN)
	return consumerARN
}

func (f kinesisSubscribeFixture) put(t *testing.T, data string) string {
	t.Helper()
	out, err := f.client.PutRecord(ctx, &kinesis.PutRecordInput{
		StreamName:   aws.String(f.stream),
		Data:         []byte(data),
		PartitionKey: aws.String("pk"),
	})
	require.NoError(t, err)
	return aws.ToString(out.SequenceNumber)
}

func (f kinesisSubscribeFixture) subscribe(t *testing.T, consumerARN string, position kinesistypes.StartingPosition) *kinesis.SubscribeToShardEventStream {
	t.Helper()
	out, err := f.client.SubscribeToShard(ctx, &kinesis.SubscribeToShardInput{
		ConsumerARN:      aws.String(consumerARN),
		ShardId:          aws.String(f.shardID),
		StartingPosition: &position,
	})
	require.NoError(t, err)
	es := out.GetStream()
	t.Cleanup(func() { _ = es.Close() })
	return es
}

// nextKinesisEvent receives the next SubscribeToShardEvent; it fails the test
// when the stream ends first.
func nextKinesisEvent(t *testing.T, es *kinesis.SubscribeToShardEventStream) kinesistypes.SubscribeToShardEvent {
	t.Helper()
	select {
	case ev, ok := <-es.Events():
		require.True(t, ok, "the event stream ended early: %v", es.Err())
		member, ok := ev.(*kinesistypes.SubscribeToShardEventStreamMemberSubscribeToShardEvent)
		require.True(t, ok, "unexpected event %T", ev)
		return member.Value
	case <-time.After(kinesisEventGuard):
		t.Fatal("no SubscribeToShardEvent arrived")
	}
	return kinesistypes.SubscribeToShardEvent{}
}

// kinesisStreamEnd waits for the event stream to end and returns its error.
func kinesisStreamEnd(t *testing.T, es *kinesis.SubscribeToShardEventStream) error {
	t.Helper()
	for {
		select {
		case ev, ok := <-es.Events():
			if !ok {
				return es.Err()
			}
			t.Fatalf("unexpected event %T before the stream ended", ev)
		case <-time.After(kinesisEventGuard):
			t.Fatal("the event stream did not end")
		}
	}
}

func kinesisRecordData(records []kinesistypes.Record) []string {
	out := make([]string, 0, len(records))
	for _, rec := range records {
		out = append(out, string(rec.Data))
	}
	return out
}

// TestKinesis_SubscribeToShard subscribes an enhanced fan-out consumer, then
// puts records and receives them over the held event stream as they land.
func TestKinesis_SubscribeToShard(t *testing.T) {
	f := newKinesisSubscribeFixture(t, "subscribe-stream")
	f.put(t, "efo-record-0")
	second := f.put(t, "efo-record-1")
	consumerARN := f.register(t, "efo-consumer", map[string]string{"team": "streams"})

	es := f.subscribe(t, consumerARN, kinesistypes.StartingPosition{Type: kinesistypes.ShardIteratorTypeTrimHorizon})
	backlog := nextKinesisEvent(t, es)
	assert.Equal(t, []string{"efo-record-0", "efo-record-1"}, kinesisRecordData(backlog.Records))
	assert.Equal(t, second, aws.ToString(backlog.ContinuationSequenceNumber))
	assert.Equal(t, int64(0), aws.ToInt64(backlog.MillisBehindLatest))
	assert.Empty(t, backlog.ChildShards)

	_, err := f.client.SubscribeToShard(ctx, &kinesis.SubscribeToShardInput{
		ConsumerARN:      aws.String(consumerARN),
		ShardId:          aws.String(f.shardID),
		StartingPosition: &kinesistypes.StartingPosition{Type: kinesistypes.ShardIteratorTypeLatest},
	})
	var inUse *kinesistypes.ResourceInUseException
	require.ErrorAs(t, err, &inUse, "a second subscription within five seconds must be refused")

	third := f.put(t, "efo-record-2")
	live := nextKinesisEvent(t, es)
	assert.Equal(t, []string{"efo-record-2"}, kinesisRecordData(live.Records))
	assert.Equal(t, third, aws.ToString(live.ContinuationSequenceNumber))
	assert.Equal(t, int64(0), aws.ToInt64(live.MillisBehindLatest))

	f.put(t, "efo-record-3")
	assert.Equal(t, []string{"efo-record-3"}, kinesisRecordData(nextKinesisEvent(t, es).Records))

	tags, err := f.client.ListTagsForResource(ctx, &kinesis.ListTagsForResourceInput{ResourceARN: aws.String(consumerARN)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "team", aws.ToString(tags.Tags[0].Key))
	assert.Equal(t, "streams", aws.ToString(tags.Tags[0].Value))

	_, err = f.client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(f.stream)})
	require.ErrorAs(t, err, &inUse, "DeleteStream must refuse a stream with registered consumers unless EnforceConsumerDeletion is set")
}

// TestKinesis_SubscribeToShardStartingPositions opens one subscription per
// StartingPosition type and checks where each starts reading.
func TestKinesis_SubscribeToShardStartingPositions(t *testing.T) {
	f := newKinesisSubscribeFixture(t, "subscribe-positions")
	f.put(t, "r1")
	s2 := f.put(t, "r2")
	s3 := f.put(t, "r3")

	at := f.subscribe(t, f.register(t, "at-seq", nil), kinesistypes.StartingPosition{
		Type: kinesistypes.ShardIteratorTypeAtSequenceNumber, SequenceNumber: aws.String(s2),
	})
	assert.Equal(t, []string{"r2", "r3"}, kinesisRecordData(nextKinesisEvent(t, at).Records))

	after := f.subscribe(t, f.register(t, "after-seq", nil), kinesistypes.StartingPosition{
		Type: kinesistypes.ShardIteratorTypeAfterSequenceNumber, SequenceNumber: aws.String(s2),
	})
	assert.Equal(t, []string{"r3"}, kinesisRecordData(nextKinesisEvent(t, after).Records))

	trimHorizon := f.subscribe(t, f.register(t, "trim-horizon", nil), kinesistypes.StartingPosition{
		Type: kinesistypes.ShardIteratorTypeTrimHorizon,
	})
	all := nextKinesisEvent(t, trimHorizon).Records
	require.Len(t, all, 3)
	r3Arrival := aws.ToTime(all[2].ApproximateArrivalTimestamp)

	atTime := f.subscribe(t, f.register(t, "at-timestamp", nil), kinesistypes.StartingPosition{
		Type: kinesistypes.ShardIteratorTypeAtTimestamp, Timestamp: aws.Time(r3Arrival),
	})
	fromTime := nextKinesisEvent(t, atTime).Records
	require.NotEmpty(t, fromTime)
	assert.Equal(t, "r3", string(fromTime[len(fromTime)-1].Data))
	for _, rec := range fromTime {
		assert.False(t, aws.ToTime(rec.ApproximateArrivalTimestamp).Before(r3Arrival),
			"AT_TIMESTAMP must start at the first record that arrived at or after the timestamp")
	}

	latest := f.subscribe(t, f.register(t, "latest", nil), kinesistypes.StartingPosition{
		Type: kinesistypes.ShardIteratorTypeLatest,
	})
	caughtUp := nextKinesisEvent(t, latest)
	assert.Empty(t, caughtUp.Records, "LATEST must start after the newest record")
	assert.Equal(t, s3, aws.ToString(caughtUp.ContinuationSequenceNumber))

	f.put(t, "r4")
	assert.Equal(t, []string{"r4"}, kinesisRecordData(nextKinesisEvent(t, latest).Records))
	assert.Equal(t, []string{"r4"}, kinesisRecordData(nextKinesisEvent(t, after).Records))
}

// TestKinesis_SubscribeToShardEndsAtShardEnd splits the subscribed shard: the
// subscription delivers the event that names the children and then ends.
func TestKinesis_SubscribeToShardEndsAtShardEnd(t *testing.T) {
	f := newKinesisSubscribeFixture(t, "subscribe-shard-end")
	es := f.subscribe(t, f.register(t, "shard-end", nil), kinesistypes.StartingPosition{
		Type: kinesistypes.ShardIteratorTypeTrimHorizon,
	})
	assert.Empty(t, nextKinesisEvent(t, es).Records)

	_, err := f.client.SplitShard(ctx, &kinesis.SplitShardInput{
		StreamName:         aws.String(f.stream),
		ShardToSplit:       aws.String(f.shardID),
		NewStartingHashKey: aws.String("170141183460469231731687303715884105728"),
	})
	require.NoError(t, err)

	end := nextKinesisEvent(t, es)
	assert.Nil(t, end.ContinuationSequenceNumber, "the event at a shard's end carries no continuation")
	require.Len(t, end.ChildShards, 2)
	for _, child := range end.ChildShards {
		assert.Equal(t, []string{f.shardID}, child.ParentShards)
	}
	require.NoError(t, kinesisStreamEnd(t, es))
}

// TestKinesis_SubscribeToShardEndsWhenConsumerDeregisters deregisters the
// subscribed consumer, which ends the subscription with
// ResourceNotFoundException.
func TestKinesis_SubscribeToShardEndsWhenConsumerDeregisters(t *testing.T) {
	f := newKinesisSubscribeFixture(t, "subscribe-deregister")
	consumerARN := f.register(t, "leaving", nil)
	es := f.subscribe(t, consumerARN, kinesistypes.StartingPosition{Type: kinesistypes.ShardIteratorTypeLatest})
	nextKinesisEvent(t, es)

	_, err := f.client.DeregisterStreamConsumer(ctx, &kinesis.DeregisterStreamConsumerInput{
		ConsumerARN: aws.String(consumerARN),
	})
	require.NoError(t, err)

	var notFound *kinesistypes.ResourceNotFoundException
	require.True(t, errors.As(kinesisStreamEnd(t, es), &notFound),
		"a deregistered consumer's subscription must end with ResourceNotFoundException")
}
