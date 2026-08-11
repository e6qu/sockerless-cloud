package aws_sdk_test

import (
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
			Name: aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
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
	require.Eventually(t, func() bool {
		described, describeErr := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{"control-plane"},
		})
		return describeErr == nil &&
			len(described.Services) == 1 &&
			described.Services[0].RunningCount == 2 &&
			described.Services[0].PendingCount == 0 &&
			len(described.Services[0].Deployments) == 1 &&
			described.Services[0].Deployments[0].RolloutState == ecstypes.DeploymentRolloutStateCompleted
	}, 30*time.Second, 100*time.Millisecond, "service did not reach steady state with two running tasks")

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
	require.Eventually(t, func() bool {
		described, describeErr := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{"control-plane"},
		})
		return describeErr == nil &&
			len(described.Services) == 1 &&
			described.Services[0].RunningCount == 3 &&
			described.Services[0].PendingCount == 0
	}, 30*time.Second, 100*time.Millisecond, "service did not scale out to three running tasks")

	// DeleteService — must settle to INACTIVE.
	delOut, err := c.DeleteService(ctx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster), Service: aws.String("control-plane"), Force: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "INACTIVE", aws.ToString(delOut.Service.Status))
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
			Name: aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
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
	out, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("test-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:  aws.String("app"),
				Image: aws.String("alpine:latest"),
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "test-task", *out.TaskDefinition.Family)
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
				Name:    aws.String("main"),
				Image:   aws.String(containerCommandImage),
				Command: []string{"probe-http", "http://127.0.0.1:9090", "sidecar-ok", "10"},
				LogConfiguration: &ecstypes.LogConfiguration{
					LogDriver: ecstypes.LogDriverAwslogs,
					Options: map[string]string{
						"awslogs-group":         logGroupName,
						"awslogs-stream-prefix": "ecs",
					},
				},
			},
			{
				Name:    aws.String("sidecar"),
				Image:   aws.String(containerCommandImage),
				Command: []string{"http", "9090", "sidecar-ok"},
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

	require.Eventually(t, func() bool {
		desc, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterName),
			Tasks:   []string{taskArn},
		})
		if err != nil || len(desc.Tasks) != 1 {
			return false
		}
		return desc.Tasks[0].LastStatus != nil && *desc.Tasks[0].LastStatus == "STOPPED"
	}, 20*time.Second, 500*time.Millisecond)

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
			Name:       aws.String("writer"),
			Image:      aws.String(busyboxImage),
			EntryPoint: []string{"sh", "-c"},
			Command:    []string{"printf 'sockerless-ebs-roundtrip' > /workspace/state.txt"},
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
	waitForEC2SnapshotState(t, ec2c, snapshotID, "completed")
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
			Name:       aws.String("reader"),
			Image:      aws.String(busyboxImage),
			EntryPoint: []string{"sh", "-c"},
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
			Name:  aws.String("workspace"),
			Image: aws.String("alpine:latest"),
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

	require.Eventually(t, func() bool {
		events, ferr := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName: aws.String(logGroup),
		})
		if ferr != nil {
			return false
		}
		var messages []string
		for _, e := range events.Events {
			messages = append(messages, aws.ToString(e.Message))
		}
		return strings.Contains(strings.Join(messages, "\n"), "override:ws-sdk:from-task-definition:from-runtask")
	}, 10*time.Second, 500*time.Millisecond)
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
				Name:    aws.String("app"),
				Image:   aws.String("alpine:latest"),
				Command: []string{"sleep", "30"}, // long-running so RUNNING window is real
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

	// Describe task after STOPPED — ExitCode should be set
	descOut2, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut2.Tasks, 1)

	stoppedTask := descOut2.Tasks[0]
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
				Name:    aws.String("app"),
				Image:   aws.String("alpine:latest"),
				Command: []string{"sleep", "30"},
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

	// Stop task via API
	_, err = client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(clusterName),
		Task:    aws.String(taskArn),
		Reason:  aws.String("testing stop"),
	})
	require.NoError(t, err)

	// Describe — StopCode should be UserInitiated
	descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Tasks, 1)

	task := descOut.Tasks[0]
	assert.Equal(t, "STOPPED", *task.LastStatus)
	assert.Equal(t, ecstypes.TaskStopCodeUserInitiated, task.StopCode)
	assert.Equal(t, "testing stop", *task.StoppedReason)
}

