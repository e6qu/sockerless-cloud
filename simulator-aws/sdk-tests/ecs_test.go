package aws_sdk_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ecsClient() *ecs.Client {
	return ecs.NewFromConfig(sdkConfig(), func(o *ecs.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

var ecsTestSubnetCounter atomic.Uint32

func TestECS_CreateCluster(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	out, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String("test-cluster"),
	})
	require.NoError(t, err)
	assert.Equal(t, "test-cluster", *out.Cluster.ClusterName)
	assert.Contains(t, *out.Cluster.ClusterArn, "test-cluster")
}

func TestECS_DescribeClusters(t *testing.T) {
	t.Parallel()
	client := ecsClient()

	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String("describe-cluster"),
	})
	require.NoError(t, err)

	out, err := client.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{"describe-cluster"},
	})
	require.NoError(t, err)
	require.Len(t, out.Clusters, 1)
	assert.Equal(t, "describe-cluster", *out.Clusters[0].ClusterName)
}

// TestECS_ServiceLifecycle round-trips a Fargate service and its cluster's
// capacity providers through the control-plane state machine, as
// aws_ecs_service and aws_ecs_cluster_capacity_providers apply them.
func TestECS_ServiceLifecycle(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	cluster := "svc-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })
	_, subnetID := createECSTestVPCSubnet(t, "svc-lifecycle")

	_, err = c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("svc-task"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)

	_, err = c.PutClusterCapacityProviders(ctx, &ecs.PutClusterCapacityProvidersInput{
		Cluster:           aws.String(cluster),
		CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"},
		DefaultCapacityProviderStrategy: []ecstypes.CapacityProviderStrategyItem{
			{CapacityProvider: aws.String("FARGATE"), Weight: 1, Base: 1},
		},
	})
	require.NoError(t, err)
	descCluster, err := c.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{cluster}})
	require.NoError(t, err)
	require.Len(t, descCluster.Clusters, 1)
	assert.ElementsMatch(t, []string{"FARGATE", "FARGATE_SPOT"}, descCluster.Clusters[0].CapacityProviders,
		"DescribeClusters must echo the capacity providers set by PutClusterCapacityProviders")

	createOut, err := c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String("control-plane"),
		TaskDefinition: aws.String("svc-task"),
		DesiredCount:   aws.Int32(2),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.Service)
	assert.Equal(t, "ACTIVE", aws.ToString(createOut.Service.Status))
	assert.EqualValues(t, 2, createOut.Service.DesiredCount)
	assert.Contains(t, aws.ToString(createOut.Service.ServiceArn), ":service/"+cluster+"/control-plane")
	require.NotEmpty(t, createOut.Service.Deployments, "service must have a PRIMARY deployment")
	stable := waitForECSServicesStable(t, c, cluster, 30*time.Second, "control-plane")
	assert.EqualValues(t, 2, stable.Services[0].RunningCount)
	assert.EqualValues(t, 0, stable.Services[0].PendingCount)
	assert.Equal(t, ecstypes.DeploymentRolloutStateCompleted, stable.Services[0].Deployments[0].RolloutState)

	descSvc, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(cluster), Services: []string{"control-plane"},
	})
	require.NoError(t, err)
	require.Len(t, descSvc.Services, 1)
	assert.Equal(t, "ACTIVE", aws.ToString(descSvc.Services[0].Status))

	listOut, err := c.ListServices(ctx, &ecs.ListServicesInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
	require.Len(t, listOut.ServiceArns, 1)

	updOut, err := c.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String("control-plane"), DesiredCount: aws.Int32(3),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, updOut.Service.DesiredCount)
	scaled := waitForECSServicesStable(t, c, cluster, 30*time.Second, "control-plane")
	assert.EqualValues(t, 3, scaled.Services[0].RunningCount)
	assert.EqualValues(t, 0, scaled.Services[0].PendingCount)

	// DeleteService drains the tasks, then settles the service INACTIVE.
	delOut, err := c.DeleteService(ctx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster), Service: aws.String("control-plane"), Force: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "DRAINING", aws.ToString(delOut.Service.Status))
	waitForECSServiceInactive(t, c, cluster, "control-plane")
}

// waitForECSServiceInactive waits with the SDK's ServicesInactive waiter for a
// deleted service's tasks to stop.
func waitForECSServiceInactive(t *testing.T, client *ecs.Client, cluster, service string) {
	t.Helper()
	require.NoError(t, ecs.NewServicesInactiveWaiter(client, func(o *ecs.ServicesInactiveWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(cluster), Services: []string{service}}, time.Minute))
}

func TestECS_TagsAndListOps(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	cluster := "tag-ops-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(cluster),
		Tags:        []ecstypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	descCl, err := c.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{cluster}})
	require.NoError(t, err)
	clusterArn := aws.ToString(descCl.Clusters[0].ClusterArn)

	lt, err := c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(clusterArn)})
	require.NoError(t, err)
	require.Len(t, lt.Tags, 1)
	assert.Equal(t, "env", aws.ToString(lt.Tags[0].Key))

	_, err = c.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String(clusterArn),
		Tags:        []ecstypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)
	lt, err = c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(clusterArn)})
	require.NoError(t, err)
	assert.Len(t, lt.Tags, 2)

	_, err = c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("tag-svc-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)
	svcOut, err := c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster: aws.String(cluster), ServiceName: aws.String("tagged-svc-4c6e"),
		TaskDefinition: aws.String("tag-svc-task"), DesiredCount: aws.Int32(1),
		Tags: []ecstypes.Tag{{Key: aws.String("svc"), Value: aws.String("yes")}},
	})
	require.NoError(t, err)
	cleanupECSService(t, c, cluster, "tagged-svc-4c6e")
	svcArn := aws.ToString(svcOut.Service.ServiceArn)
	lt, err = c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(svcArn)})
	require.NoError(t, err)
	require.Len(t, lt.Tags, 1)
	assert.Equal(t, "svc", aws.ToString(lt.Tags[0].Key))

	_, err = c.UntagResource(ctx, &ecs.UntagResourceInput{ResourceArn: aws.String(svcArn), TagKeys: []string{"svc"}})
	require.NoError(t, err)
	lt, err = c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(svcArn)})
	require.NoError(t, err)
	assert.Empty(t, lt.Tags)

	lc, err := c.ListClusters(ctx, &ecs.ListClustersInput{})
	require.NoError(t, err)
	assert.Contains(t, lc.ClusterArns, clusterArn)

	ltd, err := c.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{FamilyPrefix: aws.String("tag-svc-task")})
	require.NoError(t, err)
	assert.NotEmpty(t, ltd.TaskDefinitionArns)

	ls, err := c.ListServices(ctx, &ecs.ListServicesInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
	assert.Contains(t, ls.ServiceArns, svcArn)
}

func TestECS_RegisterTaskDefinition(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	family := uniquename.New("test-task")
	out, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				StopTimeout: aws.Int32(2),
				Name:        aws.String("app"),
				Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, family, *out.TaskDefinition.Family)
	assert.Equal(t, int32(1), out.TaskDefinition.Revision)
}

func TestECS_MultiContainerTaskSharesLocalhost(t *testing.T) {
	t.Parallel()
	client := ecsClient()

	clusterName := "pod-localhost"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(clusterName),
	})
	require.NoError(t, err)

	logGroupName := "/ecs/pod-localhost"
	cw := cwLogsClient()
	_, _ = cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroupName)})
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
	})

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("pod-localhost"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				StopTimeout: aws.Int32(2),
				Name:        aws.String("main"),
				Image:       aws.String(containerCommandImage),
				Command:     []string{"probe-http", "http://127.0.0.1:9090", "sidecar-ok", "10"},
				LogConfiguration: &ecstypes.LogConfiguration{
					LogDriver: ecstypes.LogDriverAwslogs,
					Options: map[string]string{
						"awslogs-group":         logGroupName,
						"awslogs-create-group":  "true",
						"awslogs-stream-prefix": "ecs",
					},
				},
			},
			{
				StopTimeout: aws.Int32(2),
				Name:        aws.String("sidecar"),
				Image:       aws.String(containerCommandImage),
				Command:     []string{"http", "9090", "sidecar-ok"},
			},
		},
	})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "pod-localhost")

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: tdOut.TaskDefinition.TaskDefinitionArn,
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
	cleanupECSTask(t, client, clusterName, taskArn)

	waitTaskStopped(t, client, clusterName, taskArn)

	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroupName),
	})
	require.NoError(t, err)
	var messages []string
	for _, e := range events.Events {
		messages = append(messages, aws.ToString(e.Message))
	}
	assert.Contains(t, strings.Join(messages, "\n"), "sidecar-ok")
}

