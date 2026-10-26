package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_RequestValidation covers three documented Amazon ECS request
// constraints.
func TestECS_RequestValidation(t *testing.T) {
	t.Parallel()
	c := ecsClient()

	// A Fargate task definition without task-level cpu and memory is a
	// ClientException.
	_, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("probe-fargate-nocpu"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{StopTimeout: aws.Int32(2), Name: aws.String("c"), Image: aws.String("mirror.gcr.io/library/nginx:latest"), Essential: aws.Bool(true)},
		},
	})
	assert.Equal(t, "ClientException", errCode(t, err))

	// With cpu and memory it registers.
	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("probe-fargate-ok"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{StopTimeout: aws.Int32(2), Name: aws.String("c"), Image: aws.String("mirror.gcr.io/library/nginx:latest"), Essential: aws.Bool(true)},
		},
	})
	require.NoError(t, err)

	// RunTask with count over 10 is an InvalidParameterException.
	_, err = c.RunTask(ctx, &ecs.RunTaskInput{
		TaskDefinition: reg.TaskDefinition.TaskDefinitionArn,
		Count:          aws.Int32(11),
	})
	assert.Equal(t, "InvalidParameterException", errCode(t, err))

	// DescribeTasks with an empty list is an InvalidParameterException.
	_, err = c.DescribeTasks(ctx, &ecs.DescribeTasksInput{Tasks: []string{}})
	assert.Equal(t, "InvalidParameterException", errCode(t, err))
}

// TestScheduler_ExpressionValidation expects CreateSchedule to refuse an
// expression that is not an at(), rate() or cron() form.
func TestScheduler_ExpressionValidation(t *testing.T) {
	t.Parallel()
	c := schedulerClient()
	mk := func(name, expr string) error {
		_, err := c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
			Name:               aws.String(name),
			ScheduleExpression: aws.String(expr),
			FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
			Target: &schedtypes.Target{
				Arn:     aws.String("arn:aws:lambda:us-east-1:123456789012:function:x"),
				RoleArn: aws.String(serviceRole(t, "scheduler.amazonaws.com")),
			},
		})
		return err
	}
	assert.Equal(t, "ValidationException", errCode(t, mk("probe-badexpr", "not-a-valid-expression")))
	for _, expr := range []string{"rate(5 minutes)", "cron(0 12 * * ? *)", "at(2030-01-01T00:00:00)"} {
		require.NoError(t, mk(uniquename.New("probe-ok"), expr), "expr %q should be valid", expr)
	}
}
