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
	client := ecsClient()
	out, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String("test-cluster"),
	})
	require.NoError(t, err)
	assert.Equal(t, "test-cluster", *out.Cluster.ClusterName)
	assert.Contains(t, *out.Cluster.ClusterArn, "test-cluster")
}

func TestECS_DescribeClusters(t *testing.T) {
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

// TestECS_ServiceLifecycle pins the ECS Service family
// (CreateService/DescribeServices/ListServices/UpdateService/DeleteService) and
// PutClusterCapacityProviders must round-trip a Fargate service through its
// control-plane state machine. Pre-fix every one of these returned
// UnknownOperationException, so aws_ecs_service / aws_ecs_cluster_capacity_providers
// could not apply.
func TestECS_ServiceLifecycle(t *testing.T) {
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

	// Cluster capacity providers (aws_ecs_cluster_capacity_providers).
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

	// CreateService begins placement of two real task-definition workloads.
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

	// DescribeServices + ListServices.
	descSvc, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(cluster), Services: []string{"control-plane"},
	})
	require.NoError(t, err)
	require.Len(t, descSvc.Services, 1)
	assert.Equal(t, "ACTIVE", aws.ToString(descSvc.Services[0].Status))

	listOut, err := c.ListServices(ctx, &ecs.ListServicesInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
	require.Len(t, listOut.ServiceArns, 1)

	// UpdateService scales the real workload to three tasks.
	updOut, err := c.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String("control-plane"), DesiredCount: aws.Int32(3),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, updOut.Service.DesiredCount)
	scaled := waitForECSServicesStable(t, c, cluster, 30*time.Second, "control-plane")
	assert.EqualValues(t, 3, scaled.Services[0].RunningCount)
	assert.EqualValues(t, 0, scaled.Services[0].PendingCount)

	// DeleteService drains the service's tasks, then settles it INACTIVE.
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

// TestECS_TagsAndListOps covers tagging clusters and services (previously
// errored with "tag-target type not implemented"), plus ListClusters /
// ListTaskDefinitions / ListServices.
func TestECS_TagsAndListOps(t *testing.T) {
	c := ecsClient()
	cluster := "tag-ops-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(cluster),
		Tags:        []ecstypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	// Cluster ARN.
	descCl, err := c.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{cluster}})
	require.NoError(t, err)
	clusterArn := aws.ToString(descCl.Clusters[0].ClusterArn)

	// CreateCluster tags must round-trip via ListTagsForResource.
	lt, err := c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(clusterArn)})
	require.NoError(t, err)
	require.Len(t, lt.Tags, 1)
	assert.Equal(t, "env", aws.ToString(lt.Tags[0].Key))

	// TagResource on the cluster.
	_, err = c.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String(clusterArn),
		Tags:        []ecstypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)
	lt, err = c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(clusterArn)})
	require.NoError(t, err)
	assert.Len(t, lt.Tags, 2)

	// Service with tags.
	_, err = c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("tag-svc-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)
	svcOut, err := c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster: aws.String(cluster), ServiceName: aws.String("tagged-svc"),
		TaskDefinition: aws.String("tag-svc-task"), DesiredCount: aws.Int32(1),
		Tags: []ecstypes.Tag{{Key: aws.String("svc"), Value: aws.String("yes")}},
	})
	require.NoError(t, err)
	cleanupECSService(t, c, cluster, "tagged-svc")
	svcArn := aws.ToString(svcOut.Service.ServiceArn)
	lt, err = c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(svcArn)})
	require.NoError(t, err)
	require.Len(t, lt.Tags, 1)
	assert.Equal(t, "svc", aws.ToString(lt.Tags[0].Key))

	// UntagResource on the service.
	_, err = c.UntagResource(ctx, &ecs.UntagResourceInput{ResourceArn: aws.String(svcArn), TagKeys: []string{"svc"}})
	require.NoError(t, err)
	lt, err = c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(svcArn)})
	require.NoError(t, err)
	assert.Empty(t, lt.Tags)

	// List ops.
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
	client := ecsClient()
	family := uniqueName("test-task")
	out, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				StopTimeout: aws.Int32(2),
				Name:        aws.String("app"),
				Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, family, *out.TaskDefinition.Family)
	assert.Equal(t, int32(1), out.TaskDefinition.Revision)
}

