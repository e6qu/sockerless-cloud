package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Lambda FaaS tests ---

func TestLambda_InvokeArithmetic(t *testing.T) {
	lc := lambdaClient()
	fnName := "arith-basic-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(evalImageName)},
		ImageConfig: &lambdatypes.ImageConfig{
			Command: []string{"3 + 4 * 2"},
		},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	out, err := lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	assert.Contains(t, string(out.Payload), "11")
}

func TestLambda_InvokeArithmeticParentheses(t *testing.T) {
	lc := lambdaClient()
	fnName := "arith-paren-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(evalImageName)},
		ImageConfig: &lambdatypes.ImageConfig{
			Command: []string{"(3 + 4) * 2"},
		},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	out, err := lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	assert.Contains(t, string(out.Payload), "14")
}

func TestLambda_InvokeArithmeticInvalid(t *testing.T) {
	lc := lambdaClient()
	cw := cwLogsClient()
	fnName := "arith-invalid-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(evalImageName)},
		ImageConfig: &lambdatypes.ImageConfig{
			Command: []string{"3 +"},
		},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	_, err = lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)

	logGroupName := "/aws/lambda/" + fnName
	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroupName),
	})
	require.NoError(t, err)
	require.NotEmpty(t, events.Events)

	found := false
	for _, e := range events.Events {
		if strings.Contains(*e.Message, "ERROR") {
			found = true
		}
	}
	assert.True(t, found, "expected ERROR log entry for invalid expression")

	cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
}

func TestLambda_InvokeArithmeticLogs(t *testing.T) {
	lc := lambdaClient()
	cw := cwLogsClient()
	fnName := "arith-logs-fn"

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(evalImageName)},
		ImageConfig: &lambdatypes.ImageConfig{
			Command: []string{"((2+3)*4-1)/3"},
		},
	})
	require.NoError(t, err)
	defer lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})

	out, err := lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	assert.Contains(t, string(out.Payload), "6.333")

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
	allLogs := strings.Join(messages, "\n")
	assert.Contains(t, allLogs, "Parsing expression:")
	assert.Contains(t, allLogs, "Result:")

	cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
}

// --- ECS container tests ---

func TestECS_TaskArithmetic(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "arith-ecs", ecstypes.ContainerDefinition{
		Name:    aws.String("app"),
		Image:   aws.String(evalImageName),
		Command: []string{"(10 + 5) * 2"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/arith-ecs",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	waitForECSTaskStatus(t, client, cluster, taskArn, "STOPPED")

	descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Tasks, 1)

	task := descOut.Tasks[0]
	assert.Equal(t, "STOPPED", *task.LastStatus)
	require.NotEmpty(t, task.Containers)
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(0), *task.Containers[0].ExitCode)

	cw := cwLogsClient()
	logEvents, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String("/ecs/arith-ecs"),
	})
	require.NoError(t, err)

	var messages []string
	for _, e := range logEvents.Events {
		messages = append(messages, *e.Message)
	}
	assert.Contains(t, messages, "30", "expected output '30' in CloudWatch logs")
}

func TestECS_TaskArithmeticInvalid(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "arith-ecs-fail", ecstypes.ContainerDefinition{
		Name:    aws.String("app"),
		Image:   aws.String(evalImageName),
		Command: []string{"3 +"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/arith-ecs-fail",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	waitForECSTaskStatus(t, client, cluster, taskArn, "STOPPED")

	descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Tasks, 1)

	task := descOut.Tasks[0]
	assert.Equal(t, "STOPPED", *task.LastStatus)
	require.NotEmpty(t, task.Containers)
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(1), *task.Containers[0].ExitCode)
}

func TestECS_TaskArithmeticLogs(t *testing.T) {
	_, _, _ = ecsRunTaskHelper(t, "arith-ecs-logs", ecstypes.ContainerDefinition{
		Name:    aws.String("app"),
		Image:   aws.String(evalImageName),
		Command: []string{"10 / 3"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/arith-ecs-logs",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	// Poll CloudWatch until the task has run and its logs are ingested (both
	// async in the sim) — a fixed sleep races a loaded runner.
	cw := cwLogsClient()
	var allLogs string
	require.Eventually(t, func() bool {
		out, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName: aws.String("/ecs/arith-ecs-logs"),
		})
		if err != nil {
			return false
		}
		var messages []string
		for _, e := range out.Events {
			messages = append(messages, *e.Message)
		}
		allLogs = strings.Join(messages, "\n")
		return strings.Contains(allLogs, "3.333") && strings.Contains(allLogs, "Parsing expression:")
	}, 60*time.Second, 250*time.Millisecond)
	assert.Contains(t, allLogs, "3.333", "expected result '3.333...' in CloudWatch logs")
	assert.Contains(t, allLogs, "Parsing expression:", "expected parsing log in CloudWatch")
}
