package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/stretchr/testify/require"
)

// TestScheduler_FiresECSTarget covers the Scheduler target contract: a schedule
// whose expression becomes due must actually invoke its target. Uses a one-time
// at() expression a few seconds in the future so the firing loop launches the
// ECS task, which then appears in ListTasks.
func TestScheduler_FiresECSTarget(t *testing.T) {
	ecsc := ecsClient()
	sched := schedulerClient()

	const cluster = "scheduler-fire-cluster"
	cc, err := ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	clusterArn := aws.ToString(cc.Cluster.ClusterArn)

	td, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("scheduler-fire-task"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("app"),
			Image:      aws.String("public.ecr.aws/docker/library/busybox:latest"),
			EntryPoint: []string{"sh", "-c"},
			Command:    []string{"sleep 5"},
		}},
	})
	require.NoError(t, err)
	tdArn := aws.ToString(td.TaskDefinition.TaskDefinitionArn)
	subnetID := createECSTestSubnet(t, "scheduler-fire")

	fireAt := time.Now().UTC().Add(4 * time.Second).Format("2006-01-02T15:04:05")
	_, err = sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String("fire-ecs"),
		ScheduleExpression: aws.String("at(" + fireAt + ")"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String(clusterArn),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/scheduler"),
			EcsParameters: &schedtypes.EcsParameters{
				TaskDefinitionArn: aws.String(tdArn),
				LaunchType:        schedtypes.LaunchTypeFargate,
				NetworkConfiguration: &schedtypes.NetworkConfiguration{
					AwsvpcConfiguration: &schedtypes.AwsVpcConfiguration{
						Subnets: []string{subnetID},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sched.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String("fire-ecs")})
		lt, _ := ecsc.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster)})
		for _, ta := range lt.TaskArns {
			_, _ = ecsc.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String(cluster), Task: aws.String(ta)})
		}
	})

	require.Eventually(t, func() bool {
		lt, err := ecsc.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(cluster)})
		return err == nil && len(lt.TaskArns) >= 1
	}, 25*time.Second, 1*time.Second, "scheduler must launch the ECS task at the scheduled time")
}
