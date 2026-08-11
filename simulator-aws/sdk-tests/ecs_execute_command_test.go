package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// busyboxImage is a shell-bearing image (unlike the scratch eval image) so the
// task can run a long-lived `sleep` and ECS exec can target a live container.
const busyboxImage = "public.ecr.aws/docker/library/busybox:latest"

func runLongLivedECSTask(t *testing.T, client *ecs.Client, cluster, family string, enableExec bool) string {
	t.Helper()
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, family)

	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(family),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("app"),
			Image:      aws.String(busyboxImage),
			EntryPoint: []string{"sh", "-c"},
			Command:    []string{"sleep 30"},
		}},
	})
	require.NoError(t, err)

	run, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:              aws.String(cluster),
		TaskDefinition:       td.TaskDefinition.TaskDefinitionArn,
		LaunchType:           ecstypes.LaunchTypeFargate,
		EnableExecuteCommand: enableExec,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := aws.ToString(run.Tasks[0].TaskArn)
	cleanupECSTask(t, client, cluster, taskArn)
	waitForECSTaskStatus(t, client, cluster, taskArn, "RUNNING")
	return taskArn
}

// TestECS_ExecuteCommandOnRunningTask covers ECS ExecuteCommand against a live
// RUNNING task started with enableExecuteCommand: it returns an SSM session.
func TestECS_ExecuteCommandOnRunningTask(t *testing.T) {
	client := ecsClient()
	taskArn := runLongLivedECSTask(t, client, "exec-cmd-enabled", "exec-cmd-enabled", true)

	out, err := client.ExecuteCommand(ctx, &ecs.ExecuteCommandInput{
		Cluster:     aws.String("exec-cmd-enabled"),
		Task:        aws.String(taskArn),
		Container:   aws.String("app"),
		Command:     aws.String("echo hello"),
		Interactive: true,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Session)
	assert.Equal(t, "arn:aws:ecs:us-east-1:123456789012:cluster/exec-cmd-enabled", aws.ToString(out.ClusterArn))
	assert.NotEmpty(t, aws.ToString(out.ContainerArn))
	assert.Equal(t, "app", aws.ToString(out.ContainerName))
	assert.True(t, out.Interactive)
	assert.Equal(t, taskArn, aws.ToString(out.TaskArn))
	assert.NotEmpty(t, aws.ToString(out.Session.SessionId))
	assert.Contains(t, aws.ToString(out.Session.StreamUrl), "/ecs-exec/")
	assert.NotEmpty(t, aws.ToString(out.Session.TokenValue))
}

// TestECS_ExecuteCommandRejectedWhenNotEnabled covers the fidelity rule that
// exec is rejected on a RUNNING task that was NOT started with
// enableExecuteCommand (the SSM exec agent is only injected when enabled).
func TestECS_ExecuteCommandRejectedWhenNotEnabled(t *testing.T) {
	client := ecsClient()
	taskArn := runLongLivedECSTask(t, client, "exec-cmd-disabled", "exec-cmd-disabled", false)

	_, err := client.ExecuteCommand(ctx, &ecs.ExecuteCommandInput{
		Cluster:     aws.String("exec-cmd-disabled"),
		Task:        aws.String(taskArn),
		Container:   aws.String("app"),
		Command:     aws.String("echo hello"),
		Interactive: true,
	})
	require.Error(t, err, "exec must be rejected when execute command was not enabled at RunTask")
	assert.Contains(t, err.Error(), "execute command was not enabled")
}

func TestECS_ExecuteCommandRejectsNonInteractive(t *testing.T) {
	client := ecsClient()
	taskArn := runLongLivedECSTask(t, client, "exec-cmd-noninteractive", "exec-cmd-noninteractive", true)

	_, err := client.ExecuteCommand(ctx, &ecs.ExecuteCommandInput{
		Cluster:     aws.String("exec-cmd-noninteractive"),
		Task:        aws.String(taskArn),
		Container:   aws.String("app"),
		Command:     aws.String("echo hello"),
		Interactive: false,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only supports initiating interactive")
}

func TestECS_ExecuteCommandRejectsUnknownContainer(t *testing.T) {
	client := ecsClient()
	taskArn := runLongLivedECSTask(t, client, "exec-cmd-container", "exec-cmd-container", true)

	_, err := client.ExecuteCommand(ctx, &ecs.ExecuteCommandInput{
		Cluster:     aws.String("exec-cmd-container"),
		Task:        aws.String(taskArn),
		Container:   aws.String("missing"),
		Command:     aws.String("echo hello"),
		Interactive: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Container not found")
}
