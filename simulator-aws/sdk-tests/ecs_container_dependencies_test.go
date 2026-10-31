package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ecsDependencyImage = "mirror.gcr.io/library/busybox:latest"

// ecsDependencyContainer is a busybox container logging to logGroup under the
// stream prefix "ecs", with the task's shared volume at /shared.
func ecsDependencyContainer(name, logGroup, script string, essential bool, dependsOn ...ecstypes.ContainerDependency) ecstypes.ContainerDefinition {
	return ecstypes.ContainerDefinition{
		Name:        aws.String(name),
		Image:       aws.String(ecsDependencyImage),
		Essential:   aws.Bool(essential),
		StopTimeout: aws.Int32(2),
		Command:     []string{"sh", "-c", script},
		DependsOn:   dependsOn,
		MountPoints: []ecstypes.MountPoint{{SourceVolume: aws.String("shared"), ContainerPath: aws.String("/shared")}},
		LogConfiguration: &ecstypes.LogConfiguration{
			LogDriver: ecstypes.LogDriverAwslogs,
			Options: map[string]string{
				"awslogs-group":         logGroup,
				"awslogs-create-group":  "true",
				"awslogs-stream-prefix": "ecs",
			},
		},
	}
}

// runECSDependencyTask registers containers as one Fargate task definition
// with a shared volume and runs it on its own cluster and subnet.
func runECSDependencyTask(t *testing.T, name string, containers ...ecstypes.ContainerDefinition) (*ecs.Client, string, string) {
	t.Helper()
	client := ecsClient()
	clusterName := name
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
	})
	subnetID := createECSTestSubnet(t, name)
	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(name),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		Volumes:                 []ecstypes.Volume{{Name: aws.String("shared")}},
		ContainerDefinitions:    containers,
	})
	require.NoError(t, err)
	run, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: td.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := aws.ToString(run.Tasks[0].TaskArn)
	cleanupECSTask(t, client, clusterName, taskArn)
	return client, clusterName, taskArn
}

func ecsTaskContainerNamed(t *testing.T, task ecstypes.Task, name string) ecstypes.Container {
	t.Helper()
	for _, c := range task.Containers {
		if aws.ToString(c.Name) == name {
			return c
		}
	}
	t.Fatalf("task %s has no container %s", aws.ToString(task.TaskArn), name)
	return ecstypes.Container{}
}

func ecsContainerLog(t *testing.T, logGroup, container string) string {
	t.Helper()
	events, err := cwLogsClient().FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:        aws.String(logGroup),
		LogStreamNamePrefix: aws.String("ecs/" + container + "/"),
	})
	require.NoError(t, err)
	var lines []string
	for _, e := range events.Events {
		lines = append(lines, aws.ToString(e.Message))
	}
	return strings.Join(lines, "\n")
}

// A COMPLETE dependency holds the essential container until the setup
// container exits, with any code. The setup container reads its own metadata
// while the essential container waits on it, and leaves a file on the shared
// volume the essential container then reads.
func TestECS_ContainerDependencyCompleteStartsAfterSetupExits(t *testing.T) {
	t.Parallel()
	const logGroup = "/ecs/dependency-complete"
	client, cluster, taskArn := runECSDependencyTask(t, "dependency-complete",
		ecsDependencyContainer("app", logGroup,
			`cat /shared/ready || exit 1; echo APP-RAN`, true,
			ecstypes.ContainerDependency{ContainerName: aws.String("setup"), Condition: ecstypes.ContainerConditionComplete}),
		ecsDependencyContainer("setup", logGroup,
			`wget -qO- "$ECS_CONTAINER_METADATA_URI_V4"; echo; echo setup-wrote > /shared/ready; exit 3`, false),
	)

	stopped := waitTaskStopped(t, client, cluster, taskArn)
	assert.Equal(t, ecstypes.TaskStopCodeEssentialContainerExited, stopped.StopCode)
	setup := ecsTaskContainerNamed(t, stopped, "setup")
	app := ecsTaskContainerNamed(t, stopped, "app")
	require.NotNil(t, setup.ExitCode)
	assert.Equal(t, int32(3), *setup.ExitCode, "COMPLETE accepts any exit code")
	require.NotNil(t, app.ExitCode)
	assert.Equal(t, int32(0), *app.ExitCode, "app found the setup container's file")

	var setupLog, appLog string
	require.Eventually(t, func() bool {
		setupLog = ecsContainerLog(t, logGroup, "setup")
		appLog = ecsContainerLog(t, logGroup, "app")
		return strings.Contains(appLog, "APP-RAN") && strings.Contains(setupLog, `"Name":"setup"`)
	}, time.Minute, waiterMinDelay, "setup log %q, app log %q", setupLog, appLog)
	assert.Contains(t, appLog, "setup-wrote")
}

