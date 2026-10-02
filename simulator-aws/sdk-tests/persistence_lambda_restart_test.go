package aws_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/require"
)

func TestAcceptedAWSLambdaAsynchronousInvocationSurvivesSimulatorRestart_SDK(t *testing.T) {
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	lambdaAPI := lambda.NewFromConfig(cfg, func(o *lambda.Options) { o.BaseEndpoint = aws.String(endpoint) })
	sqsAPI := sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(endpoint) })
	testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	queue, err := sqsAPI.CreateQueue(testCtx, &sqs.CreateQueueInput{
		QueueName: aws.String("persistent-lambda-destination"),
	})
	require.NoError(t, err)
	queueURL := aws.ToString(queue.QueueUrl)
	attributes, err := sqsAPI.GetQueueAttributes(testCtx, &sqs.GetQueueAttributesInput{
		QueueUrl:       queue.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := attributes.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]

	const functionName = "persistent-async-invocation"
	_, err = lambdaAPI.CreateFunction(testCtx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String("arn:aws:iam::123456789012:role/persistent-lambda"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{ZipFile: lambdaNodeSourceZip(t,
			`exports.handler = async event => {
				await new Promise(resolve => setTimeout(resolve, 8000));
				return {received:event, completed:true};
			};`)},
		Timeout: aws.Int32(30),
	})
	require.NoError(t, err)
	_, err = lambdaAPI.PutFunctionEventInvokeConfig(testCtx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName: aws.String(functionName),
		DestinationConfig: &lambdatypes.DestinationConfig{
			OnSuccess: &lambdatypes.OnSuccess{Destination: aws.String(queueARN)},
		},
	})
	require.NoError(t, err)
	// Watch the function's log group with Live Tail, so the restart happens
	// once the asynchronous invocation has started and not before.
	logsAPI := cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("http://logs.localhost:%d", tcpPort))
	})
	logGroup := "/aws/lambda/" + functionName
	_, err = logsAPI.CreateLogGroup(testCtx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroup)})
	require.NoError(t, err)
	groups, err := logsAPI.DescribeLogGroups(testCtx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.Len(t, groups.LogGroups, 1)
	tail, err := logsAPI.StartLiveTail(testCtx, &cloudwatchlogs.StartLiveTailInput{
		LogGroupIdentifiers: []string{aws.ToString(groups.LogGroups[0].Arn)},
	})
	require.NoError(t, err)
	tailStream := tail.GetStream()
	defer tailStream.Close() //nolint:errcheck
	first, ok := <-tailStream.Events()
	require.True(t, ok, "Live Tail closed before its sessionStart: %v", tailStream.Err())
	require.IsType(t, &cwltypes.StartLiveTailResponseStreamMemberSessionStart{}, first)

	invoked, err := lambdaAPI.Invoke(testCtx, &lambda.InvokeInput{
		FunctionName:   aws.String(functionName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        []byte(`{"source":"restart-test"}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 202, invoked.StatusCode)

	awaitLiveTailMessage(t, tailStream, "START RequestId:")
	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	sqsAPI = sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(endpoint) })

	var destinationRecord struct {
		RequestContext map[string]any `json:"requestContext"`
		RequestPayload map[string]any `json:"requestPayload"`
	}
	delivered := receiveSQSMessages(t, sqsAPI, aws.String(queueURL), 1, 45*time.Second)
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(delivered[0].Body)), &destinationRecord))
	require.Equal(t, "restart-test", destinationRecord.RequestPayload["source"])
	require.Equal(t, "Success", destinationRecord.RequestContext["condition"])
	require.GreaterOrEqual(t, destinationRecord.RequestContext["approximateInvokeCount"], float64(1))
}

// awaitLiveTailMessage reads Live Tail session updates until one carries a
// log event whose message starts with prefix.
func awaitLiveTailMessage(t *testing.T, stream *cloudwatchlogs.StartLiveTailEventStream, prefix string) {
	t.Helper()
	for event := range stream.Events() {
		update, ok := event.(*cwltypes.StartLiveTailResponseStreamMemberSessionUpdate)
		if !ok {
			continue
		}
		for _, result := range update.Value.SessionResults {
			if strings.HasPrefix(aws.ToString(result.Message), prefix) {
				return
			}
		}
	}
	t.Fatalf("Live Tail closed before a %q line arrived: %v", prefix, stream.Err())
}