func TestECS_MultiContainerTaskSharesLocalhost(t *testing.T) {
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
				RoleArn:    aws.String("arn:aws:iam::123456789012:role/ecsInfrastructureRole"),
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
				RoleArn:    aws.String("arn:aws:iam::123456789012:role/ecsInfrastructureRole"),
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

// A managed EBS volume is a block device of the size the task asked for,
// formatted with the filesystem it named: the workload sees that capacity and
// runs out of space where EBS would.
func TestECS_ManagedEBSVolumeHasItsOwnSizeAndFilesystemSDK(t *testing.T) {
	client := ecsClient()
	cw := cwLogsClient()

	clusterName := "managed-ebs-capacity"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "managed-ebs-capacity")

	logGroupName := "/ecs/managed-ebs-capacity"
	_, _ = cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroupName)})
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
	})

	tdOut, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("managed-ebs-capacity"),
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
			// A 1 GiB volume holds a little under 1 GiB once formatted, and a
			// 1.2 GiB write has to stop short.
			Command: []string{`set -e
total_kb=$(df -k /data | tail -1 | awk '{print $2}')
echo "total_kb=$total_kb"
test "$total_kb" -gt 900000
test "$total_kb" -le 1048576
if dd if=/dev/zero of=/data/fill bs=1M count=1200 2>/dev/null; then echo "wrote past the volume"; exit 1; fi
echo EBS_CAPACITY_OK`},
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
				RoleArn:        aws.String("arn:aws:iam::123456789012:role/ecsInfrastructureRole"),
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
	require.Contains(t, strings.Join(messages, "\n"), "EBS_CAPACITY_OK",
		"stopped: %s; output: %s", aws.ToString(task.StoppedReason), strings.Join(messages, " | "))

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
				RoleArn:        aws.String("arn:aws:iam::123456789012:role/ecsInfrastructureRole"),
				SizeInGiB:      aws.Int32(1),
				FilesystemType: ecstypes.TaskFilesystemType("btrfs"),
			},
		}},
	})
	var invalid *ecstypes.InvalidParameterException
	require.ErrorAs(t, err, &invalid)
}

func TestECS_RunTaskContainerOverridesApplyToRuntimeSDK(t *testing.T) {
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
			Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
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
	client := ecsClient()

	// Setup: cluster + task definition
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
				Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
				Command:     []string{"sleep", "30"}, // long-running so RUNNING window is real
			},
		},
	})
	require.NoError(t, err)

	// Run task
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

	// Describe task while RUNNING — ExitCode should be nil
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

	// Stop task explicitly (real ECS has no task timeout — tasks run until stopped)
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
		// A user-initiated stop SIGKILLs the container → 137 (128+SIGKILL),
		// the code real Fargate reports, not a clean-exit 0.
		assert.Equal(t, int32(137), *c.ExitCode)
	}
}

func TestECS_StopCodeUserInitiated(t *testing.T) {
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
				Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
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

	// Amazon ECS answers StopTask at once, with the task still running and
	// asked to stop, and gives the container its stop timeout afterwards. The
	// simulator used to wait out the timeout before it answered.
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

// ecsRunTaskHelper creates a cluster, registers a task definition, and runs a task.
// Returns the ECS client, cluster name, and task ARN.
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

func createECSTestSubnet(t *testing.T, name string) string {
	t.Helper()
	_, subnetID := createECSTestVPCSubnet(t, name)
	return subnetID
}

func createECSTestVPCSubnet(t *testing.T, name string) (string, string) {
	t.Helper()
	ec2c := ec2Client()
	// Keep ECS helper VPCs in 10.225.0.0/16 through 10.249.0.0/16, clear of
	// the fixed CIDRs other SDK tests use (which occupy ranges through
	// 10.224.0.0/16 and resume at 10.250.0.0/16). A VPC CIDR never reaches
	// the host — bridge subnets come from the reserved allocator pool — but
	// distinct ranges keep every concurrently running task's ENI address
	// unique, so address-keyed assertions cannot cross-match. Cleanup below
	// makes reuse after wrapping safe even though StopTask releases
	// containers asynchronously.
	n := ecsTestSubnetCounter.Add(1)
	second := 225 + int(n%25)
	third := int((n / 100) % 200)
	vpcCIDR := fmt.Sprintf("10.%d.0.0/16", second)
	subnetCIDR := fmt.Sprintf("10.%d.%d.0/24", second, third)

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

	subnetOut, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     aws.String(vpcID),
		CidrBlock: aws.String(subnetCIDR),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSubnet,
			Tags: []ec2types.Tag{{
				Key:   aws.String("Name"),
				Value: aws.String(name + "-subnet"),
			}},
		}},
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnetOut.Subnet.SubnetId)

	t.Cleanup(func() {
		stopECSTasksInSubnet(t, ecsClient(), subnetID)
		_, err := ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetID)})
		require.NoError(t, err, "delete ECS test subnet %s once its tasks stopped", subnetID)
		_, err = ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
		require.NoError(t, err, "delete ECS test VPC %s", vpcID)
	})
	return vpcID, subnetID
}

