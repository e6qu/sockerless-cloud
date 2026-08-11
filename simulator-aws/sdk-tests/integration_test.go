package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_ECSFullLifecycle exercises the complete ECS backend flow:
// RegisterTaskDefinition → RunTask → wait RUNNING → logs → StopTask → verify STOPPED → Deregister
func TestIntegration_ECSFullLifecycle(t *testing.T) {
	ecsC := ecsClient()
	cwC := cwLogsClient()

	clusterName := "integration-ecs-cluster"
	logGroup := "/ecs/integration-test"
	subnetID := createECSTestSubnet(t, "integration-ecs")

	// Setup: create cluster
	_, err := ecsC.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(clusterName),
	})
	require.NoError(t, err)

	// Register task definition with awslogs driver
	tdOut, err := ecsC.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("integration-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:    aws.String("app"),
				Image:   aws.String("alpine:latest"),
				Command: []string{"tail", "-f", "/dev/null"},
				LogConfiguration: &ecstypes.LogConfiguration{
					LogDriver: "awslogs",
					Options: map[string]string{
						"awslogs-group":         logGroup,
						"awslogs-stream-prefix": "ecs",
						"awslogs-region":        "us-east-1",
					},
				},
			},
		},
	})
	require.NoError(t, err)
	tdArn := *tdOut.TaskDefinition.TaskDefinitionArn

	// Run task
	runOut, err := ecsC.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: aws.String(tdArn),
		Count:          aws.Int32(1),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets: []string{subnetID},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := *runOut.Tasks[0].TaskArn
	cleanupECSTask(t, ecsC, clusterName, taskArn)

	waitForECSTaskStatus(t, ecsC, clusterName, taskArn, "RUNNING")

	descOut, err := ecsC.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Tasks, 1)
	assert.Equal(t, "RUNNING", *descOut.Tasks[0].LastStatus)

	// Verify CloudWatch log group was auto-created
	groups, err := cwC.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.NotEmpty(t, groups.LogGroups, "ECS should auto-create log group")

	// Find log streams
	streams, err := cwC.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.NotEmpty(t, streams.LogStreams, "ECS should auto-create log stream")

	// Get log events
	events, err := cwC.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: streams.LogStreams[0].LogStreamName,
		StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	require.NotEmpty(t, events.Events, "should have at least one log event")

	// Stop task
	_, err = ecsC.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(clusterName),
		Task:    aws.String(taskArn),
		Reason:  aws.String("integration test complete"),
	})
	require.NoError(t, err)

	// Verify STOPPED state
	descOut2, err := ecsC.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut2.Tasks, 1)
	stoppedTask := descOut2.Tasks[0]
	assert.Equal(t, "STOPPED", *stoppedTask.LastStatus)
	assert.Equal(t, ecstypes.TaskStopCodeUserInitiated, stoppedTask.StopCode)
	for _, c := range stoppedTask.Containers {
		require.NotNil(t, c.ExitCode)
		// User-initiated StopTask SIGKILLs the container → 137 (128+SIGKILL).
		assert.Equal(t, int32(137), *c.ExitCode)
	}

	// Deregister task definition
	_, err = ecsC.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{
		TaskDefinition: aws.String(tdArn),
	})
	require.NoError(t, err)

	// Cleanup logs
	cwC.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroup),
	})
}

// TestIntegration_LambdaFullLifecycle exercises the complete Lambda backend flow:
// CreateFunction → Invoke → DescribeLogStreams → GetLogEvents → DeleteFunction
func TestIntegration_LambdaFullLifecycle(t *testing.T) {
	lc := lambdaClient()
	cwC := cwLogsClient()

	fnName := "integration-lambda-fn"
	logGroupName := "/aws/lambda/" + fnName

	// Create function
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/integration-role"),
		Runtime:      lambdatypes.RuntimeNodejs18x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)

	// Invoke
	invokeOut, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(fnName),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(200), invokeOut.StatusCode)
	assert.Equal(t, "$LATEST", *invokeOut.ExecutedVersion)

	// Find log stream (same call pattern as Lambda backend logs.go:67)
	streams, err := cwC.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroupName),
		OrderBy:      cwlogtypes.OrderByLastEventTime,
		Descending:   aws.Bool(true),
		Limit:        aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, streams.LogStreams, 1, "Lambda invoke should create exactly one log stream")
	streamName := *streams.LogStreams[0].LogStreamName
	assert.Contains(t, streamName, "[$LATEST]")

	// Get log events
	events, err := cwC.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroupName),
		LogStreamName: aws.String(streamName),
		StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events.Events), 3, "should have START, END, REPORT entries")
	messages := make([]string, 0, len(events.Events))
	for _, event := range events.Events {
		messages = append(messages, aws.ToString(event.Message))
	}
	logOutput := strings.Join(messages, "\n")
	assert.Contains(t, logOutput, "START RequestId:")
	assert.Contains(t, logOutput, "END RequestId:")
	assert.Contains(t, logOutput, "REPORT RequestId:")

	// Verify pagination tokens work (follow mode simulation)
	token := events.NextForwardToken
	require.NotNil(t, token)

	// Re-read with token — no new events expected
	events2, err := cwC.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroupName),
		LogStreamName: aws.String(streamName),
		StartFromHead: aws.Bool(true),
		NextToken:     token,
	})
	require.NoError(t, err)
	assert.Empty(t, events2.Events, "no new events should be returned")
	assert.Equal(t, *token, *events2.NextForwardToken, "token should be stable when no new events")

	// Delete function
	_, err = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{
		FunctionName: aws.String(fnName),
	})
	require.NoError(t, err)

	// Cleanup logs
	cwC.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(logGroupName),
	})
}

// TestIntegration_ECRAuthToken verifies the ECR GetAuthorizationToken flow.
func TestIntegration_ECRAuthToken(t *testing.T) {
	ecrC := ecr.NewFromConfig(sdkConfig(), func(o *ecr.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})

	out, err := ecrC.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.NotEmpty(t, out.AuthorizationData)

	authData := out.AuthorizationData[0]
	require.NotNil(t, authData.AuthorizationToken)
	assert.NotEmpty(t, *authData.AuthorizationToken, "should return a base64 auth token")
	require.NotNil(t, authData.ProxyEndpoint)
	assert.NotEmpty(t, *authData.ProxyEndpoint, "should return a proxy endpoint")
}
