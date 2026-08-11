package aws_sdk_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	ec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lambdaClient() *lambda.Client {
	return lambda.NewFromConfig(sdkConfig(), func(o *lambda.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestLambdaExplicitDeploymentEnvironmentAndDownstreamSDK proves the complete
// AWS contract the runtime is meant to provide: the caller deploys code and
// environment through CreateFunction, then that managed-runtime code uses the
// bundled AWS SDK and standard endpoint coordinate to authenticate to another
// AWS service.
func TestLambdaExplicitDeploymentEnvironmentAndDownstreamSDK(t *testing.T) {
	lambdaAPI := lambdaClient()
	queueAPI := sqsClient()
	queue, err := queueAPI.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("lambda-downstream-sdk")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = queueAPI.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl})
	})
	containerEndpoint := fmt.Sprintf("http://host.docker.internal:%d", simPort)
	containerQueueURL := strings.Replace(aws.ToString(queue.QueueUrl), baseURL, containerEndpoint, 1)
	source := `
import boto3
import os

def handler(event, context):
    result = boto3.client("sqs").send_message(
        QueueUrl=os.environ["QUEUE_URL"],
        MessageBody=os.environ["MESSAGE_BODY"],
    )
    return {
        "messageId": result["MessageId"],
        "configured": os.environ["CONFIGURED_VALUE"],
    }
`
	const functionName = "lambda-downstream-sdk"
	_, err = lambdaAPI.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimePython312,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaPythonSourceZip(t, source)},
		Environment: &lambdatypes.Environment{Variables: map[string]string{
			"AWS_ENDPOINT_URL":      containerEndpoint,
			"AWS_ACCESS_KEY_ID":     "test",
			"AWS_SECRET_ACCESS_KEY": "test",
			"AWS_DEFAULT_REGION":    "us-east-1",
			"QUEUE_URL":             containerQueueURL,
			"MESSAGE_BODY":          "from-explicit-lambda-deployment",
			"CONFIGURED_VALUE":      "environment-wired",
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaAPI.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})
	invoked, err := lambdaAPI.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(functionName),
		Payload:      []byte(`{}`),
	})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(invoked.FunctionError), "invoke payload: %s", invoked.Payload)
	var output map[string]string
	require.NoError(t, json.Unmarshal(invoked.Payload, &output))
	assert.NotEmpty(t, output["messageId"])
	assert.Equal(t, "environment-wired", output["configured"])

	received, err := queueAPI.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: queue.QueueUrl, MaxNumberOfMessages: 1, VisibilityTimeout: 0,
	})
	require.NoError(t, err)
	require.Len(t, received.Messages, 1)
	assert.Equal(t, "from-explicit-lambda-deployment", aws.ToString(received.Messages[0].Body))
}

