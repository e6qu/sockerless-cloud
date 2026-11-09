package aws_sdk_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/firehose"
	firehosetypes "github.com/aws/aws-sdk-go-v2/service/firehose/types"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKinesisSDK_RecordSizeLimits holds PutRecord and PutRecords to the
// stream's maximum record size, data and partition key together, and
// PutRecords to 500 records and 10 MiB.
func TestKinesisSDK_RecordSizeLimits(t *testing.T) {
	t.Parallel()
	client := kinesisClient()
	name := "sdk-kinesis-record-size"
	_, err := client.CreateStream(ctx, &kinesis.CreateStreamInput{StreamName: aws.String(name), ShardCount: aws.Int32(1)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(name)}) })
	require.NoError(t, kinesis.NewStreamExistsWaiter(client, func(o *kinesis.StreamExistsWaiterOptions) {
		o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
	}).Wait(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(name)}, 2*time.Minute))

	const mib = 1024 * 1024
	invalid := func(err error) {
		t.Helper()
		var refused *ktypes.InvalidArgumentException
		require.True(t, errors.As(err, &refused), "want InvalidArgumentException, got %v", err)
	}
	put := func(size int) error {
		_, err := client.PutRecord(ctx, &kinesis.PutRecordInput{StreamName: aws.String(name), PartitionKey: aws.String("k"), Data: make([]byte, size)})
		return err
	}
	require.NoError(t, put(mib-1), "a record of 1 MiB with its partition key fits")
	invalid(put(mib))

	_, err = client.UpdateMaxRecordSize(ctx, &kinesis.UpdateMaxRecordSizeInput{StreamARN: aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/" + name), MaxRecordSizeInKiB: aws.Int32(2048)})
	require.NoError(t, err)
	require.NoError(t, put(mib+mib/2), "a raised maximum record size admits larger records")
	invalid(put(2 * mib))

	entries := func(n, size int) []ktypes.PutRecordsRequestEntry {
		out := make([]ktypes.PutRecordsRequestEntry, n)
		for i := range out {
			out[i] = ktypes.PutRecordsRequestEntry{PartitionKey: aws.String("k"), Data: make([]byte, size)}
		}
		return out
	}
	_, err = client.PutRecords(ctx, &kinesis.PutRecordsInput{StreamName: aws.String(name), Records: entries(501, 1)})
	invalid(err)
	_, err = client.PutRecords(ctx, &kinesis.PutRecordsInput{StreamName: aws.String(name), Records: entries(6, 2*mib-1)})
	invalid(err)
	_, err = client.PutRecords(ctx, &kinesis.PutRecordsInput{StreamName: aws.String(name), Records: append(entries(1, 10), ktypes.PutRecordsRequestEntry{PartitionKey: aws.String("k"), Data: make([]byte, 2*mib)})})
	invalid(err)
	out, err := client.PutRecords(ctx, &kinesis.PutRecordsInput{StreamName: aws.String(name), Records: entries(500, 10)})
	require.NoError(t, err)
	assert.Equal(t, int32(0), aws.ToInt32(out.FailedRecordCount))
	assert.Len(t, out.Records, 500)
}

// TestFirehose_RecordSizeLimits holds PutRecord to 1,000 KiB and
// PutRecordBatch to 500 records, 1,000 KiB each and 4 MiB in all.
func TestFirehose_RecordSizeLimits(t *testing.T) {
	t.Parallel()
	const name = "sdk-firehose-record-size"
	createImmediateFirehose(t, name)
	client := firehoseClient()
	invalid := func(err error) {
		t.Helper()
		var refused *firehosetypes.InvalidArgumentException
		require.True(t, errors.As(err, &refused), "want InvalidArgumentException, got %v", err)
	}
	_, err := client.PutRecord(ctx, &firehose.PutRecordInput{DeliveryStreamName: aws.String(name), Record: &firehosetypes.Record{Data: make([]byte, 1000*1024+1)}})
	invalid(err)
	_, err = client.PutRecord(ctx, &firehose.PutRecordInput{DeliveryStreamName: aws.String(name), Record: &firehosetypes.Record{Data: bytes.Repeat([]byte("a"), 1000*1024)}})
	require.NoError(t, err)

	records := func(n, size int) []firehosetypes.Record {
		out := make([]firehosetypes.Record, n)
		for i := range out {
			out[i] = firehosetypes.Record{Data: bytes.Repeat([]byte("b"), size)}
		}
		return out
	}
	_, err = client.PutRecordBatch(ctx, &firehose.PutRecordBatchInput{DeliveryStreamName: aws.String(name), Records: records(501, 1)})
	invalid(err)
	_, err = client.PutRecordBatch(ctx, &firehose.PutRecordBatchInput{DeliveryStreamName: aws.String(name), Records: records(5, 1000*1024)})
	invalid(err)
	_, err = client.PutRecordBatch(ctx, &firehose.PutRecordBatchInput{DeliveryStreamName: aws.String(name), Records: append(records(1, 1), firehosetypes.Record{Data: make([]byte, 1000*1024+1)})})
	invalid(err)
	out, err := client.PutRecordBatch(ctx, &firehose.PutRecordBatchInput{DeliveryStreamName: aws.String(name), Records: records(4, 1000*1024)})
	require.NoError(t, err)
	assert.Equal(t, int32(0), aws.ToInt32(out.FailedPutCount))
}

// TestLambda_PayloadSizeLimits refuses an invocation request over its
// payload quota, 6 MB synchronous and 1 MB asynchronous, with
// RequestTooLargeException, and turns a synchronous response over 6 MB into
// the Function.ResponseSizeTooLarge function error.
func TestLambda_PayloadSizeLimits(t *testing.T) {
	t.Parallel()
	lc := lambdaClient()
	fnName := "sdk-payload-limits"
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String(serviceRole(t, "lambda.amazonaws.com")),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaNodeDeploymentZip(t, `event.big ? "x".repeat(7 * 1024 * 1024) : {"ok": true}`)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)}) })
	require.NoError(t, lambda.NewFunctionActiveV2Waiter(lc).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)}, 2*time.Minute))

	payload := func(size int) []byte {
		body := `{"pad":"` + strings.Repeat("p", size-len(`{"pad":""}`)) + `"}`
		return []byte(body)
	}
	tooLarge := func(err error) {
		t.Helper()
		var refused *lambdatypes.RequestTooLargeException
		require.True(t, errors.As(err, &refused), "want RequestTooLargeException, got %v", err)
	}
	_, err = lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName), Payload: payload(6*1024*1024 + 1)})
	tooLarge(err)
	_, err = lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName), InvocationType: lambdatypes.InvocationTypeEvent, Payload: payload(1024*1024 + 1)})
	tooLarge(err)
	accepted, err := lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName), InvocationType: lambdatypes.InvocationTypeEvent, Payload: payload(1024 * 1024)})
	require.NoError(t, err)
	assert.Equal(t, int32(202), accepted.StatusCode)

	out, err := lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName), Payload: []byte(`{"big":true}`)})
	require.NoError(t, err)
	assert.Equal(t, "Unhandled", aws.ToString(out.FunctionError))
	var fnErr struct {
		ErrorType    string `json:"errorType"`
		ErrorMessage string `json:"errorMessage"`
	}
	require.NoError(t, json.Unmarshal(out.Payload, &fnErr))
	assert.Equal(t, "Function.ResponseSizeTooLarge", fnErr.ErrorType)
	assert.Contains(t, fnErr.ErrorMessage, "Response payload size exceeded maximum allowed payload size")

	out, err = lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName), Payload: payload(6 * 1024 * 1024)})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(out.FunctionError))
	assert.JSONEq(t, `{"ok":true}`, string(out.Payload))
}
