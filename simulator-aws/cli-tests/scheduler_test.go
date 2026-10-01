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

// TestSchedulerCLI_EventBridgeAndUniversalTargets round-trips an EventBridge
// PutEvents target and a universal target through `aws scheduler`.
func TestSchedulerCLI_EventBridgeAndUniversalTargets(t *testing.T) {
	for name, target := range map[string]string{
		"cli-eventbridge-target": `{"Arn":"arn:aws:events:us-east-1:123456789012:event-bus/default","RoleArn":"arn:aws:iam::123456789012:role/scheduler-role","Input":"{\"run\":\"nightly\"}","EventBridgeParameters":{"DetailType":"Nightly Run","Source":"scheduler.cli"}}`,
		"cli-universal-target":   `{"Arn":"arn:aws:scheduler:::aws-sdk:sqs:sendMessage","RoleArn":"arn:aws:iam::123456789012:role/scheduler-role","Input":"{\"QueueUrl\":\"https://sqs.us-east-1.amazonaws.com/123456789012/cli\",\"MessageBody\":\"hello\"}"}`,
	} {
		runCLI(t, awsCLI("scheduler", "create-schedule", "--name", name,
			"--schedule-expression", "rate(1 hour)", "--flexible-time-window", `{"Mode":"OFF"}`,
			"--target", target, "--output", "json"))
		t.Cleanup(func() { _ = awsCLI("scheduler", "delete-schedule", "--name", name).Run() })

		var got struct {
			Target struct {
				Arn                   string `json:"Arn"`
				Input                 string `json:"Input"`
				EventBridgeParameters *struct {
					DetailType string `json:"DetailType"`
					Source     string `json:"Source"`
				} `json:"EventBridgeParameters"`
			} `json:"Target"`
		}
		parseJSON(t, runCLI(t, awsCLI("scheduler", "get-schedule", "--name", name, "--output", "json")), &got)
		var want struct {
			Arn   string `json:"Arn"`
			Input string `json:"Input"`
		}
		parseJSON(t, target, &want)
		assert.Equal(t, want.Arn, got.Target.Arn, name)
		assert.Equal(t, want.Input, got.Target.Input, name)
		if name == "cli-eventbridge-target" {
			require.NotNil(t, got.Target.EventBridgeParameters)
			assert.Equal(t, "Nightly Run", got.Target.EventBridgeParameters.DetailType)
			assert.Equal(t, "scheduler.cli", got.Target.EventBridgeParameters.Source)
		}
	}
}

// TestSchedulerCLI_RetryPolicyRange proves `aws scheduler create-schedule` is
// refused a MaximumRetryAttempts above the model's 185.
func TestSchedulerCLI_RetryPolicyRange(t *testing.T) {
	out := runCLIExpectError(t, awsCLI("scheduler", "create-schedule", "--name", "cli-retry-range",
		"--schedule-expression", "rate(1 hour)", "--flexible-time-window", `{"Mode":"OFF"}`,
		"--target", `{"Arn":"arn:aws:lambda:us-east-1:123456789012:function:cli","RoleArn":"arn:aws:iam::123456789012:role/scheduler-role","RetryPolicy":{"MaximumRetryAttempts":186}}`))
	assert.Contains(t, out, "ValidationException")
}
