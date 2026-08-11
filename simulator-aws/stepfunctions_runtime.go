package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

func sfnWaitDuration(state sfnState, input, context any) (time.Duration, *sfnExecutionError) {
	switch {
	case state.Seconds != nil:
		if *state.Seconds < 0 {
			return 0, &sfnExecutionError{Name: "States.Runtime", Cause: "Seconds must be non-negative"}
		}
		return time.Duration(*state.Seconds) * time.Second, nil
	case state.SecondsPath != "":
		value, ok := sfnPathValue(input, context, state.SecondsPath)
		seconds, numberOK := sfnInteger(value)
		if !ok || !numberOK || seconds < 0 {
			return 0, &sfnExecutionError{Name: "States.Runtime", Cause: "SecondsPath must resolve to a non-negative integer"}
		}
		return time.Duration(seconds) * time.Second, nil
	case state.Timestamp != "":
		target, err := time.Parse(time.RFC3339, state.Timestamp)
		if err != nil {
			return 0, &sfnExecutionError{Name: "States.Runtime", Cause: "Timestamp must be RFC3339"}
		}
		if wait := time.Until(target); wait > 0 {
			return wait, nil
		}
		return 0, nil
	case state.TimestampPath != "":
		value, ok := sfnPathValue(input, context, state.TimestampPath)
		timestamp, stringOK := value.(string)
		if !ok || !stringOK {
			return 0, &sfnExecutionError{Name: "States.Runtime", Cause: "TimestampPath must resolve to a string"}
		}
		target, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return 0, &sfnExecutionError{Name: "States.Runtime", Cause: "TimestampPath must resolve to an RFC3339 timestamp"}
		}
		if wait := time.Until(target); wait > 0 {
			return wait, nil
		}
		return 0, nil
	default:
		return 0, &sfnExecutionError{Name: "States.Runtime", Cause: "Wait state requires Seconds, SecondsPath, Timestamp, or TimestampPath"}
	}
}

func sfnErrorMatches(names []string, actual string) bool {
	for _, name := range names {
		if name == actual || name == "States.ALL" {
			return true
		}
	}
	return false
}

func sfnRunTaskWithRetry(
	state sfnState,
	input, context any,
	cancel <-chan struct{},
	executionARN string,
) (any, *sfnExecutionError) {
	attempts := make([]int, len(state.Retry))
	for {
		sfnAppendTaskScheduledHistory(executionARN, state, input, context)
		result, taskErr := sfnRunTaskValue(state, input, context, cancel)
		sfnAppendTaskCompletedHistory(executionARN, state, result, taskErr)
		if taskErr == nil {
			return result, nil
		}
		retried := false
		for i, retrier := range state.Retry {
			if !sfnErrorMatches(retrier.ErrorEquals, taskErr.Name) {
				continue
			}
			maxAttempts := 3
			if retrier.MaxAttempts != nil {
				maxAttempts = *retrier.MaxAttempts
			}
			if attempts[i] >= maxAttempts {
				continue
			}
			interval := retrier.IntervalSeconds
			if interval == 0 {
				interval = 1
			}
			backoff := retrier.BackoffRate
			if backoff == 0 {
				backoff = 2
			}
			delaySeconds := float64(interval) * math.Pow(backoff, float64(attempts[i]))
			if retrier.MaxDelaySeconds > 0 && delaySeconds > float64(retrier.MaxDelaySeconds) {
				delaySeconds = float64(retrier.MaxDelaySeconds)
			}
			if retrier.JitterStrategy == "FULL" {
				delaySeconds *= float64(time.Now().UnixNano()&0xffff) / float64(0xffff)
			}
			attempts[i]++
			if stateContext, ok := context.(map[string]any)["State"].(map[string]any); ok {
				stateContext["RetryCount"] = attempts[i]
			}
			timer := time.NewTimer(time.Duration(delaySeconds * float64(time.Second)))
			select {
			case <-cancel:
				timer.Stop()
				return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "execution aborted"}
			case <-timer.C:
			}
			retried = true
			break
		}
		if !retried {
			return nil, taskErr
		}
	}
}

func sfnAppendTaskScheduledHistory(executionARN string, state sfnState, input, context any) {
	if executionARN == "" {
		return
	}
	encodedInput, err := sfnEncodeJSON(input)
	if err != nil {
		encodedInput = "null"
	}
	timeoutSeconds := int64(sfnTaskTimeout(state, input, context) / time.Second)
	if name, directLambda := sfnLambdaNameFromResource(state.Resource); directLambda &&
		!strings.HasPrefix(state.Resource, "arn:aws:states:::") {
		details := map[string]any{
			"resource":     state.Resource,
			"input":        encodedInput,
			"inputDetails": map[string]any{"truncated": false},
		}
		if timeoutSeconds > 0 {
			details["timeoutInSeconds"] = timeoutSeconds
		}
		sfnAppendHistory(executionARN, "LambdaFunctionScheduled", details)
		sfnAppendHistory(executionARN, "LambdaFunctionStarted", nil)
		_ = name
		return
	}
	if strings.HasPrefix(state.Resource, "arn:aws:states:::") {
		resourceType, action := sfnServiceIntegrationParts(state.Resource)
		details := map[string]any{
			"parameters":   encodedInput,
			"region":       awsRegion(),
			"resource":     action,
			"resourceType": resourceType,
		}
		if heartbeat := int64(sfnTaskHeartbeat(state, input, context) / time.Second); heartbeat > 0 {
			details["heartbeatInSeconds"] = heartbeat
		}
		if timeoutSeconds > 0 {
			details["timeoutInSeconds"] = timeoutSeconds
		}
		sfnAppendHistory(executionARN, "TaskScheduled", details)
		sfnAppendHistory(executionARN, "TaskStarted", map[string]any{
			"resource":     action,
			"resourceType": resourceType,
		})
	}
}

func sfnAppendTaskCompletedHistory(
	executionARN string,
	state sfnState,
	result any,
	executionErr *sfnExecutionError,
) {
	if executionARN == "" {
		return
	}
	if _, directLambda := sfnLambdaNameFromResource(state.Resource); directLambda &&
		!strings.HasPrefix(state.Resource, "arn:aws:states:::") {
		if executionErr != nil {
			eventType := "LambdaFunctionFailed"
			if executionErr.Name == "States.Timeout" {
				eventType = "LambdaFunctionTimedOut"
			}
			sfnAppendHistory(executionARN, eventType, map[string]any{
				"error": executionErr.Name,
				"cause": executionErr.Cause,
			})
			return
		}
		encodedOutput, err := sfnEncodeJSON(result)
		if err != nil {
			encodedOutput = "null"
		}
		sfnAppendHistory(executionARN, "LambdaFunctionSucceeded", map[string]any{
			"output":        encodedOutput,
			"outputDetails": map[string]any{"truncated": false},
		})
		return
	}
	if strings.HasPrefix(state.Resource, "arn:aws:states:::") {
		resourceType, action := sfnServiceIntegrationParts(state.Resource)
		if executionErr != nil {
			eventType := "TaskFailed"
			if executionErr.Name == "States.Timeout" ||
				executionErr.Name == "States.HeartbeatTimeout" {
				eventType = "TaskTimedOut"
			}
			sfnAppendHistory(executionARN, eventType, map[string]any{
				"resource":     action,
				"resourceType": resourceType,
				"error":        executionErr.Name,
				"cause":        executionErr.Cause,
			})
			return
		}
		encodedOutput, err := sfnEncodeJSON(result)
		if err != nil {
			encodedOutput = "null"
		}
		sfnAppendHistory(executionARN, "TaskSucceeded", map[string]any{
			"resource":      action,
			"resourceType":  resourceType,
			"output":        encodedOutput,
			"outputDetails": map[string]any{"truncated": false},
		})
	}
}

