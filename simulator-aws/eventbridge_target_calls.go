package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// Amazon EventBridge delivers to a target through the target service's API:
// an Amazon SQS queue, an AWS Lambda function, an Amazon SNS topic and a
// CloudWatch Logs log group as events.amazonaws.com, which the target's
// resource policy must admit; an Amazon ECS cluster, an AWS Batch job queue,
// an Amazon Kinesis data stream, an AWS Step Functions state machine, an AWS
// CodeBuild project, an Amazon Data Firehose delivery stream, a Systems
// Manager Run Command document and another event bus as the target's RoleArn.

// ebTargetActor is who EventBridge calls a target as for the rule.
func ebTargetActor(ruleArn string, target EBTarget, withRole bool) awsActor {
	actor := awsActor{Service: "events.amazonaws.com", Source: ruleArn}
	if withRole {
		actor.Role = target.RoleArn
	}
	return actor
}

// ebCallResult reads a target service's answer as a delivery outcome.
func ebCallResult(status int, body []byte, err error, xmlErrors bool) delivery.Outcome {
	if err != nil {
		if callErr, ok := err.(*awsCallError); ok {
			return delivery.Permanent(ebTargetError{callErr.Code, callErr.Message}).WithStatus(callErr.Status)
		}
		return delivery.Retryable(err)
	}
	if status < 400 {
		return delivery.Delivered()
	}
	code, message := awsJSONError(body)
	if xmlErrors {
		code, message = awsXMLError(body)
	}
	failure := ebTargetError{code, message}
	if status == http.StatusTooManyRequests || status >= 500 || code == "ThrottlingException" || code == "Throttling" {
		return delivery.Retryable(failure).WithStatus(status)
	}
	return delivery.Permanent(failure).WithStatus(status)
}

// ebRequireRole refuses a target whose service EventBridge reaches only as a
// role when the target names none.
func ebRequireRole(target EBTarget) *delivery.Outcome {
	if target.RoleArn != "" {
		return nil
	}
	outcome := delivery.Permanent(ebTargetError{"AccessDeniedException", "Target " + target.Arn + " requires a RoleArn"})
	return &outcome
}

func ebSendToQueue(ctx context.Context, ruleArn string, target EBTarget, record EBEventRecord, body string) delivery.Outcome {
	queue := target.Arn[strings.LastIndex(target.Arn, ":")+1:]
	var params struct {
		MessageGroupID string `json:"MessageGroupId"`
	}
	if len(target.SqsParameters) > 0 {
		_ = json.Unmarshal(target.SqsParameters, &params)
	}
	input := map[string]any{"QueueUrl": sqsQueueURL(queue), "MessageBody": body}
	if params.MessageGroupID != "" {
		input["MessageGroupId"] = params.MessageGroupID
	}
	if strings.HasSuffix(queue, ".fifo") {
		input["MessageDeduplicationId"] = record.ID
	}
	status, response, err := awsCallJSON(ctx, ebTargetActor(ruleArn, target, false), "sqs", "AmazonSQS.SendMessage", "application/x-amz-json-1.0", input)
	return ebCallResult(status, response, err, false)
}

func ebInvokeFunction(ctx context.Context, ruleArn string, target EBTarget, body string) delivery.Outcome {
	header := http.Header{"X-Amz-Invocation-Type": {"Event"}, "Content-Type": {"application/json"}}
	status, response, err := awsCall(ctx, ebTargetActor(ruleArn, target, false), "lambda", http.MethodPost,
		"/2015-03-31/functions/"+url.PathEscape(target.Arn)+"/invocations", header, []byte(body))
	return ebCallResult(status, response, err, false)
}

func ebPublishToTopic(ctx context.Context, ruleArn string, target EBTarget, body string) delivery.Outcome {
	form := url.Values{"TopicArn": {target.Arn}, "Message": {body}}
	status, response, err := awsCallQuery(ctx, ebTargetActor(ruleArn, target, false), "sns", "Publish", "2010-03-31", form)
	return ebCallResult(status, response, err, true)
}

func ebStartExecution(ctx context.Context, ruleArn string, target EBTarget, record EBEventRecord, body string) delivery.Outcome {
	if refused := ebRequireRole(target); refused != nil {
		return *refused
	}
	input := map[string]any{"stateMachineArn": target.Arn, "name": record.ID, "input": body}
	status, response, err := awsCallJSON(ctx, ebTargetActor(ruleArn, target, true), "states", "AWSStepFunctions.StartExecution", "application/x-amz-json-1.0", input)
	if code, _ := awsJSONError(response); status >= 400 && code == "ExecutionAlreadyExists" {
		// A retried delivery of one event starts the execution named for it once.
		return delivery.Delivered()
	}
	return ebCallResult(status, response, err, false)
}

