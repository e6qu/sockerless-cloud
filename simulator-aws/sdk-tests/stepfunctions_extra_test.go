package aws_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSFN_LambdaTaskHistory_SDK verifies the real direct-Lambda integration
// lifecycle, including the service events that surround the Task state.
func TestSFN_LambdaTaskHistory_SDK(t *testing.T) {
	lambdaAPI := lambdaClient()
	functionName := "sfn-sdk-history-lambda"
	_, err := lambdaAPI.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code: &lambdatypes.FunctionCode{
			ImageUri: aws.String(lambdaHandlerImageName),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaAPI.DeleteFunction(ctx, &lambda.DeleteFunctionInput{
			FunctionName: aws.String(functionName),
		})
	})

	statesAPI := sfnClient()
	functionARN := lambdaFunctionArn(functionName)
	definition := fmt.Sprintf(
		`{"StartAt":"Invoke","States":{"Invoke":{"Type":"Task","Resource":%q,"End":true}}}`,
		functionARN,
	)
	created, err := statesAPI.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-lambda-history"),
		Definition: aws.String(definition),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = statesAPI.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
			StateMachineArn: created.StateMachineArn,
		})
	})
	started, err := statesAPI.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: created.StateMachineArn,
		Input:           aws.String(`{"request":"history"}`),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		described, describeErr := statesAPI.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
			ExecutionArn: started.ExecutionArn,
		})
		return describeErr == nil && described.Status == sfntypes.ExecutionStatusSucceeded
	}, 20*time.Second, 100*time.Millisecond)

	history, err := statesAPI.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{
		ExecutionArn: started.ExecutionArn,
	})
	require.NoError(t, err)
	types := make([]sfntypes.HistoryEventType, 0, len(history.Events))
	for _, event := range history.Events {
		types = append(types, event.Type)
	}
	assert.Equal(t, []sfntypes.HistoryEventType{
		sfntypes.HistoryEventTypeExecutionStarted,
		sfntypes.HistoryEventTypeTaskStateEntered,
		sfntypes.HistoryEventTypeLambdaFunctionScheduled,
		sfntypes.HistoryEventTypeLambdaFunctionStarted,
		sfntypes.HistoryEventTypeLambdaFunctionSucceeded,
		sfntypes.HistoryEventTypeTaskStateExited,
		sfntypes.HistoryEventTypeExecutionSucceeded,
	}, types)
	require.NotNil(t, history.Events[2].LambdaFunctionScheduledEventDetails)
	assert.Equal(t, functionARN, aws.ToString(history.Events[2].LambdaFunctionScheduledEventDetails.Resource))
	assert.JSONEq(t, `{"request":"history"}`, aws.ToString(history.Events[2].LambdaFunctionScheduledEventDetails.Input))
	require.NotNil(t, history.Events[0].ExecutionStartedEventDetails.InputDetails)
	assert.False(t, history.Events[0].ExecutionStartedEventDetails.InputDetails.Truncated)
	require.NotNil(t, history.Events[1].StateEnteredEventDetails.InputDetails)
	assert.False(t, history.Events[1].StateEnteredEventDetails.InputDetails.Truncated)
	require.NotNil(t, history.Events[2].LambdaFunctionScheduledEventDetails.InputDetails)
	assert.False(t, history.Events[2].LambdaFunctionScheduledEventDetails.InputDetails.Truncated)
	require.NotNil(t, history.Events[4].LambdaFunctionSucceededEventDetails)
	assert.JSONEq(t, `{"request":"history"}`,
		aws.ToString(history.Events[4].LambdaFunctionSucceededEventDetails.Output))
	require.NotNil(t, history.Events[5].StateExitedEventDetails.OutputDetails)
	assert.False(t, history.Events[5].StateExitedEventDetails.OutputDetails.Truncated)
	require.NotNil(t, history.Events[6].ExecutionSucceededEventDetails.OutputDetails)
	assert.False(t, history.Events[6].ExecutionSucceededEventDetails.OutputDetails.Truncated)

	withoutData, err := statesAPI.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{
		ExecutionArn:         started.ExecutionArn,
		IncludeExecutionData: aws.Bool(false),
	})
	require.NoError(t, err)
	require.Len(t, withoutData.Events, len(history.Events))
	assert.Nil(t, withoutData.Events[0].ExecutionStartedEventDetails.Input)
	require.NotNil(t, withoutData.Events[0].ExecutionStartedEventDetails.InputDetails)
	assert.False(t, withoutData.Events[0].ExecutionStartedEventDetails.InputDetails.Truncated)
	assert.Nil(t, withoutData.Events[6].ExecutionSucceededEventDetails.Output)
	require.NotNil(t, withoutData.Events[6].ExecutionSucceededEventDetails.OutputDetails)
	assert.False(t, withoutData.Events[6].ExecutionSucceededEventDetails.OutputDetails.Truncated)
}

