package aws_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sfnClient is a stock Step Functions client pointed at the simulator's states
// endpoint coordinate. StartSyncExecution and TestState carry a modeled `sync-`
// endpoint host prefix, so the SDK sends and signs them against
// `sync-states.localhost` exactly as it sends them to
// `sync-states.us-east-1.amazonaws.com` against real AWS.
func sfnClient() *sfn.Client {
	return sfn.NewFromConfig(sdkConfig(), func(o *sfn.Options) {
		o.BaseEndpoint = aws.String(simEndpoint("states"))
	})
}

func TestSFN_StateMachineCRUD_SDK(t *testing.T) {
	c := sfnClient()

	create, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-sm"),
		Definition: aws.String(`{"Comment":"test","StartAt":"Pass","States":{"Pass":{"Type":"Pass","End":true}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
		Type:       sfntypes.StateMachineTypeStandard,
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(create.StateMachineArn))
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
			StateMachineArn: create.StateMachineArn,
		})
	})

	describe, err := c.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{
		StateMachineArn: create.StateMachineArn,
	})
	require.NoError(t, err)
	assert.Equal(t, "sfn-sdk-sm", aws.ToString(describe.Name))
	assert.Equal(t, aws.ToString(create.StateMachineArn), aws.ToString(describe.StateMachineArn))
	assert.Equal(t, sfntypes.StateMachineStatusActive, describe.Status)
	assert.Equal(t, sfntypes.StateMachineTypeStandard, describe.Type)

	list, err := c.ListStateMachines(ctx, &sfn.ListStateMachinesInput{})
	require.NoError(t, err)
	found := false
	for _, sm := range list.StateMachines {
		if aws.ToString(sm.Name) == "sfn-sdk-sm" {
			found = true
			break
		}
	}
	assert.True(t, found)

	_, err = c.TagResource(ctx, &sfn.TagResourceInput{
		ResourceArn: create.StateMachineArn,
		Tags: []sfntypes.Tag{
			{Key: aws.String("env"), Value: aws.String("sdk")},
		},
	})
	require.NoError(t, err)

	tags, err := c.ListTagsForResource(ctx, &sfn.ListTagsForResourceInput{
		ResourceArn: create.StateMachineArn,
	})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "env", aws.ToString(tags.Tags[0].Key))
	assert.Equal(t, "sdk", aws.ToString(tags.Tags[0].Value))

	_, err = c.UntagResource(ctx, &sfn.UntagResourceInput{
		ResourceArn: create.StateMachineArn,
		TagKeys:     []string{"env"},
	})
	require.NoError(t, err)

	tags, err = c.ListTagsForResource(ctx, &sfn.ListTagsForResourceInput{
		ResourceArn: create.StateMachineArn,
	})
	require.NoError(t, err)
	assert.Empty(t, tags.Tags)

	// Update definition
	_, err = c.UpdateStateMachine(ctx, &sfn.UpdateStateMachineInput{
		StateMachineArn: create.StateMachineArn,
		Definition:      aws.String(`{"Comment":"updated","StartAt":"Pass","States":{"Pass":{"Type":"Pass","End":true}}}`),
	})
	require.NoError(t, err)

	updated, err := c.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{
		StateMachineArn: create.StateMachineArn,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(updated.Definition), "updated")

	versions, err := c.ListStateMachineVersions(ctx, &sfn.ListStateMachineVersionsInput{
		StateMachineArn: create.StateMachineArn,
	})
	require.NoError(t, err)
	assert.NotNil(t, versions.StateMachineVersions)

	validation, err := c.ValidateStateMachineDefinition(ctx, &sfn.ValidateStateMachineDefinitionInput{
		Definition: aws.String(`{"StartAt":"Pass","States":{"Pass":{"Type":"Pass","End":true}}}`),
	})
	require.NoError(t, err)
	assert.Equal(t, sfntypes.ValidateStateMachineDefinitionResultCodeOk, validation.Result)
}

func TestSFN_ExecutionLifecycle_SDK(t *testing.T) {
	c := sfnClient()

	create, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-exec-sm"),
		Definition: aws.String(`{"Comment":"exec test","StartAt":"Pass","States":{"Pass":{"Type":"Pass","End":true}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: create.StateMachineArn})
	})

	start, err := c.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: create.StateMachineArn,
		Name:            aws.String("exec-1"),
		Input:           aws.String(`{"key":"value"}`),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(start.ExecutionArn))

	var describe *sfn.DescribeExecutionOutput
	require.Eventually(t, func() bool {
		describe, err = c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
			ExecutionArn: start.ExecutionArn,
		})
		require.NoError(t, err)
		return describe.Status == sfntypes.ExecutionStatusSucceeded
	}, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, aws.ToString(start.ExecutionArn), aws.ToString(describe.ExecutionArn))
	assert.Equal(t, sfntypes.ExecutionStatusSucceeded, describe.Status)
	assert.Equal(t, aws.ToString(create.StateMachineArn), aws.ToString(describe.StateMachineArn))

	list, err := c.ListExecutions(ctx, &sfn.ListExecutionsInput{
		StateMachineArn: create.StateMachineArn,
	})
	require.NoError(t, err)
	require.Len(t, list.Executions, 1)
	assert.Equal(t, aws.ToString(start.ExecutionArn), aws.ToString(list.Executions[0].ExecutionArn))
}

