package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/require"
)

// Amazon ECS stops returning a stopped task from ListTasks about an hour after
// it stops. Retaining them forever is not merely untidy: the reported cluster
// held 6,129 stopped tasks, the oldest a week old, so listing paged through
// thousands of ARNs and the cluster read at a glance like a crash loop (GitHub
// issue #908).
//
// This drives the real client to the point the simulator can observe — a task
// that has just stopped is still listed — because a test cannot wait an hour
// and moving the clock is not something a client can ask for. The aging itself
// is pinned by the unit regressions next to the retention window.
func TestECS_ListTasks_StillReportsARecentlyStoppedTask(t *testing.T) {
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
			Name:    aws.String("brief"),
			Image:   aws.String("public.ecr.aws/docker/library/alpine:3"),
			Command: []string{"sh", "-c", "true"},
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

	deadline := time.Now().Add(20 * time.Second)
	for {
		listed, err := client.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster:       aws.String(clusterName),
			DesiredStatus: ecstypes.DesiredStatusStopped,
		})
		require.NoError(t, err)
		for _, arn := range listed.TaskArns {
			if arn == taskArn {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("a task that stopped moments ago is not listed among %d stopped task(s); "+
				"the retention window drops them far too early", len(listed.TaskArns))
		}
		time.Sleep(500 * time.Millisecond)
	}
}