func sfnServiceIntegrationParts(resource string) (resourceType, action string) {
	value := strings.TrimPrefix(resource, "arn:aws:states:::")
	parts := strings.SplitN(value, ":", 2)
	resourceType = parts[0]
	if len(parts) == 2 {
		action = parts[1]
	}
	action = strings.TrimSuffix(action, ".waitForTaskToken")
	action = strings.TrimSuffix(action, ".sync:2")
	action = strings.TrimSuffix(action, ".sync")
	return resourceType, action
}

func sfnRunTaskValue(state sfnState, input, context any, cancel <-chan struct{}) (any, *sfnExecutionError) {
	inputJSON, err := sfnEncodeJSON(input)
	if err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
	}
	timeout := sfnTaskTimeout(state, input, context)
	type taskResult struct {
		value any
		err   *sfnExecutionError
	}
	done := make(chan taskResult, 1)
	go func() {
		value, taskErr := sfnInvokeTaskResource(
			state.Resource, input, inputJSON, context, cancel, sfnTaskHeartbeat(state, input, context),
		)
		done <- taskResult{value: value, err: taskErr}
	}()
	if timeout <= 0 {
		select {
		case <-cancel:
			return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "execution aborted"}
		case result := <-done:
			return result.value, result.err
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-cancel:
		return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "execution aborted"}
	case <-timer.C:
		return nil, &sfnExecutionError{Name: "States.Timeout", Cause: "Task timed out"}
	case result := <-done:
		return result.value, result.err
	}
}