// TestSFN_NestedWorkflowIntegration_SDK executes the optimized
// states:startExecution.sync:2 integration against a second real state
// machine and verifies both the parent result and the independently
// discoverable child execution.
func TestSFN_NestedWorkflowIntegration_SDK(t *testing.T) {
	c := sfnClient()
	child, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-nested-child"),
		Definition: aws.String(`{"StartAt":"Complete","States":{"Complete":{"Type":"Pass","Parameters":{"child.$":"$"},"End":true}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: child.StateMachineArn})
	})
	parentDefinition := fmt.Sprintf(
		`{"StartAt":"Child","States":{"Child":{"Type":"Task","Resource":"arn:aws:states:::states:startExecution.sync:2","Parameters":{"StateMachineArn":%q,"Input":{"order":"A-1"}},"OutputPath":"$.Output","End":true}}}`,
		aws.ToString(child.StateMachineArn),
	)
	parent, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-nested-parent"),
		Definition: aws.String(parentDefinition),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: parent.StateMachineArn})
	})
	started, err := c.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: parent.StateMachineArn})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		described, describeErr := c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: started.ExecutionArn})
		return describeErr == nil && described.Status == sfntypes.ExecutionStatusSucceeded &&
			aws.ToString(described.Output) == `{"child":{"order":"A-1"}}`
	}, 10*time.Second, 100*time.Millisecond)
	children, err := c.ListExecutions(ctx, &sfn.ListExecutionsInput{StateMachineArn: child.StateMachineArn})
	require.NoError(t, err)
	require.Len(t, children.Executions, 1)
	assert.Equal(t, sfntypes.ExecutionStatusSucceeded, children.Executions[0].Status)
}

// TestSFN_Activities_SDK covers CreateActivity / DescribeActivity /
// ListActivities / DeleteActivity and the GetActivityTask + SendTask*
// task-token lifecycle.
func TestSFN_Activities_SDK(t *testing.T) {
	c := sfnClient()

	create, err := c.CreateActivity(ctx, &sfn.CreateActivityInput{
		Name: aws.String("sfn-sdk-activity"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(create.ActivityArn))
	t.Cleanup(func() {
		_, _ = c.DeleteActivity(ctx, &sfn.DeleteActivityInput{ActivityArn: create.ActivityArn})
	})

	desc, err := c.DescribeActivity(ctx, &sfn.DescribeActivityInput{ActivityArn: create.ActivityArn})
	require.NoError(t, err)
	assert.Equal(t, "sfn-sdk-activity", aws.ToString(desc.Name))
	assert.Equal(t, aws.ToString(create.ActivityArn), aws.ToString(desc.ActivityArn))

	list, err := c.ListActivities(ctx, &sfn.ListActivitiesInput{})
	require.NoError(t, err)
	found := false
	for _, a := range list.Activities {
		if aws.ToString(a.Name) == "sfn-sdk-activity" {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.TagResource(ctx, &sfn.TagResourceInput{
		ResourceArn: create.ActivityArn,
		Tags:        []sfntypes.Tag{{Key: aws.String("worker"), Value: aws.String("sdk")}},
	})
	require.NoError(t, err)
	tagged, err := c.ListTagsForResource(ctx, &sfn.ListTagsForResourceInput{ResourceArn: create.ActivityArn})
	require.NoError(t, err)
	require.Len(t, tagged.Tags, 1)

	machine, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-activity-sm"),
		Definition: aws.String(fmt.Sprintf(`{"StartAt":"Work","States":{"Work":{"Type":"Task","Resource":%q,"End":true}}}`, aws.ToString(create.ActivityArn))),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: machine.StateMachineArn})
	})
	execution, err := c.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: machine.StateMachineArn,
		Input:           aws.String(`{"job":"real"}`),
	})
	require.NoError(t, err)

	task, err := c.GetActivityTask(ctx, &sfn.GetActivityTaskInput{
		ActivityArn: create.ActivityArn,
		WorkerName:  aws.String("worker-1"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(task.TaskToken))
	assert.JSONEq(t, `{"job":"real"}`, aws.ToString(task.Input))
	_, err = c.SendTaskHeartbeat(ctx, &sfn.SendTaskHeartbeatInput{TaskToken: task.TaskToken})
	require.NoError(t, err)
	_, err = c.SendTaskSuccess(ctx, &sfn.SendTaskSuccessInput{
		TaskToken: task.TaskToken,
		Output:    aws.String(`{"completed":true}`),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		result, describeErr := c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: execution.ExecutionArn})
		return describeErr == nil && result.Status == sfntypes.ExecutionStatusSucceeded &&
			aws.ToString(result.Output) == `{"completed":true}`
	}, 10e9, 1e8)

	failedExecution, err := c.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: machine.StateMachineArn,
		Input:           aws.String(`{"job":"fail"}`),
	})
	require.NoError(t, err)
	failedTask, err := c.GetActivityTask(ctx, &sfn.GetActivityTaskInput{
		ActivityArn: create.ActivityArn,
		WorkerName:  aws.String("worker-1"),
	})
	require.NoError(t, err)
	_, err = c.SendTaskFailure(ctx, &sfn.SendTaskFailureInput{
		TaskToken: failedTask.TaskToken,
		Error:     aws.String("Worker.Failed"),
		Cause:     aws.String("real worker failure"),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		result, describeErr := c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: failedExecution.ExecutionArn})
		return describeErr == nil && result.Status == sfntypes.ExecutionStatusFailed
	}, 10e9, 1e8)
}

// TestSFN_VersionsAndAliases_SDK covers PublishStateMachineVersion,
// CreateStateMachineAlias, DescribeStateMachineAlias,
// ListStateMachineAliases, UpdateStateMachineAlias,
// DeleteStateMachineAlias, DeleteStateMachineVersion.
func TestSFN_VersionsAndAliases_SDK(t *testing.T) {
	c := sfnClient()

	sm, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-ver-sm"),
		Definition: aws.String(`{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
		Publish:    true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: sm.StateMachineArn})
	})

	pub, err := c.PublishStateMachineVersion(ctx, &sfn.PublishStateMachineVersionInput{
		StateMachineArn: sm.StateMachineArn,
		Description:     aws.String("v1"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(pub.StateMachineVersionArn))

	alias, err := c.CreateStateMachineAlias(ctx, &sfn.CreateStateMachineAliasInput{
		Name:        aws.String("PROD"),
		Description: aws.String("prod alias"),
		RoutingConfiguration: []sfntypes.RoutingConfigurationListItem{
			{StateMachineVersionArn: pub.StateMachineVersionArn, Weight: 100},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(alias.StateMachineAliasArn))
	aliasDeleted := false
	t.Cleanup(func() {
		if !aliasDeleted {
			_, _ = c.DeleteStateMachineAlias(ctx, &sfn.DeleteStateMachineAliasInput{StateMachineAliasArn: alias.StateMachineAliasArn})
		}
	})

	da, err := c.DescribeStateMachineAlias(ctx, &sfn.DescribeStateMachineAliasInput{
		StateMachineAliasArn: alias.StateMachineAliasArn,
	})
	require.NoError(t, err)
	assert.Equal(t, "PROD", aws.ToString(da.Name))
	require.Len(t, da.RoutingConfiguration, 1)
	assert.Equal(t, aws.ToString(pub.StateMachineVersionArn), aws.ToString(da.RoutingConfiguration[0].StateMachineVersionArn))
	assert.Equal(t, int32(100), da.RoutingConfiguration[0].Weight)

	la, err := c.ListStateMachineAliases(ctx, &sfn.ListStateMachineAliasesInput{
		StateMachineArn: sm.StateMachineArn,
	})
	require.NoError(t, err)
	require.Len(t, la.StateMachineAliases, 1)
	assert.Equal(t, aws.ToString(alias.StateMachineAliasArn), aws.ToString(la.StateMachineAliases[0].StateMachineAliasArn))

	_, err = c.UpdateStateMachineAlias(ctx, &sfn.UpdateStateMachineAliasInput{
		StateMachineAliasArn: alias.StateMachineAliasArn,
		Description:          aws.String("updated"),
	})
	require.NoError(t, err)
	da, err = c.DescribeStateMachineAlias(ctx, &sfn.DescribeStateMachineAliasInput{
		StateMachineAliasArn: alias.StateMachineAliasArn,
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(da.Description))

	_, err = c.DeleteStateMachineVersion(ctx, &sfn.DeleteStateMachineVersionInput{
		StateMachineVersionArn: pub.StateMachineVersionArn,
	})
	require.ErrorContains(t, err, "ConflictException")
	_, err = c.DeleteStateMachineAlias(ctx, &sfn.DeleteStateMachineAliasInput{
		StateMachineAliasArn: alias.StateMachineAliasArn,
	})
	require.NoError(t, err)
	aliasDeleted = true
	_, err = c.DeleteStateMachineVersion(ctx, &sfn.DeleteStateMachineVersionInput{
		StateMachineVersionArn: pub.StateMachineVersionArn,
	})
	require.NoError(t, err)
}

// TestSFN_TestState_SDK runs a single Pass state synchronously and asserts the
// real evaluated output.
func TestSFN_TestState_SDK(t *testing.T) {
	c := sfnClient()

	out, err := c.TestState(ctx, &sfn.TestStateInput{
		Definition: aws.String(`{"Type":"Pass","Result":{"hello":"world"},"End":true}`),
		Input:      aws.String(`{"x":1}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	assert.Equal(t, sfntypes.TestExecutionStatusSucceeded, out.Status)
	assert.JSONEq(t, `{"hello":"world"}`, aws.ToString(out.Output))
}

// TestSFN_StartSyncExecution_SDK runs a whole EXPRESS-shaped state machine
// synchronously and returns the terminal result inline.
func TestSFN_StartSyncExecution_SDK(t *testing.T) {
	c := sfnClient()

	sm, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-sync-sm"),
		Definition: aws.String(`{"StartAt":"P","States":{"P":{"Type":"Pass","Result":{"ok":true},"End":true}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
		Type:       sfntypes.StateMachineTypeExpress,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: sm.StateMachineArn})
	})

	// StartSyncExecution carries the modeled `sync-` endpoint host prefix, so
	// the request the SDK signs and sends addresses sync-states.<endpoint> —
	// the same host shape real AWS serves it on.
	var sent capturedRequest
	out, err := c.StartSyncExecution(ctx, &sfn.StartSyncExecutionInput{
		StateMachineArn: sm.StateMachineArn,
		Input:           aws.String(`{}`),
	}, func(o *sfn.Options) {
		o.APIOptions = append(o.APIOptions, captureSignedRequest(&sent))
	})
	require.NoError(t, err)
	assert.Equal(t, sfntypes.SyncExecutionStatusSucceeded, out.Status)
	assert.JSONEq(t, `{"ok":true}`, aws.ToString(out.Output))
	require.NotEmpty(t, aws.ToString(out.ExecutionArn))
	assert.Equal(t, fmt.Sprintf("sync-states.localhost:%d", simPort), sent.host)
	assert.Contains(t, sent.signedHeaders(), "host")
}

// TestSFN_DescribeStateMachineForExecution_SDK + RedriveExecution.
func TestSFN_DescribeForExecutionAndRedrive_SDK(t *testing.T) {
	c := sfnClient()

	sm, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String("sfn-sdk-redrive-sm"),
		Definition: aws.String(`{
			"StartAt":"AlreadyCompleted",
			"States":{
				"AlreadyCompleted":{"Type":"Pass","Next":"F"},
				"F":{"Type":"Fail","Error":"E","Cause":"boom"}
			}
		}`),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: sm.StateMachineArn})
	})

	start, err := c.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: sm.StateMachineArn,
		Input:           aws.String(`{}`),
	})
	require.NoError(t, err)

	dfe, err := c.DescribeStateMachineForExecution(ctx, &sfn.DescribeStateMachineForExecutionInput{
		ExecutionArn: start.ExecutionArn,
	})
	require.NoError(t, err)
	assert.Equal(t, "sfn-sdk-redrive-sm", aws.ToString(dfe.Name))
	assert.Contains(t, aws.ToString(dfe.Definition), "Fail")

	// Wait until the execution reaches a terminal FAILED state, then redrive.
	require.Eventually(t, func() bool {
		d, err := c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: start.ExecutionArn})
		return err == nil && d.Status == sfntypes.ExecutionStatusFailed
	}, 10e9, 1e8)

	clientToken := aws.String("redrive-idempotency-token")
	rd, err := c.RedriveExecution(ctx, &sfn.RedriveExecutionInput{
		ExecutionArn: start.ExecutionArn,
		ClientToken:  clientToken,
	})
	require.NoError(t, err)
	require.NotNil(t, rd.RedriveDate)
	assert.False(t, rd.RedriveDate.IsZero())
	repeated, err := c.RedriveExecution(ctx, &sfn.RedriveExecutionInput{
		ExecutionArn: start.ExecutionArn,
		ClientToken:  clientToken,
	})
	require.NoError(t, err)
	assert.Equal(t, rd.RedriveDate, repeated.RedriveDate)

	require.Eventually(t, func() bool {
		d, describeErr := c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: start.ExecutionArn})
		return describeErr == nil && d.Status == sfntypes.ExecutionStatusFailed
	}, 10e9, 1e8)
	history, err := c.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{
		ExecutionArn: start.ExecutionArn,
	})
	require.NoError(t, err)
	completedStateEntries := 0
	for _, event := range history.Events {
		if event.Type == sfntypes.HistoryEventTypePassStateEntered &&
			event.StateEnteredEventDetails != nil &&
			aws.ToString(event.StateEnteredEventDetails.Name) == "AlreadyCompleted" {
			completedStateEntries++
		}
	}
	assert.Equal(t, 1, completedStateEntries, "redrive must resume at the failed state")
}

