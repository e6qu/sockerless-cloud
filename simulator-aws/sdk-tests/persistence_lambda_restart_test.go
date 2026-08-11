package aws_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	invoked, err := lambdaAPI.Invoke(testCtx, &lambda.InvokeInput{
		FunctionName:   aws.String(functionName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        []byte(`{"source":"restart-test"}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 202, invoked.StatusCode)

	time.Sleep(500 * time.Millisecond)
	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	sqsAPI = sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(endpoint) })

	var destinationRecord struct {
		RequestContext map[string]any `json:"requestContext"`
		RequestPayload map[string]any `json:"requestPayload"`
	}
	require.Eventually(t, func() bool {
		received, receiveErr := sqsAPI.ReceiveMessage(testCtx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
		})
		if receiveErr != nil || len(received.Messages) != 1 {
			return false
		}
		return json.Unmarshal([]byte(aws.ToString(received.Messages[0].Body)), &destinationRecord) == nil
	}, 45*time.Second, 500*time.Millisecond)
	require.Equal(t, "restart-test", destinationRecord.RequestPayload["source"])
	require.Equal(t, "Success", destinationRecord.RequestContext["condition"])
	require.GreaterOrEqual(t, destinationRecord.RequestContext["approximateInvokeCount"], float64(1))
}