func cwLogsClient() *cloudwatchlogs.Client {
	return cloudwatchlogs.NewFromConfig(sdkConfig(), func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestLambda_FunctionConfigurationOmitsCodeAndTags(t *testing.T) {
	lc := lambdaClient()
	fnName := "shape-fn"
	deployment := lambdaDeploymentZip(t)

	createOut, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: deployment},
		Tags:         map[string]string{"env": "sdk"},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	assert.NotEmpty(t, aws.ToString(createOut.FunctionArn))

	rawCreate, err := json.Marshal(createOut)
	require.NoError(t, err)
	assert.NotContains(t, string(rawCreate), `"Code"`)
	assert.NotContains(t, string(rawCreate), `"Tags"`)
	assert.NotContains(t, string(rawCreate), "zip-bytes")

	getOut, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	require.NotNil(t, getOut.Configuration)
	require.NotNil(t, getOut.Code)
	require.NotEmpty(t, aws.ToString(getOut.Code.Location))
	download, err := http.Get(aws.ToString(getOut.Code.Location))
	require.NoError(t, err)
	defer download.Body.Close()
	downloaded, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, download.StatusCode, string(downloaded))
	require.Equal(t, deployment, downloaded)
	assert.Equal(t, "sdk", getOut.Tags["env"])
	rawConfig, err := json.Marshal(getOut.Configuration)
	require.NoError(t, err)
	assert.NotContains(t, string(rawConfig), `"Code"`)
	assert.NotContains(t, string(rawConfig), `"Tags"`)
	assert.NotContains(t, string(rawConfig), `"SubnetIPv4Allocations"`)
	assert.NotContains(t, string(rawConfig), "zip-bytes")

	listOut, err := lc.ListFunctions(ctx, &lambda.ListFunctionsInput{})
	require.NoError(t, err)
	for _, fn := range listOut.Functions {
		if aws.ToString(fn.FunctionName) != fnName {
			continue
		}
		rawList, err := json.Marshal(fn)
		require.NoError(t, err)
		assert.NotContains(t, string(rawList), `"Code"`)
		assert.NotContains(t, string(rawList), `"Tags"`)
		assert.NotContains(t, string(rawList), `"SubnetIPv4Allocations"`)
	}

	rawReq, err := json.Marshal(map[string]any{
		"FunctionName": "raw-shape-fn",
		"Role":         "arn:aws:iam::123456789012:role/test-role",
		"Runtime":      "nodejs20.x",
		"Handler":      "index.handler",
		"Code": map[string]string{
			"ZipFile": base64.StdEncoding.EncodeToString(lambdaDeploymentZip(t)),
		},
		"Tags": map[string]string{"env": "raw"},
	})
	require.NoError(t, err)
	rawPostReq, err := http.NewRequest(http.MethodPost, baseURL+"/2015-03-31/functions", bytes.NewReader(rawReq))
	require.NoError(t, err)
	rawPostReq.Header.Set("Content-Type", "application/json")
	signRawSigV4JSON(t, rawPostReq, "lambda", rawReq)
	resp, err := http.DefaultClient.Do(rawPostReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	assert.NotContains(t, string(body), `"Code"`)
	assert.NotContains(t, string(body), `"Tags"`)
	assert.NotContains(t, string(body), "zip-bytes")

	rawGetReq, err := http.NewRequest(http.MethodGet, baseURL+"/2015-03-31/functions/raw-shape-fn", nil)
	require.NoError(t, err)
	signRawSigV4(t, rawGetReq, "lambda", emptySHA256)
	getResp, err := http.DefaultClient.Do(rawGetReq)
	require.NoError(t, err)
	defer getResp.Body.Close()
	getBody, err := io.ReadAll(getResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode, string(getBody))
	var rawGet struct {
		Configuration json.RawMessage
		Tags          map[string]string
	}
	require.NoError(t, json.Unmarshal(getBody, &rawGet))
	assert.NotContains(t, string(rawGet.Configuration), `"Code"`)
	assert.NotContains(t, string(rawGet.Configuration), `"Tags"`)
	assert.NotContains(t, string(rawGet.Configuration), `"SubnetIPv4Allocations"`)
	assert.NotContains(t, string(rawGet.Configuration), "zip-bytes")
	assert.Equal(t, "raw", rawGet.Tags["env"])
}

func TestLambda_InvokeCreatesLogStream(t *testing.T) {
	lc := lambdaClient()
	cw := cwLogsClient()

	fnName := "test-log-inject-fn"

	// Create a Lambda function
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs18x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	// Invoke the function
	_, err = lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
	})
	require.NoError(t, err)

	// Verify log group was created
	logGroupName := "/aws/lambda/" + fnName
	groups, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroupName),
	})
	require.NoError(t, err)
	require.NotEmpty(t, groups.LogGroups, "expected log group to be created")
	assert.Equal(t, logGroupName, *groups.LogGroups[0].LogGroupName)

	// Verify log stream was created (using OrderBy=LastEventTime, Descending=true, Limit=1)
	streams, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroupName),
		OrderBy:      cwltypes.OrderByLastEventTime,
		Descending:   aws.Bool(true),
		Limit:        aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, streams.LogStreams, 1, "expected exactly one log stream")

	streamName := *streams.LogStreams[0].LogStreamName
	assert.Contains(t, streamName, "[$LATEST]", "stream name should contain [$LATEST]")

	// Verify the real runtime lifecycle and application output were recorded.
	events, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroupName),
		LogStreamName: aws.String(streamName),
		StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events.Events), 3, "expected at least 3 log events (START, END, REPORT)")

	messages := make([]string, 0, len(events.Events))
	for _, event := range events.Events {
		messages = append(messages, aws.ToString(event.Message))
	}
	logOutput := strings.Join(messages, "\n")
	assert.Contains(t, logOutput, "START RequestId:")
	assert.Contains(t, logOutput, "END RequestId:")
	assert.Contains(t, logOutput, "REPORT RequestId:")

	// Cleanup: delete the log group
	cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroupName),
	})
}

