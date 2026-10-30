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

// Each container of a task reads its own record at its
// ECS_CONTAINER_METADATA_URI_V4, its engine stats at /stats, and every
// container's stats, by Docker ID, at /task/stats.
func TestECS_TaskMetadataV4PerContainerRecordAndStats(t *testing.T) {
	t.Parallel()
	ecsc := ecsClient()
	cw := cwLogsClient()
	_, subnetID := createECSTestVPCSubnet(t, "metadata-stats")

	clusterName := "metadata-stats"
	_, err := ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	logGroup := "/ecs/metadata-stats"
	// Each container reads while both run, then stays up so neither read races
	// the task's end.
	read := `wget -qO- "$ECS_CONTAINER_METADATA_URI_V4"; echo; echo STATS; wget -qO- "$ECS_CONTAINER_METADATA_URI_V4/stats"; echo; echo TASKSTATS; wget -qO- "$ECS_CONTAINER_METADATA_URI_V4/task/stats"; echo; echo DONE; exec sleep 600`
	container := func(name string) ecstypes.ContainerDefinition {
		return ecstypes.ContainerDefinition{
			Name:      aws.String(name),
			Image:     aws.String("mirror.gcr.io/library/busybox:latest"),
			Essential: aws.Bool(true),
			Command:   []string{"sh", "-c", read},
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
	td, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("metadata-stats"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions:    []ecstypes.ContainerDefinition{container("first"), container("second")},
	})
	require.NoError(t, err)
	run, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
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
	cleanupECSTask(t, ecsc, clusterName, taskArn)
	require.NoError(t, ecs.NewTasksRunningWaiter(ecsc).Wait(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName), Tasks: []string{taskArn},
	}, 2*time.Minute))

	logged := func(name string) string {
		events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName:        aws.String(logGroup),
			LogStreamNamePrefix: aws.String("ecs/" + name + "/"),
		})
		require.NoError(t, err)
		var lines []string
		for _, e := range events.Events {
			lines = append(lines, aws.ToString(e.Message))
		}
		return strings.Join(lines, "\n")
	}
	// CloudWatch Logs offers nothing to wait on, so read the streams on the
	// suite's waiter cadence until both containers have finished their reads.
	outs := map[string]string{}
	require.Eventually(t, func() bool {
		for _, name := range []string{"first", "second"} {
			outs[name] = logged(name)
			if !strings.Contains(outs[name], "DONE") {
				return false
			}
		}
		return true
	}, 2*time.Minute, waiterMinDelay)
	for _, name := range []string{"first", "second"} {
		out := outs[name]
		record, rest, found := strings.Cut(out, "STATS")
		require.True(t, found, "container %s logged no stats: %s", name, out)
		assert.Contains(t, record, `"Name":"`+name+`"`, "container %s reads its own record", name)
		stats, taskStats, found := strings.Cut(rest, "TASKSTATS")
		require.True(t, found, "container %s logged no task stats: %s", name, out)
		assert.Contains(t, stats, `"cpu_stats"`, "container %s reads its engine stats", name)
		assert.Contains(t, taskStats, `"cpu_stats"`, "container %s reads the task's stats", name)
	}
}