// A SUCCESS dependency that exits non-zero can never be met: the container
// that names it never starts and the task stops.
func TestECS_ContainerDependencySuccessFailureStopsTask(t *testing.T) {
	t.Parallel()
	const logGroup = "/ecs/dependency-success-fails"
	client, cluster, taskArn := runECSDependencyTask(t, "dependency-success-fails",
		ecsDependencyContainer("setup", logGroup, `echo SETUP-FAILED; exit 1`, false),
		ecsDependencyContainer("app", logGroup, `echo APP-RAN`, true,
			ecstypes.ContainerDependency{ContainerName: aws.String("setup"), Condition: ecstypes.ContainerConditionSuccess}),
	)

	stopped := waitTaskStopped(t, client, cluster, taskArn)
	assert.Equal(t, ecstypes.TaskStopCodeEssentialContainerExited, stopped.StopCode)
	assert.Contains(t, aws.ToString(stopped.StoppedReason), "setup")
	setup := ecsTaskContainerNamed(t, stopped, "setup")
	app := ecsTaskContainerNamed(t, stopped, "app")
	require.NotNil(t, setup.ExitCode)
	assert.Equal(t, int32(1), *setup.ExitCode)
	assert.Equal(t, "STOPPED", aws.ToString(app.LastStatus))
	assert.Nil(t, app.ExitCode, "app never started, so it has no exit code")
	assert.Nil(t, stopped.StartedAt, "the task never reached RUNNING")
	assert.NotContains(t, ecsContainerLog(t, logGroup, "app"), "APP-RAN")
}

// A HEALTHY dependency holds the dependent container until the dependency's
// health check passes, on the check's own interval; the task is RUNNING only
// once both containers run.
func TestECS_ContainerDependencyHealthyStartsAfterHealthCheckPasses(t *testing.T) {
	t.Parallel()
	const logGroup = "/ecs/dependency-healthy"
	db := ecsDependencyContainer("db", logGroup, `touch /tmp/ready; exec sleep 600`, true)
	db.HealthCheck = &ecstypes.HealthCheck{
		Command:  []string{"CMD-SHELL", "test -f /tmp/ready"},
		Interval: aws.Int32(5),
		Retries:  aws.Int32(3),
		Timeout:  aws.Int32(2),
	}
	client, cluster, taskArn := runECSDependencyTask(t, "dependency-healthy",
		db,
		ecsDependencyContainer("app", logGroup, `echo APP-RAN; exec sleep 600`, true,
			ecstypes.ContainerDependency{ContainerName: aws.String("db"), Condition: ecstypes.ContainerConditionHealthy}),
	)

	running := waitForECSTasksRunning(t, client, cluster, time.Minute, taskArn).Tasks[0]
	assert.Equal(t, ecstypes.HealthStatusHealthy, ecsTaskContainerNamed(t, running, "db").HealthStatus)
	assert.Equal(t, "RUNNING", aws.ToString(ecsTaskContainerNamed(t, running, "app").LastStatus))
	assert.Equal(t, ecstypes.HealthStatusHealthy, running.HealthStatus,
		"db is the only essential container with a health check")
}

// A HEALTHY dependency that does not pass within its startTimeout fails the
// task without starting the container that waits on it.
func TestECS_ContainerDependencyStartTimeoutStopsTask(t *testing.T) {
	t.Parallel()
	const logGroup = "/ecs/dependency-timeout"
	db := ecsDependencyContainer("db", logGroup, `exec sleep 600`, true)
	db.HealthCheck = &ecstypes.HealthCheck{
		Command:  []string{"CMD-SHELL", "exit 1"},
		Interval: aws.Int32(5),
	}
	db.StartTimeout = aws.Int32(2)
	client, cluster, taskArn := runECSDependencyTask(t, "dependency-timeout",
		db,
		ecsDependencyContainer("app", logGroup, `echo APP-RAN`, true,
			ecstypes.ContainerDependency{ContainerName: aws.String("db"), Condition: ecstypes.ContainerConditionHealthy}),
	)

	stopped := waitTaskStopped(t, client, cluster, taskArn)
	assert.Contains(t, aws.ToString(stopped.StoppedReason), "db")
	app := ecsTaskContainerNamed(t, stopped, "app")
	assert.Nil(t, app.ExitCode, "app never started")
	assert.NotNil(t, ecsTaskContainerNamed(t, stopped, "db").ExitCode)
}

