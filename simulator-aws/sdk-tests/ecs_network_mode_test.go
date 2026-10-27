package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Amazon ECS task definition's networkMode decides the task's networking:
// awsvpc gives the task its own elastic network interface and requires a
// networkConfiguration, while bridge, host and none share the container
// instance's networking and accept none.

func ecsNetworkModeTaskDefinition(t *testing.T, client *ecs.Client, family string, mode ecstypes.NetworkMode) string {
	t.Helper()
	in := &ecs.RegisterTaskDefinitionInput{
		Family:      aws.String(family),
		NetworkMode: mode,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"),
			Image:       aws.String(containerCommandImage),
			Command:     []string{"sleep", "60"},
			Essential:   aws.Bool(true),
		}},
	}
	if mode == ecstypes.NetworkModeAwsvpc {
		in.RequiresCompatibilities = []ecstypes.Compatibility{ecstypes.CompatibilityFargate}
		in.Cpu = aws.String("256")
		in.Memory = aws.String("512")
	}
	out, err := client.RegisterTaskDefinition(ctx, in)
	require.NoError(t, err)
	return aws.ToString(out.TaskDefinition.TaskDefinitionArn)
}

func ecsNetworkModeCluster(t *testing.T, client *ecs.Client, name string) {
	t.Helper()
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)})
	})
}

// TestECS_RunTask_AwsvpcRequiresNetworkConfiguration expects an awsvpc task
// without the networkConfiguration naming its ENI's subnet to be refused rather
// than run with an address that is not its ENI's.
func TestECS_RunTask_AwsvpcRequiresNetworkConfiguration(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	cluster := "netmode-awsvpc-required"
	ecsNetworkModeCluster(t, client, cluster)
	tdArn := ecsNetworkModeTaskDefinition(t, client, "netmode-awsvpc-required", ecstypes.NetworkModeAwsvpc)

	_, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: aws.String(tdArn),
	})
	require.Error(t, err)
	var invalid *ecstypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "want InvalidParameterException, got %T: %v", err, err)
	assert.Contains(t, aws.ToString(invalid.Message), "awsvpc")
}

// TestECS_RunTask_NetworkConfigurationRejectedForBridgeNetworkMode expects a
// networkConfiguration refused, not ignored, for a non-awsvpc task.
func TestECS_RunTask_NetworkConfigurationRejectedForBridgeNetworkMode(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	cluster := "netmode-bridge-reject"
	ecsNetworkModeCluster(t, client, cluster)
	tdArn := ecsNetworkModeTaskDefinition(t, client, "netmode-bridge-reject", ecstypes.NetworkModeBridge)
	subnetID := createECSTestSubnet(t, "netmode-bridge-reject")

	_, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: aws.String(tdArn),
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.Error(t, err)
	var invalid *ecstypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "want InvalidParameterException, got %T: %v", err, err)
	assert.Contains(t, aws.ToString(invalid.Message), "bridge")
}

// TestECS_RunTask_BridgeNetworkModeHasNoElasticNetworkInterface expects a
// bridge-mode task to carry no ENI attachment and no per-container
// networkInterfaces.
func TestECS_RunTask_BridgeNetworkModeHasNoElasticNetworkInterface(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	cluster := "netmode-bridge-eni"
	ecsNetworkModeCluster(t, client, cluster)
	tdArn := ecsNetworkModeTaskDefinition(t, client, "netmode-bridge-eni", ecstypes.NetworkModeBridge)

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: aws.String(tdArn),
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := aws.ToString(runOut.Tasks[0].TaskArn)
	cleanupECSTask(t, client, cluster, taskArn)

	for _, att := range runOut.Tasks[0].Attachments {
		assert.NotEqual(t, "ElasticNetworkInterface", aws.ToString(att.Type),
			"a bridge-mode task must not be allocated an elastic network interface")
	}
	for _, c := range runOut.Tasks[0].Containers {
		assert.Empty(t, c.NetworkInterfaces,
			"a bridge-mode container must not report awsvpc network interfaces")
	}

	desc, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, desc.Tasks, 1)
	for _, att := range desc.Tasks[0].Attachments {
		assert.NotEqual(t, "ElasticNetworkInterface", aws.ToString(att.Type))
	}
}

// TestECS_RunTask_AwsvpcAttachesElasticNetworkInterface expects the ENI
// attachment to carry its subnet and a private address from the subnet's CIDR.
func TestECS_RunTask_AwsvpcAttachesElasticNetworkInterface(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	cluster := "netmode-awsvpc-eni"
	ecsNetworkModeCluster(t, client, cluster)
	tdArn := ecsNetworkModeTaskDefinition(t, client, "netmode-awsvpc-eni", ecstypes.NetworkModeAwsvpc)
	subnetID := createECSTestSubnet(t, "netmode-awsvpc-eni")

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: aws.String(tdArn),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := aws.ToString(runOut.Tasks[0].TaskArn)
	cleanupECSTask(t, client, cluster, taskArn)

	var details map[string]string
	for _, att := range runOut.Tasks[0].Attachments {
		if aws.ToString(att.Type) != "ElasticNetworkInterface" {
			continue
		}
		details = map[string]string{}
		for _, d := range att.Details {
			details[aws.ToString(d.Name)] = aws.ToString(d.Value)
		}
	}
	require.NotNil(t, details, "awsvpc task must be allocated an elastic network interface")
	assert.Equal(t, subnetID, details["subnetId"])
	assert.NotEmpty(t, details["privateIPv4Address"])
	require.Len(t, runOut.Tasks[0].Containers, 1)
	require.Len(t, runOut.Tasks[0].Containers[0].NetworkInterfaces, 1)
	assert.Equal(t, details["privateIPv4Address"],
		aws.ToString(runOut.Tasks[0].Containers[0].NetworkInterfaces[0].PrivateIpv4Address))

	// An awsvpc launch that cannot resolve its VPC network fails rather than run
	// on the default bridge.
	waitForECSTaskStatus(t, client, cluster, taskArn, "RUNNING")
}
