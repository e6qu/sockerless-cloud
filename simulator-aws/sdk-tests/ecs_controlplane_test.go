package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestECS_ARecordServiceRegistryRejectsPort(t *testing.T) {
	ecsClient := ecsClient()
	cloudMapClient := cmClient()
	cluster := "a-record-registry-cluster"
	_, err := ecsClient.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ecsClient.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	_, err = ecsClient.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:      aws.String("a-record-registry-task"),
		NetworkMode: ecstypes.NetworkModeAwsvpc,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String("dqlite"),
			Image: aws.String("public.ecr.aws/docker/library/alpine:3.20"),
		}},
	})
	require.NoError(t, err)

	namespace, err := cloudMapClient.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("a-record-registry.local"),
		Vpc:  aws.String("vpc-a-record-registry"),
	})
	require.NoError(t, err)
	operation, err := cloudMapClient.GetOperation(ctx, &servicediscovery.GetOperationInput{OperationId: namespace.OperationId})
	require.NoError(t, err)
	namespaceID := operation.Operation.Targets["NAMESPACE"]
	t.Cleanup(func() {
		_, _ = cloudMapClient.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(namespaceID)})
	})

	registry, err := cloudMapClient.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("dqlite"),
		NamespaceId: aws.String(namespaceID),
		DnsConfig: &sdtypes.DnsConfig{DnsRecords: []sdtypes.DnsRecord{{
			Type: sdtypes.RecordTypeA,
			TTL:  aws.Int64(10),
		}}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cloudMapClient.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: registry.Service.Id})
	})

	_, err = ecsClient.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String("invalid-a-record-registry"),
		TaskDefinition: aws.String("a-record-registry-task"),
		ServiceRegistries: []ecstypes.ServiceRegistry{{
			RegistryArn:   registry.Service.Arn,
			ContainerPort: aws.Int32(9000),
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not require a value for 'containerPort'")

	created, err := ecsClient.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String("valid-a-record-registry"),
		TaskDefinition: aws.String("a-record-registry-task"),
		ServiceRegistries: []ecstypes.ServiceRegistry{{
			RegistryArn: registry.Service.Arn,
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "valid-a-record-registry", aws.ToString(created.Service.ServiceName))
}

// TestECS_CapacityProviderLifecycle exercises CreateCapacityProvider,
// UpdateCapacityProvider, and DeleteCapacityProvider (DescribeCapacityProviders
// already covered elsewhere) — the aws_ecs_capacity_provider control-plane CRUD.
func TestECS_CapacityProviderLifecycle(t *testing.T) {
	c := ecsClient()
	name := "sdk-cap-provider"

	createOut, err := c.CreateCapacityProvider(ctx, &ecs.CreateCapacityProviderInput{
		Name: aws.String(name),
		AutoScalingGroupProvider: &ecstypes.AutoScalingGroupProvider{
			AutoScalingGroupArn:          aws.String("arn:aws:autoscaling:us-east-1:000000000000:autoScalingGroup:uuid:autoScalingGroupName/asg"),
			ManagedTerminationProtection: ecstypes.ManagedTerminationProtectionDisabled,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.CapacityProvider)
	assert.Equal(t, name, aws.ToString(createOut.CapacityProvider.Name))
	assert.Equal(t, ecstypes.CapacityProviderStatusActive, createOut.CapacityProvider.Status)
	assert.Contains(t, aws.ToString(createOut.CapacityProvider.CapacityProviderArn), name)

	descOut, err := c.DescribeCapacityProviders(ctx, &ecs.DescribeCapacityProvidersInput{
		CapacityProviders: []string{name},
	})
	require.NoError(t, err)
	require.Len(t, descOut.CapacityProviders, 1)
	require.NotNil(t, descOut.CapacityProviders[0].AutoScalingGroupProvider)

	updOut, err := c.UpdateCapacityProvider(ctx, &ecs.UpdateCapacityProviderInput{
		Name: aws.String(name),
		AutoScalingGroupProvider: &ecstypes.AutoScalingGroupProviderUpdate{
			ManagedTerminationProtection: ecstypes.ManagedTerminationProtectionEnabled,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(updOut.CapacityProvider.Name))

	delOut, err := c.DeleteCapacityProvider(ctx, &ecs.DeleteCapacityProviderInput{
		CapacityProvider: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, ecstypes.CapacityProviderStatusInactive, delOut.CapacityProvider.Status)
}

// TestECS_TaskSetLifecycle exercises CreateTaskSet, DescribeTaskSets,
// UpdateTaskSet, UpdateServicePrimaryTaskSet and DeleteTaskSet — the
// aws_ecs_task_set blue/green primitive on an EXTERNAL-controller service.
func TestECS_TaskSetLifecycle(t *testing.T) {
	c := ecsClient()
	cluster := "ts-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	_, err = c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("ts-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)

	_, err = c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:              aws.String(cluster),
		ServiceName:          aws.String("ts-svc"),
		TaskDefinition:       aws.String("ts-task"),
		DesiredCount:         aws.Int32(1),
		DeploymentController: &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeExternal},
	})
	require.NoError(t, err)

	createOut, err := c.CreateTaskSet(ctx, &ecs.CreateTaskSetInput{
		Cluster:        aws.String(cluster),
		Service:        aws.String("ts-svc"),
		TaskDefinition: aws.String("ts-task"),
		Scale:          &ecstypes.Scale{Value: 50, Unit: ecstypes.ScaleUnitPercent},
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.TaskSet)
	tsID := aws.ToString(createOut.TaskSet.Id)
	require.NotEmpty(t, tsID)

	descOut, err := c.DescribeTaskSets(ctx, &ecs.DescribeTaskSetsInput{
		Cluster: aws.String(cluster), Service: aws.String("ts-svc"), TaskSets: []string{tsID},
	})
	require.NoError(t, err)
	require.Len(t, descOut.TaskSets, 1)
	assert.Equal(t, tsID, aws.ToString(descOut.TaskSets[0].Id))

	_, err = c.UpdateTaskSet(ctx, &ecs.UpdateTaskSetInput{
		Cluster: aws.String(cluster), Service: aws.String("ts-svc"), TaskSet: aws.String(tsID),
		Scale: &ecstypes.Scale{Value: 100, Unit: ecstypes.ScaleUnitPercent},
	})
	require.NoError(t, err)

	primOut, err := c.UpdateServicePrimaryTaskSet(ctx, &ecs.UpdateServicePrimaryTaskSetInput{
		Cluster: aws.String(cluster), Service: aws.String("ts-svc"), PrimaryTaskSet: aws.String(tsID),
	})
	require.NoError(t, err)
	assert.Equal(t, "PRIMARY", aws.ToString(primOut.TaskSet.Status))

	_, err = c.DeleteTaskSet(ctx, &ecs.DeleteTaskSetInput{
		Cluster: aws.String(cluster), Service: aws.String("ts-svc"), TaskSet: aws.String(tsID), Force: aws.Bool(true),
	})
	require.NoError(t, err)
}

// TestECS_ContainerInstanceLifecycle exercises the container-instance + agent
// surface: RegisterContainerInstance, DescribeContainerInstances,
// ListContainerInstances, UpdateContainerInstancesState, UpdateContainerAgent,
// DeregisterContainerInstance, plus the agent-poll ops SubmitContainerStateChange /
// SubmitTaskStateChange / SubmitAttachmentStateChanges / DiscoverPollEndpoint.
func TestECS_ContainerInstanceLifecycle(t *testing.T) {
	c := ecsClient()
	cluster := "ci-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	regOut, err := c.RegisterContainerInstance(ctx, &ecs.RegisterContainerInstanceInput{
		Cluster:     aws.String(cluster),
		VersionInfo: &ecstypes.VersionInfo{AgentVersion: aws.String("1.0.0"), DockerVersion: aws.String("20.10")},
	})
	require.NoError(t, err)
	require.NotNil(t, regOut.ContainerInstance)
	ciArn := aws.ToString(regOut.ContainerInstance.ContainerInstanceArn)
	require.NotEmpty(t, ciArn)
	assert.True(t, regOut.ContainerInstance.AgentConnected)

	listOut, err := c.ListContainerInstances(ctx, &ecs.ListContainerInstancesInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
	require.Contains(t, listOut.ContainerInstanceArns, ciArn)

	descOut, err := c.DescribeContainerInstances(ctx, &ecs.DescribeContainerInstancesInput{
		Cluster: aws.String(cluster), ContainerInstances: []string{ciArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.ContainerInstances, 1)

	stateOut, err := c.UpdateContainerInstancesState(ctx, &ecs.UpdateContainerInstancesStateInput{
		Cluster: aws.String(cluster), ContainerInstances: []string{ciArn}, Status: ecstypes.ContainerInstanceStatusDraining,
	})
	require.NoError(t, err)
	require.Len(t, stateOut.ContainerInstances, 1)
	assert.Equal(t, "DRAINING", aws.ToString(stateOut.ContainerInstances[0].Status))

	_, err = c.UpdateContainerAgent(ctx, &ecs.UpdateContainerAgentInput{
		Cluster: aws.String(cluster), ContainerInstance: aws.String(ciArn),
	})
	require.NoError(t, err)

	// Agent-poll ops: each acknowledges the change.
	_, err = c.DiscoverPollEndpoint(ctx, &ecs.DiscoverPollEndpointInput{
		Cluster: aws.String(cluster), ContainerInstance: aws.String(ciArn),
	})
	require.NoError(t, err)
	_, err = c.SubmitContainerStateChange(ctx, &ecs.SubmitContainerStateChangeInput{
		Cluster: aws.String(cluster), Task: aws.String("task-1"), ContainerName: aws.String("app"), Status: aws.String("RUNNING"),
	})
	require.NoError(t, err)
	_, err = c.SubmitTaskStateChange(ctx, &ecs.SubmitTaskStateChangeInput{
		Cluster: aws.String(cluster), Task: aws.String("task-1"), Status: aws.String("RUNNING"),
	})
	require.NoError(t, err)
	_, err = c.SubmitAttachmentStateChanges(ctx, &ecs.SubmitAttachmentStateChangesInput{
		Cluster:     aws.String(cluster),
		Attachments: []ecstypes.AttachmentStateChange{{AttachmentArn: aws.String("att-1"), Status: aws.String("ATTACHED")}},
	})
	require.NoError(t, err)

	_, err = c.DeregisterContainerInstance(ctx, &ecs.DeregisterContainerInstanceInput{
		Cluster: aws.String(cluster), ContainerInstance: aws.String(ciArn), Force: aws.Bool(true),
	})
	require.NoError(t, err)
}

// TestECS_AccountSettings exercises PutAccountSetting, PutAccountSettingDefault,
// ListAccountSettings and DeleteAccountSetting — aws_ecs_account_setting_default.
func TestECS_AccountSettings(t *testing.T) {
	c := ecsClient()

	putOut, err := c.PutAccountSetting(ctx, &ecs.PutAccountSettingInput{
		Name: ecstypes.SettingNameServiceLongArnFormat, Value: aws.String("enabled"),
	})
	require.NoError(t, err)
	require.NotNil(t, putOut.Setting)
	assert.Equal(t, "enabled", aws.ToString(putOut.Setting.Value))

	_, err = c.PutAccountSettingDefault(ctx, &ecs.PutAccountSettingDefaultInput{
		Name: ecstypes.SettingNameTaskLongArnFormat, Value: aws.String("enabled"),
	})
	require.NoError(t, err)

	listOut, err := c.ListAccountSettings(ctx, &ecs.ListAccountSettingsInput{
		Name: ecstypes.SettingNameServiceLongArnFormat,
	})
	require.NoError(t, err)
	require.NotEmpty(t, listOut.Settings)

	delOut, err := c.DeleteAccountSetting(ctx, &ecs.DeleteAccountSettingInput{
		Name: ecstypes.SettingNameServiceLongArnFormat,
	})
	require.NoError(t, err)
	assert.Equal(t, ecstypes.SettingNameServiceLongArnFormat, delOut.Setting.Name)
}

// TestECS_Attributes exercises PutAttributes, ListAttributes and
// DeleteAttributes — the placement-constraint label surface.
func TestECS_Attributes(t *testing.T) {
	c := ecsClient()
	cluster := "attr-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	attr := ecstypes.Attribute{
		Name:       aws.String("stack"),
		Value:      aws.String("prod"),
		TargetType: ecstypes.TargetTypeContainerInstance,
		TargetId:   aws.String("ci-123"),
	}
	_, err = c.PutAttributes(ctx, &ecs.PutAttributesInput{
		Cluster: aws.String(cluster), Attributes: []ecstypes.Attribute{attr},
	})
	require.NoError(t, err)

	listOut, err := c.ListAttributes(ctx, &ecs.ListAttributesInput{
		Cluster: aws.String(cluster), TargetType: ecstypes.TargetTypeContainerInstance, AttributeName: aws.String("stack"),
	})
	require.NoError(t, err)
	require.Len(t, listOut.Attributes, 1)
	assert.Equal(t, "prod", aws.ToString(listOut.Attributes[0].Value))

	_, err = c.DeleteAttributes(ctx, &ecs.DeleteAttributesInput{
		Cluster: aws.String(cluster), Attributes: []ecstypes.Attribute{attr},
	})
	require.NoError(t, err)

	listOut2, err := c.ListAttributes(ctx, &ecs.ListAttributesInput{
		Cluster: aws.String(cluster), TargetType: ecstypes.TargetTypeContainerInstance,
	})
	require.NoError(t, err)
	assert.Empty(t, listOut2.Attributes)
}

// TestECS_TaskProtection exercises GetTaskProtection and UpdateTaskProtection
// against a running task.
func TestECS_TaskProtection(t *testing.T) {
	c := ecsClient()
	cluster := "prot-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	_, err = c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String("prot-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("app"), Image: aws.String("alpine:latest")}},
	})
	require.NoError(t, err)
	runOut, err := c.RunTask(ctx, &ecs.RunTaskInput{
		Cluster: aws.String(cluster), TaskDefinition: aws.String("prot-task"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runOut.Tasks)
	taskArn := aws.ToString(runOut.Tasks[0].TaskArn)
	t.Cleanup(func() {
		_, _ = c.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: aws.String(taskArn)})
	})

	updOut, err := c.UpdateTaskProtection(ctx, &ecs.UpdateTaskProtectionInput{
		Cluster: aws.String(cluster), Tasks: []string{taskArn}, ProtectionEnabled: true, ExpiresInMinutes: aws.Int32(60),
	})
	require.NoError(t, err)
	require.Len(t, updOut.ProtectedTasks, 1)
	assert.True(t, updOut.ProtectedTasks[0].ProtectionEnabled)
	require.NotNil(t, updOut.ProtectedTasks[0].ExpirationDate)

	getOut, err := c.GetTaskProtection(ctx, &ecs.GetTaskProtectionInput{
		Cluster: aws.String(cluster), Tasks: []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, getOut.ProtectedTasks, 1)
	assert.True(t, getOut.ProtectedTasks[0].ProtectionEnabled)

	// Disable protection.
	_, err = c.UpdateTaskProtection(ctx, &ecs.UpdateTaskProtectionInput{
		Cluster: aws.String(cluster), Tasks: []string{taskArn}, ProtectionEnabled: false,
	})
	require.NoError(t, err)
}

// TestECS_StartTask runs a task on a specific registered container instance.
func TestECS_StartTask(t *testing.T) {
	c := ecsClient()
	cluster := "start-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	_, err = c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("start-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("app"),
			Image:      aws.String("alpine:latest"),
			Privileged: aws.Bool(true),
			Command:    []string{"sh", "-c", "mkdir -p /tmp/start-task-mount && mount -t tmpfs tmpfs /tmp/start-task-mount && umount /tmp/start-task-mount"},
		}},
	})
	require.NoError(t, err)
	regOut, err := c.RegisterContainerInstance(ctx, &ecs.RegisterContainerInstanceInput{Cluster: aws.String(cluster)})
	require.NoError(t, err)
	ciArn := aws.ToString(regOut.ContainerInstance.ContainerInstanceArn)

	startOut, err := c.StartTask(ctx, &ecs.StartTaskInput{
		Cluster:            aws.String(cluster),
		ContainerInstances: []string{ciArn},
		TaskDefinition:     aws.String("start-task"),
	})
	require.NoError(t, err)
	require.Len(t, startOut.Tasks, 1)
	assert.Equal(t, "PROVISIONING", aws.ToString(startOut.Tasks[0].LastStatus))
	assert.Equal(t, ciArn, aws.ToString(startOut.Tasks[0].ContainerInstanceArn))
	taskARN := aws.ToString(startOut.Tasks[0].TaskArn)
	waitForECSTaskStatus(t, c, cluster, taskARN, "STOPPED")
	described, err := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   []string{taskARN},
	})
	require.NoError(t, err)
	require.Len(t, described.Tasks, 1)
	require.Len(t, described.Tasks[0].Containers, 1)
	require.NotNil(t, described.Tasks[0].Containers[0].ExitCode)
	assert.Equal(t, int32(0), *described.Tasks[0].Containers[0].ExitCode)
	assert.Equal(t, ecstypes.LaunchTypeEc2, described.Tasks[0].LaunchType)
}

