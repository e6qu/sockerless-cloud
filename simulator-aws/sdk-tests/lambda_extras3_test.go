package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lambdaFunctionArn builds the function ARN the sim mints for a given name.
func lambdaFunctionArn(name string) string {
	return "arn:aws:lambda:us-east-1:123456789012:function:" + name
}

// TestLambda_ConfigAndCode exercises GetFunctionConfiguration and
// UpdateFunctionCode against a real stored function.
func TestLambda_ConfigAndCode(t *testing.T) {
	lc := lambdaClient()
	fn := "extras3-config-fn"
	lambdaExtrasFunc(t, lc, fn)

	cfg, err := lc.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
	assert.Equal(t, fn, aws.ToString(cfg.FunctionName))
	assert.Equal(t, lambdatypes.StateActive, cfg.State)

	upd, err := lc.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
		FunctionName: aws.String(fn),
		ZipFile:      lambdaNodeDeploymentZip(t, `{"updated":true}`),
	})
	require.NoError(t, err)
	assert.Equal(t, fn, aws.ToString(upd.FunctionName))
	// A code swap re-stamps CodeSha256 and CodeSize.
	assert.NotEmpty(t, aws.ToString(upd.CodeSha256))

	// GetFunctionConfiguration for an unknown function is a 404.
	_, err = lc.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
		FunctionName: aws.String("extras3-no-such-fn"),
	})
	require.Error(t, err)
}

// TestLambda_FunctionScalingConfig exercises Put/Get of the per-function
// scaling config (2025-11-30).
func TestLambda_FunctionScalingConfig(t *testing.T) {
	lc := lambdaClient()
	fn := "extras3-scaling-fn"
	lambdaExtrasFunc(t, lc, fn)

	pub, err := lc.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String(fn)})
	require.NoError(t, err)
	qual := aws.ToString(pub.Version)

	// Get before put: no scaling config set, but the call succeeds with the ARN.
	pre, err := lc.GetFunctionScalingConfig(ctx, &lambda.GetFunctionScalingConfigInput{
		FunctionName: aws.String(fn),
		Qualifier:    aws.String(qual),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(pre.FunctionArn))

	_, err = lc.PutFunctionScalingConfig(ctx, &lambda.PutFunctionScalingConfigInput{
		FunctionName: aws.String(fn),
		Qualifier:    aws.String(qual),
		FunctionScalingConfig: &lambdatypes.FunctionScalingConfig{
			MinExecutionEnvironments: aws.Int32(1),
			MaxExecutionEnvironments: aws.Int32(5),
		},
	})
	require.NoError(t, err)

	got, err := lc.GetFunctionScalingConfig(ctx, &lambda.GetFunctionScalingConfigInput{
		FunctionName: aws.String(fn),
		Qualifier:    aws.String(qual),
	})
	require.NoError(t, err)
	require.NotNil(t, got.RequestedFunctionScalingConfig)
	assert.Equal(t, int32(1), aws.ToInt32(got.RequestedFunctionScalingConfig.MinExecutionEnvironments))
	assert.Equal(t, int32(5), aws.ToInt32(got.RequestedFunctionScalingConfig.MaxExecutionEnvironments))
	require.NotNil(t, got.AppliedFunctionScalingConfig)
	assert.Equal(t, int32(5), aws.ToInt32(got.AppliedFunctionScalingConfig.MaxExecutionEnvironments))
}

// TestLambda_ListFunctionsByCodeSigningConfig wires a function to a
// code-signing config and reads the membership back.
func TestLambda_ListFunctionsByCodeSigningConfig(t *testing.T) {
	lc := lambdaClient()
	fn := "extras3-csc-fn"
	lambdaExtrasFunc(t, lc, fn)

	csc, err := lc.CreateCodeSigningConfig(ctx, &lambda.CreateCodeSigningConfigInput{
		AllowedPublishers: &lambdatypes.AllowedPublishers{
			SigningProfileVersionArns: []string{
				"arn:aws:signer:us-east-1:123456789012:/signing-profiles/p/v",
			},
		},
	})
	require.NoError(t, err)
	cscArn := aws.ToString(csc.CodeSigningConfig.CodeSigningConfigArn)

	_, err = lc.PutFunctionCodeSigningConfig(ctx, &lambda.PutFunctionCodeSigningConfigInput{
		FunctionName:         aws.String(fn),
		CodeSigningConfigArn: aws.String(cscArn),
	})
	require.NoError(t, err)

	lst, err := lc.ListFunctionsByCodeSigningConfig(ctx, &lambda.ListFunctionsByCodeSigningConfigInput{
		CodeSigningConfigArn: aws.String(cscArn),
	})
	require.NoError(t, err)
	assert.Contains(t, lst.FunctionArns, lambdaFunctionArn(fn))

	_, _ = lc.DeleteFunctionCodeSigningConfig(ctx, &lambda.DeleteFunctionCodeSigningConfigInput{
		FunctionName: aws.String(fn),
	})
	_, _ = lc.DeleteCodeSigningConfig(ctx, &lambda.DeleteCodeSigningConfigInput{
		CodeSigningConfigArn: aws.String(cscArn),
	})
}