// ebWriteToLogGroup writes the event to the log group in a stream named for
// the rule, creating the stream on the first delivery.
func ebWriteToLogGroup(ctx context.Context, ruleArn string, target EBTarget, body string) delivery.Outcome {
	group := strings.TrimSuffix(strings.SplitN(target.Arn, ":log-group:", 2)[1], ":*")
	stream := "eventbridge/" + cloudTrailShortName(ruleArn)
	actor := ebTargetActor(ruleArn, target, false)
	status, response, err := awsCallJSON(ctx, actor, "logs", "Logs_20140328.CreateLogStream", "application/x-amz-json-1.1",
		map[string]any{"logGroupName": group, "logStreamName": stream})
	if code, _ := awsJSONError(response); err != nil || (status >= 400 && code != "ResourceAlreadyExistsException") {
		return ebCallResult(status, response, err, false)
	}
	status, response, err = awsCallJSON(ctx, actor, "logs", "Logs_20140328.PutLogEvents", "application/x-amz-json-1.1",
		map[string]any{"logGroupName": group, "logStreamName": stream,
			"logEvents": []map[string]any{{"timestamp": time.Now().UnixMilli(), "message": body}}})
	return ebCallResult(status, response, err, false)
}

func ebPutStreamRecord(ctx context.Context, ruleArn string, target EBTarget, record EBEventRecord, body string) delivery.Outcome {
	if refused := ebRequireRole(target); refused != nil {
		return *refused
	}
	partitionKey := record.ID
	var params struct {
		PartitionKeyPath string `json:"PartitionKeyPath"`
	}
	if len(target.KinesisParameters) > 0 && json.Unmarshal(target.KinesisParameters, &params) == nil && params.PartitionKeyPath != "" {
		value, ok := ebJSONPath(ebBuildEvent(record), params.PartitionKeyPath)
		if !ok {
			return delivery.Permanent(ebTargetError{"InvalidParameterException", "PartitionKeyPath " + params.PartitionKeyPath + " selects nothing in the event"})
		}
		if text, isText := value.(string); isText {
			partitionKey = text
		} else {
			raw, _ := json.Marshal(value)
			partitionKey = string(raw)
		}
	}
	input := map[string]any{"StreamARN": target.Arn, "PartitionKey": partitionKey, "Data": base64.StdEncoding.EncodeToString([]byte(body))}
	status, response, err := awsCallJSON(ctx, ebTargetActor(ruleArn, target, true), "kinesis", "Kinesis_20131202.PutRecord", "application/x-amz-json-1.1", input)
	return ebCallResult(status, response, err, false)
}

func ebRunTask(ctx context.Context, ruleArn string, target EBTarget, body string) delivery.Outcome {
	if refused := ebRequireRole(target); refused != nil {
		return *refused
	}
	request, refused := ebRunTaskRequest(ruleArn, target, body)
	if refused != nil {
		return *refused
	}
	status, response, err := awsCallJSON(ctx, ebTargetActor(ruleArn, target, true), "ecs",
		"AmazonEC2ContainerServiceV20141113.RunTask", "application/x-amz-json-1.1", request)
	if outcome := ebCallResult(status, response, err, false); !outcome.OK() {
		return outcome
	}
	return ecsRunTaskFailuresOutcome(response)
}

func ebSubmitJob(ctx context.Context, ruleArn string, target EBTarget) delivery.Outcome {
	if refused := ebRequireRole(target); refused != nil {
		return *refused
	}
	var params struct {
		JobDefinition   string          `json:"JobDefinition"`
		JobName         string          `json:"JobName"`
		ArrayProperties json.RawMessage `json:"ArrayProperties"`
		RetryStrategy   json.RawMessage `json:"RetryStrategy"`
	}
	if len(target.BatchParameters) == 0 || json.Unmarshal(target.BatchParameters, &params) != nil || params.JobDefinition == "" || params.JobName == "" {
		return delivery.Permanent(ebTargetError{"ClientException", "Target " + target.Arn + " names no BatchParameters.JobDefinition and JobName"})
	}
	request := map[string]any{"jobName": params.JobName, "jobQueue": target.Arn, "jobDefinition": params.JobDefinition}
	if len(params.ArrayProperties) > 0 {
		request["arrayProperties"] = lowerFirstKeys(params.ArrayProperties)
	}
	if len(params.RetryStrategy) > 0 {
		request["retryStrategy"] = lowerFirstKeys(params.RetryStrategy)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return delivery.Permanent(err)
	}
	status, response, err := awsCall(ctx, ebTargetActor(ruleArn, target, true), "batch", http.MethodPost, "/v1/submitjob",
		http.Header{"Content-Type": {"application/json"}}, raw)
	return ebCallResult(status, response, err, false)
}

