package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchedulerCLI_ScheduleLifecycle drives `aws scheduler` create/get/delete
// against the REST-JSON EventBridge Scheduler surface.
func TestSchedulerCLI_ScheduleLifecycle(t *testing.T) {
	const name = "cli-schedule"

	createOut := runCLI(t, awsCLI("scheduler", "create-schedule",
		"--name", name,
		"--schedule-expression", "rate(1 hour)",
		"--flexible-time-window", `{"Mode":"OFF"}`,
		"--target", `{"Arn":"arn:aws:lambda:us-east-1:123456789012:function:cli","RoleArn":"arn:aws:iam::123456789012:role/scheduler-role"}`,
		"--output", "json"))
	var created struct {
		ScheduleArn string `json:"ScheduleArn"`
	}
	parseJSON(t, createOut, &created)
	assert.Contains(t, created.ScheduleArn, ":schedule/default/"+name)
	t.Cleanup(func() {
		_ = awsCLI("scheduler", "delete-schedule", "--name", name).Run()
	})

	getOut := runCLI(t, awsCLI("scheduler", "get-schedule", "--name", name, "--output", "json"))
	var got struct {
		Name               string `json:"Name"`
		GroupName          string `json:"GroupName"`
		ScheduleExpression string `json:"ScheduleExpression"`
		State              string `json:"State"`
		Target             struct {
			Arn string `json:"Arn"`
		} `json:"Target"`
	}
	parseJSON(t, getOut, &got)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, "default", got.GroupName)
	assert.Equal(t, "rate(1 hour)", got.ScheduleExpression)
	assert.Equal(t, "ENABLED", got.State)
	assert.Equal(t, "arn:aws:lambda:us-east-1:123456789012:function:cli", got.Target.Arn)

	runCLI(t, awsCLI("scheduler", "delete-schedule", "--name", name))

	// Deleted schedule must 404.
	err := awsCLI("scheduler", "get-schedule", "--name", name).Run()
	require.Error(t, err, "get-schedule on a deleted schedule must fail")
}