// TestLambda_CapacityProviders runs the full create/get/list/update/delete
// round-trip plus the function-versions read-back (2025-11-30).
func TestLambda_CapacityProviders(t *testing.T) {
	lc := lambdaClient()
	cpName := "extras3-cp"

	create, err := lc.CreateCapacityProvider(ctx, &lambda.CreateCapacityProviderInput{
		CapacityProviderName: aws.String(cpName),
		VpcConfig: &lambdatypes.CapacityProviderVpcConfig{
			SubnetIds:        []string{"subnet-0a1b2c3d"},
			SecurityGroupIds: []string{"sg-0a1b2c3d"},
		},
		PermissionsConfig: &lambdatypes.CapacityProviderPermissionsConfig{
			CapacityProviderOperatorRoleArn: aws.String("arn:aws:iam::123456789012:role/cp-operator"),
		},
		CapacityProviderScalingConfig: &lambdatypes.CapacityProviderScalingConfig{
			MaxVCpuCount: aws.Int32(8),
			ScalingMode:  lambdatypes.CapacityProviderScalingModeAuto,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, create.CapacityProvider)
	cpArn := aws.ToString(create.CapacityProvider.CapacityProviderArn)
	assert.NotEmpty(t, cpArn)
	assert.Equal(t, lambdatypes.CapacityProviderStateActive, create.CapacityProvider.State)

	t.Cleanup(func() {
		_, _ = lc.DeleteCapacityProvider(ctx, &lambda.DeleteCapacityProviderInput{
			CapacityProviderName: aws.String(cpName),
		})
	})

	get, err := lc.GetCapacityProvider(ctx, &lambda.GetCapacityProviderInput{
		CapacityProviderName: aws.String(cpName),
	})
	require.NoError(t, err)
	require.NotNil(t, get.CapacityProvider)
	assert.Equal(t, cpArn, aws.ToString(get.CapacityProvider.CapacityProviderArn))
	require.NotNil(t, get.CapacityProvider.VpcConfig)
	assert.Equal(t, []string{"subnet-0a1b2c3d"}, get.CapacityProvider.VpcConfig.SubnetIds)

	upd, err := lc.UpdateCapacityProvider(ctx, &lambda.UpdateCapacityProviderInput{
		CapacityProviderName: aws.String(cpName),
		CapacityProviderScalingConfig: &lambdatypes.CapacityProviderScalingConfig{
			MaxVCpuCount: aws.Int32(16),
			ScalingMode:  lambdatypes.CapacityProviderScalingModeManual,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, upd.CapacityProvider)
	require.NotNil(t, upd.CapacityProvider.CapacityProviderScalingConfig)
	assert.Equal(t, int32(16), aws.ToInt32(upd.CapacityProvider.CapacityProviderScalingConfig.MaxVCpuCount))

	lst, err := lc.ListCapacityProviders(ctx, &lambda.ListCapacityProvidersInput{})
	require.NoError(t, err)
	found := false
	for _, cp := range lst.CapacityProviders {
		if aws.ToString(cp.CapacityProviderArn) == cpArn {
			found = true
		}
	}
	assert.True(t, found, "created capacity provider should appear in the list")

	fv, err := lc.ListFunctionVersionsByCapacityProvider(ctx, &lambda.ListFunctionVersionsByCapacityProviderInput{
		CapacityProviderName: aws.String(cpName),
	})
	require.NoError(t, err)
	assert.Equal(t, cpArn, aws.ToString(fv.CapacityProviderArn))
	assert.Empty(t, fv.FunctionVersions)

	attachedFunction := "extras3-cp-function"
	_, err = lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(attachedFunction),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{
			ZipFile: lambdaNodeDeploymentZip(t, `{}`),
		},
		CapacityProviderConfig: &lambdatypes.CapacityProviderConfig{
			LambdaManagedInstancesCapacityProviderConfig: &lambdatypes.LambdaManagedInstancesCapacityProviderConfig{
				CapacityProviderArn: aws.String(cpArn),
			},
		},
	})
	require.NoError(t, err)
	_, err = lc.DeleteCapacityProvider(ctx, &lambda.DeleteCapacityProviderInput{
		CapacityProviderName: aws.String(cpName),
	})
	require.ErrorContains(t, err, "ResourceConflictException")
	_, err = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{
		FunctionName: aws.String(attachedFunction),
	})
	require.NoError(t, err)

	deleted, err := lc.DeleteCapacityProvider(ctx, &lambda.DeleteCapacityProviderInput{
		CapacityProviderName: aws.String(cpName),
	})
	require.NoError(t, err)
	require.NotNil(t, deleted.CapacityProvider)
	assert.Equal(t, lambdatypes.CapacityProviderStateDeleting, deleted.CapacityProvider.State)
	_, err = lc.GetCapacityProvider(ctx, &lambda.GetCapacityProviderInput{
		CapacityProviderName: aws.String(cpName),
	})
	require.ErrorContains(t, err, "ResourceNotFoundException")
}

// TestLambda_InvokeAsync exercises the deprecated InvokeAsync entry point (202).
func TestLambda_InvokeAsync(t *testing.T) {
	lc := lambdaClient()
	fn := "extras3-async-fn"
	lambdaExtrasFunc(t, lc, fn)

	out, err := lc.InvokeAsync(ctx, &lambda.InvokeAsyncInput{
		FunctionName: aws.String(fn),
		InvokeArgs:   strings.NewReader(`{"hello":"world"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(202), out.Status)
}

// TestLambda_InvokeWithResponseStream exercises the streaming invoke entry
// point and reassembles the event stream the handler emits.
func TestLambda_InvokeWithResponseStream(t *testing.T) {
	lc := lambdaClient()
	fn := "extras3-stream-fn"
	lambdaExtrasFunc(t, lc, fn)

	out, err := lc.InvokeWithResponseStream(ctx, &lambda.InvokeWithResponseStreamInput{
		FunctionName: aws.String(fn),
		Payload:      []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(200), out.StatusCode)

	stream := out.GetStream()
	defer stream.Close()
	sawComplete := false
	for ev := range stream.Events() {
		switch ev.(type) {
		case *lambdatypes.InvokeWithResponseStreamResponseEventMemberPayloadChunk:
			// A chunk of the Zip-package function's {} response is fine.
		case *lambdatypes.InvokeWithResponseStreamResponseEventMemberInvokeComplete:
			sawComplete = true
		}
	}
	require.NoError(t, stream.Err())
	assert.True(t, sawComplete, "stream must terminate with an InvokeComplete event")
}

// TestLambda_DurableExecutionLifecycle starts a durable execution through the
// real Invoke entry point, then observes its result, history, listing, and
// idempotent execution-name behavior through the official SDK.
func TestLambda_DurableExecutionLifecycle(t *testing.T) {
	lc := lambdaClient()
	fn := "extras3-durable-fn"
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fn),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{
			ZipFile: lambdaNodeDeploymentZip(t, `({
				Status: "SUCCEEDED",
				Result: JSON.stringify({
					durable: true,
					input: JSON.parse(event.InitialExecutionState.Operations[0].ExecutionDetails.InputPayload)
				})
			})`),
		},
		DurableConfig: &lambdatypes.DurableConfig{
			ExecutionTimeout:      aws.Int32(60),
			RetentionPeriodInDays: aws.Int32(1),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fn)})
	})

	invoked, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:         aws.String(fn + ":$LATEST"),
		DurableExecutionName: aws.String("order-saga"),
		Payload:              []byte(`{"orderId":"A-1"}`),
	})
	require.NoError(t, err)
	deArn := aws.ToString(invoked.DurableExecutionArn)
	require.NotEmpty(t, deArn)
	assert.JSONEq(t, `{"durable":true,"input":{"orderId":"A-1"}}`, string(invoked.Payload))

	got, err := lc.GetDurableExecution(ctx, &lambda.GetDurableExecutionInput{
		DurableExecutionArn: aws.String(deArn),
	})
	require.NoError(t, err)
	assert.Equal(t, deArn, aws.ToString(got.DurableExecutionArn))
	assert.Equal(t, lambdatypes.ExecutionStatusSucceeded, got.Status)
	assert.True(t, aws.ToBool(got.ExecutionDataIncluded))
	assert.JSONEq(t, `{"orderId":"A-1"}`, aws.ToString(got.InputPayload))
	assert.JSONEq(t, `{"durable":true,"input":{"orderId":"A-1"}}`, aws.ToString(got.Result))

	withoutData, err := lc.GetDurableExecution(ctx, &lambda.GetDurableExecutionInput{
		DurableExecutionArn:  aws.String(deArn),
		IncludeExecutionData: aws.Bool(false),
	})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(withoutData.ExecutionDataIncluded))
	assert.Nil(t, withoutData.InputPayload)
	assert.Nil(t, withoutData.Result)

	hist, err := lc.GetDurableExecutionHistory(ctx, &lambda.GetDurableExecutionHistoryInput{
		DurableExecutionArn: aws.String(deArn),
		MaxItems:            1,
	})
	require.NoError(t, err)
	require.Len(t, hist.Events, 1)
	require.NotNil(t, hist.NextMarker)
	assert.Equal(t, lambdatypes.EventTypeExecutionStarted, hist.Events[0].EventType)
	histNext, err := lc.GetDurableExecutionHistory(ctx, &lambda.GetDurableExecutionHistoryInput{
		DurableExecutionArn: aws.String(deArn),
		Marker:              hist.NextMarker,
		MaxItems:            1,
	})
	require.NoError(t, err)
	require.Len(t, histNext.Events, 1)
	assert.Equal(t, lambdatypes.EventTypeExecutionSucceeded, histNext.Events[0].EventType)

	_, err = lc.GetDurableExecutionState(ctx, &lambda.GetDurableExecutionStateInput{
		DurableExecutionArn: aws.String(deArn),
		CheckpointToken:     aws.String("not-a-runtime-token"),
	})
	require.Error(t, err)

	second, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:         aws.String(fn + ":$LATEST"),
		DurableExecutionName: aws.String("order-saga-2"),
		Payload:              []byte(`{"orderId":"A-2"}`),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(second.DurableExecutionArn))

	lst, err := lc.ListDurableExecutionsByFunction(ctx, &lambda.ListDurableExecutionsByFunctionInput{
		FunctionName: aws.String(fn),
		Statuses:     []lambdatypes.ExecutionStatus{lambdatypes.ExecutionStatusSucceeded},
		MaxItems:     1,
	})
	require.NoError(t, err)
	require.Len(t, lst.DurableExecutions, 1)
	require.NotNil(t, lst.NextMarker)
	lstNext, err := lc.ListDurableExecutionsByFunction(ctx, &lambda.ListDurableExecutionsByFunctionInput{
		FunctionName: aws.String(fn),
		Statuses:     []lambdatypes.ExecutionStatus{lambdatypes.ExecutionStatusSucceeded},
		MaxItems:     1,
		Marker:       lst.NextMarker,
	})
	require.NoError(t, err)
	require.Len(t, lstNext.DurableExecutions, 1)
	filtered, err := lc.ListDurableExecutionsByFunction(ctx, &lambda.ListDurableExecutionsByFunctionInput{
		FunctionName:         aws.String(fn),
		DurableExecutionName: aws.String("order-saga"),
	})
	require.NoError(t, err)
	require.Len(t, filtered.DurableExecutions, 1)
	assert.Equal(t, deArn, aws.ToString(filtered.DurableExecutions[0].DurableExecutionArn))

	reused, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:         aws.String(fn + ":$LATEST"),
		DurableExecutionName: aws.String("order-saga"),
		Payload:              []byte(`{"orderId":"A-1"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, deArn, aws.ToString(reused.DurableExecutionArn))
	assert.JSONEq(t, `{"durable":true,"input":{"orderId":"A-1"}}`, string(reused.Payload))

	_, err = lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:         aws.String(fn + ":$LATEST"),
		DurableExecutionName: aws.String("order-saga"),
		Payload:              []byte(`{"orderId":"different"}`),
	})
	require.ErrorContains(t, err, "DurableExecutionAlreadyStartedException")

	_, err = lc.SendDurableExecutionCallbackFailure(ctx, &lambda.SendDurableExecutionCallbackFailureInput{
		CallbackId: aws.String("cb-does-not-exist"),
		Error:      &lambdatypes.ErrorObject{ErrorMessage: aws.String("nope")},
	})
	require.Error(t, err)

	_, err = lc.StopDurableExecution(ctx, &lambda.StopDurableExecutionInput{
		DurableExecutionArn: aws.String(deArn),
		Error:               &lambdatypes.ErrorObject{ErrorMessage: aws.String("operator stop")},
	})
	require.Error(t, err)
}

// TestLambda_DurableCheckpointAndCallbackReplay drives the private durable
// execution envelope through a real managed-runtime container, then uses the
// official AWS SDK for Go for checkpoint, heartbeat, and callback completion.
// The synchronous Invoke remains open until callback completion causes Lambda
// to replay the function and return its customer result.
func TestLambda_DurableCheckpointAndCallbackReplay(t *testing.T) {
	lc := lambdaClient()
	fn := "extras3-durable-callback-fn"
	source := `
exports.handler = async (event) => {
  const callback = event.InitialExecutionState.Operations.find(
    (operation) => operation.Type === "CALLBACK"
  );
  if (callback?.Status === "SUCCEEDED") {
    return {
      Status: "SUCCEEDED",
      Result: JSON.stringify({
        approved: JSON.parse(callback.CallbackDetails.Result),
        replayedOperationIds: event.UpdatedOperationIds
      })
    };
  }
  console.log("CHECKPOINT_TOKEN=" + event.CheckpointToken);
  console.log("DURABLE_ARN=" + event.DurableExecutionArn);
  return {Status: "PENDING"};
};`
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fn),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{
			ZipFile: lambdaNodeSourceZip(t, source),
		},
		DurableConfig: &lambdatypes.DurableConfig{
			ExecutionTimeout:      aws.Int32(60),
			RetentionPeriodInDays: aws.Int32(1),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fn)})
	})

	type invocationResult struct {
		output *lambda.InvokeOutput
		err    error
	}
	invocationDone := make(chan invocationResult, 1)
	go func() {
		output, invokeErr := lc.Invoke(ctx, &lambda.InvokeInput{
			FunctionName:         aws.String(fn + ":$LATEST"),
			DurableExecutionName: aws.String("callback-replay"),
			Payload:              []byte(`{"orderId":"B-2"}`),
		})
		invocationDone <- invocationResult{output: output, err: invokeErr}
	}()

	cw := cwLogsClient()
	var checkpointToken, durableARN string
	require.Eventually(t, func() bool {
		streams, streamErr := cw.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
			LogGroupName: aws.String("/aws/lambda/" + fn),
		})
		if streamErr != nil || len(streams.LogStreams) == 0 {
			return false
		}
		for _, stream := range streams.LogStreams {
			events, eventErr := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
				LogGroupName:  aws.String("/aws/lambda/" + fn),
				LogStreamName: stream.LogStreamName,
			})
			if eventErr != nil {
				continue
			}
			for _, event := range events.Events {
				message := aws.ToString(event.Message)
				if strings.Contains(message, "CHECKPOINT_TOKEN=") {
					checkpointToken = strings.TrimSpace(strings.SplitN(message, "CHECKPOINT_TOKEN=", 2)[1])
				}
				if strings.Contains(message, "DURABLE_ARN=") {
					durableARN = strings.TrimSpace(strings.SplitN(message, "DURABLE_ARN=", 2)[1])
				}
			}
		}
		return checkpointToken != "" && durableARN != ""
	}, 15*time.Second, 100*time.Millisecond)

	checkpoint, err := lc.CheckpointDurableExecution(ctx, &lambda.CheckpointDurableExecutionInput{
		DurableExecutionArn: aws.String(durableARN),
		CheckpointToken:     aws.String(checkpointToken),
		Updates: []lambdatypes.OperationUpdate{{
			Id:     aws.String("approval"),
			Name:   aws.String("approval"),
			Type:   lambdatypes.OperationTypeCallback,
			Action: lambdatypes.OperationActionStart,
			CallbackOptions: &lambdatypes.CallbackOptions{
				HeartbeatTimeoutSeconds: 10,
				TimeoutSeconds:          30,
			},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, checkpoint.NewExecutionState)
	var callbackID string
	for _, operation := range checkpoint.NewExecutionState.Operations {
		if operation.Type == lambdatypes.OperationTypeCallback && operation.CallbackDetails != nil {
			callbackID = aws.ToString(operation.CallbackDetails.CallbackId)
		}
	}
	require.NotEmpty(t, callbackID)

	_, err = lc.SendDurableExecutionCallbackHeartbeat(ctx, &lambda.SendDurableExecutionCallbackHeartbeatInput{
		CallbackId: aws.String(callbackID),
	})
	require.NoError(t, err)
	_, err = lc.SendDurableExecutionCallbackSuccess(ctx, &lambda.SendDurableExecutionCallbackSuccessInput{
		CallbackId: aws.String(callbackID),
		Result:     []byte(`{"by":"operator"}`),
	})
	require.NoError(t, err)

	select {
	case result := <-invocationDone:
		require.NoError(t, result.err)
		require.NotNil(t, result.output)
		assert.Equal(t, durableARN, aws.ToString(result.output.DurableExecutionArn))
		assert.JSONEq(t, `{
			"approved":{"by":"operator"},
			"replayedOperationIds":["approval"]
		}`, string(result.output.Payload))
	case <-time.After(20 * time.Second):
		t.Fatal("synchronous durable Invoke did not complete after callback success")
	}
}