func TestECS_ManagedEBSVolumeSnapshotRoundTripSDK(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	ec2c := ec2Client()
	cw := cwLogsClient()

	clusterName := "managed-ebs-roundtrip"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "managed-ebs-roundtrip")

	logGroupName := "/ecs/managed-ebs-roundtrip"
	_, _ = cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroupName)})
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
	})

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("managed-ebs-roundtrip"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		Volumes: []ecstypes.Volume{{
			Name:               aws.String("workspace"),
			ConfiguredAtLaunch: aws.Bool(true),
		}},
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("writer"),
			Image:       aws.String(busyboxImage),
			EntryPoint:  []string{"sh", "-c"},
			Command:     []string{"printf 'sockerless-ebs-roundtrip' > /workspace/state.txt"},
			MountPoints: []ecstypes.MountPoint{{
				SourceVolume:  aws.String("workspace"),
				ContainerPath: aws.String("/workspace"),
			}},
		}},
	})
	require.NoError(t, err)

	keepVolume := false
	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: tdOut.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
		VolumeConfigurations: []ecstypes.TaskVolumeConfiguration{{
			Name: aws.String("workspace"),
			ManagedEBSVolume: &ecstypes.TaskManagedEBSVolumeConfiguration{
				RoleArn:    aws.String(createECSVolumeInfrastructureRole(t)),
				SizeInGiB:  aws.Int32(1),
				VolumeType: aws.String("gp3"),
				TerminationPolicy: &ecstypes.TaskManagedEBSVolumeTerminationPolicy{
					DeleteOnTermination: aws.Bool(keepVolume),
				},
				TagSpecifications: []ecstypes.EBSTagSpecification{{
					ResourceType: ecstypes.EBSResourceTypeVolume,
					Tags:         []ecstypes.Tag{{Key: aws.String("purpose"), Value: aws.String("roundtrip")}},
				}},
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	writerTaskArn := aws.ToString(runOut.Tasks[0].TaskArn)
	waitForECSTaskStatus(t, client, clusterName, writerTaskArn, "STOPPED")
	writerDesc, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{writerTaskArn},
	})
	require.NoError(t, err)
	volumeID := ebsVolumeIDFromTask(t, writerDesc.Tasks[0])
	t.Cleanup(func() {
		_, _ = ec2c.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeID)})
	})

	snapshotOut, err := ec2c.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{
		VolumeId:    aws.String(volumeID),
		Description: aws.String("ecs managed ebs roundtrip"),
	})
	require.NoError(t, err)
	snapshotID := aws.ToString(snapshotOut.SnapshotId)
	require.NotEmpty(t, snapshotID)
	waitForEC2SnapshotCompleted(t, ec2c, snapshotID)
	t.Cleanup(func() {
		_, _ = ec2c.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: aws.String(snapshotID)})
	})

	readerTD, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("managed-ebs-reader"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		Volumes: []ecstypes.Volume{{
			Name:               aws.String("workspace"),
			ConfiguredAtLaunch: aws.Bool(true),
		}},
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("reader"),
			Image:       aws.String(busyboxImage),
			EntryPoint:  []string{"sh", "-c"},
			Command: []string{`test "$(cat /workspace/state.txt)" = "sockerless-ebs-roundtrip"
echo EBS_ROUNDTRIP_OK`},
			MountPoints: []ecstypes.MountPoint{{
				SourceVolume:  aws.String("workspace"),
				ContainerPath: aws.String("/workspace"),
			}},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         logGroupName,
					"awslogs-create-group":  "true",
					"awslogs-stream-prefix": "ecs",
				},
			},
		}},
	})
	require.NoError(t, err)

	runReader, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: readerTD.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
		VolumeConfigurations: []ecstypes.TaskVolumeConfiguration{{
			Name: aws.String("workspace"),
			ManagedEBSVolume: &ecstypes.TaskManagedEBSVolumeConfiguration{
				RoleArn:    aws.String(createECSVolumeInfrastructureRole(t)),
				SnapshotId: aws.String(snapshotID),
				VolumeType: aws.String("gp3"),
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, runReader.Tasks, 1)
	readerTaskArn := aws.ToString(runReader.Tasks[0].TaskArn)
	waitForECSTaskStatus(t, client, clusterName, readerTaskArn, "STOPPED")

	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroupName),
	})
	require.NoError(t, err)
	var messages []string
	for _, e := range events.Events {
		messages = append(messages, aws.ToString(e.Message))
	}
	assert.Contains(t, strings.Join(messages, "\n"), "EBS_ROUNDTRIP_OK")
}

// A managed EBS volume is the task's own: what one task writes to it is
// there for the task that mounts it next and absent from a volume another
// task asked for, and a filesystem type Amazon ECS does not offer is refused.
func TestECS_ManagedEBSVolumeIsTheTasksOwnSDK(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	cw := cwLogsClient()

	clusterName := "managed-ebs-isolation"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "managed-ebs-isolation")

	logGroupName := "/ecs/managed-ebs-isolation"
	_, _ = cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroupName)})
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
	})

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("managed-ebs-isolation"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		Volumes: []ecstypes.Volume{{
			Name:               aws.String("data"),
			ConfiguredAtLaunch: aws.Bool(true),
		}},
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("probe"),
			Image:       aws.String(busyboxImage),
			EntryPoint:  []string{"sh", "-c"},
			// A fresh volume holds nothing of another task's: the marker a task leaves is
			// absent when the next task asks for a volume of its own.
			Command: []string{`set -e
test ! -e /data/marker
echo mine > /data/marker
test "$(cat /data/marker)" = mine
echo EBS_ISOLATION_OK`},
			MountPoints: []ecstypes.MountPoint{{
				SourceVolume:  aws.String("data"),
				ContainerPath: aws.String("/data"),
			}},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         logGroupName,
					"awslogs-create-group":  "true",
					"awslogs-stream-prefix": "ecs",
				},
			},
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
		VolumeConfigurations: []ecstypes.TaskVolumeConfiguration{{
			Name: aws.String("data"),
			ManagedEBSVolume: &ecstypes.TaskManagedEBSVolumeConfiguration{
				RoleArn:        aws.String(createECSVolumeInfrastructureRole(t)),
				SizeInGiB:      aws.Int32(1),
				FilesystemType: ecstypes.TaskFilesystemTypeExt4,
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := aws.ToString(runOut.Tasks[0].TaskArn)
	waitForECSTaskStatus(t, client, clusterName, taskArn, "STOPPED")
	described, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(clusterName), Tasks: []string{taskArn}})
	require.NoError(t, err)
	require.Len(t, described.Tasks, 1)
	task := described.Tasks[0]

	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(logGroupName),
	})
	require.NoError(t, err)
	var messages []string
	for _, e := range events.Events {
		messages = append(messages, aws.ToString(e.Message))
	}
	var exits []string
	for _, c := range task.Containers {
		exits = append(exits, fmt.Sprintf("%s exit %d %s", aws.ToString(c.Name), aws.ToInt32(c.ExitCode), aws.ToString(c.Reason)))
	}
	require.Contains(t, strings.Join(messages, "\n"), "EBS_ISOLATION_OK",
		"stopped: %s; containers: %s; output: %s", aws.ToString(task.StoppedReason), strings.Join(exits, ", "), strings.Join(messages, " | "))

	runAgain, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: tdOut.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
		VolumeConfigurations: []ecstypes.TaskVolumeConfiguration{{
			Name: aws.String("data"),
			ManagedEBSVolume: &ecstypes.TaskManagedEBSVolumeConfiguration{
				RoleArn:        aws.String(createECSVolumeInfrastructureRole(t)),
				SizeInGiB:      aws.Int32(1),
				FilesystemType: ecstypes.TaskFilesystemTypeExt4,
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, runAgain.Tasks, 1)
	againArn := aws.ToString(runAgain.Tasks[0].TaskArn)
	waitForECSTaskStatus(t, client, clusterName, againArn, "STOPPED")
	again, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(clusterName), Tasks: []string{againArn}})
	require.NoError(t, err)
	require.Len(t, again.Tasks, 1)
	require.NotEqual(t, ebsVolumeIDFromTask(t, task), ebsVolumeIDFromTask(t, again.Tasks[0]), "each task's managed volume is its own")
	for _, c := range again.Tasks[0].Containers {
		require.EqualValues(t, 0, aws.ToInt32(c.ExitCode), "the second task's volume held the first task's marker: %s", aws.ToString(c.Reason))
	}

	_, err = client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: tdOut.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
		VolumeConfigurations: []ecstypes.TaskVolumeConfiguration{{
			Name: aws.String("data"),
			ManagedEBSVolume: &ecstypes.TaskManagedEBSVolumeConfiguration{
				RoleArn:        aws.String(createECSVolumeInfrastructureRole(t)),
				SizeInGiB:      aws.Int32(1),
				FilesystemType: ecstypes.TaskFilesystemType("btrfs"),
			},
		}},
	})
	var invalid *ecstypes.InvalidParameterException
	require.ErrorAs(t, err, &invalid)
}

