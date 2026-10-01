package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_Service_ReconcilesRealTasks proves that Amazon ECS services own real
// task-definition workloads. It covers initial placement, replacement after a
// service task is stopped, rolling task-definition replacement, scale-out,
// scale-in, and delete-time draining through the official ECS client.
func TestECS_Service_ReconcilesRealTasks(t *testing.T) {
	client := ecsClient()
	cluster := "sched-cluster"
	const serviceName = "sched-svc"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster: aws.String(cluster), Service: aws.String(serviceName), DesiredCount: aws.Int32(0),
		})
		_, _ = client.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: aws.String(serviceName), Force: aws.Bool(true),
		})
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	register := func(command string) string {
		out, registerErr := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
			Family: aws.String("sched-task"),
			ContainerDefinitions: []ecstypes.ContainerDefinition{{
				StopTimeout: aws.Int32(2),
				Name:        aws.String("app"),
				Image:       aws.String(containerCommandImage),
				Command:     []string{command},
				Essential:   aws.Bool(true),
			}},
		})
		require.NoError(t, registerErr)
		return aws.ToString(out.TaskDefinition.TaskDefinitionArn)
	}
	firstRevision := register("hold")

	created, err := client.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(serviceName),
		TaskDefinition: aws.String(firstRevision),
		DesiredCount:   aws.Int32(2),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Service)
	assert.EqualValues(t, 2, created.Service.DesiredCount)

	runningTasks := func() []string {
		listed, listErr := client.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
			DesiredStatus: ecstypes.DesiredStatusRunning,
		})
		if listErr != nil {
			return nil
		}
		return listed.TaskArns
	}
	serviceIsSteady := func(desired int32) bool {
		described, describeErr := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{serviceName},
		})
		if describeErr != nil || len(described.Services) != 1 {
			return false
		}
		service := described.Services[0]
		return service.DesiredCount == desired &&
			service.RunningCount == desired &&
			service.PendingCount == 0 &&
			len(service.Deployments) == 1 &&
			service.Deployments[0].RolloutState == ecstypes.DeploymentRolloutStateCompleted
	}

	waitForECSServicesStable(t, client, cluster, 30*time.Second, serviceName)
	require.Len(t, runningTasks(), 2, "service did not launch two real tasks")

	beforeStop := runningTasks()
	require.Len(t, beforeStop, 2)
	_, err = client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(cluster), Task: aws.String(beforeStop[0]),
		Reason: aws.String("prove service replacement"),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		after := runningTasks()
		return len(after) == 2 && !containsString(after, beforeStop[0]) && serviceIsSteady(2)
	}, 30*time.Second, 100*time.Millisecond, "service did not replace a stopped task")

	secondRevision := register("hold")
	_, err = client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(serviceName),
		TaskDefinition: aws.String(secondRevision), DesiredCount: aws.Int32(3),
	})
	require.NoError(t, err)
	rolled := waitForECSServicesStable(t, client, cluster, 30*time.Second, serviceName).Services[0]
	assert.EqualValues(t, 3, rolled.RunningCount)
	taskArns := runningTasks()
	require.Len(t, taskArns, 3)
	described, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster), Tasks: taskArns,
	})
	require.NoError(t, err)
	require.Len(t, described.Tasks, 3)
	for _, task := range described.Tasks {
		assert.Equal(t, secondRevision, aws.ToString(task.TaskDefinitionArn), "rolling deployment did not replace every task")
	}

	_, err = client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(serviceName), DesiredCount: aws.Int32(0),
	})
	require.NoError(t, err)
	waitForECSServicesStable(t, client, cluster, 30*time.Second, serviceName)
	assert.Empty(t, runningTasks(), "scale-to-zero did not drain service tasks")

	deleted, err := client.DeleteService(ctx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(serviceName), Force: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "DRAINING", aws.ToString(deleted.Service.Status))
	waitForECSServiceInactive(t, client, cluster, serviceName)
}

