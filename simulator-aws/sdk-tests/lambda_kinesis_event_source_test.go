package aws_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kinesisEvent is the event AWS Lambda hands a function for a Kinesis batch.
type kinesisEvent struct {
	Records []struct {
		Kinesis struct {
			Data           string `json:"data"`
			SequenceNumber string `json:"sequenceNumber"`
			PartitionKey   string `json:"partitionKey"`
		} `json:"kinesis"`
		EventSource    string `json:"eventSource"`
		EventSourceARN string `json:"eventSourceARN"`
		EventID        string `json:"eventID"`
	} `json:"Records"`
}

func (e kinesisEvent) data(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, record := range e.Records {
		decoded, err := base64.StdEncoding.DecodeString(record.Kinesis.Data)
		require.NoError(t, err)
		out = append(out, string(decoded))
	}
	return out
}

// invocationCallback is the function's brain: the test handler forwards every
// event to it and answers with what the test returns for it.
type invocationCallback struct {
	url    string
	events chan kinesisEvent
	answer chan int
}

func newInvocationCallback(t *testing.T) *invocationCallback {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	c := &invocationCallback{events: make(chan kinesisEvent), answer: make(chan int)}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var event kinesisEvent
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case c.events <- event:
		case <-r.Context().Done():
			return
		}
		status := <-c.answer
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	})}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	c.url = fmt.Sprintf("http://host.docker.internal:%d/invoke", ln.Addr().(*net.TCPAddr).Port)
	return c
}

// next waits for the function's next event and answers it with status.
func (c *invocationCallback) next(t *testing.T, status int) kinesisEvent {
	t.Helper()
	select {
	case event := <-c.events:
		c.answer <- status
		return event
	case <-time.After(2 * time.Minute):
		t.Fatal("the function never received the next batch")
		return kinesisEvent{}
	}
}

func createKinesisMappingFixture(t *testing.T, name string, callback *invocationCallback) (streamARN string) {
	t.Helper()
	kc := kinesisClient()
	lc := lambdaClient()
	_, err := kc.CreateStream(ctx, &kinesis.CreateStreamInput{StreamName: aws.String(name), ShardCount: aws.Int32(1)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = kc.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(name)}) })
	require.NoError(t, kinesis.NewStreamExistsWaiter(kc, func(o *kinesis.StreamExistsWaiterOptions) {
		o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
	}).Wait(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(name)}, 2*time.Minute))
	described, err := kc.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(name)})
	require.NoError(t, err)

	_, err = lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String(name),
		Role:          aws.String(lambdaRole(t, "kinesis:GetShardIterator", "kinesis:GetRecords", "sqs:SendMessage")),
		PackageType:   lambdatypes.PackageTypeImage,
		Code:          &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Architectures: nativeLambdaArchitectures(),
		Timeout:       aws.Int32(60),
		Environment:   &lambdatypes.Environment{Variables: map[string]string{"INVOCATION_CALLBACK_URL": callback.url}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)}) })
	return aws.ToString(described.StreamDescriptionSummary.StreamARN)
}

func putKinesisRecords(t *testing.T, stream string, records ...string) {
	t.Helper()
	kc := kinesisClient()
	for _, record := range records {
		_, err := kc.PutRecord(ctx, &kinesis.PutRecordInput{StreamName: aws.String(stream), PartitionKey: aws.String("p"), Data: []byte(record)})
		require.NoError(t, err)
	}
}

// "Lambda processes records in each shard in order", checkpointing what the
// function handled, so a later batch carries only what arrived since
// (with-kinesis.md.gz).
func TestLambda_KinesisEventSourceMappingDeliversRecordsInOrder(t *testing.T) {
	t.Parallel()
	const name = "esm-kinesis-order"
	callback := newInvocationCallback(t)
	streamARN := createKinesisMappingFixture(t, name, callback)
	putKinesisRecords(t, name, "one", "two", "three")

	lc := lambdaClient()
	mapping, err := lc.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		FunctionName: aws.String(name), EventSourceArn: aws.String(streamARN),
		StartingPosition: lambdatypes.EventSourcePositionTrimHorizon, BatchSize: aws.Int32(10),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: mapping.UUID})
	})
	assert.Equal(t, int32(-1), aws.ToInt32(mapping.MaximumRetryAttempts), "MaximumRetryAttempts defaults to -1, infinite")
	assert.Equal(t, int32(-1), aws.ToInt32(mapping.MaximumRecordAgeInSeconds))
	assert.Equal(t, int32(1), aws.ToInt32(mapping.ParallelizationFactor))

	first := callback.next(t, http.StatusOK)
	assert.Equal(t, []string{"one", "two", "three"}, first.data(t))
	assert.Equal(t, "aws:kinesis", first.Records[0].EventSource)
	assert.Equal(t, streamARN, first.Records[0].EventSourceARN)

	putKinesisRecords(t, name, "four")
	second := callback.next(t, http.StatusOK)
	assert.Equal(t, []string{"four"}, second.data(t), "the handled batch is checkpointed and not delivered again")
}

// A batch the function keeps failing is retried until MaximumRetryAttempts is
// spent, then its description goes to the on-failure destination and the
// shard moves on (kinesis-on-failure-destination.md.gz).
func TestLambda_KinesisEventSourceMappingDiscardsAfterItsRetries(t *testing.T) {
	t.Parallel()
	const name = "esm-kinesis-retries"
	callback := newInvocationCallback(t)
	streamARN := createKinesisMappingFixture(t, name, callback)
	sqsC := sqsClient()
	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name + "-failures")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })
	attributes, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: queue.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	putKinesisRecords(t, name, "poison")

	lc := lambdaClient()
	mapping, err := lc.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		FunctionName: aws.String(name), EventSourceArn: aws.String(streamARN),
		StartingPosition: lambdatypes.EventSourcePositionTrimHorizon, MaximumRetryAttempts: aws.Int32(1),
		DestinationConfig: &lambdatypes.DestinationConfig{OnFailure: &lambdatypes.OnFailure{
			Destination: aws.String(attributes.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: mapping.UUID})
	})

	assert.Equal(t, []string{"poison"}, callback.next(t, http.StatusInternalServerError).data(t))
	assert.Equal(t, []string{"poison"}, callback.next(t, http.StatusInternalServerError).data(t), "one retry")

	body := aws.ToString(receiveSQSMessages(t, sqsC, queue.QueueUrl, 1, 2*time.Minute)[0].Body)
	var record struct {
		RequestContext   map[string]any `json:"requestContext"`
		KinesisBatchInfo map[string]any `json:"KinesisBatchInfo"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &record))
	assert.Equal(t, "RetryAttemptsExhausted", record.RequestContext["condition"])
	assert.Equal(t, streamARN, record.KinesisBatchInfo["streamArn"])
	assert.Equal(t, float64(1), record.KinesisBatchInfo["batchSize"])

	putKinesisRecords(t, name, "healthy")
	assert.Equal(t, []string{"healthy"}, callback.next(t, http.StatusOK).data(t), "the shard moves past the discarded batch")
}
