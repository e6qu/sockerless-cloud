package aws_sdk_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/stretchr/testify/require"
)

// A stopping simulator ends the AWS Lambda invocations its durable-execution
// coordinators and Step Functions Lambda tasks have in flight, and leaves both
// executions RUNNING for the next process to resume, rather than leaving the
// execution environments running until the function timeout or failing the
// executions with the interrupted invocation's error.
func TestLambdaWorkInFlightEndsWithTheSimulatorAndResumes_SDK(t *testing.T) {
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { _ = shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	lambdaAPI := lambda.NewFromConfig(cfg, func(o *lambda.Options) { o.BaseEndpoint = aws.String(endpoint) })
	statesAPI := sfn.NewFromConfig(cfg, func(o *sfn.Options) { o.BaseEndpoint = aws.String(endpoint) })
	logsAPI := cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("http://logs.localhost:%d", tcpPort))
	})
	testCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const holdSource = `exports.handler = async () => {
		await new Promise(resolve => setTimeout(resolve, 600000));
		return {Status:"SUCCEEDED",Result:"{}"};
	};`
	const (
		durableFunction = "interrupted-durable-execution"
		taskFunction    = "interrupted-step-functions-task"
	)
	var logGroupARNs []string
	for _, name := range []string{durableFunction, taskFunction} {
		input := &lambda.CreateFunctionInput{
			FunctionName: aws.String(name),
			Role:         aws.String("arn:aws:iam::123456789012:role/interrupted-lambda"),
			Runtime:      lambdatypes.RuntimeNodejs20x,
			Handler:      aws.String("index.handler"),
			Code:         &lambdatypes.FunctionCode{ZipFile: lambdaNodeSourceZip(t, holdSource)},
			Timeout:      aws.Int32(900),
		}
		if name == durableFunction {
			input.DurableConfig = &lambdatypes.DurableConfig{
				ExecutionTimeout: aws.Int32(3600), RetentionPeriodInDays: aws.Int32(1),
			}
		}
		_, err := lambdaAPI.CreateFunction(testCtx, input)
		require.NoError(t, err)
		logGroup := "/aws/lambda/" + name
		_, err = logsAPI.CreateLogGroup(testCtx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroup)})
		require.NoError(t, err)
		groups, err := logsAPI.DescribeLogGroups(testCtx, &cloudwatchlogs.DescribeLogGroupsInput{
			LogGroupNamePrefix: aws.String(logGroup),
		})
		require.NoError(t, err)
		require.Len(t, groups.LogGroups, 1)
		logGroupARNs = append(logGroupARNs, aws.ToString(groups.LogGroups[0].Arn))
	}
	tail, err := logsAPI.StartLiveTail(testCtx, &cloudwatchlogs.StartLiveTailInput{LogGroupIdentifiers: logGroupARNs})
	require.NoError(t, err)
	tailStream := tail.GetStream()
	defer func() { _ = tailStream.Close() }()
	first, ok := <-tailStream.Events()
	require.True(t, ok, "Live Tail closed before its sessionStart: %v", tailStream.Err())
	require.IsType(t, &cwltypes.StartLiveTailResponseStreamMemberSessionStart{}, first)

	invocation, err := lambdaAPI.Invoke(testCtx, &lambda.InvokeInput{
		FunctionName:         aws.String(durableFunction + ":$LATEST"),
		DurableExecutionName: aws.String("interrupted"),
		InvocationType:       lambdatypes.InvocationTypeEvent,
		Payload:              []byte(`{"source":"interrupt-test"}`),
	})
	require.NoError(t, err)
	durableARN := aws.ToString(invocation.DurableExecutionArn)
	require.NotEmpty(t, durableARN)

	task, err := lambdaAPI.GetFunction(testCtx, &lambda.GetFunctionInput{FunctionName: aws.String(taskFunction)})
	require.NoError(t, err)
	machine, err := statesAPI.CreateStateMachine(testCtx, &sfn.CreateStateMachineInput{
		Name: aws.String("interrupted-lambda-task"),
		Definition: aws.String(`{"StartAt":"Hold","States":{"Hold":{"Type":"Task","Resource":"` +
			aws.ToString(task.Configuration.FunctionArn) + `","End":true}}}`),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/interrupted-lambda"),
	})
	require.NoError(t, err)
	execution, err := statesAPI.StartExecution(testCtx, &sfn.StartExecutionInput{
		StateMachineArn: machine.StateMachineArn,
		Input:           aws.String(`{"source":"interrupt-test"}`),
	})
	require.NoError(t, err)

	requestIDs := awaitLiveTailRequestIDs(t, tailStream, 2)
	require.NoError(t, shutdownSimulator(cmd))
	for _, requestID := range requestIDs {
		running, err := exec.Command("docker", "ps", "-q", "--filter", "label=sockerless-sim-lambda="+requestID).Output()
		require.NoError(t, err)
		require.Empty(t, strings.TrimSpace(string(running)),
			"the execution environment of invocation %s outlived the simulator that ran it", requestID)
	}

	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	lambdaAPI = lambda.NewFromConfig(cfg, func(o *lambda.Options) { o.BaseEndpoint = aws.String(endpoint) })
	statesAPI = sfn.NewFromConfig(cfg, func(o *sfn.Options) { o.BaseEndpoint = aws.String(endpoint) })

	durable, err := lambdaAPI.GetDurableExecution(testCtx, &lambda.GetDurableExecutionInput{
		DurableExecutionArn: aws.String(durableARN),
	})
	require.NoError(t, err)
	require.Equal(t, lambdatypes.ExecutionStatusRunning, durable.Status)

	described, err := statesAPI.DescribeExecution(testCtx, &sfn.DescribeExecutionInput{
		ExecutionArn: execution.ExecutionArn,
	})
	require.NoError(t, err)
	require.Equal(t, sfntypes.ExecutionStatusRunning, described.Status)
	history, err := statesAPI.GetExecutionHistory(testCtx, &sfn.GetExecutionHistoryInput{
		ExecutionArn: execution.ExecutionArn,
	})
	require.NoError(t, err)
	for _, event := range history.Events {
		require.NotContains(t, string(event.Type), "Failed",
			"the interrupted Lambda task was recorded as an outcome")
	}
}

// awaitLiveTailRequestIDs returns the RequestId of the first count START lines
// Live Tail delivers.
func awaitLiveTailRequestIDs(t *testing.T, stream *cloudwatchlogs.StartLiveTailEventStream, count int) []string {
	t.Helper()
	var requestIDs []string
	for event := range stream.Events() {
		update, ok := event.(*cwltypes.StartLiveTailResponseStreamMemberSessionUpdate)
		if !ok {
			continue
		}
		for _, result := range update.Value.SessionResults {
			message := aws.ToString(result.Message)
			if !strings.HasPrefix(message, "START RequestId: ") {
				continue
			}
			requestIDs = append(requestIDs, strings.Fields(strings.TrimPrefix(message, "START RequestId: "))[0])
		}
		if len(requestIDs) >= count {
			return requestIDs
		}
	}
	t.Fatalf("Live Tail closed after %d of %d START lines: %v", len(requestIDs), count, stream.Err())
	return nil
}
