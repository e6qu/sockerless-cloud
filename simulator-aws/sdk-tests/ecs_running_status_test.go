package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/require"
)

// lastStatus and healthStatus answer different questions. lastStatus is where
// the task is in its lifecycle; healthStatus is whether the application inside
// is well, and Amazon ECS only has an opinion about that when the container
// definition declares a healthCheck. A task whose application takes minutes to
// bind its ports is RUNNING the moment its container runs — the wait belongs to
// healthStatus, or to nothing at all.
//
// Gating lastStatus on readiness instead is not a delay but a failure: a caller
// that waits for RUNNING times out and stops a task that was serving perfectly
// well (GitHub issue #904).
func TestECS_RunTask_RunningDoesNotWaitForTheApplication(t *testing.T) {
	client := ecsClient()

	const clusterName = "running-status-sdk"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
	})
	subnetID := createECSTestSubnet(t, "running-status-sdk")

	// The container runs immediately and binds nothing for a minute — the shape
	// of a slow-starting workload, with no healthCheck declared.
	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("running-status-sdk-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:    aws.String("slow-to-listen"),
			Image:   aws.String("public.ecr.aws/docker/library/alpine:3"),
			Command: []string{"sh", "-c", "sleep 60"},
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

	// Well inside the minute the container spends binding nothing.
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		desc, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterName),
			Tasks:   []string{taskArn},
		})
		require.NoError(t, err)
		require.Len(t, desc.Tasks, 1)
		last = aws.ToString(desc.Tasks[0].LastStatus)
		if last == "RUNNING" {
			// A task with no healthCheck has no health opinion to report.
			require.Empty(t, string(desc.Tasks[0].HealthStatus)+"",
				"a task whose definition declares no healthCheck reports no healthStatus")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the task never reported RUNNING while its container ran; last status %q — "+
		"lastStatus is gated on something other than the container running", last)
}