// ebStartBuild starts a build of the project the target names. A target with
// its own input passes it as the StartBuild request, as an Amazon ECS target
// passes its input as the task override.
func ebStartBuild(ctx context.Context, ruleArn string, target EBTarget, body string) delivery.Outcome {
	if refused := ebRequireRole(target); refused != nil {
		return *refused
	}
	var request map[string]any
	if ebCustomInput(target) {
		if json.Unmarshal([]byte(body), &request) != nil || request == nil {
			return delivery.Permanent(ebTargetError{"InvalidParameterException", "The input for an AWS CodeBuild target is not the JSON of a StartBuild request"})
		}
	} else {
		request = map[string]any{}
	}
	request["projectName"] = target.Arn[strings.LastIndex(target.Arn, "/")+1:]
	status, response, err := awsCallJSON(ctx, ebTargetActor(ruleArn, target, true), "codebuild", "CodeBuild_20161006.StartBuild", "application/x-amz-json-1.1", request)
	return ebCallResult(status, response, err, false)
}

// ebPutFirehoseRecord puts the event on the delivery stream as one record.
func ebPutFirehoseRecord(ctx context.Context, ruleArn string, target EBTarget, body string) delivery.Outcome {
	if refused := ebRequireRole(target); refused != nil {
		return *refused
	}
	input := map[string]any{
		"DeliveryStreamName": target.Arn[strings.LastIndex(target.Arn, "/")+1:],
		"Record":             map[string]any{"Data": base64.StdEncoding.EncodeToString([]byte(body))},
	}
	status, response, err := awsCallJSON(ctx, ebTargetActor(ruleArn, target, true), "firehose", "Firehose_20150804.PutRecord", "application/x-amz-json-1.1", input)
	return ebCallResult(status, response, err, false)
}

// ebSendCommand sends the document the target names to the instances its
// RunCommandTargets select. Nothing from the event reaches Run Command; a
// target with its own input passes it as the document's parameters.
func ebSendCommand(ctx context.Context, ruleArn string, target EBTarget, body string) delivery.Outcome {
	if refused := ebRequireRole(target); refused != nil {
		return *refused
	}
	var params struct {
		RunCommandTargets []SSMTarget `json:"RunCommandTargets"`
	}
	if len(target.RunCommandParameters) == 0 || json.Unmarshal(target.RunCommandParameters, &params) != nil || len(params.RunCommandTargets) == 0 {
		return delivery.Permanent(ebTargetError{"InvalidParameterException", "Target " + target.Arn + " names no RunCommandParameters.RunCommandTargets"})
	}
	request := map[string]any{
		"DocumentName": target.Arn[strings.Index(target.Arn, ":document/")+len(":document/"):],
		"Targets":      params.RunCommandTargets,
	}
	if ebCustomInput(target) {
		var parameters map[string][]string
		if json.Unmarshal([]byte(body), &parameters) != nil || parameters == nil {
			return delivery.Permanent(ebTargetError{"InvalidParameterException", "The input for an Amazon EC2 Run Command target is not the JSON of the document's parameters"})
		}
		request["Parameters"] = parameters
	}
	status, response, err := awsCallJSON(ctx, ebTargetActor(ruleArn, target, true), "ssm", "AmazonSSM.SendCommand", "application/x-amz-json-1.1", request)
	return ebCallResult(status, response, err, false)
}

// ebPutOnBus sends the event to another event bus as it arrived, which that
// bus's rules then match.
func ebPutOnBus(ctx context.Context, ruleArn string, target EBTarget, record EBEventRecord) delivery.Outcome {
	if refused := ebRequireRole(target); refused != nil {
		return *refused
	}
	entry := map[string]any{
		"EventBusName": target.Arn,
		"Source":       record.Source,
		"DetailType":   record.DetailType,
		"Detail":       record.Detail,
		"Resources":    record.Resources,
	}
	if record.Time > 0 {
		entry["Time"] = record.Time
	}
	status, response, err := awsCallJSON(ctx, ebTargetActor(ruleArn, target, true), "events", "AWSEvents.PutEvents", "application/x-amz-json-1.1",
		map[string]any{"Entries": []any{entry}})
	if outcome := ebCallResult(status, response, err, false); !outcome.OK() {
		return outcome
	}
	var entryErr *awsCallError
	if err := ebPutEventsEntryError(response); errors.As(err, &entryErr) {
		return delivery.Permanent(ebTargetError{entryErr.Code, entryErr.Message})
	} else if err != nil {
		return delivery.Permanent(err)
	}
	return delivery.Delivered()
}