// TestECS_DeleteTaskDefinitions deletes a deregistered (INACTIVE) revision and
// surfaces the ACTIVE-revision failure shape.
func TestECS_DeleteTaskDefinitions(t *testing.T) {
	c := ecsClient()
	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String("del-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("app"), Image: aws.String("alpine:latest")}},
	})
	require.NoError(t, err)
	arn := aws.ToString(reg.TaskDefinition.TaskDefinitionArn)

	// An ACTIVE revision cannot be deleted — it's a failure.
	failOut, err := c.DeleteTaskDefinitions(ctx, &ecs.DeleteTaskDefinitionsInput{TaskDefinitions: []string{arn}})
	require.NoError(t, err)
	require.Len(t, failOut.Failures, 1)

	_, err = c.DeregisterTaskDefinition(ctx, &ecs.DeregisterTaskDefinitionInput{TaskDefinition: aws.String(arn)})
	require.NoError(t, err)

	delOut, err := c.DeleteTaskDefinitions(ctx, &ecs.DeleteTaskDefinitionsInput{TaskDefinitions: []string{arn}})
	require.NoError(t, err)
	require.Len(t, delOut.TaskDefinitions, 1)
	assert.Empty(t, delOut.Failures)
}

// TestECS_DaemonLifecycle exercises the daemon control-plane: CreateDaemon,
// DescribeDaemon, UpdateDaemon, ListDaemons, DeleteDaemon, the daemon-deployment
// + daemon-revision read-backs, and the daemon-task-definition family.
func TestECS_DaemonLifecycle(t *testing.T) {
	c := ecsClient()
	cluster := "daemon-cluster"
	clOut, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	clusterArn := aws.ToString(clOut.Cluster.ClusterArn)
	t.Cleanup(func() { _, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	// Daemon task definition family.
	regDtd, err := c.RegisterDaemonTaskDefinition(ctx, &ecs.RegisterDaemonTaskDefinitionInput{
		Family: aws.String("daemon-td"),
		ContainerDefinitions: []ecstypes.DaemonContainerDefinition{
			{Name: aws.String("agent"), Image: aws.String("alpine:latest")},
		},
	})
	require.NoError(t, err)
	dtdArn := aws.ToString(regDtd.DaemonTaskDefinitionArn)
	require.NotEmpty(t, dtdArn)

	descDtd, err := c.DescribeDaemonTaskDefinition(ctx, &ecs.DescribeDaemonTaskDefinitionInput{
		DaemonTaskDefinition: aws.String(dtdArn),
	})
	require.NoError(t, err)
	assert.Equal(t, "daemon-td", aws.ToString(descDtd.DaemonTaskDefinition.Family))

	listDtd, err := c.ListDaemonTaskDefinitions(ctx, &ecs.ListDaemonTaskDefinitionsInput{
		Family: aws.String("daemon-td"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, listDtd.DaemonTaskDefinitions)

	// Daemon CRUD.
	createOut, err := c.CreateDaemon(ctx, &ecs.CreateDaemonInput{
		DaemonName:              aws.String("my-daemon"),
		ClusterArn:              aws.String(clusterArn),
		DaemonTaskDefinitionArn: aws.String(dtdArn),
		CapacityProviderArns:    []string{"FARGATE"},
	})
	require.NoError(t, err)
	daemonArn := aws.ToString(createOut.DaemonArn)
	require.NotEmpty(t, daemonArn)
	deploymentArn := aws.ToString(createOut.DeploymentArn)
	require.NotEmpty(t, deploymentArn)

	descDaemon, err := c.DescribeDaemon(ctx, &ecs.DescribeDaemonInput{DaemonArn: aws.String(daemonArn)})
	require.NoError(t, err)
	require.NotNil(t, descDaemon.Daemon)
	require.NotEmpty(t, descDaemon.Daemon.CurrentRevisions)
	revArn := aws.ToString(descDaemon.Daemon.CurrentRevisions[0].Arn)
	require.NotEmpty(t, revArn)

	listDaemons, err := c.ListDaemons(ctx, &ecs.ListDaemonsInput{ClusterArn: aws.String(clusterArn)})
	require.NoError(t, err)
	require.NotEmpty(t, listDaemons.DaemonSummariesList)

	_, err = c.UpdateDaemon(ctx, &ecs.UpdateDaemonInput{
		DaemonArn:               aws.String(daemonArn),
		DaemonTaskDefinitionArn: aws.String(dtdArn),
		CapacityProviderArns:    []string{"FARGATE", "FARGATE_SPOT"},
	})
	require.NoError(t, err)

	descDep, err := c.DescribeDaemonDeployments(ctx, &ecs.DescribeDaemonDeploymentsInput{
		DaemonDeploymentArns: []string{deploymentArn},
	})
	require.NoError(t, err)
	require.Len(t, descDep.DaemonDeployments, 1)

	listDep, err := c.ListDaemonDeployments(ctx, &ecs.ListDaemonDeploymentsInput{DaemonArn: aws.String(daemonArn)})
	require.NoError(t, err)
	require.NotEmpty(t, listDep.DaemonDeployments)

	descRev, err := c.DescribeDaemonRevisions(ctx, &ecs.DescribeDaemonRevisionsInput{
		DaemonRevisionArns: []string{revArn},
	})
	require.NoError(t, err)
	require.Len(t, descRev.DaemonRevisions, 1)

	_, err = c.DeleteDaemon(ctx, &ecs.DeleteDaemonInput{DaemonArn: aws.String(daemonArn)})
	require.NoError(t, err)

	_, err = c.DeleteDaemonTaskDefinition(ctx, &ecs.DeleteDaemonTaskDefinitionInput{
		DaemonTaskDefinition: aws.String(dtdArn),
	})
	require.NoError(t, err)
}

// TestECS_ServiceDeployments exercises the rollout-tracking surface:
// ListServiceDeployments, DescribeServiceDeployments, DescribeServiceRevisions,
// StopServiceDeployment, ContinueServiceDeployment, and ListServicesByNamespace.
func TestECS_ServiceDeployments(t *testing.T) {
	c := ecsClient()
	cluster := "sd-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)}) })

	_, err = c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("sd-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)

	namespace := "arn:aws:servicediscovery:us-east-1:000000000000:namespace/ns-sd"
	_, err = c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String("sd-svc"),
		TaskDefinition: aws.String("sd-task"),
		DesiredCount:   aws.Int32(1),
		ServiceConnectConfiguration: &ecstypes.ServiceConnectConfiguration{
			Enabled:   true,
			Namespace: aws.String(namespace),
		},
	})
	require.NoError(t, err)
	cleanupECSService(t, c, cluster, "sd-svc")

	listOut, err := c.ListServiceDeployments(ctx, &ecs.ListServiceDeploymentsInput{
		Cluster: aws.String(cluster), Service: aws.String("sd-svc"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, listOut.ServiceDeployments)
	depArn := aws.ToString(listOut.ServiceDeployments[0].ServiceDeploymentArn)
	revArn := aws.ToString(listOut.ServiceDeployments[0].TargetServiceRevisionArn)

	descOut, err := c.DescribeServiceDeployments(ctx, &ecs.DescribeServiceDeploymentsInput{
		ServiceDeploymentArns: []string{depArn},
	})
	require.NoError(t, err)
	require.Len(t, descOut.ServiceDeployments, 1)

	revOut, err := c.DescribeServiceRevisions(ctx, &ecs.DescribeServiceRevisionsInput{
		ServiceRevisionArns: []string{revArn},
	})
	require.NoError(t, err)
	require.Len(t, revOut.ServiceRevisions, 1)

	_, err = c.StopServiceDeployment(ctx, &ecs.StopServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(depArn),
	})
	require.NoError(t, err)

	_, err = c.ContinueServiceDeployment(ctx, &ecs.ContinueServiceDeploymentInput{
		ServiceDeploymentArn: aws.String(depArn),
		HookId:               aws.String("hook-1"),
		Action:               ecstypes.DeploymentLifecycleHookActionContinue,
	})
	require.NoError(t, err)

	nsOut, err := c.ListServicesByNamespace(ctx, &ecs.ListServicesByNamespaceInput{
		Namespace: aws.String(namespace),
	})
	require.NoError(t, err)
	require.Contains(t, nsOut.ServiceArns, aws.ToString(listOut.ServiceDeployments[0].ServiceArn))
}
