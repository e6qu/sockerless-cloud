package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/require"
)

// lastStatus is where the task is in its lifecycle; healthStatus is whether the
// application is well, which Amazon ECS judges only when the container
// definition declares a healthCheck. A task whose application takes minutes to
// bind its ports is RUNNING as soon as its container runs. Gating lastStatus on
// readiness makes a caller waiting for RUNNING time out and stop a task that
// was serving.
func TestECS_RunTask_RunningDoesNotWaitForTheApplication(t *testing.T) {
	t.Parallel()
	client := ecsClient()

	const clusterName = "running-status-sdk"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
	})
	subnetID := createECSTestSubnet(t, "running-status-sdk")

	// The container binds nothing for a minute, as a slow-starting workload does,
	// with no healthCheck declared.
	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("running-status-sdk-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("slow-to-listen"),
			Image:       aws.String("mirror.gcr.io/library/alpine:3"),
			Command:     []string{"sh", "-c", "sleep 60"},
		}},
	})
	require.NoError(t, err)
	require.Nil(t, tdOut.TaskDefinition.ContainerDefinitions[0].HealthCheck,
		"the definition must declare no healthCheck for this to be the case under test")

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: tdOut.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := aws.ToString(runOut.Tasks[0].TaskArn)
	cleanupECSTask(t, client, clusterName, taskArn)

	running := waitForECSTasksRunning(t, client, clusterName, 30*time.Second, taskArn)
	// A task with no healthCheck has no health opinion to report.
	require.Empty(t, string(running.Tasks[0].HealthStatus),
		"a task whose definition declares no healthCheck reports no healthStatus")
}