func TestSFN_StopExecution_SDK(t *testing.T) {
	c := sfnClient()

	create, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-stop-sm"),
		Definition: aws.String(`{"StartAt":"Wait","States":{"Wait":{"Type":"Wait","Seconds":5,"End":true}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: create.StateMachineArn})
	})

	start, err := c.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: create.StateMachineArn,
		Input:           aws.String(`{}`),
	})
	require.NoError(t, err)

	_, err = c.StopExecution(ctx, &sfn.StopExecutionInput{
		ExecutionArn: start.ExecutionArn,
		Error:        aws.String("Manual"),
		Cause:        aws.String("test stop"),
	})
	require.NoError(t, err)

	describe, err := c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
		ExecutionArn: start.ExecutionArn,
	})
	require.NoError(t, err)
	assert.Equal(t, sfntypes.ExecutionStatusAborted, describe.Status)
}

func TestSFN_FailState_SDK(t *testing.T) {
	c := sfnClient()

	create, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-sdk-fail-sm"),
		Definition: aws.String(`{"StartAt":"Fail","States":{"Fail":{"Type":"Fail","Error":"Manual","Cause":"expected failure"}}}`),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: create.StateMachineArn})
	})

	start, err := c.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: create.StateMachineArn,
		Input:           aws.String(`{}`),
	})
	require.NoError(t, err)

	var describe *sfn.DescribeExecutionOutput
	require.Eventually(t, func() bool {
		describe, err = c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
			ExecutionArn: start.ExecutionArn,
		})
		require.NoError(t, err)
		return describe.Status == sfntypes.ExecutionStatusFailed
	}, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, sfntypes.ExecutionStatusFailed, describe.Status)
}

// DescribeStateMachineOutput has no tags member — tags ride
// ListTagsForResource. The SDK silently drops unknown members, so the
// describe response is probed raw.
func TestSFNDescribeStateMachineOmitsTags(t *testing.T) {
	c := sfnClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	create, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name:       aws.String("sfn-tags-shape"),
		RoleArn:    aws.String("arn:aws:iam::123456789012:role/sfn-role"),
		Definition: aws.String(`{"StartAt":"Done","States":{"Done":{"Type":"Succeed"}}}`),
		Tags:       []sfntypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: create.StateMachineArn})
	})

	tags, err := c.ListTagsForResource(ctx, &sfn.ListTagsForResourceInput{ResourceArn: create.StateMachineArn})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)

	body, _ := json.Marshal(map[string]string{"stateMachineArn": aws.ToString(create.StateMachineArn)})
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AWSStepFunctions.DescribeStateMachine")
	signRawSigV4JSON(t, req, "states", body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var raw map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
	assert.Contains(t, raw, "stateMachineArn")
	assert.NotContains(t, raw, "tags", "DescribeStateMachineOutput has no tags member")
}

// TestSFN_GetExecutionHistory_SDK covers GetExecutionHistory and exercises the
// Choice interpreter end-to-end (input routes to the >5 branch).
func TestSFN_GetExecutionHistory_SDK(t *testing.T) {
	c := sfnClient()
	create, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String("sfn-sdk-history-sm"),
		Definition: aws.String(`{"StartAt":"C","States":{` +
			`"C":{"Type":"Choice","Choices":[{"Variable":"$.x","NumericGreaterThan":5,"Next":"Big"}],"Default":"Small"},` +
			`"Big":{"Type":"Pass","Result":"big","End":true},` +
			`"Small":{"Type":"Pass","Result":"small","End":true}}}`),
		RoleArn: aws.String("arn:aws:iam::123456789012:role/sfn-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: create.StateMachineArn})
	})

	start, err := c.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: create.StateMachineArn,
		Name:            aws.String("hist-1"),
		Input:           aws.String(`{"x":10}`),
	})
	require.NoError(t, err)

	var describe *sfn.DescribeExecutionOutput
	require.Eventually(t, func() bool {
		describe, err = c.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: start.ExecutionArn})
		require.NoError(t, err)
		return describe.Status == sfntypes.ExecutionStatusSucceeded
	}, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, `"big"`, aws.ToString(describe.Output), "Choice must route x=10 to the >5 branch")

	hist, err := c.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{ExecutionArn: start.ExecutionArn})
	require.NoError(t, err)
	types := map[sfntypes.HistoryEventType]bool{}
	for _, e := range hist.Events {
		types[e.Type] = true
	}
	assert.True(t, types[sfntypes.HistoryEventTypeExecutionStarted], "history must include ExecutionStarted")
	assert.True(t, types[sfntypes.HistoryEventTypeExecutionSucceeded], "history must include ExecutionSucceeded")
}
