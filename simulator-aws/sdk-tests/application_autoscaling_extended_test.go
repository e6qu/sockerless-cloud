package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appASRegisterTarget registers a scalable target for the extended-op tests and
// schedules its deregistration.
func appASRegisterTarget(t *testing.T, c *applicationautoscaling.Client, ns aastypes.ServiceNamespace, resourceID string, dim aastypes.ScalableDimension) {
	t.Helper()
	_, err := c.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		MinCapacity:       aws.Int32(1),
		MaxCapacity:       aws.Int32(5),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeregisterScalableTarget(ctx, &applicationautoscaling.DeregisterScalableTargetInput{
			ServiceNamespace:  ns,
			ResourceId:        aws.String(resourceID),
			ScalableDimension: dim,
		})
	})
}

func TestApplicationAutoScaling_ScheduledActionLifecycle(t *testing.T) {
	c := appAutoScalingClient()
	const (
		ns         = aastypes.ServiceNamespaceEcs
		resourceID = "service/sched-cluster/sched-svc"
		dim        = aastypes.ScalableDimensionECSServiceDesiredCount
		actionName = "scale-up-mornings"
	)
	appASRegisterTarget(t, c, ns, resourceID, dim)

	_, err := c.PutScheduledAction(ctx, &applicationautoscaling.PutScheduledActionInput{
		ServiceNamespace:    ns,
		ResourceId:          aws.String(resourceID),
		ScalableDimension:   dim,
		ScheduledActionName: aws.String(actionName),
		Schedule:            aws.String("cron(0 8 * * ? *)"),
		Timezone:            aws.String("UTC"),
		ScalableTargetAction: &aastypes.ScalableTargetAction{
			MinCapacity: aws.Int32(2),
			MaxCapacity: aws.Int32(10),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteScheduledAction(ctx, &applicationautoscaling.DeleteScheduledActionInput{
			ServiceNamespace:    ns,
			ResourceId:          aws.String(resourceID),
			ScalableDimension:   dim,
			ScheduledActionName: aws.String(actionName),
		})
	})

	desc, err := c.DescribeScheduledActions(ctx, &applicationautoscaling.DescribeScheduledActionsInput{
		ServiceNamespace:     ns,
		ResourceId:           aws.String(resourceID),
		ScheduledActionNames: []string{actionName},
	})
	require.NoError(t, err)
	require.Len(t, desc.ScheduledActions, 1)
	sa := desc.ScheduledActions[0]
	assert.Equal(t, actionName, *sa.ScheduledActionName)
	assert.Equal(t, "cron(0 8 * * ? *)", *sa.Schedule)
	assert.Equal(t, "UTC", *sa.Timezone)
	require.NotNil(t, sa.ScheduledActionARN)
	assert.Contains(t, *sa.ScheduledActionARN, ":scheduledAction:")
	require.NotNil(t, sa.ScalableTargetAction)
	require.NotNil(t, sa.ScalableTargetAction.MinCapacity)
	assert.Equal(t, int32(2), *sa.ScalableTargetAction.MinCapacity)

	_, err = c.DeleteScheduledAction(ctx, &applicationautoscaling.DeleteScheduledActionInput{
		ServiceNamespace:    ns,
		ResourceId:          aws.String(resourceID),
		ScalableDimension:   dim,
		ScheduledActionName: aws.String(actionName),
	})
	require.NoError(t, err)

	descAfter, err := c.DescribeScheduledActions(ctx, &applicationautoscaling.DescribeScheduledActionsInput{
		ServiceNamespace:     ns,
		ResourceId:           aws.String(resourceID),
		ScheduledActionNames: []string{actionName},
	})
	require.NoError(t, err)
	assert.Empty(t, descAfter.ScheduledActions, "scheduled action must be gone after delete")
}

func TestApplicationAutoScaling_DescribeScalingActivities(t *testing.T) {
	c := appAutoScalingClient()
	const (
		ns         = aastypes.ServiceNamespaceEcs
		resourceID = "service/activity-cluster/activity-svc"
		dim        = aastypes.ScalableDimensionECSServiceDesiredCount
	)
	appASRegisterTarget(t, c, ns, resourceID, dim)

	// No real capacity change has occurred, so the activity log is empty —
	// faithful to AWS, not fabricated.
	out, err := c.DescribeScalingActivities(ctx, &applicationautoscaling.DescribeScalingActivitiesInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
	})
	require.NoError(t, err)
	assert.Empty(t, out.ScalingActivities)
}

func TestApplicationAutoScaling_GetPredictiveScalingForecast(t *testing.T) {
	c := appAutoScalingClient()
	const (
		ns         = aastypes.ServiceNamespaceEcs
		resourceID = "service/forecast-cluster/forecast-svc"
		dim        = aastypes.ScalableDimensionECSServiceDesiredCount
		policyName = "predictive-policy"
	)
	appASRegisterTarget(t, c, ns, resourceID, dim)

	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	out, err := c.GetPredictiveScalingForecast(ctx, &applicationautoscaling.GetPredictiveScalingForecastInput{
		ServiceNamespace:  ns,
		ResourceId:        aws.String(resourceID),
		ScalableDimension: dim,
		PolicyName:        aws.String(policyName),
		StartTime:         aws.Time(start),
		EndTime:           aws.Time(start.Add(24 * time.Hour)),
	})
	require.NoError(t, err)
	// No historical data to forecast from: empty load/capacity curves with an
	// UpdateTime, matching a freshly-created target.
	assert.Empty(t, out.LoadForecast)
	require.NotNil(t, out.UpdateTime)
}