// TestSFN_MapRuns_SDK launches a real Distributed Map and exercises its
// ListMapRuns, DescribeMapRun, and UpdateMapRun control plane while its child
// workflows are running.
func TestSFN_MapRuns_SDK(t *testing.T) {
	c := sfnClient()

	sm, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String("sfn-sdk-maprun-sm"),
		Definition: aws.String(`{
			"StartAt":"Distributed",
			"States":{"Distributed":{
				"Type":"Map",
				"MaxConcurrency":1,
				"ItemProcessor":{
					"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"EXPRESS"},
					"StartAt":"Delay",
					"States":{"Delay":{"Type":"Wait","Seconds":1,"End":true}}
				},
				"End":true
			}}
		}`),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: sm.StateMachineArn})
	})

	start, err := c.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: sm.StateMachineArn,
		Input:           aws.String(`[{"id":1},{"id":2}]`),
	})
	require.NoError(t, err)

	var mapRunArn *string
	require.Eventually(t, func() bool {
		list, listErr := c.ListMapRuns(ctx, &sfn.ListMapRunsInput{ExecutionArn: start.ExecutionArn})
		if listErr != nil || len(list.MapRuns) != 1 {
			return false
		}
		mapRunArn = list.MapRuns[0].MapRunArn
		return aws.ToString(mapRunArn) != ""
	}, 10e9, 5e7)

	_, err = c.UpdateMapRun(ctx, &sfn.UpdateMapRunInput{
		MapRunArn:      mapRunArn,
		MaxConcurrency: aws.Int32(2),
	})
	require.NoError(t, err)
	described, err := c.DescribeMapRun(ctx, &sfn.DescribeMapRunInput{MapRunArn: mapRunArn})
	require.NoError(t, err)
	assert.Equal(t, int32(2), described.MaxConcurrency)
	assert.Equal(t, int64(2), described.ItemCounts.Total)

	require.Eventually(t, func() bool {
		result, describeErr := c.DescribeMapRun(ctx, &sfn.DescribeMapRunInput{MapRunArn: mapRunArn})
		return describeErr == nil && result.Status == sfntypes.MapRunStatusSucceeded &&
			result.ItemCounts.Succeeded == 2
	}, 10e9, 1e8)
	described, err = c.DescribeMapRun(ctx, &sfn.DescribeMapRunInput{MapRunArn: mapRunArn})
	require.NoError(t, err)
	assert.Equal(t, int64(2), described.ItemCounts.ResultsWritten)
	children, err := c.ListExecutions(ctx, &sfn.ListExecutionsInput{MapRunArn: mapRunArn})
	require.NoError(t, err)
	require.Len(t, children.Executions, 2)
	for _, child := range children.Executions {
		assert.Equal(t, sfntypes.ExecutionStatusSucceeded, child.Status)
		assert.Equal(t, aws.ToString(mapRunArn), aws.ToString(child.MapRunArn))
		assert.Contains(t, aws.ToString(child.StateMachineArn), "/Distributed")
	}
}