func TestECS_RunTaskContainerOverridesApplyToRuntimeSDK(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	cw := cwLogsClient()

	clusterName := "override-runtime-sdk"
	logGroup := "/ecs/override-runtime-sdk"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
	})
	subnetID := createECSTestSubnet(t, "override-runtime-sdk")

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("override-runtime-sdk-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("workspace"),
			Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
			Command: []string{
				"sh", "-c",
				`echo taskdef:${EDD_WORKSPACE_ID:-missing}:${BASE_ONLY}:${OVERRIDE_ME}`,
			},
			Environment: []ecstypes.KeyValuePair{
				{Name: aws.String("BASE_ONLY"), Value: aws.String("from-task-definition")},
				{Name: aws.String("OVERRIDE_ME"), Value: aws.String("from-task-definition")},
			},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         logGroup,
					"awslogs-create-group":  "true",
					"awslogs-stream-prefix": "ecs",
				},
			},
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
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{{
				Name: aws.String("workspace"),
				Command: []string{
					"sh", "-c",
					`echo override:${EDD_WORKSPACE_ID}:${BASE_ONLY}:${OVERRIDE_ME}`,
				},
				Environment: []ecstypes.KeyValuePair{
					{Name: aws.String("EDD_WORKSPACE_ID"), Value: aws.String("ws-sdk")},
					{Name: aws.String("OVERRIDE_ME"), Value: aws.String("from-runtask")},
				},
			}},
			Cpu:    aws.String("512"),
			Memory: aws.String("1024"),
		},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := aws.ToString(runOut.Tasks[0].TaskArn)
	cleanupECSTask(t, client, clusterName, taskArn)

	waitForECSTaskStatus(t, client, clusterName, taskArn, "STOPPED")

	desc, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, desc.Tasks, 1)
	assert.Equal(t, "512", aws.ToString(desc.Tasks[0].Cpu))
	assert.Equal(t, "1024", aws.ToString(desc.Tasks[0].Memory))
	require.NotNil(t, desc.Tasks[0].Overrides)
	require.Len(t, desc.Tasks[0].Overrides.ContainerOverrides, 1)
	assert.Equal(t, "workspace", aws.ToString(desc.Tasks[0].Overrides.ContainerOverrides[0].Name))
	require.Len(t, desc.Tasks[0].Overrides.ContainerOverrides[0].Environment, 2)
	assert.Equal(t, "ws-sdk", aws.ToString(desc.Tasks[0].Overrides.ContainerOverrides[0].Environment[0].Value))

	awaitLogLine(t, cw, logGroup, "override:ws-sdk:from-task-definition:from-runtask", 10*time.Second)
}

func TestECS_ExitCodeNilWhileRunning(t *testing.T) {
	t.Parallel()
	client := ecsClient()

	clusterName := "exitcode-test-cluster"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(clusterName),
	})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "exitcode")

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("exitcode-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				StopTimeout: aws.Int32(2),
				Name:        aws.String("app"),
				Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
				Command:     []string{"sleep", "30"}, // long enough for a real RUNNING window
			},
		},
	})
	require.NoError(t, err)

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: aws.String(*tdOut.TaskDefinition.TaskDefinitionArn),
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
	cleanupECSTask(t, client, clusterName, taskArn)

	waitForECSTaskStatus(t, client, clusterName, taskArn, "RUNNING")

	// While RUNNING, ExitCode is nil.
	descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Tasks, 1)
	require.NotEmpty(t, descOut.Tasks[0].Containers)

	runningTask := descOut.Tasks[0]
	assert.Equal(t, "RUNNING", *runningTask.LastStatus)
	for _, c := range runningTask.Containers {
		assert.Nil(t, c.ExitCode, "ExitCode should be nil while task is RUNNING")
	}

	// Amazon ECS has no task timeout; a task runs until stopped.
	_, err = client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(clusterName),
		Task:    aws.String(taskArn),
	})
	require.NoError(t, err)

	// StopTask answers while the container is still stopping, so the exit
	// code is read once the task has stopped.
	stoppedTask := waitTaskStopped(t, client, clusterName, taskArn)
	assert.Equal(t, "STOPPED", *stoppedTask.LastStatus)
	assert.Equal(t, ecstypes.TaskStopCodeUserInitiated, stoppedTask.StopCode)
	for _, c := range stoppedTask.Containers {
		require.NotNil(t, c.ExitCode, "ExitCode should be set when task is STOPPED")
		// A user stop SIGKILLs the container, giving 137 (128+SIGKILL) as Fargate
		// reports, not 0.
		assert.Equal(t, int32(137), *c.ExitCode)
	}
}

func TestECS_StopCodeUserInitiated(t *testing.T) {
	t.Parallel()
	client := ecsClient()

	clusterName := "stopcode-user-cluster"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(clusterName),
	})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "stopcode-user")

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("stopcode-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				// sleep runs as PID 1 and ignores SIGTERM, so the container is
				// stopped only when this timeout runs out and is SIGKILLed.
				StopTimeout: aws.Int32(10),
				Name:        aws.String("app"),
				Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
				Command:     []string{"sleep", "30"},
			},
		},
	})
	require.NoError(t, err)

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: aws.String(*tdOut.TaskDefinition.TaskDefinitionArn),
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
	cleanupECSTask(t, client, clusterName, taskArn)

	waitForECSTaskStatus(t, client, clusterName, taskArn, "RUNNING")

	// Amazon ECS answers StopTask at once, with the task still running, and gives
	// the container its stop timeout afterwards.
	requested := time.Now()
	stopOut, err := client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(clusterName),
		Task:    aws.String(taskArn),
		Reason:  aws.String("testing stop"),
	})
	require.NoError(t, err)
	assert.Less(t, time.Since(requested), 10*time.Second,
		"StopTask must answer before the container's stop timeout has run out")
	require.NotNil(t, stopOut.Task)
	assert.Equal(t, "RUNNING", aws.ToString(stopOut.Task.LastStatus))
	assert.Equal(t, "STOPPED", aws.ToString(stopOut.Task.DesiredStatus))
	assert.Equal(t, ecstypes.TaskStopCodeUserInitiated, stopOut.Task.StopCode)
	assert.Equal(t, "testing stop", aws.ToString(stopOut.Task.StoppedReason))
	assert.NotNil(t, stopOut.Task.StoppingAt)
	assert.Nil(t, stopOut.Task.StoppedAt)

	task := waitTaskStopped(t, client, clusterName, taskArn)
	assert.Equal(t, ecstypes.TaskStopCodeUserInitiated, task.StopCode)
	assert.Equal(t, "testing stop", *task.StoppedReason)
	assert.NotNil(t, task.StoppingAt)
	assert.NotNil(t, task.StoppedAt)
}

// waitTaskStopped waits with the SDK's TasksStopped waiter and returns the
// stopped task.
func waitTaskStopped(t *testing.T, client *ecs.Client, cluster, taskArn string) ecstypes.Task {
	t.Helper()
	out, err := ecs.NewTasksStoppedWaiter(client, func(o *ecs.TasksStoppedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{taskArn},
	}, 60*time.Second)
	require.NoError(t, err, "task %s did not stop", taskArn)
	require.Len(t, out.Tasks, 1)
	return out.Tasks[0]
}

func ecsRunTaskHelper(t *testing.T, name string, containerDef ecstypes.ContainerDefinition) (*ecs.Client, string, string) {
	t.Helper()
	client := ecsClient()
	clusterName := name + "-cluster"
	subnetID := createECSTestSubnet(t, name)

	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(clusterName),
	})
	require.NoError(t, err)

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(name + "-task"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions:    []ecstypes.ContainerDefinition{containerDef},
	})
	require.NoError(t, err)

	runOut, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: aws.String(*tdOut.TaskDefinition.TaskDefinitionArn),
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
	cleanupECSTask(t, client, clusterName, taskArn)

	return client, clusterName, taskArn
}

// ecsVolumeInfrastructureActions are the Amazon EC2 actions the AWS managed
// policy AmazonECSInfrastructureRolePolicyForVolumes grants the infrastructure
// role that creates, attaches and deletes a task's Amazon EBS volumes.
var ecsVolumeInfrastructureActions = []string{
	"ec2:CreateVolume", "ec2:DeleteVolume", "ec2:AttachVolume", "ec2:DetachVolume",
	"ec2:DescribeVolumes", "ec2:DescribeSnapshots", "ec2:ModifyVolume", "ec2:CreateTags",
}

// createECSVolumeInfrastructureRole creates the role Amazon ECS manages a
// task's Amazon EBS volumes as: trusted by ecs.amazonaws.com and allowed the
// volume actions.
func createECSVolumeInfrastructureRole(t *testing.T) string {
	t.Helper()
	return createServiceRole(t, uniquename.New("ecs-volumes"), "ecs.amazonaws.com", ecsVolumeInfrastructureActions...)
}

func createECSTestSubnet(t *testing.T, name string) string {
	t.Helper()
	_, subnetID := createECSTestVPCSubnet(t, name)
	return subnetID
}

func createECSTestVPCSubnet(t *testing.T, name string) (string, string) {
	t.Helper()
	vpcID, subnets := createECSTestVPCSubnets(t, name, 1)
	return vpcID, subnets[0]
}

// createECSTestVPCSubnets creates a VPC with count subnets, each in its own
// Availability Zone, as an Application Load Balancer needs at least two.
func createECSTestVPCSubnets(t *testing.T, name string, count int) (string, []string) {
	t.Helper()
	ec2c := ec2Client()
	// ECS helper VPCs take a /20 each in 10.225.0.0/16 through 10.249.0.0/16, clear
	// of the fixed CIDRs other SDK tests use. 400 of them keep every live VPC in
	// one run on its own range: two engine networks over one range cannot coexist.
	n := int(ecsTestSubnetCounter.Add(1) % 400)
	second, third := 225+n/16, (n%16)*16
	vpcCIDR := fmt.Sprintf("10.%d.%d.0/20", second, third)

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String(vpcCIDR),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeVpc,
			Tags: []ec2types.Tag{{
				Key:   aws.String("Name"),
				Value: aws.String(name + "-vpc"),
			}},
		}},
	})
	require.NoError(t, err)
	vpcID := aws.ToString(vpcOut.Vpc.VpcId)

	var subnets []string
	t.Cleanup(func() {
		for _, subnetID := range subnets {
			stopECSTasksInSubnet(t, ecsClient(), subnetID)
			_, err := ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetID)})
			require.NoError(t, err, "delete ECS test subnet %s once its tasks stopped", subnetID)
		}
		_, err := ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
		require.NoError(t, err, "delete ECS test VPC %s", vpcID)
	})
	for i := range count {
		subnetOut, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
			VpcId:            aws.String(vpcID),
			CidrBlock:        aws.String(fmt.Sprintf("10.%d.%d.0/24", second, third+i)),
			AvailabilityZone: aws.String("us-east-1" + string(rune('a'+i))),
			TagSpecifications: []ec2types.TagSpecification{{
				ResourceType: ec2types.ResourceTypeSubnet,
				Tags: []ec2types.Tag{{
					Key:   aws.String("Name"),
					Value: aws.String(name + "-subnet"),
				}},
			}},
		})
		require.NoError(t, err)
		subnets = append(subnets, aws.ToString(subnetOut.Subnet.SubnetId))
	}
	return vpcID, subnets
}