// TestLambda_InvokeRoundTrip exercises the Runtime API invoke path:
// the simulator implements the real AWS Lambda Runtime API slice.
// The handler image polls /next, echoes the payload back via
// response. Round-trip proves the Runtime API slice is wired
// end-to-end (simulator-side per-invocation sidecar + container env +
// host.docker.internal + host gateway + /response propagation back
// to the SDK caller).
func TestLambda_InvokeRoundTrip(t *testing.T) {
	lc := lambdaClient()

	fnName := "roundtrip-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	payload := []byte(`{"ping":1,"msg":"hello"}`)
	invokeOut, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
		Payload:      payload,
	})
	require.NoError(t, err)
	assert.Nil(t, invokeOut.FunctionError, "unexpected function error: %v", aws.ToString(invokeOut.FunctionError))
	assert.JSONEq(t, string(payload), string(invokeOut.Payload),
		"handler should echo payload back via /response")
}

func TestLambda_InvokeZIPDeploymentPackage(t *testing.T) {
	lc := lambdaClient()
	fnName := "zip-roundtrip-fn"
	deployment := lambdaNodeDeploymentZip(t, `{received:event, functionName:process.env.AWS_LAMBDA_FUNCTION_NAME}`)

	created, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: deployment},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})
	require.Equal(t, int64(len(deployment)), created.CodeSize)

	payload := []byte(`{"ping":1,"message":"real deployment package"}`)
	invoked, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
		Payload:      payload,
	})
	require.NoError(t, err)
	require.Empty(t, aws.ToString(invoked.FunctionError), string(invoked.Payload))
	require.JSONEq(t,
		`{"received":{"ping":1,"message":"real deployment package"},"functionName":"zip-roundtrip-fn"}`,
		string(invoked.Payload),
	)
}

// TestLambda_InvokeHandlerError exercises the /error branch of the
// Runtime API: payload with "cause":"error" triggers a POST to
// invocation/{id}/error; caller sees X-Amz-Function-Error: Unhandled.
func TestLambda_InvokeHandlerError(t *testing.T) {
	lc := lambdaClient()

	fnName := "error-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	invokeOut, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
		Payload:      []byte(`{"cause":"error"}`),
	})
	require.NoError(t, err)
	require.NotNil(t, invokeOut.FunctionError, "expected FunctionError set to 'Unhandled'")
	assert.Equal(t, "Unhandled", aws.ToString(invokeOut.FunctionError))
	assert.Contains(t, string(invokeOut.Payload), "errorMessage",
		"response body should be a Lambda error JSON")
	assert.Contains(t, string(invokeOut.Payload), "test error from handler")
}