// stopECSTasksInSubnet stops every Amazon ECS task whose network interface is
// in the subnet and waits until each has stopped: EC2 refuses DeleteSubnet
// with DependencyViolation while a task's interface is still in it.
func stopECSTasksInSubnet(t *testing.T, client *ecs.Client, subnetID string) {
	t.Helper()
	stopECSTasks(t, client, func(task ecstypes.Task) bool { return ecsTaskInSubnet(task, subnetID) })
}

// stopECSTasks stops every Amazon ECS task that match selects and waits with
// the SDK's TasksStopped waiter until each has stopped.
func stopECSTasks(t *testing.T, client *ecs.Client, match func(ecstypes.Task) bool) {
	t.Helper()
	clusters, err := client.ListClusters(ctx, &ecs.ListClustersInput{})
	require.NoError(t, err)
	for _, cluster := range clusters.ClusterArns {
		var taskArns []string
		for _, desired := range []ecstypes.DesiredStatus{ecstypes.DesiredStatusRunning, ecstypes.DesiredStatusStopped} {
			pages := ecs.NewListTasksPaginator(client, &ecs.ListTasksInput{Cluster: aws.String(cluster), DesiredStatus: desired})
			for pages.HasMorePages() {
				page, err := pages.NextPage(ctx)
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

// waitForECSTaskStatus waits for RUNNING or STOPPED with the SDK's own
// TasksRunning or TasksStopped waiter. TasksRunning fails at once when the task
// stops instead of running.
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

// waitForECSServicesStable waits with the SDK's ServicesStable waiter for
// steady state: one deployment per service, runningCount equal to
// desiredCount, and that deployment's rollout COMPLETED, which Amazon ECS
// records at the same moment ("when the service reaches a steady state, the
// deployment transitions to a COMPLETED state").
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
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-cmd", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
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

	// Poll until the task completes (start + command execution + STOPPED
	// transition all run asynchronously in the sim).
	task := waitTaskStopped(t, client, cluster, taskArn)
	require.NotEmpty(t, task.Containers)
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(0), *task.Containers[0].ExitCode, "stopped reason: %s", aws.ToString(task.StoppedReason))
}

func TestECS_TaskExitCodeNonZero(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-fail", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
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

// TestECS_TaskFailsWhenAwslogsGroupIsMissing proves the awslogs driver creates
// no log group unless awslogs-create-group asks for one: the task fails to
// start with a ResourceInitializationError instead.
func TestECS_TaskFailsWhenAwslogsGroupIsMissing(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-no-group", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
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
	logGroup := uniqueName("/ecs/exec-logs")
	client, clusterName, taskArn := ecsRunTaskHelper(t, "exec-logs", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
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

	// The stream's first event is the container's own first line. Amazon ECS
	// seeds nothing at RunTask time; a synthetic "container started" (or the
	// joined entrypoint) stamped then made the provisioning window read as the
	// entrypoint's silence and was text the container never wrote.
	require.Equal(t, "hello from process", messages[0],
		"the log stream must begin with the container's first line, not a simulator marker")

	// The pull window is reported on the task, so a slow start can be attributed
	// to the image pull rather than to the container: both timestamps are set
	// (even for a cached image), and they bracket the start in order.
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
	// At full precision: every task timestamp carries milliseconds, as on
	// Amazon ECS. Whole-second startedAt used to land before pullStoppedAt.
	require.False(t, task.StartedAt.Before(*task.PullStoppedAt), "the container cannot start before its image is present")
}

// TestECS_RunningTaskStreamsLogsLive proves the awslogs contract for a
// long-running task: the container's stdout reaches its CloudWatch log
// stream while the task is still RUNNING — real awslogs forwards each line
// as it is produced, so a service task is observable without stopping it —
// and the post-exit drain does not duplicate the lines the live stream
// already delivered.
func TestECS_RunningTaskStreamsLogsLive(t *testing.T) {
	logGroup := uniqueName("/ecs/live-logs")
	client, cluster, taskArn := ecsRunTaskHelper(t, "live-logs", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
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

	// The application line must reach CloudWatch while the task runs.
	waitForECSTasksRunning(t, client, cluster, 30*time.Second, taskArn)
	awaitLogLine(t, cw, logGroup, "live-line-from-running-task", 30*time.Second)

	// The task is still RUNNING at the moment the line is observable.
	descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Tasks, 1)
	require.Equal(t, "RUNNING", aws.ToString(descOut.Tasks[0].LastStatus),
		"the log line must be visible while the task is still RUNNING")

	// Stop the task; the post-exit drain must not re-append the lines the
	// live stream already delivered.
	_, err = client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(cluster),
		Task:    aws.String(taskArn),
		Reason:  aws.String("live-log streaming regression complete"),
	})
	require.NoError(t, err)
	// The post-exit drain has finished by the time the task reports STOPPED.
	waitTaskStopped(t, client, cluster, taskArn)
	assert.Equal(t, 1, countLiveLines(),
		"the post-exit drain must not duplicate lines the live stream delivered")
}

func TestECS_TaskNoCommandStaysRunning(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-nocmd", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
		Command:     []string{"tail", "-f", "/dev/null"}, // Long-running — stays RUNNING
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

// TagResource/UntagResource contract: tag a running task, list tags,
// untag, and confirm STOPPED tasks reject tagging.
func TestECS_TagResource_OnRunningTask(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "tag-task", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
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

	// Overwrite an existing key — merge-by-key semantics.
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

	// Untag one key.
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
	client, cluster, taskArn := ecsRunTaskHelper(t, "tag-stopped", ecstypes.ContainerDefinition{
		StopTimeout: aws.Int32(2),
		Name:        aws.String("app"),
		Image:       aws.String("public.ecr.aws/docker/library/alpine:latest"),
		Command:     []string{"sh", "-c", "exit 0"},
	})

	stopped := waitTaskStopped(t, client, cluster, taskArn)
	require.Equal(t, "STOPPED", aws.ToString(stopped.LastStatus), "task should be STOPPED before this assertion")

	// Real ECS rejects TagResource on STOPPED tasks; sim must too.
	_, err := client.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String(taskArn),
		Tags: []ecstypes.Tag{
			{Key: aws.String("sockerless-name"), Value: aws.String("late-tag")},
		},
	})
	require.Error(t, err, "TagResource on a STOPPED task should fail with InvalidParameterException")
}

func TestECS_ListTasks_Pagination(t *testing.T) {
	family := uniqueName("pag-family")
	client := ecsClient()
	cluster := uniqueName("pag-cluster")
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	// Bridge network mode (the default): the tasks share the container
	// instance's networking, so no networkConfiguration is needed to run them.
	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{StopTimeout: aws.Int32(2), Name: aws.String("app"), Image: aws.String("public.ecr.aws/docker/library/alpine:latest")},
		},
		NetworkMode: ecstypes.NetworkModeBridge,
	})
	require.NoError(t, err)
	tdArn := aws.ToString(td.TaskDefinition.TaskDefinitionArn)

	// Run 3 tasks.
	for i := 0; i < 3; i++ {
		_, err = client.RunTask(ctx, &ecs.RunTaskInput{
			Cluster:        aws.String(cluster),
			TaskDefinition: aws.String(tdArn),
		})
		require.NoError(t, err)
	}

	// Page with MaxResults=1 — should need 3 pages to see all tasks.
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
	client := ecsClient()
	// DescribeClusters returns 200 with a Failures list for missing clusters.
	// RunTask is the reliable path to ClusterNotFoundException.
	_, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String("nonexistent-cluster"),
		TaskDefinition: aws.String("nonexistent-td"),
	})
	require.Error(t, err)
	var notFound *ecstypes.ClusterNotFoundException
	assert.True(t, errors.As(err, &notFound),
		"ECS ClusterNotFoundException must be parsed by SDK errors.As; got %T: %v", err, err)
}

