package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/stretchr/testify/require"
)

// TestEventBridgeAndScheduler_StartStepFunctionsExecutions_SDK validates both
// AWS-managed event paths with official clients: an EventBridge rule and an
// EventBridge Scheduler one-time schedule each start a real AWS Step Functions
// execution that reaches SUCCEEDED.
func TestEventBridgeAndScheduler_StartStepFunctionsExecutions_SDK(t *testing.T) {
	stepFunctions := sfnClient()
	events := eventbridgeClient()
	schedules := schedulerClient()
	stateMachineName := "eventing-runtime-state-machine"
	ruleName := "eventing-runtime-rule"
	scheduleName := "eventing-runtime-schedule"

	stateMachine, err := stepFunctions.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String(stateMachineName),
		Definition: aws.String(`{"StartAt":"Complete","States":{"Complete":{"Type":"Pass","End":true}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/step-functions-eventing"),
		Type:       sfntypes.StateMachineTypeStandard,
	})
	require.NoError(t, err)
	stateMachineARN := aws.ToString(stateMachine.StateMachineArn)
	t.Cleanup(func() {
		_, _ = stepFunctions.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: aws.String(stateMachineARN)})
	})

	_, err = events.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(ruleName),
		EventPattern: aws.String(`{"source":["external.validation"]}`),
		State:        ebtypes.RuleStateEnabled,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = events.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
			Rule: aws.String(ruleName),
			Ids:  []string{"step-functions"},
		})
		_, _ = events.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(ruleName)})
	})
	targets, err := events.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String(ruleName),
		Targets: []ebtypes.Target{{
			Id:      aws.String("step-functions"),
			Arn:     aws.String(stateMachineARN),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/eventbridge-step-functions"),
		}},
	})
	require.NoError(t, err)
	require.Zero(t, targets.FailedEntryCount)
	put, err := events.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{{
			Source:     aws.String("external.validation"),
			DetailType: aws.String("External validation"),
			Detail:     aws.String(`{"path":"eventbridge"}`),
		}},
	})
	require.NoError(t, err)
	require.Zero(t, put.FailedEntryCount)
	require.Eventually(t, func() bool {
		executions, listErr := stepFunctions.ListExecutions(ctx, &sfn.ListExecutionsInput{
			StateMachineArn: aws.String(stateMachineARN),
			StatusFilter:    sfntypes.ExecutionStatusSucceeded,
		})
		return listErr == nil && len(executions.Executions) >= 1
	}, 10*time.Second, 200*time.Millisecond)

	fireAt := time.Now().UTC().Add(2 * time.Second).Format("2006-01-02T15:04:05")
	_, err = schedules.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(scheduleName),
		ScheduleExpression: aws.String("at(" + fireAt + ")"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String(stateMachineARN),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/scheduler-step-functions"),
			Input:   aws.String(`{"path":"scheduler"}`),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = schedules.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(scheduleName)})
	})
	require.Eventually(t, func() bool {
		executions, listErr := stepFunctions.ListExecutions(ctx, &sfn.ListExecutionsInput{
			StateMachineArn: aws.String(stateMachineARN),
			StatusFilter:    sfntypes.ExecutionStatusSucceeded,
		})
		return listErr == nil && len(executions.Executions) >= 2
	}, 10*time.Second, 200*time.Millisecond)
}