// stopECSTasksInSubnet stops every Amazon ECS task whose interface is in the
// subnet and waits for each: Amazon EC2 refuses DeleteSubnet with
// DependencyViolation while one remains.
func stopECSTasksInSubnet(t *testing.T, client *ecs.Client, subnetID string) {
	t.Helper()
	stopECSTasks(t, client, func(task ecstypes.Task) bool { return ecsTaskInSubnet(task, subnetID) })
}

// stopECSTasks stops every Amazon ECS task that match selects and waits with
// the SDK's TasksStopped waiter until each has stopped.
func stopECSTasks(t *testing.T, client *ecs.Client, match func(ecstypes.Task) bool) {
	t.Helper()
	var clusterArns []string
	for pages := ecs.NewListClustersPaginator(client, &ecs.ListClustersInput{}); pages.HasMorePages(); {
		page, err := pages.NextPage(ctx)
		require.NoError(t, err)
		clusterArns = append(clusterArns, page.ClusterArns...)
	}
	// Another test may delete a listed cluster before it is read; Amazon ECS
	// deletes only a cluster with no active tasks, so a vanished one holds none.
	var gone *ecstypes.ClusterNotFoundException
clusters:
	for _, cluster := range clusterArns {
		var taskArns []string
		for _, desired := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
			pages := ecs.NewListTasksPaginator(client, &ecs.ListTasksInput{Cluster: aws.String(cluster), DesiredStatus: desired})
			for pages.HasMorePages() {
				page, err := pages.NextPage(ctx)
				if errors.As(err, &gone) {
					continue clusters
				}
				require.NoError(t, err)
				taskArns = append(taskArns, page.TaskArns...)
			}
		}
		var inSubnet []string
		for start := 0; start < len(taskArns); start += 100 {
			described, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String(cluster),
				Tasks:   taskArns[start:min(start+100, len(taskArns))],
			})
			if errors.As(err, &gone) {
				continue clusters
			}
			require.NoError(t, err)
			for _, task := range described.Tasks {
				if aws.ToString(task.LastStatus) == "STOPPED" || !match(task) {
					continue
				}
				inSubnet = append(inSubnet, aws.ToString(task.TaskArn))
				if aws.ToString(task.DesiredStatus) != "STOPPED" {
					_, err := client.StopTask(ctx, &ecs.StopTaskInput{
						Cluster: aws.String(cluster), Task: task.TaskArn, Reason: aws.String("test cleanup"),
					})
					require.NoError(t, err)
				}
			}
		}
		for start := 0; start < len(inSubnet); start += 100 {
			_, err := ecs.NewTasksStoppedWaiter(client, func(o *ecs.TasksStoppedWaiterOptions) {
				o.MinDelay = waiterMinDelay
				o.MaxDelay = waiterMaxDelay
			}).WaitForOutput(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String(cluster),
				Tasks:   inSubnet[start:min(start+100, len(inSubnet))],
			}, 2*time.Minute)
			require.NoError(t, err, "tasks in cluster %s did not stop", cluster)
		}
	}
}

func ecsTaskInSubnet(task ecstypes.Task, subnetID string) bool {
	for _, attachment := range task.Attachments {
		if aws.ToString(attachment.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, detail := range attachment.Details {
			if aws.ToString(detail.Name) == "subnetId" && aws.ToString(detail.Value) == subnetID {
				return true
			}
		}
	}
	return false
}

func cleanupECSTask(t *testing.T, client *ecs.Client, clusterName, taskArn string) {
	t.Helper()
	t.Cleanup(func() {
		_, err := client.StopTask(ctx, &ecs.StopTaskInput{
			Cluster: aws.String(clusterName),
			Task:    aws.String(taskArn),
			Reason:  aws.String("test cleanup"),
		})
		require.NoError(t, err)
	})
}

func cleanupECSService(t *testing.T, client *ecs.Client, clusterName, serviceName string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = client.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster: aws.String(clusterName), Service: aws.String(serviceName), DesiredCount: aws.Int32(0),
		})
		_, _ = client.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(clusterName), Service: aws.String(serviceName), Force: aws.Bool(true),
		})
	})
}

// waitForECSTaskStatus waits for RUNNING or STOPPED with the SDK's TasksRunning
// or TasksStopped waiter; TasksRunning fails at once if the task stops instead.
func waitForECSTaskStatus(t *testing.T, client *ecs.Client, clusterName, taskArn, want string) {
	t.Helper()
	input := &ecs.DescribeTasksInput{Cluster: aws.String(clusterName), Tasks: []string{taskArn}}
	var err error
	switch want {
	case "RUNNING":
		err = ecs.NewTasksRunningWaiter(client, func(o *ecs.TasksRunningWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).Wait(ctx, input, 60*time.Second)
	case "STOPPED":
		err = ecs.NewTasksStoppedWaiter(client, func(o *ecs.TasksStoppedWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).Wait(ctx, input, 60*time.Second)
	default:
		t.Fatalf("the Amazon ECS SDK has no waiter for lastStatus %s", want)
	}
	require.NoError(t, err, "task %s did not reach %s", taskArn, want)
}

// waitForECSTasksRunning waits with the SDK's TasksRunning waiter, which fails
// at once when any of the tasks stops instead of running.
func waitForECSTasksRunning(t *testing.T, client *ecs.Client, cluster string, timeout time.Duration, tasks ...string) *ecs.DescribeTasksOutput {
	t.Helper()
	out, err := ecs.NewTasksRunningWaiter(client, func(o *ecs.TasksRunningWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(cluster), Tasks: tasks}, timeout)
	require.NoError(t, err, "tasks %v did not reach RUNNING", tasks)
	require.Len(t, out.Tasks, len(tasks))
	return out
}

// waitForECSServicesStable waits with the SDK's ServicesStable waiter for one
// deployment per service at the desired count with its rollout COMPLETED,
// which Amazon ECS records at the same moment ("when the service reaches a
// steady state, the deployment transitions to a COMPLETED state").
func waitForECSServicesStable(t *testing.T, client *ecs.Client, cluster string, timeout time.Duration, services ...string) *ecs.DescribeServicesOutput {
	t.Helper()
	out, err := ecs.NewServicesStableWaiter(client, func(o *ecs.ServicesStableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
		o.Retryable = ecsServicesSteadyRetryable
	}).WaitForOutput(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(cluster), Services: services}, timeout)
	if err != nil {
		last, describeErr := client.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(cluster), Services: services})
		require.NoError(t, describeErr)
		var states []string
		for _, service := range last.Services {
			states = append(states, fmt.Sprintf("%s: %d deployment(s), %s",
				aws.ToString(service.ServiceName), len(service.Deployments), describeECSService(service)))
		}
		require.NoError(t, err, "services did not reach a steady state: %v", states)
	}
	require.Len(t, out.Services, len(services))
	return out
}

func ecsServicesSteadyRetryable(_ context.Context, _ *ecs.DescribeServicesInput, out *ecs.DescribeServicesOutput, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	for _, failure := range out.Failures {
		if aws.ToString(failure.Reason) == "MISSING" {
			return false, fmt.Errorf("service %s is MISSING", aws.ToString(failure.Arn))
		}
	}
	for _, service := range out.Services {
		switch status := aws.ToString(service.Status); status {
		case "DRAINING", "INACTIVE":
			return false, fmt.Errorf("service %s is %s", aws.ToString(service.ServiceName), status)
		}
	}
	for _, service := range out.Services {
		if len(service.Deployments) != 1 || service.RunningCount != service.DesiredCount ||
			service.Deployments[0].RolloutState != ecstypes.DeploymentRolloutStateCompleted {
			return true, nil
		}
	}
	return false, nil
}

func ebsVolumeIDFromTask(t *testing.T, task ecstypes.Task) string {
	t.Helper()
	for _, att := range task.Attachments {
		if aws.ToString(att.Type) != "AmazonElasticBlockStorage" {
			continue
		}
		for _, detail := range att.Details {
			if aws.ToString(detail.Name) == "volumeId" {
				return aws.ToString(detail.Value)
			}
		}
	}
	t.Fatalf("task %s did not include an AmazonElasticBlockStorage volume attachment", aws.ToString(task.TaskArn))
	return ""
}

