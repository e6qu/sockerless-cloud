package aws_sdk_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAmazonECSServiceAdoptsItsTaskAcrossSimulatorRestart_SDK expects the
// service and task stores to stay authoritative across a hard control-plane
// replacement: the restarted simulator adopts the workload container and
// launches no duplicate task.
func TestAmazonECSServiceAdoptsItsTaskAcrossSimulatorRestart_SDK(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	client := ecs.NewFromConfig(cfg, func(options *ecs.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const (
		cluster = "persistent-service-cluster"
		service = "persistent-service"
	)
	_, err := client.CreateCluster(testCtx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	_, err = client.RegisterTaskDefinition(testCtx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("persistent-service-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("application"), Image: aws.String(containerCommandImage),
			Command: []string{"hold"}, Essential: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	_, err = client.CreateService(testCtx, &ecs.CreateServiceInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(service),
		TaskDefinition: aws.String("persistent-service-task"), DesiredCount: aws.Int32(1),
	})
	require.NoError(t, err)

	waitForECSServicesStable(t, client, cluster, 30*time.Second, service)
	original, err := client.ListTasks(testCtx, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(service),
		DesiredStatus: ecstypes.DesiredStatusRunning,
	})
	require.NoError(t, err)
	require.Len(t, original.TaskArns, 1, "service task did not reach RUNNING")
	originalTaskARN := original.TaskArns[0]

	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	client = ecs.NewFromConfig(cfg, func(options *ecs.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})

	adopted := waitForECSServicesStable(t, client, cluster, 30*time.Second, service).Services[0]
	assert.EqualValues(t, 1, adopted.RunningCount)
	assert.EqualValues(t, 0, adopted.PendingCount)
	listed, err := client.ListTasks(testCtx, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(service),
		DesiredStatus: ecstypes.DesiredStatusRunning,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{originalTaskARN}, listed.TaskArns, "restart did not adopt exactly the original service task")

	_, err = client.UpdateService(testCtx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(service), DesiredCount: aws.Int32(0),
	})
	require.NoError(t, err)
	_, err = client.DeleteService(testCtx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(service), Force: aws.Bool(true),
	})
	require.NoError(t, err)
	// A persistent simulator leaves its workloads running when it stops, so stop
	// the task before the cleanup stops the simulator.
	require.NoError(t, ecs.NewTasksStoppedWaiter(client, func(o *ecs.TasksStoppedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(testCtx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster), Tasks: []string{originalTaskARN},
	}, 30*time.Second))
}

// TestAmazonECSServiceReleasesItsVPCNetworkAcrossSimulatorRestart_SDK expects
// deleting a restarted Fargate service to remove the adopted workload from its
// VPC network, so Amazon EC2 deletes the subnet and VPC without
// DependencyViolation.
func TestAmazonECSServiceReleasesItsVPCNetworkAcrossSimulatorRestart_SDK(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	ecsAPI := ecs.NewFromConfig(cfg, func(options *ecs.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	ec2API := ec2.NewFromConfig(cfg, func(options *ec2.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	vpc, err := ec2API.CreateVpc(testCtx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.253.0.0/16")})
	require.NoError(t, err)
	subnet, err := ec2API.CreateSubnet(testCtx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.253.1.0/24"),
	})
	require.NoError(t, err)
	const (
		cluster = "persistent-vpc-service-cluster"
		service = "persistent-vpc-service"
	)
	_, err = ecsAPI.CreateCluster(testCtx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	registered, err := ecsAPI.RegisterTaskDefinition(testCtx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("persistent-vpc-service-task"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("application"), Image: aws.String(containerCommandImage),
			Command: []string{"hold"}, Essential: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	_, err = ecsAPI.CreateService(testCtx, &ecs.CreateServiceInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(service),
		TaskDefinition: registered.TaskDefinition.TaskDefinitionArn, DesiredCount: aws.Int32(1),
		LaunchType: ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets: []string{aws.ToString(subnet.Subnet.SubnetId)},
			},
		},
	})
	require.NoError(t, err)
	waitForECSServicesStable(t, ecsAPI, cluster, 30*time.Second, service)

	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	ecsAPI = ecs.NewFromConfig(cfg, func(options *ecs.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	ec2API = ec2.NewFromConfig(cfg, func(options *ec2.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})

	_, err = ecsAPI.DeleteService(testCtx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(service), Force: aws.Bool(true),
	})
	require.NoError(t, err)
	// The deleted service's tasks hold interfaces in the subnet until they stop,
	// and Amazon EC2 refuses DeleteSubnet meanwhile.
	stopECSTasksInSubnet(t, ecsAPI, aws.ToString(subnet.Subnet.SubnetId))
	_, err = ec2API.DeleteSubnet(testCtx, &ec2.DeleteSubnetInput{SubnetId: subnet.Subnet.SubnetId})
	require.NoError(t, err)
	_, err = ec2API.DeleteVpc(testCtx, &ec2.DeleteVpcInput{VpcId: vpc.Vpc.VpcId})
	require.NoError(t, err, "VPC network remained attached after service deletion")
	waitForECSServiceInactive(t, ecsAPI, cluster, service)
	_, err = ecsAPI.DeleteCluster(testCtx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
}

// TestAmazonECSServiceDeploymentFailureStateSurvivesSimulatorRestart_SDK
// expects deployment failure counts and the pending retry deadline to survive:
// a hard replacement between the first and second failed launch still reaches
// the threshold once and rolls back to the last completed task definition.
func TestAmazonECSServiceDeploymentFailureStateSurvivesSimulatorRestart_SDK(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	client := ecs.NewFromConfig(cfg, func(options *ecs.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const (
		cluster = "persistent-deployment-cluster"
		service = "persistent-deployment-service"
		family  = "persistent-deployment-task"
	)
	_, err := client.CreateCluster(testCtx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	stable := registerECSServiceDeploymentTask(t, client, family, []string{"hold"})
	_, err = client.CreateService(testCtx, &ecs.CreateServiceInput{
		Cluster: aws.String(cluster), ServiceName: aws.String(service),
		TaskDefinition: aws.String(stable), DesiredCount: aws.Int32(1),
	})
	require.NoError(t, err)
	waitForECSServiceTaskDefinition(t, client, cluster, service, stable)
	failing := registerECSServiceDeploymentTask(t, client, family, []string{"not-a-supported-command"})
	_, err = client.UpdateService(testCtx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(service),
		TaskDefinition: aws.String(failing),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{
				Enable: true, Rollback: true,
				ThresholdConfiguration: &ecstypes.ThresholdConfiguration{
					Type: ecstypes.ThresholdTypeCount, Value: 2,
				},
			},
		},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		output, describeErr := client.DescribeServices(testCtx, &ecs.DescribeServicesInput{
			Cluster: aws.String(cluster), Services: []string{service},
		})
		if describeErr != nil || len(output.Services) != 1 {
			return false
		}
		return serviceEventsContain(output.Services[0], "was unable to place a task")
	}, 30*time.Second, 50*time.Millisecond, "first deployment launch failure was not persisted")

	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	client = ecs.NewFromConfig(cfg, func(options *ecs.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})

	rolledBack := waitForECSServiceTaskDefinition(t, client, cluster, service, stable)
	failureEvents := 0
	for _, event := range rolledBack.Events {
		if strings.Contains(aws.ToString(event.Message), "was unable to place a task") {
			failureEvents++
		}
	}
	require.Equal(t, 2, failureEvents,
		"the persisted first failure must count toward the two-failure circuit-breaker threshold")
	require.Equal(t, 1, serviceEventCount(rolledBack, "deployment rollback completed"),
		"the response that first reports the rollout COMPLETED must already carry the "+
			"rollback event, exactly once: %v", serviceEventMessages(rolledBack))

	_, err = client.UpdateService(testCtx, &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(service), DesiredCount: aws.Int32(0),
	})
	require.NoError(t, err)
	_, err = client.DeleteService(testCtx, &ecs.DeleteServiceInput{
		Cluster: aws.String(cluster), Service: aws.String(service), Force: aws.Bool(true),
	})
	require.NoError(t, err)
	// Amazon ECS refuses DeleteCluster while the service drains its tasks.
	waitForECSServiceInactive(t, client, cluster, service)
	_, err = client.DeleteCluster(testCtx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
}