// TestECS_ListTasks_StartedByAndServiceFilters verifies that the StartedBy
// and ServiceName filters narrow ListTasks to matching tasks, matching real
// AWS which supports both filter dimensions.
func TestECS_ListTasks_StartedByAndServiceFilters(t *testing.T) {
	family := uniqueName("listtasks-filters-td")
	client := ecsClient()
	cluster := uniqueName("listtasks-filters")
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{StopTimeout: aws.Int32(2), Name: aws.String("app"), Image: aws.String("public.ecr.aws/docker/library/alpine:latest")}},
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

	// Filter by StartedBy=deployment-A → only the first task.
	filtered, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster:   aws.String(cluster),
		StartedBy: aws.String("deployment-A"),
	})
	require.NoError(t, err)
	assert.Len(t, filtered.TaskArns, 1, "StartedBy filter should match exactly one task")
	assert.Equal(t, aws.ToString(runA.Tasks[0].TaskArn), filtered.TaskArns[0])

	// No filter → both tasks.
	all, err := client.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
	assert.Len(t, all.TaskArns, 2, "no filter should return both tasks")

	_, _ = client.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: runA.Tasks[0].TaskArn})
	_, _ = client.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: runB.Tasks[0].TaskArn})
}

// Every Amazon ECS resource type AWS declares taggable accepts tags, returns
// them, and gives them up again.
//
// The service reference lists nine types for ecs:TagResource. Four were
// served; the other five answered "tag-target type not implemented in sim"
// even though the simulator holds every one of them. This drives the full set
// through the SDK, because a type that TagResource accepts and
// ListTagsForResource cannot see is the failure this is really guarding
// against.
func TestECS_EveryTaggableResourceTypeRoundTripsItsTags(t *testing.T) {
	daemonName := uniqueName("taggable-types-daemon")
	daemonFamily := uniqueName("taggable-types-daemon-task")
	family := uniqueName("taggable-types-task")
	serviceName := uniqueName("taggable-types-service")
	providerName := uniqueName("taggable-types-provider")
	c := ecsClient()
	cluster := uniqueName("taggable-types-cluster")
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	// One ARN per taggable type, each from the operation that creates it.
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

	// A task set's ARN is task-set/<cluster>/<service>/<id>, not the service's
	// own ARN with an id appended: anything dispatching on the resource type in
	// an ARN — this tagging path included — would otherwise read it as the
	// service and tag the wrong resource.
	assert.Contains(t, arns["task-set"], ":task-set/",
		"a task set must be named by a task-set ARN")

	// A create's own tags must survive the create, not just a later
	// TagResource: both of these accept a tags member and had been dropping it.
	for _, created := range []string{"daemon", "daemon-task-definition"} {
		listed, listErr := c.ListTagsForResource(ctx,
			&ecs.ListTagsForResourceInput{ResourceArn: aws.String(arns[created])})
		require.NoError(t, listErr, created)
		require.Len(t, listed.Tags, 1, "%s must keep the tags its create supplied", created)
		assert.Equal(t, "created-with", aws.ToString(listed.Tags[0].Key), created)
	}

	// Every type: tag, read back, untag, read back empty.
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

	// A type Amazon ECS does not tag is refused, rather than silently accepted.
	_, err = c.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: aws.String("arn:aws:ecs:us-east-1:123456789012:container-definition/nope"),
		Tags:        []ecstypes.Tag{{Key: aws.String("owner"), Value: aws.String("platform")}},
	})
	require.Error(t, err, "a resource type ECS does not tag must be refused")
}

