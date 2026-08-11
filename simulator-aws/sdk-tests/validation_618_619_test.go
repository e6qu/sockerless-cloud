package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_RequestValidation covers issue #618 — three documented ECS request
// constraints the sim previously under-validated.
func TestECS_RequestValidation(t *testing.T) {
	c := ecsClient()

	// Case 1: Fargate task def without task-level cpu/memory → ClientException.
	_, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("probe-fargate-nocpu"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{Name: aws.String("c"), Image: aws.String("nginx"), Essential: aws.Bool(true)},
		},
	})
	assert.Equal(t, "ClientException", errCode(t, err))

	// Control: the same with cpu+memory is accepted.
	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("probe-fargate-ok"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{Name: aws.String("c"), Image: aws.String("nginx"), Essential: aws.Bool(true)},
		},
	})
	require.NoError(t, err)

	// Case 2: RunTask count > 10 → InvalidParameterException.
	_, err = c.RunTask(ctx, &ecs.RunTaskInput{
		TaskDefinition: reg.TaskDefinition.TaskDefinitionArn,
		Count:          aws.Int32(11),
	})
	assert.Equal(t, "InvalidParameterException", errCode(t, err))

	// Case 3: DescribeTasks with an empty tasks list → InvalidParameterException.
	_, err = c.DescribeTasks(ctx, &ecs.DescribeTasksInput{Tasks: []string{}})
	assert.Equal(t, "InvalidParameterException", errCode(t, err))
}

// TestScheduler_ExpressionValidation covers issue #619 — CreateSchedule must
// reject a ScheduleExpression that isn't a valid at()/rate()/cron() form.
func TestScheduler_ExpressionValidation(t *testing.T) {
	c := schedulerClient()
	mk := func(name, expr string) error {
		_, err := c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
			Name:               aws.String(name),
			ScheduleExpression: aws.String(expr),
			FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
			Target: &schedtypes.Target{
				Arn:     aws.String("arn:aws:lambda:us-east-1:123456789012:function:x"),
				RoleArn: aws.String("arn:aws:iam::123456789012:role/x"),
			},
		})
		return err
	}
	assert.Equal(t, "ValidationException", errCode(t, mk("probe-badexpr", "not-a-valid-expression")))
	// Valid forms are accepted.
	for i, expr := range []string{"rate(5 minutes)", "cron(0 12 * * ? *)", "at(2030-01-01T00:00:00)"} {
		require.NoError(t, mk("probe-ok-"+string(rune('a'+i)), expr), "expr %q should be valid", expr)
	}
}