func sfnTaskHeartbeat(state sfnState, input, context any) time.Duration {
	if state.HeartbeatSeconds != nil {
		return time.Duration(*state.HeartbeatSeconds) * time.Second
	}
	if state.HeartbeatSecondsPath != "" {
		if value, ok := sfnPathValue(input, context, state.HeartbeatSecondsPath); ok {
			if seconds, ok := sfnInteger(value); ok {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return 0
}

func sfnTaskTimeout(state sfnState, input, context any) time.Duration {
	if state.TimeoutSeconds != nil {
		return time.Duration(*state.TimeoutSeconds) * time.Second
	}
	if state.TimeoutSecondsPath != "" {
		if value, ok := sfnPathValue(input, context, state.TimeoutSecondsPath); ok {
			if seconds, ok := sfnInteger(value); ok {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return 0
}

func sfnInvokeTaskResource(resource string, input any, inputJSON string, context any, cancel <-chan struct{}, heartbeat time.Duration) (any, *sfnExecutionError) {
	if strings.Contains(resource, ":activity:") {
		return sfnInvokeActivity(resource, inputJSON, cancel, heartbeat)
	}
	if resource == "arn:aws:states:::lambda:invoke" || resource == "arn:aws:states:::lambda:invoke.waitForTaskToken" {
		parameters, ok := input.(map[string]any)
		if !ok {
			return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "Lambda integration parameters must be an object"}
		}
		functionName, _ := parameters["FunctionName"].(string)
		if functionName == "" {
			return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "FunctionName is required"}
		}
		payload := parameters["Payload"]
		if payload == nil {
			payload = map[string]any{}
		}
		payloadJSON, err := sfnEncodeJSON(payload)
		if err != nil {
			return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
		}
		value, invokeErr := sfnInvokeLambda(functionName, payloadJSON)
		if invokeErr != nil {
			return nil, invokeErr
		}
		if strings.HasSuffix(resource, ".waitForTaskToken") {
			task, tokenErr := sfnTaskFromContext(context)
			if tokenErr != nil {
				return nil, tokenErr
			}
			return sfnWaitForTaskToken(task, cancel, heartbeat)
		}
		return map[string]any{
			"ExecutedVersion": "$LATEST",
			"Payload":         value,
			"StatusCode":      200,
			"SdkHttpMetadata": map[string]any{
				"HttpStatusCode": 200,
			},
		}, nil
	}
	if name, ok := sfnLambdaNameFromResource(resource); ok {
		return sfnInvokeLambda(name, inputJSON)
	}
	if strings.HasPrefix(resource, "arn:aws:states:::states:startExecution") ||
		resource == "arn:aws:states:::aws-sdk:sfn:startExecution" {
		return sfnInvokeNestedExecution(resource, input, context, cancel, heartbeat)
	}
	if resource == "arn:aws:states:::sqs:sendMessage" ||
		resource == "arn:aws:states:::sqs:sendMessage.waitForTaskToken" ||
		resource == "arn:aws:states:::aws-sdk:sqs:sendMessage" {
		result, taskErr := sfnInvokeJSONService(handleSQSSendMessage, input)
		if taskErr != nil {
			return nil, taskErr
		}
		if strings.HasSuffix(resource, ".waitForTaskToken") {
			task, tokenErr := sfnTaskFromContext(context)
			if tokenErr != nil {
				return nil, tokenErr
			}
			return sfnWaitForTaskToken(task, cancel, heartbeat)
		}
		return result, nil
	}
	if resource == "arn:aws:states:::sns:publish" ||
		resource == "arn:aws:states:::sns:publish.waitForTaskToken" ||
		resource == "arn:aws:states:::aws-sdk:sns:publish" {
		result, taskErr := sfnInvokeSNSPublish(input)
		if taskErr != nil {
			return nil, taskErr
		}
		if strings.HasSuffix(resource, ".waitForTaskToken") {
			task, tokenErr := sfnTaskFromContext(context)
			if tokenErr != nil {
				return nil, tokenErr
			}
			return sfnWaitForTaskToken(task, cancel, heartbeat)
		}
		return result, nil
	}
	if resource == "arn:aws:states:::events:putEvents" ||
		resource == "arn:aws:states:::aws-sdk:eventbridge:putEvents" {
		return sfnInvokeJSONService(handleEBPutEvents, input)
	}
	if resource == "arn:aws:states:::aws-sdk:cloudwatch:putMetricData" {
		return sfnInvokeJSONService(handleCWJSONPutMetricData, input)
	}
	if resource == "arn:aws:states:::aws-sdk:cloudwatchlogs:putLogEvents" {
		return sfnInvokeJSONService(handleCWPutLogEvents, input)
	}
	if resource == "arn:aws:states:::ecs:runTask" ||
		resource == "arn:aws:states:::ecs:runTask.sync" ||
		resource == "arn:aws:states:::ecs:runTask.waitForTaskToken" ||
		resource == "arn:aws:states:::aws-sdk:ecs:runTask" {
		return sfnInvokeECSRunTask(resource, input, context, cancel, heartbeat)
	}
	if strings.HasPrefix(resource, "arn:aws:states:::codebuild:") {
		return sfnInvokeCodeBuild(resource, input, context, cancel)
	}
	if strings.HasPrefix(resource, "arn:aws:states:::aws-sdk:") {
		return sfnInvokeAWSSDK(resource, input)
	}
	return nil, &sfnExecutionError{
		Name:  "States.TaskFailed",
		Cause: fmt.Sprintf("Task resource %q is not implemented by an AWS service slice", resource),
	}
}

var sfnAWSJSONServiceTargets = map[string]string{
	"acm":                    "CertificateManager",
	"acmpca":                 "ACMPrivateCA",
	"applicationautoscaling": "AnyScaleFrontendService",
	"budgets":                "AWSBudgetServiceGateway",
	"cloudtrail":             "CloudTrail_20131101",
	"cloudwatch":             "GraniteServiceVersion20100801",
	"cloudwatchlogs":         "Logs_20140328",
	"codebuild":              "CodeBuild_20161006",
	"dynamodb":               "DynamoDB_20120810",
	"ecr":                    "AmazonEC2ContainerRegistry_V20150921",
	"ecs":                    "AmazonEC2ContainerServiceV20141113",
	"eventbridge":            "AWSEvents",
	"firehose":               "Firehose_20150804",
	"glue":                   "AWSGlue",
	"kinesis":                "Kinesis_20131202",
	"kms":                    "TrentService",
	"organizations":          "AWSOrganizationsV20161128",
	"secretsmanager":         "secretsmanager",
	"servicediscovery":       "Route53AutoNaming_v20170314",
	"sfn":                    "AWSStepFunctions",
	"sqs":                    "AmazonSQS",
	"ssm":                    "AmazonSSM",
	"wafv2":                  "AWSWAF_20190729",
}

// sfnInvokeAWSSDK dispatches a generic AWS SDK integration through the same
// registered awsJson handler used by official clients. The Step Functions
// resource uses the SDK's lower-camel operation name while X-Amz-Target uses
// the Smithy operation name, whose first letter is uppercase.
func sfnInvokeAWSSDK(resource string, input any) (any, *sfnExecutionError) {
	const prefix = "arn:aws:states:::aws-sdk:"
	integration := strings.TrimPrefix(resource, prefix)
	parts := strings.Split(integration, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, &sfnExecutionError{
			Name:  "States.Runtime",
			Cause: fmt.Sprintf("The resource provided in the task state is not a valid AWS SDK integration ARN: %s", resource),
		}
	}
	service, action := parts[0], parts[1]
	if _, restJSONService := sfnAWSRESTJSONOperations[service]; restJSONService {
		return sfnInvokeRESTJSONService(service, action, input)
	}
	if _, restXMLService := sfnAWSRESTXMLOperations[service]; restXMLService {
		return sfnInvokeRESTXMLService(service, action, input)
	}
	if _, queryService := sfnAWSQueryServices[service]; queryService {
		return sfnInvokeQueryService(service, action, input)
	}
	targetPrefix, ok := sfnAWSJSONServiceTargets[service]
	if !ok {
		return nil, &sfnExecutionError{
			Name:  "States.TaskFailed",
			Cause: fmt.Sprintf("The service %q is not implemented by an AWS service slice", service),
		}
	}
	action = strings.ToUpper(action[:1]) + action[1:]
	handler, ok := sfnAWSRouter.Handler(targetPrefix + "." + action)
	if !ok {
		return nil, &sfnExecutionError{
			Name:  "States.TaskFailed",
			Cause: fmt.Sprintf("The operation %s:%s is not implemented by the AWS service slice", service, action),
		}
	}
	return sfnInvokeJSONService(handler, input)
}

func sfnInvokeECSRunTask(resource string, input, context any, cancel <-chan struct{}, heartbeat time.Duration) (any, *sfnExecutionError) {
	var (
		tasks    []ECSTask
		failures []any
	)
	if checkpoint, ok := sfnLoadTaskCheckpoint(context, resource); ok && strings.HasSuffix(resource, ".sync") {
		for _, taskID := range checkpoint.ResourceIDs {
			task, exists := ecsTasks.Get(taskID)
			if !exists {
				return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "checkpointed Amazon ECS task no longer exists: " + taskID}
			}
			tasks = append(tasks, task)
		}
	} else {
		result, taskErr := sfnInvokeJSONService(handleECSRunTask, input)
		if taskErr != nil {
			return nil, taskErr
		}
		response, ok := result.(map[string]any)
		if !ok {
			return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "Amazon ECS returned an invalid RunTask response"}
		}
		var decodeErr *sfnExecutionError
		tasks, decodeErr = sfnDecodeECSTasks(response["tasks"])
		if decodeErr != nil {
			return nil, decodeErr
		}
		failures, _ = response["failures"].([]any)
		if strings.HasSuffix(resource, ".sync") {
			taskIDs := make([]string, 0, len(tasks))
			for _, task := range tasks {
				taskIDs = append(taskIDs, task.TaskID())
			}
			sfnStoreTaskCheckpoint(context, resource, taskIDs)
		}
	}
	optimized := !strings.Contains(resource, ":::aws-sdk:")
	if optimized && len(failures) > 0 {
		return nil, &sfnExecutionError{
			Name:  "AmazonECS.Unknown",
			Cause: "Amazon ECS RunTask returned a non-empty failures field",
		}
	}
	output := map[string]any{
		"Tasks":    ecsTasksWire(tasks),
		"Failures": failures,
	}
	if strings.HasSuffix(resource, ".waitForTaskToken") {
		task, tokenErr := sfnTaskFromContext(context)
		if tokenErr != nil {
			sfnStopECSTasks(tasks, "AWS Step Functions callback integration failed")
			return nil, tokenErr
		}
		value, waitErr := sfnWaitForTaskToken(task, cancel, heartbeat)
		if waitErr != nil {
			sfnStopECSTasks(tasks, "AWS Step Functions callback integration stopped")
			return nil, waitErr
		}
		return value, nil
	}
	if !strings.HasSuffix(resource, ".sync") {
		return output, nil
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			sfnStopECSTasks(tasks, "AWS Step Functions execution aborted")
			return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "execution aborted"}
		case <-ticker.C:
			current := make([]ECSTask, 0, len(tasks))
			allStopped := true
			for _, started := range tasks {
				task, exists := ecsTasks.Get(started.TaskID())
				if !exists {
					return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "Amazon ECS task no longer exists: " + started.TaskArn}
				}
				current = append(current, task)
				if task.LastStatus != ECSTaskStatusStopped {
					allStopped = false
				}
			}
			if !allStopped {
				continue
			}
			for _, task := range current {
				for _, container := range task.Containers {
					if container.ExitCode != nil && *container.ExitCode != 0 {
						return nil, &sfnExecutionError{
							Name:  "States.TaskFailed",
							Cause: fmt.Sprintf("Amazon ECS task %s container %s exited with status %d", task.TaskArn, container.Name, *container.ExitCode),
						}
					}
				}
			}
			output["Tasks"] = ecsTasksWire(current)
			return output, nil
		}
	}
}

