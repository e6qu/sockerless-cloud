package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/stretchr/testify/require"
)

// TestScheduler_ECSRunTaskFailureSurfaced covers scheduler-fired RunTask error
// reporting: when the target API rejects the request (here, a security group
// that does not exist), it must NOT be recorded as a phantom success. Real
// CloudTrail records the RunTask attempt WITH an errorCode, and — because
// RunTask failed — no task is created and none ever transitions to STOPPED.
func TestScheduler_ECSRunTaskFailureSurfaced(t *testing.T) {
	ecsc := ecsClient()
	sched := schedulerClient()
	ct := cloudTrailClient()

	const cluster = "sched-runtask-fail-cluster"
	cc, err := ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	clusterArn := aws.ToString(cc.Cluster.ClusterArn)

	td, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("sched-runtask-fail-task"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String("app"),
			Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
		}},
	})
	require.NoError(t, err)
	tdArn := aws.ToString(td.TaskDefinition.TaskDefinitionArn)
	subnetID := createECSTestSubnet(t, "scheduler-fail")

	const scheduleName = "fire-ecs-badsg"
	fireAt := time.Now().UTC().Add(4 * time.Second).Format("2006-01-02T15:04:05")
	_, err = sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(scheduleName),
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
						Subnets:        []string{subnetID},
						SecurityGroups: []string{"sg-doesnotexist0514"}, // not created in the sim
					},
				},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sched.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(scheduleName)})
	})

	// The fire must record a RunTask CloudTrail event carrying an errorCode (not
	// a clean success).
	var runTaskErr *cttypes.Event
	require.Eventually(t, func() bool {
		evs, lerr := ct.LookupEvents(ctx, &cloudtrail.LookupEventsInput{
			LookupAttributes: []cttypes.LookupAttribute{{
				AttributeKey: cttypes.LookupAttributeKeyEventName, AttributeValue: aws.String("RunTask"),
			}},
		})
		if lerr != nil {
			return false
		}
		for i := range evs.Events {
			rec := aws.ToString(evs.Events[i].CloudTrailEvent)
			if strings.Contains(rec, "scheduler.amazonaws.com") && strings.Contains(rec, `"errorCode"`) {
				runTaskErr = &evs.Events[i]
				return true
			}
		}
		return false
	}, 25*time.Second, 1*time.Second, "scheduler-fired RunTask failure must be recorded in CloudTrail with an errorCode")

	rec := aws.ToString(runTaskErr.CloudTrailEvent)
	require.Contains(t, rec, "InvalidParameterException", "the errorCode must reflect the real RunTask rejection")

	// And no task was created — certainly none transitioned to STOPPED.
	stopped, err := ecsc.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(cluster), DesiredStatus: ecstypes.DesiredStatusStopped,
	})
	require.NoError(t, err)
	require.Empty(t, stopped.TaskArns, "a rejected RunTask must not leave a phantom STOPPED task")
}
