package aws_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/stretchr/testify/require"
)

func TestStepFunctionsAmazonECSSynchronousTaskSurvivesSimulatorRestart_SDK(t *testing.T) {
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	ecsAPI := ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(endpoint) })
	statesAPI := sfn.NewFromConfig(cfg, func(o *sfn.Options) { o.BaseEndpoint = aws.String(endpoint) })
	testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const (
		clusterName = "persistent-step-functions-cluster"
		familyName  = "persistent-step-functions-task"
	)
	_, err := ecsAPI.CreateCluster(testCtx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	taskDefinition, err := ecsAPI.RegisterTaskDefinition(testCtx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(familyName),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("workload"),
			Image:     aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Essential: aws.Bool(true),
			Command:   []string{"sh", "-c", "sleep 8"},
		}},
	})
	require.NoError(t, err)
	definition, err := json.Marshal(map[string]any{
		"StartAt": "RunTask",
		"States": map[string]any{
			"RunTask": map[string]any{
				"Type":     "Task",
				"Resource": "arn:aws:states:::ecs:runTask.sync",
				"Parameters": map[string]any{
					"Cluster":        clusterName,
					"TaskDefinition": aws.ToString(taskDefinition.TaskDefinition.TaskDefinitionArn),
				},
				"End": true,
			},
		},
	})
	require.NoError(t, err)
	machine, err := statesAPI.CreateStateMachine(testCtx, &sfn.CreateStateMachineInput{
		Name:       aws.String("persistent-step-functions-ecs"),
		Definition: aws.String(string(definition)),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/persistent-step-functions"),
	})
	require.NoError(t, err)
	execution, err := statesAPI.StartExecution(testCtx, &sfn.StartExecutionInput{
		StateMachineArn: machine.StateMachineArn,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		tasks, listErr := ecsAPI.ListTasks(testCtx, &ecs.ListTasksInput{Cluster: aws.String(clusterName)})
		if listErr != nil || len(tasks.TaskArns) != 1 {
			return false
		}
		described, describeErr := ecsAPI.DescribeTasks(testCtx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterName), Tasks: tasks.TaskArns,
		})
		return describeErr == nil && len(described.Tasks) == 1 &&
			aws.ToString(described.Tasks[0].LastStatus) == "RUNNING"
	}, 30*time.Second, 100*time.Millisecond)

	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	statesAPI = sfn.NewFromConfig(cfg, func(o *sfn.Options) { o.BaseEndpoint = aws.String(endpoint) })
	ecsAPI = ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(endpoint) })

	require.Eventually(t, func() bool {
		described, describeErr := statesAPI.DescribeExecution(testCtx, &sfn.DescribeExecutionInput{
			ExecutionArn: execution.ExecutionArn,
		})
		return describeErr == nil && described.Status == sfntypes.ExecutionStatusSucceeded
	}, 45*time.Second, 100*time.Millisecond)
	tasks, err := ecsAPI.ListTasks(testCtx, &ecs.ListTasksInput{Cluster: aws.String(clusterName)})
	require.NoError(t, err)
	require.Len(t, tasks.TaskArns, 1, "recovery must observe the original Amazon ECS task instead of launching a duplicate")
}
