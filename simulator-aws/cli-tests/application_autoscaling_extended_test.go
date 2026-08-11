package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appASCLIRegister registers a scalable target via the CLI and registers a
// deregister cleanup.
func appASCLIRegister(t *testing.T, ns, resourceID, dim string) {
	t.Helper()
	runCLI(t, awsCLI("application-autoscaling", "register-scalable-target",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--scalable-dimension", dim,
		"--min-capacity", "1",
		"--max-capacity", "5"))
	t.Cleanup(func() {
		_ = awsCLI("application-autoscaling", "deregister-scalable-target",
			"--service-namespace", ns,
			"--resource-id", resourceID,
			"--scalable-dimension", dim).Run()
	})
}

func TestApplicationAutoScalingCLI_ScheduledAction(t *testing.T) {
	const (
		ns         = "ecs"
		resourceID = "service/cli-sched-cluster/cli-sched-svc"
		dim        = "ecs:service:DesiredCount"
		actionName = "cli-scale-up"
	)
	appASCLIRegister(t, ns, resourceID, dim)

	runCLI(t, awsCLI("application-autoscaling", "put-scheduled-action",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--scalable-dimension", dim,
		"--scheduled-action-name", actionName,
		"--schedule", "cron(0 8 * * ? *)",
		"--timezone", "UTC",
		"--scalable-target-action", `{"MinCapacity":2,"MaxCapacity":10}`))
	t.Cleanup(func() {
		_ = awsCLI("application-autoscaling", "delete-scheduled-action",
			"--service-namespace", ns,
			"--resource-id", resourceID,
			"--scalable-dimension", dim,
			"--scheduled-action-name", actionName).Run()
	})

	descOut := runCLI(t, awsCLI("application-autoscaling", "describe-scheduled-actions",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--output", "json"))
	var desc struct {
		ScheduledActions []struct {
			ScheduledActionName  string `json:"ScheduledActionName"`
			ScheduledActionARN   string `json:"ScheduledActionARN"`
			Schedule             string `json:"Schedule"`
			ScalableTargetAction struct {
				MinCapacity int `json:"MinCapacity"`
				MaxCapacity int `json:"MaxCapacity"`
			} `json:"ScalableTargetAction"`
		} `json:"ScheduledActions"`
	}
	parseJSON(t, descOut, &desc)
	require.Len(t, desc.ScheduledActions, 1)
	sa := desc.ScheduledActions[0]
	assert.Equal(t, actionName, sa.ScheduledActionName)
	assert.Contains(t, sa.ScheduledActionARN, ":scheduledAction:")
	assert.Equal(t, "cron(0 8 * * ? *)", sa.Schedule)
	assert.Equal(t, 2, sa.ScalableTargetAction.MinCapacity)
	assert.Equal(t, 10, sa.ScalableTargetAction.MaxCapacity)

	runCLI(t, awsCLI("application-autoscaling", "delete-scheduled-action",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--scalable-dimension", dim,
		"--scheduled-action-name", actionName))

	descAfter := runCLI(t, awsCLI("application-autoscaling", "describe-scheduled-actions",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--output", "json"))
	var after struct {
		ScheduledActions []struct {
			ScheduledActionName string `json:"ScheduledActionName"`
		} `json:"ScheduledActions"`
	}
	parseJSON(t, descAfter, &after)
	assert.Empty(t, after.ScheduledActions)
}

func TestApplicationAutoScalingCLI_DescribeScalingActivities(t *testing.T) {
	const (
		ns         = "ecs"
		resourceID = "service/cli-activity-cluster/cli-activity-svc"
		dim        = "ecs:service:DesiredCount"
	)
	appASCLIRegister(t, ns, resourceID, dim)

	out := runCLI(t, awsCLI("application-autoscaling", "describe-scaling-activities",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--scalable-dimension", dim,
		"--output", "json"))
	var res struct {
		ScalingActivities []struct {
			ActivityId string `json:"ActivityId"`
		} `json:"ScalingActivities"`
	}
	parseJSON(t, out, &res)
	// No real capacity change has happened, so the activity log is empty.
	assert.Empty(t, res.ScalingActivities)
}

func TestApplicationAutoScalingCLI_GetPredictiveScalingForecast(t *testing.T) {
	const (
		ns         = "ecs"
		resourceID = "service/cli-forecast-cluster/cli-forecast-svc"
		dim        = "ecs:service:DesiredCount"
		policyName = "cli-predictive"
	)
	appASCLIRegister(t, ns, resourceID, dim)

	out := runCLI(t, awsCLI("application-autoscaling", "get-predictive-scaling-forecast",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--scalable-dimension", dim,
		"--policy-name", policyName,
		"--start-time", "2020-01-01T00:00:00Z",
		"--end-time", "2020-01-02T00:00:00Z",
		"--output", "json"))
	var res struct {
		LoadForecast []any  `json:"LoadForecast"`
		UpdateTime   string `json:"UpdateTime"`
	}
	parseJSON(t, out, &res)
	assert.Empty(t, res.LoadForecast)
	assert.NotEmpty(t, res.UpdateTime)
}