func sfnDecodeECSTasks(value any) ([]ECSTask, *sfnExecutionError) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "Amazon ECS returned invalid tasks: " + err.Error()}
	}
	var tasks []ECSTask
	if err := json.Unmarshal(encoded, &tasks); err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "Amazon ECS returned invalid tasks: " + err.Error()}
	}
	return tasks, nil
}

func sfnStopECSTasks(tasks []ECSTask, reason string) {
	for _, task := range tasks {
		stopECSTask(task.TaskID(), reason, "ServiceSchedulerInitiated")
	}
}

func sfnInvokeCodeBuild(resource string, input, context any, cancel <-chan struct{}) (any, *sfnExecutionError) {
	action := strings.TrimPrefix(resource, "arn:aws:states:::codebuild:")
	syncIntegration := strings.HasSuffix(action, ".sync")
	action = strings.TrimSuffix(action, ".sync")

	var handler http.HandlerFunc
	switch action {
	case "startBuild":
		handler = handleCBStartBuild
	case "stopBuild":
		handler = handleCBStopBuild
	case "batchDeleteBuilds":
		handler = handleCBBatchDeleteBuilds
	case "batchGetReports":
		handler = handleCBBatchGetReports
	case "startBuildBatch":
		handler = handleCBStartBuildBatch
	case "stopBuildBatch":
		handler = handleCBStopBuildBatch
	case "retryBuildBatch":
		handler = handleCBRetryBuildBatch
	case "deleteBuildBatch":
		handler = handleCBDeleteBuildBatch
	default:
		return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "unsupported AWS CodeBuild integration action: " + action}
	}
	var checkpointID string
	if checkpoint, ok := sfnLoadTaskCheckpoint(context, resource); ok && syncIntegration && len(checkpoint.ResourceIDs) == 1 {
		checkpointID = checkpoint.ResourceIDs[0]
	}
	if checkpointID != "" {
		switch action {
		case "startBuild":
			return sfnWaitForCodeBuildBuild(checkpointID, cancel)
		case "startBuildBatch", "retryBuildBatch":
			return sfnWaitForCodeBuildBatch(checkpointID, cancel)
		}
	}
	result, taskErr := sfnInvokeJSONService(handler, input)
	if taskErr != nil || !syncIntegration {
		return result, taskErr
	}
	response, ok := result.(map[string]any)
	if !ok {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "AWS CodeBuild returned an invalid response"}
	}
	switch action {
	case "startBuild":
		build, decodeErr := sfnDecodeCodeBuildBuild(response["build"])
		if decodeErr != nil {
			return nil, decodeErr
		}
		sfnStoreTaskCheckpoint(context, resource, []string{build.ID})
		return sfnWaitForCodeBuildBuild(build.ID, cancel)
	case "startBuildBatch", "retryBuildBatch":
		batch, decodeErr := sfnDecodeCodeBuildBatch(response["buildBatch"])
		if decodeErr != nil {
			return nil, decodeErr
		}
		sfnStoreTaskCheckpoint(context, resource, []string{batch.ID})
		return sfnWaitForCodeBuildBatch(batch.ID, cancel)
	default:
		return result, nil
	}
}

func sfnTaskCheckpointCoordinates(context any) (string, string, bool) {
	contextMap, ok := context.(map[string]any)
	if !ok {
		return "", "", false
	}
	execution, ok := contextMap["Execution"].(map[string]any)
	if !ok {
		return "", "", false
	}
	state, ok := contextMap["State"].(map[string]any)
	if !ok {
		return "", "", false
	}
	executionARN, _ := execution["Id"].(string)
	stateName, _ := state["Name"].(string)
	return executionARN, stateName, executionARN != "" && stateName != ""
}

func sfnLoadTaskCheckpoint(context any, resource string) (SFNTaskCheckpoint, bool) {
	executionARN, stateName, ok := sfnTaskCheckpointCoordinates(context)
	if !ok {
		return SFNTaskCheckpoint{}, false
	}
	execution, exists := sfnExecutions.Get(executionARN)
	if !exists || execution.TaskCheckpoint == nil ||
		execution.TaskCheckpoint.StateName != stateName ||
		execution.TaskCheckpoint.Resource != resource {
		return SFNTaskCheckpoint{}, false
	}
	return *execution.TaskCheckpoint, true
}

func sfnStoreTaskCheckpoint(context any, resource string, resourceIDs []string) {
	executionARN, stateName, ok := sfnTaskCheckpointCoordinates(context)
	if !ok {
		return
	}
	sfnExecutions.Update(executionARN, func(execution *SFNExecution) {
		execution.TaskCheckpoint = &SFNTaskCheckpoint{
			StateName: stateName, Resource: resource, ResourceIDs: append([]string(nil), resourceIDs...),
		}
	})
}

func sfnDecodeCodeBuildBuild(value any) (CBBuild, *sfnExecutionError) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return CBBuild{}, &sfnExecutionError{Name: "States.Runtime", Cause: "AWS CodeBuild returned an invalid build: " + err.Error()}
	}
	var build CBBuild
	if err := json.Unmarshal(encoded, &build); err != nil || build.ID == "" {
		return CBBuild{}, &sfnExecutionError{Name: "States.Runtime", Cause: "AWS CodeBuild returned an invalid build"}
	}
	return build, nil
}

func sfnDecodeCodeBuildBatch(value any) (CBBuildBatch, *sfnExecutionError) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return CBBuildBatch{}, &sfnExecutionError{Name: "States.Runtime", Cause: "AWS CodeBuild returned an invalid build batch: " + err.Error()}
	}
	var batch CBBuildBatch
	if err := json.Unmarshal(encoded, &batch); err != nil || batch.ID == "" {
		return CBBuildBatch{}, &sfnExecutionError{Name: "States.Runtime", Cause: "AWS CodeBuild returned an invalid build batch"}
	}
	return batch, nil
}

func sfnWaitForCodeBuildBuild(buildID string, cancel <-chan struct{}) (any, *sfnExecutionError) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			cbStopBuildByID(buildID)
			return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "execution aborted"}
		case <-ticker.C:
			build, exists := cbBuilds.Get(buildID)
			if !exists {
				return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "AWS CodeBuild build no longer exists: " + buildID}
			}
			if build.BuildStatus == "IN_PROGRESS" {
				continue
			}
			if build.BuildStatus != "SUCCEEDED" {
				return nil, &sfnExecutionError{Name: "CodeBuild.BuildFailed", Cause: "AWS CodeBuild build ended with status " + build.BuildStatus}
			}
			return map[string]any{"Build": build}, nil
		}
	}
}