func TestECS_TaskExecutesCommand(t *testing.T) {
	t.Parallel()
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-cmd", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
		Command:     []string{"echo", "hello"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/exec-cmd",
				"awslogs-create-group":  "true",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	// Starting, running and stopping all happen asynchronously.
	task := waitTaskStopped(t, client, cluster, taskArn)
	require.NotEmpty(t, task.Containers)
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(0), *task.Containers[0].ExitCode, "stopped reason: %s", aws.ToString(task.StoppedReason))
}

func TestECS_TaskExitCodeNonZero(t *testing.T) {
	t.Parallel()
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-fail", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
		Command:     []string{"sh", "-c", "exit 1"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/exec-fail",
				"awslogs-create-group":  "true",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	task := waitTaskStopped(t, client, cluster, taskArn)
	require.NotEmpty(t, task.Containers)
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(1), *task.Containers[0].ExitCode)
}

// TestECS_TaskFailsWhenAwslogsGroupIsMissing expects the awslogs driver to
// create no log group without awslogs-create-group, failing the task's start
// with ResourceInitializationError.
func TestECS_TaskFailsWhenAwslogsGroupIsMissing(t *testing.T) {
	t.Parallel()
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-no-group", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
		Command:     []string{"echo", "never runs"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/exec-no-group",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	task := waitTaskStopped(t, client, cluster, taskArn)
	assert.Equal(t, ecstypes.TaskStopCodeTaskFailedToStart, task.StopCode)
	assert.Contains(t, aws.ToString(task.StoppedReason), "ResourceInitializationError")
	assert.Contains(t, aws.ToString(task.StoppedReason), "The specified log group does not exist")

	groups, err := cwLogsClient().DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String("/ecs/exec-no-group"),
	})
	require.NoError(t, err)
	assert.Empty(t, groups.LogGroups, "the awslogs driver must not create a group awslogs-create-group did not ask for")
}

func TestECS_TaskLogsToCloudWatch(t *testing.T) {
	t.Parallel()
	logGroup := uniquename.New("/ecs/exec-logs")
	client, clusterName, taskArn := ecsRunTaskHelper(t, "exec-logs", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
		Command:     []string{"echo", "hello from process"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         logGroup,
				"awslogs-create-group":  "true",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	cw := cwLogsClient()

	// The awslogs driver has delivered every line by the time the task stops.
	waitTaskStopped(t, client, clusterName, taskArn)
	streams, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.Len(t, streams.LogStreams, 1)
	out, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: streams.LogStreams[0].LogStreamName,
	})
	require.NoError(t, err)
	var messages []string
	for _, e := range out.Events {
		messages = append(messages, aws.ToString(e.Message))
	}
	require.Contains(t, messages, "hello from process", "process stdout should reach CloudWatch logs")

	// The stream's first event is the container's own first line; Amazon ECS
	// writes nothing to it at RunTask time.
	require.Equal(t, "hello from process", messages[0],
		"the log stream must begin with the container's first line, not a simulator marker")

	// The task reports the pull window, so a slow start is attributable to the
	// pull: both timestamps are set, even for a cached image, and bracket the start
	// in order.
	described, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName), Tasks: []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, described.Tasks, 1)
	task := described.Tasks[0]
	require.NotNil(t, task.PullStartedAt, "DescribeTasks must report pullStartedAt")
	require.NotNil(t, task.PullStoppedAt, "DescribeTasks must report pullStoppedAt")
	require.NotNil(t, task.CreatedAt)
	require.NotNil(t, task.StartedAt)
	require.False(t, task.PullStartedAt.Before(*task.CreatedAt), "pull cannot begin before the task was created")
	require.False(t, task.PullStoppedAt.Before(*task.PullStartedAt), "pull cannot stop before it started")
	// Every task timestamp carries milliseconds, as on Amazon ECS.
	require.False(t, task.StartedAt.Before(*task.PullStoppedAt), "the container cannot start before its image is present")
}

// TestECS_RunningTaskStreamsLogsLive expects a long-running task's stdout in
// its log stream while it is RUNNING, as awslogs forwards each line when
// written, and the post-exit drain not to repeat those lines.
func TestECS_RunningTaskStreamsLogsLive(t *testing.T) {
	t.Parallel()
	logGroup := uniquename.New("/ecs/live-logs")
	client, cluster, taskArn := ecsRunTaskHelper(t, "live-logs", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
		Command:     []string{"sh", "-c", "echo live-line-from-running-task; tail -f /dev/null"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         logGroup,
				"awslogs-create-group":  "true",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	cw := cwLogsClient()
	countLiveLines := func() int {
		streams, serr := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
			LogGroupName: aws.String(logGroup),
		})
		if serr != nil {
			return 0
		}
		count := 0
		for _, stream := range streams.LogStreams {
			out, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
				LogGroupName:  aws.String(logGroup),
				LogStreamName: stream.LogStreamName,
			})
			if err != nil {
				continue
			}
			for _, e := range out.Events {
				if *e.Message == "live-line-from-running-task" {
					count++
				}
			}
		}
		return count
	}

	waitForECSTasksRunning(t, client, cluster, 30*time.Second, taskArn)
	awaitLogLine(t, cw, logGroup, "live-line-from-running-task", 30*time.Second)

	descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Tasks, 1)
	require.Equal(t, "RUNNING", aws.ToString(descOut.Tasks[0].LastStatus),
		"the log line must be visible while the task is still RUNNING")

	_, err = client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(cluster),
		Task:    aws.String(taskArn),
		Reason:  aws.String("live-log streaming regression complete"),
	})
	require.NoError(t, err)
	waitTaskStopped(t, client, cluster, taskArn)
	assert.Equal(t, 1, countLiveLines(),
		"the post-exit drain must not duplicate lines the live stream delivered")
}

func TestECS_TaskNoCommandStaysRunning(t *testing.T) {
	t.Parallel()
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-nocmd", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
		Command:     []string{"tail", "-f", "/dev/null"}, // stays RUNNING
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/exec-nocmd",
				"awslogs-create-group":  "true",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	running, err := ecs.NewTasksRunningWaiter(client, func(o *ecs.TasksRunningWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{taskArn},
	}, 30*time.Second)
	require.NoError(t, err, "task with no command should reach RUNNING")
	require.Len(t, running.Tasks, 1)
	task := running.Tasks[0]

	assert.Equal(t, "RUNNING", *task.LastStatus, "task with no command should stay RUNNING")
	for _, c := range task.Containers {
		assert.Nil(t, c.ExitCode, "ExitCode should be nil while RUNNING")
	}
}

// TestECS_TagResource_OnRunningTask tags a running task, lists and untags its
// tags, and expects tagging a STOPPED task to be rejected.
func TestECS_TagResource_OnRunningTask(t *testing.T) {
	t.Parallel()
	client, cluster, taskArn := ecsRunTaskHelper(t, "tag-task", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
		Command:     []string{"tail", "-f", "/dev/null"},
	})
	_ = cluster

	_, err := client.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String(taskArn),
		Tags: []ecstypes.Tag{
			{Key: aws.String("sockerless-name"), Value: aws.String("my-task")},
			{Key: aws.String("sockerless-restart-count"), Value: aws.String("0")},
		},
	})
	require.NoError(t, err)

	listOut, err := client.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{
		ResourceArn: aws.String(taskArn),
	})
	require.NoError(t, err)

	got := map[string]string{}
	for _, tag := range listOut.Tags {
		got[*tag.Key] = *tag.Value
	}
	assert.Equal(t, "my-task", got["sockerless-name"])
	assert.Equal(t, "0", got["sockerless-restart-count"])

	// Tagging an existing key overwrites it.
	_, err = client.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String(taskArn),
		Tags: []ecstypes.Tag{
			{Key: aws.String("sockerless-restart-count"), Value: aws.String("3")},
		},
	})
	require.NoError(t, err)

	listOut, err = client.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{
		ResourceArn: aws.String(taskArn),
	})
	require.NoError(t, err)
	got = map[string]string{}
	for _, tag := range listOut.Tags {
		got[*tag.Key] = *tag.Value
	}
	assert.Equal(t, "my-task", got["sockerless-name"], "existing key should persist after partial update")
	assert.Equal(t, "3", got["sockerless-restart-count"], "matching key should be overwritten")

	_, err = client.UntagResource(ctx, &ecs.UntagResourceInput{
		ResourceArn: aws.String(taskArn),
		TagKeys:     []string{"sockerless-restart-count"},
	})
	require.NoError(t, err)

	listOut, err = client.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{
		ResourceArn: aws.String(taskArn),
	})
	require.NoError(t, err)
	got = map[string]string{}
	for _, tag := range listOut.Tags {
		got[*tag.Key] = *tag.Value
	}
	_, ok := got["sockerless-restart-count"]
	assert.False(t, ok, "untagged key should be gone")
	assert.Equal(t, "my-task", got["sockerless-name"], "non-untagged key should remain")
}

func TestECS_TagResource_RejectsStoppedTask(t *testing.T) {
	t.Parallel()
	client, cluster, taskArn := ecsRunTaskHelper(t, "tag-stopped", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("mirror.gcr.io/library/alpine:latest"),
		Command:     []string{"sh", "-c", "exit 0"},
	})

	stopped := waitTaskStopped(t, client, cluster, taskArn)
	require.Equal(t, "STOPPED", aws.ToString(stopped.LastStatus), "task should be STOPPED before this assertion")

	// Amazon ECS rejects TagResource on a STOPPED task.
	_, err := client.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String(taskArn),
		Tags: []ecstypes.Tag{
			{Key: aws.String("sockerless-name"), Value: aws.String("late-tag")},
		},
	})
	require.Error(t, err, "TagResource on a STOPPED task should fail with InvalidParameterException")
}