// The agent-facing state-change APIs apply what they are told, rather than
// acknowledging it.
//
// SubmitTaskStateChange, SubmitContainerStateChange and
// SubmitAttachmentStateChanges each parsed their request, ignored every field
// and answered {"acknowledgment":"ACK"}. An agent reporting that a task had
// stopped therefore changed nothing, and DescribeTasks went on reporting
// whatever the scheduler last assumed. This drives each of them and reads the
// result back through DescribeTasks.
func TestECS_AgentStateChangesAreAppliedNotAcknowledged(t *testing.T) {
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

	// A container's reported exit code and status must reach DescribeTasks.
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

	// An unknown container is refused rather than silently acknowledged.
	_, err = c.SubmitContainerStateChange(ctx, &ecs.SubmitContainerStateChangeInput{
		Cluster:       aws.String(cluster),
		Task:          aws.String(taskArn),
		ContainerName: aws.String("not-a-container"),
		Status:        aws.String("STOPPED"),
	})
	require.Error(t, err, "a container the task does not have must be refused")

	// The task's own transition, with the reason it carries.
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

	// The timings the agent reports alongside the transition.
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

	// The reported detail beyond status: the reason a container gave, the
	// runtime it ran as, and the ports it bound.
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

	// A report is scoped to the cluster it names: the same task reported
	// against a different cluster is refused, so a report cannot reach across
	// clusters.
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

	// A task the control plane does not have is refused, not acknowledged.
	_, err = c.SubmitTaskStateChange(ctx, &ecs.SubmitTaskStateChangeInput{
		Cluster: aws.String(cluster),
		Task:    aws.String("arn:aws:ecs:us-east-1:123456789012:task/" + cluster + "/nonexistent"),
		Status:  aws.String("STOPPED"),
	})
	require.Error(t, err, "a task that does not exist must be refused")
}