func sfnWaitForCodeBuildBatch(batchID string, cancel <-chan struct{}) (any, *sfnExecutionError) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			cbStopBuildBatchByID(batchID)
			return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "execution aborted"}
		case <-ticker.C:
			batch, exists := cbBuildBatches.Get(batchID)
			if !exists {
				return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "AWS CodeBuild build batch no longer exists: " + batchID}
			}
			if batch.BuildBatchStatus == "IN_PROGRESS" {
				continue
			}
			if batch.BuildBatchStatus != "SUCCEEDED" {
				return nil, &sfnExecutionError{Name: "CodeBuild.BuildBatchFailed", Cause: "AWS CodeBuild build batch ended with status " + batch.BuildBatchStatus}
			}
			return map[string]any{"BuildBatch": batch}, nil
		}
	}
}

func sfnInvokeJSONService(handler http.HandlerFunc, input any) (any, *sfnExecutionError) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code >= http.StatusBadRequest {
		code, message := awsJSONError(rec.Body.Bytes())
		if code == "" {
			code = "States.TaskFailed"
		}
		return nil, &sfnExecutionError{Name: code, Cause: message}
	}
	if rec.Body.Len() == 0 {
		return map[string]any{}, nil
	}
	var result any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "service returned invalid JSON: " + err.Error()}
	}
	return result, nil
}