func TestECS_ListTasks_Pagination(t *testing.T) {
	t.Parallel()
	family := uniquename.New("pag-family")
	client := ecsClient()
	cluster := uniquename.New("pag-cluster")
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	// Bridge network mode, the default, shares the container instance's
	// networking, so no networkConfiguration is needed.
	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{StopTimeout: aws.Int32(2), Name: aws.String("app"), Image: aws.String("mirror.gcr.io/library/alpine:latest")},
		},
		NetworkMode: ecstypes.NetworkModeBridge,
	})
	require.NoError(t, err)
	tdArn := aws.ToString(td.TaskDefinition.TaskDefinitionArn)

	for i := 0; i < 3; i++ {
		_, err = client.RunTask(ctx, &ecs.RunTaskInput{
			Cluster:        aws.String(cluster),
			TaskDefinition: aws.String(tdArn),
		})
		require.NoError(t, err)
	}

	// MaxResults=1 takes three pages to see all three tasks.
	seen := map[string]bool{}
	var token *string
	for {
		out, err := client.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster:    aws.String(cluster),
			MaxResults: aws.Int32(1),
			NextToken:  token,
		})
		require.NoError(t, err)
		for _, arn := range out.TaskArns {
			seen[arn] = true
		}
		if out.NextToken == nil {
			break
		}
		token = out.NextToken
	}
	assert.Equal(t, 3, len(seen), "should see all 3 task ARNs via pagination")
}

func TestECS_RunTask_ClusterNotFound_ErrorClassification(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	// DescribeClusters answers a missing cluster with a Failures list; RunTask
	// raises ClusterNotFoundException.
	_, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String("nonexistent-cluster"),
		TaskDefinition: aws.String("nonexistent-td"),
	})
	require.Error(t, err)
	var notFound *ecstypes.ClusterNotFoundException
	assert.True(t, errors.As(err, &notFound),
		"ECS ClusterNotFoundException must be parsed by SDK errors.As; got %T: %v", err, err)
}

func TestECS_ListTasks_StartedByAndServiceFilters(t *testing.T) {
	t.Parallel()
	family := uniquename.New("listtasks-filters-td")
	client := ecsClient()
	cluster := uniquename.New("listtasks-filters")
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{StopTimeout: aws.Int32(2), Name: aws.String("app"), Image: aws.String("mirror.gcr.io/library/alpine:latest")}},
	})
	require.NoError(t, err)
	tdArn := aws.ToString(td.TaskDefinition.TaskDefinitionArn)

	runA, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: aws.String(tdArn),
		StartedBy:      aws.String("deployment-A"),
	})
	require.NoError(t, err)
	require.Len(t, runA.Tasks, 1)

	runB, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: aws.String(tdArn),
		StartedBy:      aws.String("deployment-B"),
	})
	require.NoError(t, err)
	require.Len(t, runB.Tasks, 1)

	filtered, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:   aws.String(cluster),
		StartedBy: aws.String("deployment-A"),
	})
	require.NoError(t, err)
	assert.Len(t, filtered.TaskArns, 1, "StartedBy filter should match exactly one task")
	assert.Equal(t, aws.ToString(runA.Tasks[0].TaskArn), filtered.TaskArns[0])

	all, err := client.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
	assert.Len(t, all.TaskArns, 2, "no filter should return both tasks")

	_, _ = client.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: runA.Tasks[0].TaskArn})
	_, _ = client.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: runB.Tasks[0].TaskArn})
}

