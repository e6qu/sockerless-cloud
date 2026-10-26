package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Amazon ECS stops returning a stopped task from ListTasks about an hour after
// it stops; retaining them leaves listings paging through thousands of ARNs.
// A test cannot wait an hour or move the clock, so this covers what a client
// can observe, a just-stopped task still listed; unit tests beside the
// retention window cover the aging.
func TestECS_ListTasks_StillReportsARecentlyStoppedTask(t *testing.T) {
	t.Parallel()
	client := ecsClient()

	const clusterName = "retention-sdk"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
	})
	subnetID := createECSTestSubnet(t, "retention-sdk")

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("retention-sdk-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("brief"),
			Image:       aws.String("mirror.gcr.io/library/alpine:3"),
			Command:     []string{"sh", "-c", "true"},
		}},
	})
	require.NoError(t, err)

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

	waitForECSTaskStatus(t, client, clusterName, taskArn, "STOPPED")

	listed, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:       aws.String(clusterName),
		DesiredStatus: ecstypes.DesiredStatusStopped,
	})
	require.NoError(t, err)
	assert.Contains(t, listed.TaskArns, taskArn,
		"a task that stopped moments ago is not listed among the stopped tasks; the retention window drops them far too early")
}