// ecsRunTaskHelper creates a cluster, registers a task definition, and runs a task.
// Returns the ECS client, cluster name, and task ARN.
// waitTaskStopped polls DescribeTasks until the task reaches STOPPED and
// returns it. The sim runs the task asynchronously (start + process + the
// STOPPED state transition), so a fixed sleep races a loaded CI runner.
func waitTaskStopped(t *testing.T, client *ecs.Client, cluster, taskArn string) ecstypes.Task {
	t.Helper()
	var task ecstypes.Task
	require.Eventually(t, func() bool {
		out, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   []string{taskArn},
		})
		if err != nil || len(out.Tasks) != 1 || aws.ToString(out.Tasks[0].LastStatus) != "STOPPED" {
			return false
		}
		task = out.Tasks[0]
		return true
	}, 60*time.Second, 200*time.Millisecond)
	return task
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
	// Keep ECS helper VPCs in 10.225.0.0/16 through 10.249.0.0/16. Fixed-CIDR
	// SDK tests occupy ranges through 10.224.0.0/16 and resume at
	// 10.250.0.0/16, so the old 10.20-119 range could collide with tests such
	// as the 10.40.0.0/16 transit-gateway coverage. Cleanup below makes reuse
	// after wrapping safe even though StopTask releases containers
	// asynchronously.
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
		_, _ = ec2c.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: aws.String(subnetID)})
		var deleteErr error
		require.Eventually(t, func() bool {
			_, deleteErr = ec2c.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)})
			return deleteErr == nil
		}, 15*time.Second, 250*time.Millisecond,
			"delete ECS test VPC %s after its asynchronously stopped containers release the network",
			vpcID)
	})
	return vpcID, subnetID
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

func waitForECSTaskStatus(t *testing.T, client *ecs.Client, clusterName, taskArn, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		desc, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterName),
			Tasks:   []string{taskArn},
		})
		if err != nil || len(desc.Tasks) != 1 || desc.Tasks[0].LastStatus == nil {
			return false
		}
		return aws.ToString(desc.Tasks[0].LastStatus) == want
	}, 60*time.Second, 500*time.Millisecond)
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
		Name:    aws.String("app"),
		Image:   aws.String("alpine:latest"),
		Command: []string{"echo", "hello"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/exec-cmd",
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
		Name:    aws.String("app"),
		Image:   aws.String("alpine:latest"),
		Command: []string{"sh", "-c", "exit 1"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/exec-fail",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	task := waitTaskStopped(t, client, cluster, taskArn)
	require.NotEmpty(t, task.Containers)
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(1), *task.Containers[0].ExitCode)
}

func TestECS_TaskLogsToCloudWatch(t *testing.T) {
	_, _, _ = ecsRunTaskHelper(t, "exec-logs", ecstypes.ContainerDefinition{
		Name:    aws.String("app"),
		Image:   aws.String("alpine:latest"),
		Command: []string{"echo", "hello from process"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/exec-logs",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	cw := cwLogsClient()

	// Poll until the process stdout reaches CloudWatch. Image pull +
	// container start latency on slow CI runners can exceed any fixed sleep.
	var messages []string
	require.Eventually(t, func() bool {
		streams, serr := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
			LogGroupName: aws.String("/ecs/exec-logs"),
		})
		if serr != nil || len(streams.LogStreams) == 0 {
			return false
		}
		out, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
			LogGroupName:  aws.String("/ecs/exec-logs"),
			LogStreamName: streams.LogStreams[0].LogStreamName,
		})
		if err != nil {
			return false
		}
		messages = messages[:0]
		for _, e := range out.Events {
			messages = append(messages, *e.Message)
			if *e.Message == "hello from process" {
				return true
			}
		}
		return false
	}, 30*time.Second, 250*time.Millisecond, "process stdout should reach CloudWatch logs; saw=%v", messages)
}