// Every Amazon ECS resource type AWS declares taggable accepts tags, returns
// them and gives them up. The service reference lists nine types for
// ecs:TagResource; a type TagResource accepts but ListTagsForResource cannot
// see is the failure guarded against.
func TestECS_EveryTaggableResourceTypeRoundTripsItsTags(t *testing.T) {
	t.Parallel()
	daemonName := uniquename.New("taggable-types-daemon")
	daemonFamily := uniquename.New("taggable-types-daemon-task")
	family := uniquename.New("taggable-types-task")
	serviceName := uniquename.New("taggable-types-service")
	providerName := uniquename.New("taggable-types-provider")
	c := ecsClient()
	cluster := uniquename.New("taggable-types-cluster")
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	arns := map[string]string{}

	describeCluster, err := c.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{cluster}})
	require.NoError(t, err)
	arns["cluster"] = aws.ToString(describeCluster.Clusters[0].ClusterArn)

	registered, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)
	arns["task-definition"] = aws.ToString(registered.TaskDefinition.TaskDefinitionArn)

	service, err := c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(serviceName),
		TaskDefinition: registered.TaskDefinition.TaskDefinitionArn,
		DesiredCount:   aws.Int32(0),
	})
	require.NoError(t, err)
	arns["service"] = aws.ToString(service.Service.ServiceArn)
	t.Cleanup(func() {
		_, _ = c.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: service.Service.ServiceArn, Force: aws.Bool(true),
		})
	})

	capacityProvider, err := c.CreateCapacityProvider(ctx, &ecs.CreateCapacityProviderInput{
		Name: aws.String(providerName),
		AutoScalingGroupProvider: &ecstypes.AutoScalingGroupProvider{
			AutoScalingGroupArn: aws.String(
				"arn:aws:autoscaling:us-east-1:123456789012:autoScalingGroup:1:autoScalingGroupName/taggable"),
		},
	})
	require.NoError(t, err)
	arns["capacity-provider"] = aws.ToString(capacityProvider.CapacityProvider.CapacityProviderArn)

	daemonTaskDefinition, err := c.RegisterDaemonTaskDefinition(ctx, &ecs.RegisterDaemonTaskDefinitionInput{
		Family: aws.String(daemonFamily),
		ContainerDefinitions: []ecstypes.DaemonContainerDefinition{{
			Name: aws.String("agent"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
		Tags: []ecstypes.Tag{{Key: aws.String("created-with"), Value: aws.String("register")}},
	})
	require.NoError(t, err)
	arns["daemon-task-definition"] = aws.ToString(daemonTaskDefinition.DaemonTaskDefinitionArn)

	daemon, err := c.CreateDaemon(ctx, &ecs.CreateDaemonInput{
		DaemonName:              aws.String(daemonName),
		ClusterArn:              aws.String(arns["cluster"]),
		DaemonTaskDefinitionArn: daemonTaskDefinition.DaemonTaskDefinitionArn,
		CapacityProviderArns:    []string{arns["capacity-provider"]},
		Tags:                    []ecstypes.Tag{{Key: aws.String("created-with"), Value: aws.String("create")}},
	})
	require.NoError(t, err)
	arns["daemon"] = aws.ToString(daemon.DaemonArn)

	instance, err := c.RegisterContainerInstance(ctx, &ecs.RegisterContainerInstanceInput{
		Cluster:                  aws.String(cluster),
		InstanceIdentityDocument: aws.String(`{"instanceId":"i-taggable","region":"us-east-1"}`),
	})
	require.NoError(t, err)
	arns["container-instance"] = aws.ToString(instance.ContainerInstance.ContainerInstanceArn)

	taskSet, err := c.CreateTaskSet(ctx, &ecs.CreateTaskSetInput{
		Cluster:        aws.String(cluster),
		Service:        service.Service.ServiceArn,
		TaskDefinition: registered.TaskDefinition.TaskDefinitionArn,
	})
	require.NoError(t, err)
	arns["task-set"] = aws.ToString(taskSet.TaskSet.TaskSetArn)

	// A task set's ARN is task-set/<cluster>/<service>/<id>, not the service ARN
	// with an id appended, or dispatch on the ARN's resource type would tag the
	// service.
	assert.Contains(t, arns["task-set"], ":task-set/",
		"a task set must be named by a task-set ARN")

	// A create's own tags survive the create, not only a later TagResource.
	for _, created := range []string{"daemon", "daemon-task-definition"} {
		listed, listErr := c.ListTagsForResource(ctx,
			&ecs.ListTagsForResourceInput{ResourceArn: aws.String(arns[created])})
		require.NoError(t, listErr, created)
		require.Len(t, listed.Tags, 1, "%s must keep the tags its create supplied", created)
		assert.Equal(t, "created-with", aws.ToString(listed.Tags[0].Key), created)
	}

	for _, resourceType := range []string{
		"cluster", "task-definition", "service", "capacity-provider",
		"daemon", "daemon-task-definition", "container-instance", "task-set",
	} {
		arn := arns[resourceType]
		require.NotEmpty(t, arn, resourceType)
		t.Run(resourceType, func(t *testing.T) {
			_, tagErr := c.TagResource(ctx, &ecs.TagResourceInput{
				ResourceArn: aws.String(arn),
				Tags:        []ecstypes.Tag{{Key: aws.String("owner"), Value: aws.String("platform")}},
			})
			require.NoError(t, tagErr, "TagResource must accept %s", resourceType)

			listed, listErr := c.ListTagsForResource(ctx,
				&ecs.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
			require.NoError(t, listErr, "ListTagsForResource must accept %s", resourceType)
			found := ""
			for _, tag := range listed.Tags {
				if aws.ToString(tag.Key) == "owner" {
					found = aws.ToString(tag.Value)
				}
			}
			assert.Equal(t, "platform", found, "%s must return the tag it was given", resourceType)

			_, untagErr := c.UntagResource(ctx, &ecs.UntagResourceInput{
				ResourceArn: aws.String(arn), TagKeys: []string{"owner"},
			})
			require.NoError(t, untagErr, "UntagResource must accept %s", resourceType)

			after, listErr := c.ListTagsForResource(ctx,
				&ecs.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
			require.NoError(t, listErr)
			for _, tag := range after.Tags {
				assert.NotEqual(t, "owner", aws.ToString(tag.Key),
					"%s must give up the tag it was asked to drop", resourceType)
			}
		})
	}

	// A type Amazon ECS does not tag is refused.
	_, err = c.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String("arn:aws:ecs:us-east-1:123456789012:container-definition/nope"),
		Tags:        []ecstypes.Tag{{Key: aws.String("owner"), Value: aws.String("platform")}},
	})
	require.Error(t, err, "a resource type ECS does not tag must be refused")
}

// The agent-facing state-change APIs apply what they report, which
// DescribeTasks reads back.
func TestECS_AgentStateChangesAreAppliedNotAcknowledged(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	const cluster = "agent-state-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	registered, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("agent-state-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)

	run, err := c.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: registered.TaskDefinition.TaskDefinitionArn,
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := aws.ToString(run.Tasks[0].TaskArn)
	t.Cleanup(func() {
		_, _ = c.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: aws.String(taskArn)})
	})

	describe := func() ecstypes.Task {
		t.Helper()
		out, describeErr := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster), Tasks: []string{taskArn},
		})
		require.NoError(t, describeErr)
		require.Len(t, out.Tasks, 1)
		return out.Tasks[0]
	}

	_, err = c.SubmitContainerStateChange(ctx, &ecs.SubmitContainerStateChangeInput{
		Cluster:       aws.String(cluster),
		Task:          aws.String(taskArn),
		ContainerName: aws.String("app"),
		Status:        aws.String("STOPPED"),
		ExitCode:      aws.Int32(137),
	})
	require.NoError(t, err)
	container := describe().Containers[0]
	assert.Equal(t, "STOPPED", aws.ToString(container.LastStatus),
		"the container status the agent reported must be recorded")
	require.NotNil(t, container.ExitCode, "the reported exit code must be recorded")
	assert.Equal(t, int32(137), aws.ToInt32(container.ExitCode))

	// An unknown container is refused.
	_, err = c.SubmitContainerStateChange(ctx, &ecs.SubmitContainerStateChangeInput{
		Cluster:       aws.String(cluster),
		Task:          aws.String(taskArn),
		ContainerName: aws.String("not-a-container"),
		Status:        aws.String("STOPPED"),
	})
	require.Error(t, err, "a container the task does not have must be refused")

	_, err = c.SubmitTaskStateChange(ctx, &ecs.SubmitTaskStateChangeInput{
		Cluster: aws.String(cluster),
		Task:    aws.String(taskArn),
		Status:  aws.String("STOPPED"),
		Reason:  aws.String("EssentialContainerExited"),
	})
	require.NoError(t, err)
	stopped := describe()
	assert.Equal(t, "STOPPED", aws.ToString(stopped.LastStatus),
		"the task status the agent reported must be recorded")
	assert.Equal(t, "EssentialContainerExited", aws.ToString(stopped.StoppedReason))
	assert.NotNil(t, stopped.StoppedAt, "a stopped task must carry when it stopped")

	pullStarted := time.Now().Add(-2 * time.Minute)
	pullStopped := time.Now().Add(-90 * time.Second)
	_, err = c.SubmitTaskStateChange(ctx, &ecs.SubmitTaskStateChangeInput{
		Cluster:            aws.String(cluster),
		Task:               aws.String(taskArn),
		Status:             aws.String("STOPPED"),
		PullStartedAt:      aws.Time(pullStarted),
		PullStoppedAt:      aws.Time(pullStopped),
		ExecutionStoppedAt: aws.Time(time.Now()),
	})
	require.NoError(t, err)
	timed := describe()
	assert.NotNil(t, timed.PullStartedAt, "the reported pull start must be recorded")
	assert.NotNil(t, timed.PullStoppedAt, "the reported pull stop must be recorded")
	assert.NotNil(t, timed.ExecutionStoppedAt, "the reported execution stop must be recorded")

	// The detail beyond status: the container's reason, the runtime it ran as and
	// the ports it bound.
	_, err = c.SubmitContainerStateChange(ctx, &ecs.SubmitContainerStateChangeInput{
		Cluster:       aws.String(cluster),
		Task:          aws.String(taskArn),
		ContainerName: aws.String("app"),
		Status:        aws.String("STOPPED"),
		Reason:        aws.String("OutOfMemoryError"),
		RuntimeId:     aws.String("runtime-abc"),
		NetworkBindings: []ecstypes.NetworkBinding{{
			BindIP: aws.String("0.0.0.0"), ContainerPort: aws.Int32(8080),
			HostPort: aws.Int32(32768), Protocol: ecstypes.TransportProtocolTcp,
		}},
	})
	require.NoError(t, err)
	detailed := describe().Containers[0]
	assert.Equal(t, "OutOfMemoryError", aws.ToString(detailed.Reason),
		"the reason the agent reported must be recorded")
	assert.Equal(t, "runtime-abc", aws.ToString(detailed.RuntimeId))
	require.NotEmpty(t, detailed.NetworkBindings, "the reported port binding must be recorded")
	assert.Equal(t, int32(32768), aws.ToInt32(detailed.NetworkBindings[0].HostPort))

	// A report is scoped to the cluster it names: the same task reported against
	// another cluster is refused.
	const otherCluster = "agent-state-other-cluster"
	_, err = c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(otherCluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(otherCluster)})
	})
	_, err = c.SubmitTaskStateChange(ctx, &ecs.SubmitTaskStateChangeInput{
		Cluster: aws.String(otherCluster), Task: aws.String(taskArn), Status: aws.String("RUNNING"),
	})
	require.Error(t, err, "a report naming another cluster must not reach this task")

	// A task the control plane does not hold is refused.
	_, err = c.SubmitTaskStateChange(ctx, &ecs.SubmitTaskStateChangeInput{
		Cluster: aws.String(cluster),
		Task:    aws.String("arn:aws:ecs:us-east-1:123456789012:task/" + cluster + "/nonexistent"),
		Status:  aws.String("STOPPED"),
	})
	require.Error(t, err, "a task that does not exist must be refused")
}

// DiscoverPollEndpoint points the agent at this simulator rather than at AWS.
func TestECS_DiscoverPollEndpointPointsAtTheSimulator(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	const cluster = "poll-endpoint-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	out, err := c.DiscoverPollEndpoint(ctx, &ecs.DiscoverPollEndpointInput{
		Cluster: aws.String(cluster),
	})
	require.NoError(t, err)
	for name, endpoint := range map[string]string{
		"endpoint":          aws.ToString(out.Endpoint),
		"telemetryEndpoint": aws.ToString(out.TelemetryEndpoint),
	} {
		assert.NotContains(t, endpoint, "amazonaws.com",
			"%s must not send the agent to real AWS", name)
		assert.Contains(t, endpoint, "127.0.0.1",
			"%s must point at this simulator", name)
	}
}