// DiscoverPollEndpoint must point the agent at this simulator. It had returned
// real Amazon hostnames, which send an agent to AWS instead.
func TestECS_DiscoverPollEndpointPointsAtTheSimulator(t *testing.T) {
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

// The rules that decide whether a destructive Amazon ECS call is allowed at
// all. Each of these flags or associations was parsed and ignored, so calls
// AWS refuses succeeded here — and a caller relying on the refusal was never
// told it had skipped a step.
func TestECS_DestructiveCallsRefuseWhatAWSRefuses(t *testing.T) {
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

		// Disassociating it is what AWS tells the caller to do, and then the
		// delete goes through.
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
		// The identity document is what names the EC2 instance; it had been
		// dropped and the id invented.
		assert.Equal(t, "i-refusal", aws.ToString(instance.ContainerInstance.Ec2InstanceId),
			"the EC2 instance id must come from the identity document")

		_, deregisterErr := c.DeregisterContainerInstance(ctx, &ecs.DeregisterContainerInstanceInput{
			Cluster:           aws.String(cluster),
			ContainerInstance: instance.ContainerInstance.ContainerInstanceArn,
		})
		require.NoError(t, deregisterErr, "an idle instance deregisters without force")
	})
}

// A deployment lifecycle hook holds the deployment, and holds its tasks back.
//
// The hook is the point: a service that configures one at PRE_SCALE_UP must
// not launch the new revision until the hook is released. A simulator that
// recorded the hook on the deployment and rolled the tasks out anyway would be
// reporting a gate it did not have, so this asserts the task count as well as
// the deployment's own state.
func TestECS_DeploymentLifecycleHookHoldsTheDeployment(t *testing.T) {
	family := uniqueName("lifecycle-hook-task")
	serviceName := uniqueName("lifecycle-hook-service")
	c := ecsClient()
	cluster := uniqueName("lifecycle-hook-cluster")
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

	// A PAUSE hook needs no target to invoke: it waits for the operator.
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

	// The deployment stops at the guarded stage and records the hook.
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

	// The gate is real: nothing launched while the hook waits.
	held, err := c.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
	})
	require.NoError(t, err)
	assert.Empty(t, held.TaskArns,
		"a deployment waiting on a PRE_SCALE_UP hook must not have launched its tasks")

	// Continuing without naming the hook is refused, because there is a hook
	// to name.
	_, err = c.ContinueServiceDeployment(ctx, &ecs.ContinueServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(deploymentArn),
		Action:               ecstypes.DeploymentLifecycleHookActionContinue,
	})
	require.Error(t, err, "continuing a hooked deployment must name the hook")

	// An identifier no hook carries is refused too.
	_, err = c.ContinueServiceDeployment(ctx, &ecs.ContinueServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(deploymentArn),
		HookId:               aws.String("not-a-hook"),
		Action:               ecstypes.DeploymentLifecycleHookActionContinue,
	})
	require.Error(t, err, "an unknown hook identifier must be refused")

	// Released, the hook succeeds and the service converges.
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

	// The same hook cannot be released twice.
	_, err = c.ContinueServiceDeployment(ctx, &ecs.ContinueServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(deploymentArn),
		HookId:               aws.String(hookID),
		Action:               ecstypes.DeploymentLifecycleHookActionContinue,
	})
	require.Error(t, err, "a hook that already succeeded must not be continued again")
}