// TestLambda_InvokeContainerCrash covers the case where the container
// exits before posting /response or /error (what real Lambda reports
// as "Runtime exited without providing a reason"). Alpine `sh -c "exit 1"`
// never talks the Runtime API; the simulator detects container exit
// without response and surfaces a proper Lambda error.
func TestLambda_InvokeContainerCrash(t *testing.T) {
	lc := lambdaClient()
	cw := cwLogsClient()

	fnName := "crash-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String("alpine:latest")},
		ImageConfig: &lambdatypes.ImageConfig{
			Command: []string{"sh", "-c", "exit 1"},
		},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	invokeOut, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
	})
	require.NoError(t, err)
	require.NotNil(t, invokeOut.FunctionError)
	assert.Equal(t, "Unhandled", aws.ToString(invokeOut.FunctionError))
	assert.Contains(t, string(invokeOut.Payload), "Runtime exited")

	// CloudWatch stream should have START/END/REPORT + an ERROR line.
	logGroupName := "/aws/lambda/" + fnName
	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroupName),
	})
	require.NoError(t, err)
	require.NotEmpty(t, events.Events)
	var messages []string
	for _, e := range events.Events {
		messages = append(messages, *e.Message)
	}
	foundError := false
	for _, m := range messages {
		if strings.Contains(m, "ERROR") {
			foundError = true
		}
	}
	assert.True(t, foundError, "expected ERROR log entry for crashed container, got: %v", messages)
	cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
}

// TestLambda_RuntimeAPILogsToCloudWatch verifies the simulator still
// injects START/END/REPORT log lines + captures the handler's stderr
// under the Runtime API path. The handler writes a log line to
// stderr describing the invocation; it should appear in the
// function's CloudWatch log stream.
func TestLambda_RuntimeAPILogsToCloudWatch(t *testing.T) {
	lc := lambdaClient()
	cw := cwLogsClient()

	fnName := "logs-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	_, err = lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
		Payload:      []byte(`{"ping":1}`),
	})
	require.NoError(t, err)

	logGroupName := "/aws/lambda/" + fnName
	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroupName),
	})
	require.NoError(t, err)
	require.NotEmpty(t, events.Events)

	var messages []string
	for _, e := range events.Events {
		messages = append(messages, *e.Message)
	}
	hasStart, hasEnd, hasReport, hasHandlerLog := false, false, false, false
	for _, m := range messages {
		if strings.Contains(m, "START RequestId:") {
			hasStart = true
		}
		if strings.Contains(m, "END RequestId:") {
			hasEnd = true
		}
		if strings.Contains(m, "REPORT RequestId:") {
			hasReport = true
		}
		if strings.Contains(m, "lambda-runtime-handler: polling") {
			hasHandlerLog = true
		}
	}
	assert.True(t, hasStart, "should have START log entry")
	assert.True(t, hasEnd, "should have END log entry")
	assert.True(t, hasReport, "should have REPORT log entry")
	assert.True(t, hasHandlerLog, "should capture handler's stderr in CloudWatch: %v", messages)

	cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
}

func TestLambda_MultipleInvokesCreateMultipleStreams(t *testing.T) {
	lc := lambdaClient()
	cw := cwLogsClient()

	fnName := "test-multi-invoke-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs18x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	logGroupName := "/aws/lambda/" + fnName

	// Invoke twice
	_, err = lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	_, err = lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)

	// Should have 2 streams (each invoke creates a new one)
	streams, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroupName),
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(streams.LogStreams), 2, "expected at least 2 log streams")

	// With Descending=true + Limit=1, we should get only the most recent stream
	latest, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroupName),
		OrderBy:      cwltypes.OrderByLastEventTime,
		Descending:   aws.Bool(true),
		Limit:        aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, latest.LogStreams, 1)

	// Cleanup
	cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroupName),
	})
}