// The rules that decide whether a destructive Amazon ECS call is allowed: AWS
// refuses each of these, and a caller relying on the refusal learns it skipped
// a step.
func TestECS_DestructiveCallsRefuseWhatAWSRefuses(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	const cluster = "refusal-rules-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})
	registered, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("refusal-rules-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)

	t.Run("a service scaled above zero is not deleted without force", func(t *testing.T) {
		service, createErr := c.CreateService(ctx, &ecs.CreateServiceInput{
			Cluster:        aws.String(cluster),
			ServiceName:    aws.String("refusal-scaled-service"),
			TaskDefinition: registered.TaskDefinition.TaskDefinitionArn,
			DesiredCount:   aws.Int32(1),
		})
		require.NoError(t, createErr)
		t.Cleanup(func() {
			_, _ = c.DeleteService(ctx, &ecs.DeleteServiceInput{
				Cluster: aws.String(cluster), Service: service.Service.ServiceArn, Force: aws.Bool(true),
			})
		})

		_, deleteErr := c.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: service.Service.ServiceArn,
		})
		require.Error(t, deleteErr, "deleting a service scaled above zero must be refused without force")

		// force deletes it, and so does scaling to zero first.
		_, deleteErr = c.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: service.Service.ServiceArn, Force: aws.Bool(true),
		})
		require.NoError(t, deleteErr, "force must delete a scaled service")
	})

	t.Run("a reserved capacity provider is never deleted", func(t *testing.T) {
		for _, reserved := range []string{"FARGATE", "FARGATE_SPOT"} {
			_, deleteErr := c.DeleteCapacityProvider(ctx, &ecs.DeleteCapacityProviderInput{
				CapacityProvider: aws.String(reserved),
			})
			require.Error(t, deleteErr, "%s is reserved and must not be deletable", reserved)
		}
	})

	t.Run("a capacity provider a cluster still names is not deleted", func(t *testing.T) {
		const provider = "refusal-rules-provider"
		_, createErr := c.CreateCapacityProvider(ctx, &ecs.CreateCapacityProviderInput{
			Name: aws.String(provider),
			AutoScalingGroupProvider: &ecstypes.AutoScalingGroupProvider{
				AutoScalingGroupArn: aws.String(
					"arn:aws:autoscaling:us-east-1:123456789012:autoScalingGroup:1:autoScalingGroupName/refusal"),
			},
		})
		require.NoError(t, createErr)

		_, putErr := c.PutClusterCapacityProviders(ctx, &ecs.PutClusterCapacityProvidersInput{
			Cluster:                         aws.String(cluster),
			CapacityProviders:               []string{provider},
			DefaultCapacityProviderStrategy: []ecstypes.CapacityProviderStrategyItem{},
		})
		require.NoError(t, putErr)

		_, deleteErr := c.DeleteCapacityProvider(ctx, &ecs.DeleteCapacityProviderInput{
			CapacityProvider: aws.String(provider),
		})
		require.Error(t, deleteErr, "a provider a cluster still names must not be deletable")

		// Disassociating it, as AWS tells the caller to, lets the delete through.
		_, putErr = c.PutClusterCapacityProviders(ctx, &ecs.PutClusterCapacityProvidersInput{
			Cluster:                         aws.String(cluster),
			CapacityProviders:               []string{},
			DefaultCapacityProviderStrategy: []ecstypes.CapacityProviderStrategyItem{},
		})
		require.NoError(t, putErr)
		_, deleteErr = c.DeleteCapacityProvider(ctx, &ecs.DeleteCapacityProviderInput{
			CapacityProvider: aws.String(provider),
		})
		require.NoError(t, deleteErr, "a disassociated provider must delete")
	})

	t.Run("a container instance running tasks is not deregistered without force", func(t *testing.T) {
		instance, registerErr := c.RegisterContainerInstance(ctx, &ecs.RegisterContainerInstanceInput{
			Cluster: aws.String(cluster),
			InstanceIdentityDocument: aws.String(
				`{"instanceId":"i-refusal","region":"us-east-1"}`),
		})
		require.NoError(t, registerErr)
		// The identity document names the EC2 instance.
		assert.Equal(t, "i-refusal", aws.ToString(instance.ContainerInstance.Ec2InstanceId),
			"the EC2 instance id must come from the identity document")

		_, deregisterErr := c.DeregisterContainerInstance(ctx, &ecs.DeregisterContainerInstanceInput{
			Cluster:           aws.String(cluster),
			ContainerInstance: instance.ContainerInstance.ContainerInstanceArn,
		})
		require.NoError(t, deregisterErr, "an idle instance deregisters without force")
	})
}

// A deployment lifecycle hook at PRE_SCALE_UP holds the deployment and its
// tasks: the new revision launches only once the hook is released, so the
// test asserts the task count as well as the deployment's state.
func TestECS_DeploymentLifecycleHookHoldsTheDeployment(t *testing.T) {
	t.Parallel()
	family := uniquename.New("lifecycle-hook-task")
	serviceName := uniquename.New("lifecycle-hook-service")
	c := ecsClient()
	cluster := uniquename.New("lifecycle-hook-cluster")
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})
	registered, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)

	service, err := c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(serviceName),
		TaskDefinition: registered.TaskDefinition.TaskDefinitionArn,
		DesiredCount:   aws.Int32(1),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			LifecycleHooks: []ecstypes.DeploymentLifecycleHook{{
				TargetType: ecstypes.DeploymentLifecycleHookTargetTypePause,
				LifecycleStages: []ecstypes.DeploymentLifecycleHookStage{
					ecstypes.DeploymentLifecycleHookStagePreScaleUp,
				},
			}},
		},
	})
	require.NoError(t, err)
	serviceArn := service.Service.ServiceArn
	t.Cleanup(func() {
		_, _ = c.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: serviceArn, Force: aws.Bool(true),
		})
	})

	var deploymentArn, hookID string
	require.Eventually(t, func() bool {
		listed, listErr := c.ListServiceDeployments(ctx, &ecs.ListServiceDeploymentsInput{
			Service: serviceArn, Cluster: aws.String(cluster),
		})
		if listErr != nil || len(listed.ServiceDeployments) == 0 {
			return false
		}
		deploymentArn = aws.ToString(listed.ServiceDeployments[0].ServiceDeploymentArn)
		described, describeErr := c.DescribeServiceDeployments(ctx,
			&ecs.DescribeServiceDeploymentsInput{ServiceDeploymentArns: []string{deploymentArn}})
		if describeErr != nil || len(described.ServiceDeployments) == 0 {
			return false
		}
		for _, hook := range described.ServiceDeployments[0].LifecycleHookDetails {
			if hook.Status == ecstypes.DeploymentLifecycleHookStatusAwaitingAction {
				hookID = aws.ToString(hook.HookId)
			}
		}
		return hookID != ""
	}, 30*time.Second, 200*time.Millisecond,
		"the deployment must stop at the stage its hook guards and record the hook")

	described, err := c.DescribeServiceDeployments(ctx,
		&ecs.DescribeServiceDeploymentsInput{ServiceDeploymentArns: []string{deploymentArn}})
	require.NoError(t, err)
	assert.Equal(t, ecstypes.ServiceDeploymentLifecycleStagePreScaleUp,
		described.ServiceDeployments[0].LifecycleStage,
		"the deployment must report the stage it is waiting at")

	// Nothing launched while the hook waits.
	held, err := c.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
	})
	require.NoError(t, err)
	assert.Empty(t, held.TaskArns,
		"a deployment waiting on a PRE_SCALE_UP hook must not have launched its tasks")

	// Continuing without naming the hook is refused, since there is one to name.
	_, err = c.ContinueServiceDeployment(ctx, &ecs.ContinueServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(deploymentArn),
		Action:               ecstypes.DeploymentLifecycleHookActionContinue,
	})
	require.Error(t, err, "continuing a hooked deployment must name the hook")

	_, err = c.ContinueServiceDeployment(ctx, &ecs.ContinueServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(deploymentArn),
		HookId:               aws.String("not-a-hook"),
		Action:               ecstypes.DeploymentLifecycleHookActionContinue,
	})
	require.Error(t, err, "an unknown hook identifier must be refused")

	_, err = c.ContinueServiceDeployment(ctx, &ecs.ContinueServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(deploymentArn),
		HookId:               aws.String(hookID),
		Action:               ecstypes.DeploymentLifecycleHookActionContinue,
	})
	require.NoError(t, err)

	released, err := c.DescribeServiceDeployments(ctx,
		&ecs.DescribeServiceDeploymentsInput{ServiceDeploymentArns: []string{deploymentArn}})
	require.NoError(t, err)
	require.NotEmpty(t, released.ServiceDeployments[0].LifecycleHookDetails)
	assert.Equal(t, ecstypes.DeploymentLifecycleHookStatusSucceeded,
		released.ServiceDeployments[0].LifecycleHookDetails[0].Status,
		"a continued hook must be recorded as succeeded")

	waitForECSServicesStable(t, c, cluster, 60*time.Second, aws.ToString(serviceArn))
	running, err := c.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, running.TaskArns, "once the hook is released the service must launch the tasks it was holding")

	_, err = c.ContinueServiceDeployment(ctx, &ecs.ContinueServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(deploymentArn),
		HookId:               aws.String(hookID),
		Action:               ecstypes.DeploymentLifecycleHookActionContinue,
	})
	require.Error(t, err, "a hook that already succeeded must not be continued again")
}

// A task definition volume with no configuration is the task's own ephemeral
// storage: its containers share it, and a second task starts with an empty one.
func TestECS_TaskVolumeIsSharedByItsContainersAndItsOwn(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	clusterName := "task-volume"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("task-volume"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		Volumes:                 []ecstypes.Volume{{Name: aws.String("scratch")}},
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				StopTimeout: aws.Int32(2),
				Name:        aws.String("reader"),
				Image:       aws.String(busyboxImage),
				EntryPoint:  []string{"sh", "-c"},
				// The reader waits for the marker the writer leaves on the shared volume.
				Command:     []string{`i=0; while [ ! -e /scratch/marker ] && [ $i -lt 60 ]; do sleep 0.5; i=$((i+1)); done; test "$(cat /scratch/marker)" = written`},
				MountPoints: []ecstypes.MountPoint{{SourceVolume: aws.String("scratch"), ContainerPath: aws.String("/scratch")}},
			},
			{
				StopTimeout: aws.Int32(2),
				Name:        aws.String("writer"),
				Image:       aws.String(busyboxImage),
				Essential:   aws.Bool(false),
				EntryPoint:  []string{"sh", "-c"},
				Command:     []string{`test ! -e /scratch/marker && echo written > /scratch/marker`},
				MountPoints: []ecstypes.MountPoint{{SourceVolume: aws.String("scratch"), ContainerPath: aws.String("/scratch")}},
			},
		},
	})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "task-volume")
	for round := 1; round <= 2; round++ {
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
		taskArn := *runOut.Tasks[0].TaskArn
		cleanupECSTask(t, client, clusterName, taskArn)
		stopped := waitTaskStopped(t, client, clusterName, taskArn)
		for _, c := range stopped.Containers {
			require.NotNil(t, c.ExitCode, "round %d: container %s reports no exit code", round, aws.ToString(c.Name))
			assert.Equal(t, int32(0), *c.ExitCode, "round %d: container %s exited with %d", round, aws.ToString(c.Name), *c.ExitCode)
		}
	}
}