// TestECS_Service_RegistersRunningTasksInCloudMap proves that service
// discovery is scheduler-owned state: Amazon ECS registered the real elastic
// network interface address of a running task, replaced that registration when
// the task was replaced, and deregistered it when the service was deleted.
func TestECS_Service_RegistersRunningTasksInCloudMap(t *testing.T) {
	family := uniqueName("discovery-scheduler-task")
	client := ecsClient()
	cloudMap := cmClient()
	cluster := uniqueName("discovery-scheduler-cluster")
	serviceName := uniqueName("discovery-scheduler-service")

	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: aws.String(serviceName), Force: aws.Bool(true),
		})
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	subnetID := createECSTestSubnet(t, "ecs-service-discovery")
	registered, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(family),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"),
			Image:       aws.String(containerCommandImage),
			Command:     []string{"hold"},
			Essential:   aws.Bool(true),
		}},
	})
	require.NoError(t, err)

	namespaceOut, err := cloudMap.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String(uniqueName("ecs-service-discovery") + ".local"),
		Vpc:  aws.String("vpc-ecs-service-discovery"),
	})
	require.NoError(t, err)
	namespaceOperation, err := cloudMap.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: namespaceOut.OperationId,
	})
	require.NoError(t, err)
	namespaceID := namespaceOperation.Operation.Targets["NAMESPACE"]
	registryOut, err := cloudMap.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("app"),
		NamespaceId: aws.String(namespaceID),
		DnsConfig: &sdtypes.DnsConfig{
			RoutingPolicy: sdtypes.RoutingPolicyMultivalue,
			DnsRecords: []sdtypes.DnsRecord{{
				Type: sdtypes.RecordTypeA,
				TTL:  aws.Int64(10),
			}},
		},
	})
	require.NoError(t, err)
	registryID := aws.ToString(registryOut.Service.Id)
	t.Cleanup(func() {
		_, _ = cloudMap.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(registryID)})
		_, _ = cloudMap.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(namespaceID)})
	})

	_, err = client.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(serviceName),
		TaskDefinition: registered.TaskDefinition.TaskDefinitionArn,
		DesiredCount:   aws.Int32(1),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets: []string{subnetID},
			},
		},
		ServiceRegistries: []ecstypes.ServiceRegistry{{
			RegistryArn: registryOut.Service.Arn,
		}},
	})
	require.NoError(t, err)

	waitForECSServicesStable(t, client, cluster, 30*time.Second, serviceName)
	listed, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(serviceName),
		DesiredStatus: ecstypes.DesiredStatusRunning,
	})
	require.NoError(t, err)
	require.Len(t, listed.TaskArns, 1)
	described, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster), Tasks: listed.TaskArns,
	})
	require.NoError(t, err)
	require.Len(t, described.Tasks, 1)
	require.Len(t, described.Tasks[0].Containers, 1)
	require.Len(t, described.Tasks[0].Containers[0].NetworkInterfaces, 1)
	firstTaskARN := listed.TaskArns[0]
	firstTaskID := firstTaskARN[strings.LastIndex(firstTaskARN, "/")+1:]
	firstIP := aws.ToString(described.Tasks[0].Containers[0].NetworkInterfaces[0].PrivateIpv4Address)
	instance, err := cloudMap.GetInstance(ctx, &servicediscovery.GetInstanceInput{
		ServiceId: aws.String(registryID), InstanceId: aws.String(firstTaskID),
	})
	require.NoError(t, err, "running task was not registered with its real ENI address")
	assert.Equal(t, firstIP, instance.Instance.Attributes["AWS_INSTANCE_IPV4"])
	assert.Equal(t, serviceName, instance.Instance.Attributes["ECS_SERVICE_NAME"])
	assert.Equal(t, cluster, instance.Instance.Attributes["ECS_CLUSTER_NAME"])
	assert.Equal(t, family, instance.Instance.Attributes["ECS_TASK_DEFINITION_FAMILY"])
	assert.Equal(t, "1", instance.Instance.Attributes["ECS_TASK_DEFINITION_REVISION"])

	_, err = client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(cluster), Task: aws.String(firstTaskARN),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		instances, listErr := cloudMap.ListInstances(ctx, &servicediscovery.ListInstancesInput{
			ServiceId: aws.String(registryID),
		})
		return listErr == nil && len(instances.Instances) == 1 &&
			aws.ToString(instances.Instances[0].Id) != firstTaskID &&
			instances.Instances[0].Attributes["AWS_INSTANCE_IPV4"] != firstIP
	}, 30*time.Second, 100*time.Millisecond, "replacement task did not replace its Cloud Map registration")

	_, err = client.DeleteService(ctx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(serviceName), Force: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		instances, listErr := cloudMap.ListInstances(ctx, &servicediscovery.ListInstancesInput{
			ServiceId: aws.String(registryID),
		})
		return listErr == nil && len(instances.Instances) == 0
	}, 10*time.Second, 100*time.Millisecond, "service deletion did not deregister Cloud Map instances")
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// TestECS_ServiceTaskStreamsLogsLive proves the awslogs contract for the tasks
// an Amazon ECS service owns. A service task never exits, so its stdout is only
// diagnosable if the log driver forwards each line while the task is RUNNING —
// the post-exit drain that covers one-shot RunTask workloads never runs for it.
// Regression: service tasks reached CloudWatch with nothing but the synthetic
// "container started" event, which made every service in the simulator opaque.
func TestECS_ServiceTaskStreamsLogsLive(t *testing.T) {
	family := uniqueName("svc-live-logs-task")
	client := ecsClient()
	cluster := uniqueName("svc-live-logs-cluster")
	serviceName := uniqueName("svc-live-logs-svc")
	logGroup := uniqueName("/ecs/svc-live-logs")
	const marker = "service-line-from-running-task"
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.UpdateService(ctx, &ecs.UpdateServiceInput{
			Cluster: aws.String(cluster), Service: aws.String(serviceName), DesiredCount: aws.Int32(0),
		})
		_, _ = client.DeleteService(ctx, &ecs.DeleteServiceInput{
			Cluster: aws.String(cluster), Service: aws.String(serviceName), Force: aws.Bool(true),
		})
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	registered, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"),
			Image:       aws.String(containerCommandImage),
			Command:     []string{"log", marker, "600"},
			Essential:   aws.Bool(true),
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
	_, err = client.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(serviceName),
		TaskDefinition: registered.TaskDefinition.TaskDefinitionArn,
		DesiredCount:   aws.Int32(1),
	})
	require.NoError(t, err)

	cw := cwLogsClient()
	markerEvents := func() int {
		streams, streamErr := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
			LogGroupName: aws.String(logGroup),
		})
		if streamErr != nil {
			return 0
		}
		count := 0
		for _, stream := range streams.LogStreams {
			events, eventErr := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
				LogGroupName:  aws.String(logGroup),
				LogStreamName: stream.LogStreamName,
			})
			if eventErr != nil {
				continue
			}
			for _, event := range events.Events {
				if aws.ToString(event.Message) == marker {
					count++
				}
			}
		}
		return count
	}

	// A service task's stdout must stream to CloudWatch while the task is
	// RUNNING.
	waitForECSServicesStable(t, client, cluster, 60*time.Second, serviceName)
	awaitLogLine(t, cw, logGroup, marker, 60*time.Second)

	// The line is observable while the service is still holding the task, so
	// an operator can diagnose a running service without stopping it.
	described, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(cluster), Services: []string{serviceName},
	})
	require.NoError(t, err)
	require.Len(t, described.Services, 1)
	require.EqualValues(t, 1, described.Services[0].RunningCount,
		"the log line must be visible while the service task is still RUNNING")
	assert.Never(t, func() bool { return markerEvents() > 1 },
		3*time.Second, 250*time.Millisecond,
		"the live stream must not deliver a line more than once")

	// DescribeLogStreams must report the ingestion, not just the creation, of
	// the stream: ordering a service's streams by LastEventTime is how an
	// operator finds the task that is still writing.
	streams, err := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.Len(t, streams.LogStreams, 1)
	stream := streams.LogStreams[0]
	require.Greater(t, aws.ToInt64(stream.LastEventTimestamp), aws.ToInt64(stream.CreationTime),
		"the service task's stream must report the workload's own output as its last event")
	require.GreaterOrEqual(t, aws.ToInt64(stream.LastIngestionTime), aws.ToInt64(stream.LastEventTimestamp),
		"the service task's stream must report when its workload output was ingested")
}