func sfnInvokeSNSPublish(input any) (any, *sfnExecutionError) {
	parameters, ok := input.(map[string]any)
	if !ok {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "SNS integration parameters must be an object"}
	}
	form := url.Values{}
	for _, key := range []string{"TopicArn", "TargetArn", "PhoneNumber", "Message", "Subject", "MessageGroupId", "MessageDeduplicationId"} {
		if value, ok := parameters[key].(string); ok && value != "" {
			form.Set(key, value)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleSNSPublish(rec, req)
	if rec.Code >= http.StatusBadRequest {
		code, message := awsXMLError(rec.Body.Bytes())
		if code == "" {
			code = "States.TaskFailed"
		}
		return nil, &sfnExecutionError{Name: code, Cause: message}
	}
	var response struct {
		MessageID string `xml:"PublishResult>MessageId"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "Amazon SNS returned invalid XML: " + err.Error()}
	}
	return map[string]any{"MessageId": response.MessageID}, nil
}

func sfnInvokeNestedExecution(resource string, input, context any, cancel <-chan struct{}, heartbeat time.Duration) (any, *sfnExecutionError) {
	parameters, ok := input.(map[string]any)
	if !ok {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "StartExecution parameters must be an object"}
	}
	stateMachineARN, _ := parameters["StateMachineArn"].(string)
	if stateMachineARN == "" {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "StateMachineArn is required"}
	}
	name, _ := parameters["Name"].(string)
	if name == "" {
		name = uuid.NewString()
	}
	nestedInput := "{}"
	if value, exists := parameters["Input"]; exists {
		if text, stringValue := value.(string); stringValue {
			nestedInput = text
		} else {
			encoded, err := sfnEncodeJSON(value)
			if err != nil {
				return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
			}
			nestedInput = encoded
		}
	}
	execution, startErr := sfnStartNestedExecution(stateMachineARN, name, nestedInput)
	if startErr != nil {
		return nil, startErr
	}
	result := map[string]any{
		"ExecutionArn": execution.ExecutionArn,
		"StartDate":    execution.StartDate,
	}
	if strings.HasSuffix(resource, ".waitForTaskToken") {
		task, tokenErr := sfnTaskFromContext(context)
		if tokenErr != nil {
			return nil, tokenErr
		}
		return sfnWaitForTaskToken(task, cancel, heartbeat)
	}
	if !strings.Contains(resource, ".sync") {
		return result, nil
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "execution aborted"}
		case <-ticker.C:
			current, exists := sfnExecutions.Get(execution.ExecutionArn)
			if !exists {
				return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "nested execution no longer exists"}
			}
			if current.Status == "RUNNING" {
				continue
			}
			result["StopDate"] = current.StopDate
			result["Status"] = current.Status
			result["Name"] = current.Name
			result["StateMachineArn"] = current.StateMachineArn
			result["Input"] = current.Input
			if current.Status != "SUCCEEDED" {
				return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: current.Cause}
			}
			if strings.HasSuffix(resource, ".sync:2") {
				output, decodeErr := sfnDecodeJSON(current.Output)
				if decodeErr != nil {
					return nil, &sfnExecutionError{Name: "States.Runtime", Cause: decodeErr.Error()}
				}
				result["Output"] = output
			} else {
				result["Output"] = current.Output
			}
			return result, nil
		}
	}
}

func sfnStartNestedExecution(stateMachineARN, name, input string) (SFNExecution, *sfnExecutionError) {
	stateMachine, versionARN, aliasARN, ok := sfnResolveExecutionTarget(stateMachineARN)
	if !ok {
		return SFNExecution{}, &sfnExecutionError{
			Name:  "StepFunctions.StateMachineDoesNotExist",
			Cause: "State machine does not exist: " + stateMachineARN,
		}
	}
	if err := sfnValidateDefinition(stateMachine.Definition); err != nil {
		return SFNExecution{}, &sfnExecutionError{Name: "StepFunctions.InvalidDefinition", Cause: err.Error()}
	}
	executionARN := sfnARN("execution:" + stateMachine.Name + ":" + name)
	sfnMu.Lock()
	defer sfnMu.Unlock()
	if _, exists := sfnExecutions.Get(executionARN); exists {
		return SFNExecution{}, &sfnExecutionError{
			Name:  "StepFunctions.ExecutionAlreadyExists",
			Cause: "Execution already exists: " + executionARN,
		}
	}
	now := sfnEpochNow()
	execution := SFNExecution{
		ExecutionArn:           executionARN,
		StateMachineArn:        stateMachine.StateMachineArn,
		StateMachineAliasArn:   aliasARN,
		StateMachineVersionArn: versionARN,
		Name:                   name,
		Status:                 "RUNNING",
		StartDate:              now,
		Input:                  input,
		DefinitionSnapshot:     stateMachine.Definition,
		RoleArnSnapshot:        stateMachine.RoleArn,
		TypeSnapshot:           stateMachine.Type,
		RevisionIdSnapshot:     stateMachine.RevisionId,
		UpdateDateSnapshot:     stateMachine.UpdateDate,
		LoggingSnapshot:        stateMachine.LoggingConfiguration,
		TracingSnapshot:        stateMachine.TracingConfiguration,
		EncryptionSnapshot:     stateMachine.EncryptionConfiguration,
	}
	sfnExecutions.Put(executionARN, execution)
	sfnAppendHistory(executionARN, "ExecutionStarted", map[string]any{
		"input":        input,
		"inputDetails": map[string]any{"truncated": false},
		"roleArn":      stateMachine.RoleArn,
	})
	executionCancel := make(chan struct{})
	sfnCancels.Store(executionARN, executionCancel)
	go sfnRunExecution(executionARN, stateMachine.Definition, input, executionCancel)
	return execution, nil
}

func sfnInvokeLambda(name, payload string) (any, *sfnExecutionError) {
	if i := strings.Index(name, ":function:"); i >= 0 {
		name = name[i+len(":function:"):]
	}
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		return nil, &sfnExecutionError{Name: "Lambda.ResourceNotFoundException", Cause: "Function not found: " + name}
	}
	response, unhandled, _ := invokeLambdaViaRuntimeAPI(fn, []byte(payload))
	if unhandled {
		var detail map[string]any
		_ = json.Unmarshal(response, &detail)
		errorType, _ := detail["errorType"].(string)
		if errorType == "" {
			errorType = "Lambda.Unknown"
		}
		return nil, &sfnExecutionError{Name: errorType, Cause: string(response)}
	}
	value, err := sfnDecodeJSON(string(response))
	if err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "AWS Lambda returned invalid JSON"}
	}
	return value, nil
}

func sfnTaskFromContext(context any) (SFNActivityTask, *sfnExecutionError) {
	root, _ := context.(map[string]any)
	taskContext, _ := root["Task"].(map[string]any)
	token, _ := taskContext["Token"].(string)
	if token == "" {
		return SFNActivityTask{}, &sfnExecutionError{Name: "States.Runtime", Cause: "task-token integration has no task token"}
	}
	task, ok := sfnActivityTasks.Get(token)
	if !ok {
		return SFNActivityTask{}, &sfnExecutionError{Name: "States.Runtime", Cause: "task token was not registered"}
	}
	return task, nil
}

func sfnWaitForTaskToken(task SFNActivityTask, cancel <-chan struct{}, heartbeat time.Duration) (any, *sfnExecutionError) {
	if heartbeat > 0 && task.LastHB == 0 {
		task.LastHB = sfnEpochNow()
		sfnActivityTasks.Put(task.TaskToken, task)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "execution aborted"}
		case <-ticker.C:
			current, ok := sfnActivityTasks.Get(task.TaskToken)
			if !ok {
				return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "task token no longer exists"}
			}
			switch current.Status {
			case "SUCCEEDED":
				value, err := sfnDecodeJSON(current.Output)
				if err != nil {
					return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "task output is not valid JSON"}
				}
				return value, nil
			case "FAILED":
				name := current.Error
				if name == "" {
					name = "States.TaskFailed"
				}
				return nil, &sfnExecutionError{Name: name, Cause: current.Cause}
			}
			if heartbeat > 0 && current.Status == "RUNNING" &&
				time.Since(time.Unix(0, int64(current.LastHB*float64(time.Second)))) >= heartbeat {
				current.Status = "TIMED_OUT"
				sfnActivityTasks.Put(current.TaskToken, current)
				return nil, &sfnExecutionError{Name: "States.HeartbeatTimeout", Cause: "Task heartbeat timed out"}
			}
		}
	}
}

func sfnInvokeActivity(activityARN, input string, cancel <-chan struct{}, heartbeat time.Duration) (any, *sfnExecutionError) {
	if _, ok := sfnActivities.Get(sfnNameFromARN(activityARN)); !ok {
		return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "Activity does not exist: " + activityARN}
	}
	token := uuid.NewString()
	task := SFNActivityTask{
		TaskToken:   token,
		ActivityArn: activityARN,
		Input:       input,
		Status:      "SCHEDULED",
	}
	sfnActivityTasks.Put(token, task)
	return sfnWaitForTaskToken(task, cancel, heartbeat)
}

func sfnRunParallel(state sfnState, input any, cancel <-chan struct{}, depth int, variables map[string]any, executionARN string) (any, *sfnExecutionError) {
	inputJSON, err := sfnEncodeJSON(input)
	if err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
	}
	type branchResult struct {
		index  int
		output any
		err    *sfnExecutionError
		abort  bool
	}
	results := make([]any, len(state.Branches))
	resultCh := make(chan branchResult, len(state.Branches))
	for i, branch := range state.Branches {
		go func(index int, definition sfnDefinition) {
			output, status, runErr := sfnRunDefDepthRuntime(definition, inputJSON, cancel, depth+1, variables, executionARN)
			if status == "ABORTED" {
				resultCh <- branchResult{index: index, abort: true}
				return
			}
			if runErr != nil {
				resultCh <- branchResult{index: index, err: sfnAsExecutionError(runErr)}
				return
			}
			value, decodeErr := sfnDecodeJSON(output)
			if decodeErr != nil {
				resultCh <- branchResult{index: index, err: &sfnExecutionError{Name: "States.Runtime", Cause: decodeErr.Error()}}
				return
			}
			resultCh <- branchResult{index: index, output: value}
		}(i, branch)
	}
	for range state.Branches {
		result := <-resultCh
		if result.abort {
			return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "parallel branch aborted"}
		}
		if result.err != nil {
			return nil, result.err
		}
		results[result.index] = result.output
	}
	return results, nil
}

func sfnRunMap(state sfnState, stateName string, input, context any, cancel <-chan struct{}, depth int, variables map[string]any, executionARN string) (output any, executionErr *sfnExecutionError) {
	processor := state.ItemProcessor
	if processor == nil {
		processor = state.Iterator
	}
	if processor == nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "Map state requires ItemProcessor or Iterator"}
	}
	itemsSource := input
	if state.ItemsPath != "" {
		value, ok := sfnPathValue(input, context, state.ItemsPath)
		if !ok {
			return nil, &sfnExecutionError{Name: "States.ItemReaderFailed", Cause: "ItemsPath did not resolve"}
		}
		itemsSource = value
	}
	items, ok := itemsSource.([]any)
	if !ok {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "Map input must be an array"}
	}
	var mapRun *SFNMapRun
	var mapParentExecution SFNExecution
	mapLabel := ""
	if processor.ProcessorConfig != nil && processor.ProcessorConfig.Mode == "DISTRIBUTED" && executionARN != "" {
		execution, exists := sfnExecutions.Get(executionARN)
		if !exists {
			return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "parent execution does not exist"}
		}
		label := state.Label
		if label == "" {
			label = stateName
		}
		mapParentExecution = execution
		mapLabel = label
		toleratedPercentage := float64(0)
		if state.ToleratedFailurePercentage != nil {
			toleratedPercentage = *state.ToleratedFailurePercentage
		}
		toleratedCount := 0
		if state.ToleratedFailureCount != nil {
			toleratedCount = *state.ToleratedFailureCount
		}
		run := SFNMapRun{
			MapRunArn:                  sfnARN("mapRun:" + sfnNameFromARN(execution.StateMachineArn) + "/" + label + ":" + uuid.NewString()),
			ExecutionArn:               executionARN,
			StateMachineArn:            execution.StateMachineArn,
			Status:                     "RUNNING",
			StartDate:                  sfnEpochNow(),
			MaxConcurrency:             state.MaxConcurrency,
			ToleratedFailurePercentage: toleratedPercentage,
			ToleratedFailureCount:      toleratedCount,
			Total:                      len(items),
			Pending:                    len(items),
		}
		sfnMapRuns.Put(run.MapRunArn, run)
		mapRun = &run
		defer func() {
			finished, exists := sfnMapRuns.Get(run.MapRunArn)
			if !exists {
				return
			}
			now := sfnEpochNow()
			finished.StopDate = &now
			if executionErr != nil {
				finished.Status = "FAILED"
			} else {
				finished.Status = "SUCCEEDED"
			}
			sfnMapRuns.Put(run.MapRunArn, finished)
		}()
	}
	results := make([]any, len(items))
	maxConcurrency := state.MaxConcurrency
	if maxConcurrency <= 0 || maxConcurrency > len(items) {
		maxConcurrency = len(items)
	}
	if maxConcurrency == 0 {
		return results, nil
	}
	type itemResult struct {
		index  int
		output any
		err    *sfnExecutionError
	}
	work := make(chan int)
	resultCh := make(chan itemResult, len(items))
	var workers sync.WaitGroup
	for worker := 0; worker < maxConcurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range work {
				if mapRun != nil {
					sfnMapRuns.Update(mapRun.MapRunArn, func(run *SFNMapRun) {
						run.Pending--
						run.Running++
					})
				}
				itemInput := items[index]
				itemContext := map[string]any{
					"Map": map[string]any{
						"Item": map[string]any{"Index": index, "Value": itemInput},
					},
				}
				if state.ItemSelector != nil {
					var selectErr error
					itemInput, selectErr = sfnResolvePayload(state.ItemSelector, input, itemContext)
					if selectErr != nil {
						resultCh <- itemResult{index: index, err: &sfnExecutionError{Name: "States.Runtime", Cause: selectErr.Error()}}
						continue
					}
				}
				itemJSON, encodeErr := sfnEncodeJSON(itemInput)
				if encodeErr != nil {
					resultCh <- itemResult{index: index, err: &sfnExecutionError{Name: "States.Runtime", Cause: encodeErr.Error()}}
					continue
				}
				itemExecutionARN := executionARN
				if mapRun != nil {
					childName := uuid.NewString()
					itemExecutionARN = sfnARN(
						"execution:" + sfnNameFromARN(mapParentExecution.StateMachineArn) + "/" + mapLabel + ":" + childName,
					)
					childStateMachineARN := mapParentExecution.StateMachineArn + "/" + mapLabel
					child := SFNExecution{
						ExecutionArn:       itemExecutionARN,
						StateMachineArn:    childStateMachineARN,
						Name:               childName,
						Status:             "RUNNING",
						StartDate:          sfnEpochNow(),
						Input:              itemJSON,
						MapRunArn:          mapRun.MapRunArn,
						DefinitionSnapshot: mapParentExecution.DefinitionSnapshot,
						RoleArnSnapshot:    mapParentExecution.RoleArnSnapshot,
						TypeSnapshot:       processor.ProcessorConfig.ExecutionType,
						RevisionIdSnapshot: mapParentExecution.RevisionIdSnapshot,
						UpdateDateSnapshot: mapParentExecution.UpdateDateSnapshot,
						LoggingSnapshot:    mapParentExecution.LoggingSnapshot,
						TracingSnapshot:    mapParentExecution.TracingSnapshot,
						EncryptionSnapshot: mapParentExecution.EncryptionSnapshot,
					}
					sfnExecutions.Put(itemExecutionARN, child)
					if child.TypeSnapshot == "STANDARD" {
						sfnAppendHistory(itemExecutionARN, "ExecutionStarted", map[string]any{
							"input":        itemJSON,
							"inputDetails": map[string]any{"truncated": false},
							"roleArn":      child.RoleArnSnapshot,
						})
					}
				}
				output, status, runErr := sfnRunDefDepthRuntime(*processor, itemJSON, cancel, depth+1, variables, itemExecutionARN)
				if mapRun != nil {
					sfnCompleteExecution(itemExecutionARN, status, output, runErr)
				}
				if status == "ABORTED" {
					resultCh <- itemResult{index: index, err: &sfnExecutionError{Name: "States.TaskFailed", Cause: "map iteration aborted"}}
					continue
				}
				if runErr != nil {
					resultCh <- itemResult{index: index, err: sfnAsExecutionError(runErr)}
					continue
				}
				value, decodeErr := sfnDecodeJSON(output)
				if decodeErr != nil {
					resultCh <- itemResult{index: index, err: &sfnExecutionError{Name: "States.Runtime", Cause: decodeErr.Error()}}
					continue
				}
				resultCh <- itemResult{index: index, output: value}
			}
		}()
	}
	go func() {
		for index := range items {
			work <- index
		}
		close(work)
		workers.Wait()
		close(resultCh)
	}()
	var firstError *sfnExecutionError
	for result := range resultCh {
		if mapRun != nil {
			sfnMapRuns.Update(mapRun.MapRunArn, func(run *SFNMapRun) { run.Running-- })
		}
		if result.err != nil {
			if mapRun != nil {
				sfnMapRuns.Update(mapRun.MapRunArn, func(run *SFNMapRun) { run.Failed++ })
			}
			if firstError == nil {
				firstError = result.err
			}
			continue
		}
		results[result.index] = result.output
		if mapRun != nil {
			sfnMapRuns.Update(mapRun.MapRunArn, func(run *SFNMapRun) {
				run.Succeeded++
				run.ResultsWritten++
			})
		}
	}
	if firstError != nil {
		failed := 1
		if mapRun != nil {
			if run, exists := sfnMapRuns.Get(mapRun.MapRunArn); exists {
				failed = run.Failed
			}
		}
		failureCountExceeded := state.ToleratedFailureCount != nil && failed > *state.ToleratedFailureCount
		failurePercentage := float64(failed) * 100 / float64(len(items))
		failurePercentageExceeded := state.ToleratedFailurePercentage != nil && failurePercentage > *state.ToleratedFailurePercentage
		noToleranceConfigured := state.ToleratedFailureCount == nil && state.ToleratedFailurePercentage == nil
		if noToleranceConfigured || failureCountExceeded || failurePercentageExceeded {
			return nil, &sfnExecutionError{
				Name:  "States.ExceedToleratedFailureThreshold",
				Cause: firstError.Cause,
			}
		}
	}
	return results, nil
}

func sfnAsExecutionError(err error) *sfnExecutionError {
	var executionErr *sfnExecutionError
	if errors.As(err, &executionErr) {
		return executionErr
	}
	return &sfnExecutionError{Name: "States.TaskFailed", Cause: err.Error()}
}

func sfnEvalChoiceValue(rule sfnChoiceRule, input, context any) bool {
	switch {
	case len(rule.And) > 0:
		for _, sub := range rule.And {
			if !sfnEvalChoiceValue(sub, input, context) {
				return false
			}
		}
		return true
	case len(rule.Or) > 0:
		for _, sub := range rule.Or {
			if sfnEvalChoiceValue(sub, input, context) {
				return true
			}
		}
		return false
	case rule.Not != nil:
		return !sfnEvalChoiceValue(*rule.Not, input, context)
	}
	value, present := sfnPathValue(input, context, rule.Variable)
	pathValue := func(expr string) (any, bool) {
		if expr == "" {
			return nil, false
		}
		return sfnPathValue(input, context, expr)
	}
	switch {
	case rule.IsPresent != nil:
		return present == *rule.IsPresent
	case rule.IsNull != nil:
		return present && (value == nil) == *rule.IsNull
	case rule.IsString != nil:
		_, ok := value.(string)
		return present && ok == *rule.IsString
	case rule.IsNumeric != nil:
		_, ok := sfnAsFloat(value)
		return present && ok == *rule.IsNumeric
	case rule.IsBoolean != nil:
		_, ok := value.(bool)
		return present && ok == *rule.IsBoolean
	case rule.IsTimestamp != nil:
		text, ok := value.(string)
		_, timeErr := time.Parse(time.RFC3339, text)
		return present && ok && (timeErr == nil) == *rule.IsTimestamp
	case rule.StringEquals != nil:
		text, ok := value.(string)
		return ok && text == *rule.StringEquals
	case rule.StringEqualsPath != "":
		right, ok := pathValue(rule.StringEqualsPath)
		return ok && value == right
	case rule.StringLessThan != nil:
		text, ok := value.(string)
		return ok && text < *rule.StringLessThan
	case rule.StringLessThanEquals != nil:
		text, ok := value.(string)
		return ok && text <= *rule.StringLessThanEquals
	case rule.StringGreaterThan != nil:
		text, ok := value.(string)
		return ok && text > *rule.StringGreaterThan
	case rule.StringGreaterThanEquals != nil:
		text, ok := value.(string)
		return ok && text >= *rule.StringGreaterThanEquals
	case rule.StringMatches != nil:
		text, ok := value.(string)
		return ok && sfnStringMatches(text, *rule.StringMatches)
	case rule.BooleanEquals != nil:
		boolean, ok := value.(bool)
		return ok && boolean == *rule.BooleanEquals
	case rule.BooleanEqualsPath != "":
		right, ok := pathValue(rule.BooleanEqualsPath)
		return ok && value == right
	case rule.NumericEquals != nil:
		number, ok := sfnAsFloat(value)
		return ok && number == *rule.NumericEquals
	case rule.NumericEqualsPath != "":
		left, leftOK := sfnAsFloat(value)
		rightValue, rightOK := pathValue(rule.NumericEqualsPath)
		right, numberOK := sfnAsFloat(rightValue)
		return leftOK && rightOK && numberOK && left == right
	case rule.NumericGreaterThan != nil:
		number, ok := sfnAsFloat(value)
		return ok && number > *rule.NumericGreaterThan
	case rule.NumericGreaterThanEquals != nil:
		number, ok := sfnAsFloat(value)
		return ok && number >= *rule.NumericGreaterThanEquals
	case rule.NumericLessThan != nil:
		number, ok := sfnAsFloat(value)
		return ok && number < *rule.NumericLessThan
	case rule.NumericLessThanEquals != nil:
		number, ok := sfnAsFloat(value)
		return ok && number <= *rule.NumericLessThanEquals
	case rule.NumericGreaterThanPath != "":
		return sfnCompareNumberPath(value, rule.NumericGreaterThanPath, input, context, func(a, b float64) bool { return a > b })
	case rule.NumericGreaterThanEqualsPath != "":
		return sfnCompareNumberPath(value, rule.NumericGreaterThanEqualsPath, input, context, func(a, b float64) bool { return a >= b })
	case rule.NumericLessThanPath != "":
		return sfnCompareNumberPath(value, rule.NumericLessThanPath, input, context, func(a, b float64) bool { return a < b })
	case rule.NumericLessThanEqualsPath != "":
		return sfnCompareNumberPath(value, rule.NumericLessThanEqualsPath, input, context, func(a, b float64) bool { return a <= b })
	}
	return sfnCompareTimestampRule(rule, value, input, context)
}

func sfnCompareNumberPath(leftValue any, rightPath string, input, context any, compare func(float64, float64) bool) bool {
	left, leftOK := sfnAsFloat(leftValue)
	rightValue, rightExists := sfnPathValue(input, context, rightPath)
	right, rightOK := sfnAsFloat(rightValue)
	return leftOK && rightExists && rightOK && compare(left, right)
}

func sfnCompareTimestampRule(rule sfnChoiceRule, value, input, context any) bool {
	leftText, ok := value.(string)
	if !ok {
		return false
	}
	left, err := time.Parse(time.RFC3339, leftText)
	if err != nil {
		return false
	}
	compareLiteral := func(literal *string, path string, predicate func(time.Time, time.Time) bool) bool {
		var rightText string
		if literal != nil {
			rightText = *literal
		} else if path != "" {
			value, exists := sfnPathValue(input, context, path)
			if !exists {
				return false
			}
			var stringValue bool
			rightText, stringValue = value.(string)
			if !stringValue {
				return false
			}
		} else {
			return false
		}
		right, parseErr := time.Parse(time.RFC3339, rightText)
		return parseErr == nil && predicate(left, right)
	}
	switch {
	case rule.TimestampEquals != nil || rule.TimestampEqualsPath != "":
		return compareLiteral(rule.TimestampEquals, rule.TimestampEqualsPath, func(a, b time.Time) bool { return a.Equal(b) })
	case rule.TimestampLessThan != nil || rule.TimestampLessThanPath != "":
		return compareLiteral(rule.TimestampLessThan, rule.TimestampLessThanPath, func(a, b time.Time) bool { return a.Before(b) })
	case rule.TimestampLessThanEquals != nil || rule.TimestampLessThanEqualsPath != "":
		return compareLiteral(rule.TimestampLessThanEquals, rule.TimestampLessThanEqualsPath, func(a, b time.Time) bool { return a.Before(b) || a.Equal(b) })
	case rule.TimestampGreaterThan != nil || rule.TimestampGreaterThanPath != "":
		return compareLiteral(rule.TimestampGreaterThan, rule.TimestampGreaterThanPath, func(a, b time.Time) bool { return a.After(b) })
	case rule.TimestampGreaterThanEquals != nil || rule.TimestampGreaterThanEqualsPath != "":
		return compareLiteral(rule.TimestampGreaterThanEquals, rule.TimestampGreaterThanEqualsPath, func(a, b time.Time) bool { return a.After(b) || a.Equal(b) })
	}
	return false
}