// TestLambda_VpcConfig_AllocatesENIPerSubnet verifies that CreateFunction
// with a VpcConfig containing real subnet IDs allocates a Hyperplane ENI
// per subnet from the subnet's CidrBlock — matches real Lambda behavior
// (sim must validate the subnet exists, not return a fake ENI).
func TestLambda_VpcConfig_AllocatesENIPerSubnet(t *testing.T) {
	lc := lambdaClient()
	ec2C := ec2Client()

	// Pre-create two subnets with distinct CIDRs so the test exercises
	// the allocator under multi-subnet VpcConfig (a common runner setup).
	vpcOut, err := ec2C.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.7.0.0/16"),
	})
	require.NoError(t, err)
	defer ec2C.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: vpcOut.Vpc.VpcId})

	sub1, err := ec2C.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     vpcOut.Vpc.VpcId,
		CidrBlock: aws.String("10.7.1.0/24"),
	})
	require.NoError(t, err)
	sub2, err := ec2C.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     vpcOut.Vpc.VpcId,
		CidrBlock: aws.String("10.7.2.0/24"),
	})
	require.NoError(t, err)

	fnName := "vpc-fn"
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	createOut, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/lambda-vpc"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{
			ZipFile: lambdaDeploymentZip(t),
		},
		VpcConfig: &lambdatypes.VpcConfig{
			SubnetIds: []string{*sub1.Subnet.SubnetId, *sub2.Subnet.SubnetId},
		},
	})
	require.NoError(t, err, "CreateFunction with VpcConfig must succeed when subnets are real")
	require.NotNil(t, createOut.VpcConfig, "VpcConfig must round-trip on CreateFunction")
	require.Len(t, createOut.VpcConfig.SubnetIds, 2)

	getOut, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	require.NotNil(t, getOut.Configuration)
	require.NotNil(t, getOut.Configuration.VpcConfig)
	assert.Equal(t, *vpcOut.Vpc.VpcId, *getOut.Configuration.VpcConfig.VpcId, "VpcConfig.VpcId must echo back from the subnet's stored VpcId")
	rawConfig, err := json.Marshal(getOut.Configuration)
	require.NoError(t, err)
	assert.NotContains(t, string(rawConfig), `"SubnetIPv4Allocations"`,
		"internal IP allocation state must not leak through the public Lambda FunctionConfiguration shape")
}

// TestLambda_VpcConfig_RuntimeReachesVpcResource proves VpcConfig changes the
// workload's real network, not just its FunctionConfiguration. An Amazon ECS
// task serves HTTP on its awsvpc elastic network interface; the AWS Lambda
// function reaches that private address through its configured subnet and
// security group using the same public APIs and identifiers used on AWS.
func TestLambda_VpcConfig_RuntimeReachesVpcResource(t *testing.T) {
	lc := lambdaClient()
	ec2c := ec2Client()
	ecsc := ecsClient()

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.77.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpcOut.Vpc.VpcId)
	subnetOut, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     aws.String(vpcID),
		CidrBlock: aws.String("10.77.1.0/24"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnetOut.Subnet.SubnetId)
	sgOut, err := ec2c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("lambda-vpc-runtime"),
		Description: aws.String("AWS Lambda VPC runtime integration"),
		VpcId:       aws.String(vpcID),
	})
	require.NoError(t, err)
	securityGroupID := aws.ToString(sgOut.GroupId)
	_, err = ec2c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(securityGroupID),
		IpPermissions: []ec2types.IpPermission{{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(8080),
			ToPort:     aws.Int32(8080),
			IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("10.77.0.0/16")}},
		}},
	})
	require.NoError(t, err)

	clusterName := "lambda-vpc-runtime"
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	tdOut, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("lambda-vpc-http-target"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("http-target"),
			Image:     aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Essential: aws.Bool(true),
			Command: []string{"sh", "-c",
				"echo lambda-vpc-ok > /tmp/index.html; exec httpd -f -p 8080 -h /tmp"},
			PortMappings: []ecstypes.PortMapping{{ContainerPort: aws.Int32(8080)}},
		}},
	})
	require.NoError(t, err)
	taskDefinitionARN := aws.ToString(tdOut.TaskDefinition.TaskDefinitionArn)
	t.Cleanup(func() {
		_, _ = ecsc.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{
			TaskDefinition: aws.String(taskDefinitionARN),
		})
		_, _ = ecsc.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
		_, _ = ec2c.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(securityGroupID)})
		_, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetID)})
		_, _ = ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
	})

	runOut, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: aws.String(taskDefinitionARN),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        []string{subnetID},
				SecurityGroups: []string{securityGroupID},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskARN := aws.ToString(runOut.Tasks[0].TaskArn)
	t.Cleanup(func() {
		_, _ = ecsc.StopTask(ctx, &ecs.StopTaskInput{
			Cluster: aws.String(clusterName),
			Task:    aws.String(taskARN),
			Reason:  aws.String("test cleanup"),
		})
	})
	waitForECSTaskStatus(t, ecsc, clusterName, taskARN, "RUNNING")
	desc, err := ecsc.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskARN},
	})
	require.NoError(t, err)
	require.Len(t, desc.Tasks, 1)
	targetIP := ""
	for _, attachment := range desc.Tasks[0].Attachments {
		if aws.ToString(attachment.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, detail := range attachment.Details {
			if aws.ToString(detail.Name) == "privateIPv4Address" {
				targetIP = aws.ToString(detail.Value)
			}
		}
	}
	require.NotEmpty(t, targetIP)

	fnName := "vpc-runtime-fn"
	_, err = lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/lambda-vpc"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Timeout:      aws.Int32(10),
		VpcConfig: &lambdatypes.VpcConfig{
			SubnetIds:        []string{subnetID},
			SecurityGroupIds: []string{securityGroupID},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})
	payload := []byte(fmt.Sprintf(`{"action":"http-get","url":"http://%s:8080/"}`, targetIP))
	invokeOut, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
		Payload:      payload,
	})
	require.NoError(t, err)
	require.Nil(t, invokeOut.FunctionError, "VPC request failed: %s", invokeOut.Payload)
	var response struct {
		StatusCode int    `json:"statusCode"`
		Body       string `json:"body"`
	}
	require.NoError(t, json.Unmarshal(invokeOut.Payload, &response))
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "lambda-vpc-ok\n", response.Body)
}

