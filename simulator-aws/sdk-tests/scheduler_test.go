package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func schedulerClient() *scheduler.Client {
	return scheduler.NewFromConfig(sdkConfig(), func(o *scheduler.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestScheduler_ScheduleLifecycle pins the EventBridge Scheduler flow that
// terraform-provider-aws drives for `aws_scheduler_schedule`: create in the
// implicit "default" group, read back the round-tripped target + flexible
// time window, update the expression, then delete.
func TestScheduler_ScheduleLifecycle(t *testing.T) {
	c := schedulerClient()
	const name = "runner-nightly"

	createOut, err := c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(name),
		ScheduleExpression: aws.String("rate(1 hour)"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{
			Mode: schedtypes.FlexibleTimeWindowModeOff,
		},
		Target: &schedtypes.Target{
			Arn:     aws.String("arn:aws:lambda:us-east-1:123456789012:function:runner-job"),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/scheduler-role"),
		},
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(createOut.ScheduleArn), ":schedule/default/"+name)
	t.Cleanup(func() {
		_, _ = c.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(name)})
	})

	getOut, err := c.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(getOut.Name))
	assert.Equal(t, "default", aws.ToString(getOut.GroupName))
	assert.Equal(t, "rate(1 hour)", aws.ToString(getOut.ScheduleExpression))
	assert.Equal(t, schedtypes.ScheduleStateEnabled, getOut.State)
	require.NotNil(t, getOut.Target, "Target must round-trip")
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:function:runner-job", aws.ToString(getOut.Target.Arn))
	require.NotNil(t, getOut.FlexibleTimeWindow)
	assert.Equal(t, schedtypes.FlexibleTimeWindowModeOff, getOut.FlexibleTimeWindow.Mode)

	// Duplicate create must conflict.
	_, err = c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(name),
		ScheduleExpression: aws.String("rate(2 hours)"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String("arn:aws:lambda:us-east-1:123456789012:function:x"),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/scheduler-role"),
		},
	})
	require.Error(t, err, "duplicate schedule must raise ConflictException")

	// Update the expression and disable it.
	_, err = c.UpdateSchedule(ctx, &scheduler.UpdateScheduleInput{
		Name:               aws.String(name),
		ScheduleExpression: aws.String("cron(0 2 * * ? *)"),
		State:              schedtypes.ScheduleStateDisabled,
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String("arn:aws:lambda:us-east-1:123456789012:function:runner-job"),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/scheduler-role"),
		},
	})
	require.NoError(t, err)
	getOut, err = c.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "cron(0 2 * * ? *)", aws.ToString(getOut.ScheduleExpression))
	assert.Equal(t, schedtypes.ScheduleStateDisabled, getOut.State)

	// ListSchedules surfaces it.
	listOut, err := c.ListSchedules(ctx, &scheduler.ListSchedulesInput{NamePrefix: aws.String("runner-")})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.Schedules {
		if aws.ToString(s.Name) == name {
			found = true
		}
	}
	assert.True(t, found, "ListSchedules must include the created schedule")

	_, err = c.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(name)})
	require.NoError(t, err)
	_, err = c.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(name)})
	require.Error(t, err, "deleted schedule must 404")
}

// TestScheduler_ScheduleGroups covers the custom-group path: create a group,
// place a schedule in it, and list both group and schedule scoped to it.
func TestScheduler_ScheduleGroups(t *testing.T) {
	c := schedulerClient()
	const group = "runner-group"

	_, err := c.CreateScheduleGroup(ctx, &scheduler.CreateScheduleGroupInput{Name: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteScheduleGroup(ctx, &scheduler.DeleteScheduleGroupInput{Name: aws.String(group)})
	})

	grpOut, err := c.GetScheduleGroup(ctx, &scheduler.GetScheduleGroupInput{Name: aws.String(group)})
	require.NoError(t, err)
	assert.Equal(t, group, aws.ToString(grpOut.Name))
	assert.Contains(t, aws.ToString(grpOut.Arn), ":schedule-group/"+group)

	const name = "grouped-schedule"
	_, err = c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(name),
		GroupName:          aws.String(group),
		ScheduleExpression: aws.String("rate(30 minutes)"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String("arn:aws:lambda:us-east-1:123456789012:function:g"),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/scheduler-role"),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{
			Name: aws.String(name), GroupName: aws.String(group),
		})
	})

	// GetSchedule must be scoped by group — the default group does not see it.
	_, err = c.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(name)})
	require.Error(t, err, "schedule in a custom group must not resolve under the default group")

	getOut, err := c.GetSchedule(ctx, &scheduler.GetScheduleInput{
		Name: aws.String(name), GroupName: aws.String(group),
	})
	require.NoError(t, err)
	assert.Equal(t, group, aws.ToString(getOut.GroupName))

	// ListSchedules scoped to the group returns only its schedules.
	listOut, err := c.ListSchedules(ctx, &scheduler.ListSchedulesInput{GroupName: aws.String(group)})
	require.NoError(t, err)
	require.Len(t, listOut.Schedules, 1)
	assert.Equal(t, name, aws.ToString(listOut.Schedules[0].Name))
}
