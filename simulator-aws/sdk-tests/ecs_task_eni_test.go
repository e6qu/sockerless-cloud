package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_AwsvpcTaskHoldsItsSubnet proves an awsvpc task owns an elastic
// network interface from RunTask on: the attachment reports PRECREATED with
// the interface already in the subnet, EC2 refuses DeleteSubnet with
// DependencyViolation while the task holds it, and the interface is gone once
// the task has stopped.
func TestECS_AwsvpcTaskHoldsItsSubnet(t *testing.T) {
	client := ecsClient()
	ec2c := ec2Client()
	name := "eni-holds-subnet"
	subnetID := createECSTestSubnet(t, name)
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(name)})
	require.NoError(t, err)
	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(name),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:        aws.String("app"),
			Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
			Command:     []string{"sleep", "300"},
			StopTimeout: aws.Int32(2),
		}},
	})
	require.NoError(t, err)

	run, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(name),
		TaskDefinition: td.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	task := run.Tasks[0]
	require.Len(t, task.Attachments, 1)
	attachment := task.Attachments[0]
	assert.Equal(t, "ElasticNetworkInterface", aws.ToString(attachment.Type))
	assert.Equal(t, "PRECREATED", aws.ToString(attachment.Status))
	details := map[string]string{}
	for _, d := range attachment.Details {
		details[aws.ToString(d.Name)] = aws.ToString(d.Value)
	}
	assert.Equal(t, subnetID, details["subnetId"])
	eniID := details["networkInterfaceId"]
	require.NotEmpty(t, eniID, "the attachment must name its network interface")
	assert.NotEmpty(t, details["macAddress"])
	assert.NotEmpty(t, details["privateIPv4Address"])

	enis, err := ec2c.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{eniID}})
	require.NoError(t, err)
	require.Len(t, enis.NetworkInterfaces, 1)
	assert.Equal(t, subnetID, aws.ToString(enis.NetworkInterfaces[0].SubnetId))
	assert.Equal(t, details["privateIPv4Address"], aws.ToString(enis.NetworkInterfaces[0].PrivateIpAddress))
	assert.Equal(t, ec2types.NetworkInterfaceStatusInUse, enis.NetworkInterfaces[0].Status)
	_, err = ec2c.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: aws.String(eniID)})
	require.Error(t, err, "EC2 refuses to delete the interface a running task holds")

	_, err = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetID)})
	requireAWSErrorCode(t, err, "DependencyViolation")
	_, err = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)})
	requireAWSErrorCode(t, err, "ClusterContainsTasksException")

	_, err = client.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(name), Task: task.TaskArn})
	require.NoError(t, err)
	stopped := waitTaskStopped(t, client, name, aws.ToString(task.TaskArn))
	require.Len(t, stopped.Attachments, 1)
	assert.Equal(t, "DELETED", aws.ToString(stopped.Attachments[0].Status))

	_, err = ec2c.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{eniID}})
	requireAWSErrorCode(t, err, "InvalidNetworkInterfaceID.NotFound")
	_, err = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(name)})
	require.NoError(t, err, "a cluster whose tasks have stopped deletes")
}