// TestLambda_VpcConfig_RejectsUnknownSubnet matches real Lambda's
// InvalidParameterValueException when the subnet doesn't exist in EC2.
func TestLambda_VpcConfig_RejectsUnknownSubnet(t *testing.T) {
	lc := lambdaClient()
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String("vpc-bad-fn"),
		Role:         aws.String("arn:aws:iam::123456789012:role/lambda-vpc"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{
			ZipFile: lambdaDeploymentZip(t),
		},
		VpcConfig: &lambdatypes.VpcConfig{
			SubnetIds: []string{"subnet-doesnotexistanywhere"},
		},
	})
	require.Error(t, err, "CreateFunction with an unknown subnet must fail like real Lambda")
}

func TestLambda_ListFunctions_Pagination(t *testing.T) {
	client := lambdaClient()
	names := []string{"pag-fn-a", "pag-fn-b", "pag-fn-c"}
	for _, n := range names {
		_, err := client.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(n),
			Runtime:      lambdatypes.RuntimeProvidedal2023,
			Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
			Handler:      aws.String("bootstrap"),
			Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(n)})
		})
	}

	seen := map[string]bool{}
	pager := lambda.NewListFunctionsPaginator(client, &lambda.ListFunctionsInput{MaxItems: aws.Int32(1)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, f := range page.Functions {
			seen[aws.ToString(f.FunctionName)] = true
		}
	}
	for _, n := range names {
		assert.True(t, seen[n], "function %s should appear via pagination", n)
	}
}

func TestLambda_GetFunction_NotFound_ErrorClassification(t *testing.T) {
	client := lambdaClient()
	_, err := client.GetFunction(ctx, &lambda.GetFunctionInput{
		FunctionName: aws.String("nonexistent-function"),
	})
	require.Error(t, err)
	var notFound *lambdatypes.ResourceNotFoundException
	assert.True(t, errors.As(err, &notFound),
		"Lambda ResourceNotFoundException must be classified by SDK errors.As; got %T: %v", err, err)
}