// While a container waits on its dependency the task and that container are
// PENDING; stopping the task then never starts it.
func TestECS_ContainerDependencyStopWhileWaiting(t *testing.T) {
	t.Parallel()
	const logGroup = "/ecs/dependency-stop-waiting"
	client, cluster, taskArn := runECSDependencyTask(t, "dependency-stop-waiting",
		ecsDependencyContainer("setup", logGroup, `exec sleep 600`, false),
		ecsDependencyContainer("app", logGroup, `echo APP-RAN; exec sleep 600`, true,
			ecstypes.ContainerDependency{ContainerName: aws.String("setup"), Condition: ecstypes.ContainerConditionComplete}),
	)

	// Amazon ECS offers no waiter on a container's status; read it on the suite's
	// waiter cadence.
	var task ecstypes.Task
	require.Eventually(t, func() bool {
		out, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(cluster), Tasks: []string{taskArn}})
		require.NoError(t, err)
		require.Len(t, out.Tasks, 1)
		task = out.Tasks[0]
		return aws.ToString(ecsTaskContainerNamed(t, task, "setup").LastStatus) == "RUNNING"
	}, time.Minute, waiterMinDelay)
	assert.Equal(t, "PENDING", aws.ToString(task.LastStatus))
	assert.Equal(t, "PENDING", aws.ToString(ecsTaskContainerNamed(t, task, "app").LastStatus))

	_, err := client.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: aws.String(taskArn), Reason: aws.String("stop while waiting")})
	require.NoError(t, err)
	stopped := waitTaskStopped(t, client, cluster, taskArn)
	assert.Equal(t, ecstypes.TaskStopCodeUserInitiated, stopped.StopCode)
	assert.Equal(t, "stop while waiting", aws.ToString(stopped.StoppedReason))
	app := ecsTaskContainerNamed(t, stopped, "app")
	assert.Equal(t, "STOPPED", aws.ToString(app.LastStatus))
	assert.Nil(t, app.ExitCode, "app never started")
	assert.NotContains(t, ecsContainerLog(t, logGroup, "app"), "APP-RAN")
}

// RegisterTaskDefinition refuses a dependsOn naming a container the task
// definition does not define, and round-trips one it does.
func TestECS_RegisterTaskDefinitionContainerDependencies(t *testing.T) {
	t.Parallel()
	client := ecsClient()
	register := func(family, dependency string) (*ecs.RegisterTaskDefinitionOutput, error) {
		return client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
			Family: aws.String(family),
			ContainerDefinitions: []ecstypes.ContainerDefinition{
				{Name: aws.String("setup"), Image: aws.String(ecsDependencyImage), Essential: aws.Bool(false), Memory: aws.Int32(64), StartTimeout: aws.Int32(30)},
				{
					Name: aws.String("app"), Image: aws.String(ecsDependencyImage), Memory: aws.Int32(64),
					DependsOn: []ecstypes.ContainerDependency{{ContainerName: aws.String(dependency), Condition: ecstypes.ContainerConditionSuccess}},
				},
			},
		})
	}

	_, err := register("dependency-unknown", "missing")
	requireECSAPIErrorCode(t, err, "ClientException")

	out, err := register("dependency-roundtrip", "setup")
	require.NoError(t, err)
	described, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: out.TaskDefinition.TaskDefinitionArn})
	require.NoError(t, err)
	defs := described.TaskDefinition.ContainerDefinitions
	require.Len(t, defs, 2)
	assert.Equal(t, int32(30), aws.ToInt32(defs[0].StartTimeout))
	require.Len(t, defs[1].DependsOn, 1)
	assert.Equal(t, "setup", aws.ToString(defs[1].DependsOn[0].ContainerName))
	assert.Equal(t, ecstypes.ContainerConditionSuccess, defs[1].DependsOn[0].Condition)
}