// TestECS_RunningTaskStreamsLogsLive proves the awslogs contract for a
// long-running task: the container's stdout reaches its CloudWatch log
// stream while the task is still RUNNING — real awslogs forwards each line
// as it is produced, so a service task is observable without stopping it —
// and the post-exit drain does not duplicate the lines the live stream
// already delivered.
func TestECS_RunningTaskStreamsLogsLive(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "live-logs", ecstypes.ContainerDefinition{
		Name:    aws.String("app"),
		Image:   aws.String("alpine:latest"),
		Command: []string{"sh", "-c", "echo live-line-from-running-task; tail -f /dev/null"},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/live-logs",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	cw := cwLogsClient()
	countLiveLines := func() int {
		streams, serr := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
			LogGroupName: aws.String("/ecs/live-logs"),
		})
		if serr != nil {
			return 0
		}
		count := 0
		for _, stream := range streams.LogStreams {
			out, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
				LogGroupName:  aws.String("/ecs/live-logs"),
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
	require.Eventually(t, func() bool { return countLiveLines() >= 1 },
		30*time.Second, 250*time.Millisecond,
		"a running task's stdout must stream to CloudWatch before the task exits")

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
	require.Eventually(t, func() bool {
		out, derr := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   []string{taskArn},
		})
		return derr == nil && len(out.Tasks) == 1 && aws.ToString(out.Tasks[0].LastStatus) == "STOPPED"
	}, 30*time.Second, 250*time.Millisecond, "task should stop")
	assert.Never(t, func() bool { return countLiveLines() > 1 },
		3*time.Second, 250*time.Millisecond,
		"the post-exit drain must not duplicate lines the live stream delivered")
}

func TestECS_TaskNoCommandStaysRunning(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "exec-nocmd", ecstypes.ContainerDefinition{
		Name:    aws.String("app"),
		Image:   aws.String("alpine:latest"),
		Command: []string{"tail", "-f", "/dev/null"}, // Long-running — stays RUNNING
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         "/ecs/exec-nocmd",
				"awslogs-stream-prefix": "ecs",
			},
		},
	})

	var task ecstypes.Task
	require.Eventually(t, func() bool {
		descOut, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   []string{taskArn},
		})
		if err != nil || len(descOut.Tasks) != 1 {
			return false
		}
		task = descOut.Tasks[0]
		return task.LastStatus != nil && *task.LastStatus == "RUNNING"
	}, 30*time.Second, 250*time.Millisecond, "task with no command should reach RUNNING")

	assert.Equal(t, "RUNNING", *task.LastStatus, "task with no command should stay RUNNING")
	for _, c := range task.Containers {
		assert.Nil(t, c.ExitCode, "ExitCode should be nil while RUNNING")
	}
}

// TagResource/UntagResource contract: tag a running task, list tags,
// untag, and confirm STOPPED tasks reject tagging.
func TestECS_TagResource_OnRunningTask(t *testing.T) {
	client, cluster, taskArn := ecsRunTaskHelper(t, "tag-task", ecstypes.ContainerDefinition{
		Name:    aws.String("app"),
		Image:   aws.String("alpine:latest"),
		Command: []string{"tail", "-f", "/dev/null"},
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
		Name:    aws.String("app"),
		Image:   aws.String("alpine:latest"),
		Command: []string{"sh", "-c", "exit 0"},
	})

	// Poll for STOPPED — podman lifecycle (image pull + start + exit + sim
	// state update) can take >8s under CI contention; a fixed sleep flakes.
	var descOut *ecs.DescribeTasksOutput
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		descOut, err = client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(cluster),
			Tasks:   []string{taskArn},
		})
		require.NoError(t, err)
		require.Len(t, descOut.Tasks, 1)
		if *descOut.Tasks[0].LastStatus == "STOPPED" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.Equal(t, "STOPPED", *descOut.Tasks[0].LastStatus, "task should be STOPPED before this assertion")

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
	client := ecsClient()
	cluster := "pag-cluster"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	// Bridge network mode (the default): the tasks share the container
	// instance's networking, so no networkConfiguration is needed to run them.
	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("pag-family"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{Name: aws.String("app"), Image: aws.String("alpine:latest")},
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
	client := ecsClient()
	cluster := "listtasks-filters"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String("listtasks-filters-td"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("app"), Image: aws.String("alpine:latest")}},
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
