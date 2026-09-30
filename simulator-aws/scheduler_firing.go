package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// The CRUD surface in scheduler.go stores schedules; the ticker evaluates each
// ScheduleExpression in its ScheduleExpressionTimezone, within StartDate and
// EndDate, and invokes the Target when due by calling the simulator's own
// handlers, as Amazon EventBridge Scheduler invokes the downstream service.

func startSchedulerFiringLoop(srv *sim.Server, store sim.Store[Schedule], records sim.Store[cron.Record]) {
	cron.NewTicker(records, func() []cron.Entry { return schedulerEntries(store, records) }).
		Start(srv, "EventBridge Scheduler firing", time.Second)
}

func schedulerEntries(store sim.Store[Schedule], records sim.Store[cron.Record]) []cron.Entry {

	var entries []cron.Entry
	for _, s := range store.List() {
		plan, err := parseAWSSchedule(s.ScheduleExpression, s.ScheduleExpressionTimezone, true)
		if err != nil {
			continue
		}
		key := scheduleKey(s.GroupName, s.Name)
		within := func(t time.Time, ok bool) (time.Time, bool) {
			if !ok || (s.EndDate != nil && t.After(schedulerDate(*s.EndDate))) {
				return time.Time{}, false
			}
			return t, true
		}
		entry := cron.Entry{
			Key:    key,
			Spec:   fmt.Sprintf("%s|%s|%s|%s|%v", s.ScheduleExpression, s.ScheduleExpressionTimezone, schedulerDateSpec(s.StartDate), schedulerDateSpec(s.EndDate), s.LastModificationDate),
			Paused: s.State != "ENABLED",
			First: func(now time.Time) (time.Time, bool) {
				start := now
				if plan.rate > 0 && s.CreationDate > 0 {
					start = schedulerDate(s.CreationDate)
				}
				if s.StartDate != nil && schedulerDate(*s.StartDate).After(start) {
					start = schedulerDate(*s.StartDate)
				}
				return within(plan.first(start))
			},
			Fire: func(time.Time) {
				fireSchedule(s)
				if s.ActionAfterCompletion == "DELETE" && !plan.recurring() {
					store.Delete(key)
					records.Delete(key)
				}
			},
		}
		if plan.recurring() {
			entry.Next = func(after time.Time) (time.Time, bool) { return within(plan.next(after)) }
		}
		entries = append(entries, entry)
	}
	return entries
}

func schedulerDateSpec(epoch *float64) string {
	if epoch == nil {
		return ""
	}
	return strconv.FormatFloat(*epoch, 'f', -1, 64)
}

func schedulerDate(epoch float64) time.Time {
	return time.Unix(0, int64(epoch*float64(time.Second))).UTC()
}

type schedulerTarget struct {
	Arn               string              `json:"Arn"`
	RoleArn           string              `json:"RoleArn"`
	Input             string              `json:"Input"`
	EcsParameters     *schedulerEcsParams `json:"EcsParameters"`
	KinesisParameters *struct {
		PartitionKey string `json:"PartitionKey"`
	} `json:"KinesisParameters"`
	SqsParameters *struct {
		MessageGroupID string `json:"MessageGroupId"`
	} `json:"SqsParameters"`
	EventBridgeParameters *struct {
		DetailType string `json:"DetailType"`
		Source     string `json:"Source"`
	} `json:"EventBridgeParameters"`
	RetryPolicy *struct {
		MaximumEventAgeInSeconds *int `json:"MaximumEventAgeInSeconds"`
		MaximumRetryAttempts     *int `json:"MaximumRetryAttempts"`
	} `json:"RetryPolicy"`
	DeadLetterConfig *struct {
		Arn string `json:"Arn"`
	} `json:"DeadLetterConfig"`
}

type schedulerEcsParams struct {
	TaskDefinitionArn    string `json:"TaskDefinitionArn"`
	TaskCount            int    `json:"TaskCount"`
	LaunchType           string `json:"LaunchType"`
	Group                string `json:"Group"`
	NetworkConfiguration *struct {
		AwsvpcConfiguration *struct {
			Subnets        []string `json:"Subnets"`
			SecurityGroups []string `json:"SecurityGroups"`
			AssignPublicIp string   `json:"AssignPublicIp"`
		} `json:"AwsvpcConfiguration"`
	} `json:"NetworkConfiguration"`
}

// schedulerDelivery is one invocation Amazon EventBridge Scheduler owes a
// schedule's target, with the target as it stood when the schedule fired.
type schedulerDelivery struct {
	ScheduleArn string          `json:"ScheduleArn"`
	Target      json.RawMessage `json:"Target"`
}

var schedulerDeliveries *delivery.Dispatcher[schedulerDelivery]

func registerSchedulerDelivery(srv *sim.Server) {
	store := sim.MakeStore[delivery.Item[schedulerDelivery]](srv.DB(), "scheduler_target_deliveries")
	schedulerDeliveries = delivery.New(srv, "EventBridge Scheduler target deliveries", store, delivery.Handler[schedulerDelivery]{
		Policy: func(d schedulerDelivery) delivery.Policy { return schedulerRetryPolicy(schedulerParseTarget(d.Target)) },
		Attempt: func(_ context.Context, item *delivery.Item[schedulerDelivery]) delivery.Outcome {
			return schedulerAttemptTarget(schedulerParseTarget(item.Payload.Target))
		},
		Finish: schedulerFinishDelivery,
	})
	schedulerDeliveries.Resume()
}

func schedulerParseTarget(raw json.RawMessage) schedulerTarget {
	var target schedulerTarget
	_ = json.Unmarshal(raw, &target)
	return target
}

// schedulerRetryPolicy reads the target's RetryPolicy: MaximumRetryAttempts
// (default 185) and MaximumEventAgeInSeconds (default 86400), retried with
// exponential backoff.
func schedulerRetryPolicy(target schedulerTarget) delivery.Policy {
	retries, age := 185, 86400
	if target.RetryPolicy != nil {
		if target.RetryPolicy.MaximumRetryAttempts != nil {
			retries = *target.RetryPolicy.MaximumRetryAttempts
		}
		if target.RetryPolicy.MaximumEventAgeInSeconds != nil {
			age = *target.RetryPolicy.MaximumEventAgeInSeconds
		}
	}
	return delivery.Policy{
		MaxAttempts: retries + 1,
		MaxAge:      time.Duration(age) * time.Second,
		Backoff:     delivery.Exponential(time.Second, 5*time.Minute),
	}
}

// fireSchedule hands a due schedule's target invocation to the delivery
// dispatcher, which makes the first attempt at once and retries under the
// target's RetryPolicy.
func fireSchedule(s Schedule) {
	if schedulerParseTarget(s.Target).Arn == "" {
		return
	}
	schedulerDeliveries.SubmitAttempted(sim.NewUUID(), schedulerDelivery{ScheduleArn: s.Arn, Target: s.Target})
}

// schedulerAttemptTarget invokes the target's service once, as the
// schedule's execution role.
func schedulerAttemptTarget(t schedulerTarget) delivery.Outcome {
	switch {
	case strings.HasPrefix(t.Arn, schedulerUniversalTargetPrefix):
		return fireUniversalTarget(t)
	case strings.Contains(t.Arn, ":ecs:") && t.EcsParameters != nil:
		if denied := schedulerAuthorizeRole(t, "ecs:RunTask", t.EcsParameters.TaskDefinitionArn); denied != nil {
			return *denied
		}
		return fireECSTarget(t.Arn, t.EcsParameters)
	case strings.Contains(t.Arn, ":lambda:"):
		if denied := schedulerAuthorizeRole(t, "lambda:InvokeFunction", t.Arn); denied != nil {
			return *denied
		}
		return fireLambdaTarget(t.Arn, t.Input)
	case strings.Contains(t.Arn, ":sqs:"):
		if denied := schedulerAuthorizeRole(t, "sqs:SendMessage", t.Arn); denied != nil {
			return *denied
		}
		return fireSQSTarget(t)
	case strings.Contains(t.Arn, ":sns:"):
		if denied := schedulerAuthorizeRole(t, "sns:Publish", t.Arn); denied != nil {
			return *denied
		}
		return fireSNSTarget(t.Arn, t.Input)
	case strings.Contains(t.Arn, ":states:"):
		if denied := schedulerAuthorizeRole(t, "states:StartExecution", t.Arn); denied != nil {
			return *denied
		}
		return fireStepFunctionsTarget(t.Arn, t.Input)
	case strings.Contains(t.Arn, ":kinesis:") && t.KinesisParameters != nil:
		if denied := schedulerAuthorizeRole(t, "kinesis:PutRecord", t.Arn); denied != nil {
			return *denied
		}
		return fireKinesisTarget(t.Arn, t.KinesisParameters.PartitionKey, t.Input)
	case strings.HasPrefix(t.Arn, "arn:aws:events:") && strings.Contains(t.Arn, ":event-bus/") && t.EventBridgeParameters != nil:
		if denied := schedulerAuthorizeRole(t, "events:PutEvents", t.Arn); denied != nil {
			return *denied
		}
		return fireEventBridgeTarget(t)
	}
	return delivery.Permanent(ebTargetError{"UnsupportedTarget", "the simulator does not invoke targets of this service: " + t.Arn})
}

// schedulerAuthorizeRole checks that the target's execution role trusts
// scheduler.amazonaws.com and allows the call the invocation makes.
func schedulerAuthorizeRole(t schedulerTarget, action, resource string) *delivery.Outcome {
	if err := iamValidateServiceRole(t.RoleArn, "scheduler.amazonaws.com", map[string]string{action: resource}); err != nil {
		outcome := delivery.Permanent(ebTargetError{"AccessDeniedException", err.Error()})
		return &outcome
	}
	return nil
}

// schedulerUniversalTargetPrefix begins a universal target ARN,
// arn:aws:scheduler:::aws-sdk:<service>:<apiAction>, which calls any API
// action with the target's Input as the request.
const schedulerUniversalTargetPrefix = "arn:aws:scheduler:::aws-sdk:"

// fireUniversalTarget calls the API action the target ARN names, as the
// schedule's execution role, with the Input as its request parameters.
func fireUniversalTarget(t schedulerTarget) delivery.Outcome {
	service, action, ok := strings.Cut(strings.TrimPrefix(t.Arn, schedulerUniversalTargetPrefix), ":")
	if !ok || service == "" || action == "" || strings.Contains(action, ":") || !awsSDKAuthorizableService(service) {
		return delivery.Permanent(ebTargetError{"UnsupportedTarget", "the simulator does not invoke targets of this service: " + t.Arn})
	}
	input := t.Input
	if input == "" {
		input = "{}"
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(input), &request); err != nil {
		return delivery.Permanent(ebTargetError{"ValidationException", "The Input of a universal target must be the API request as a JSON object"})
	}
	var eventName, eventSource string
	authorize := func(r *http.Request) *sfnExecutionError {
		call, ok := iamActionForRequest(r)
		if !ok {
			return &sfnExecutionError{Name: "UnsupportedTarget", Cause: "the simulator does not invoke targets of this service: " + t.Arn}
		}
		eventSource, _ = awsEventSource(r)
		_, eventName, _ = strings.Cut(call, ":")
		if iamPermissionlessAction(call) {
			return nil
		}
		for _, target := range iamAuthorizationTargets(r, call) {
			if err := iamValidateServiceRole(t.RoleArn, "scheduler.amazonaws.com", map[string]string{target.action: target.resource}); err != nil {
				return &sfnExecutionError{Name: "AccessDeniedException", Cause: err.Error()}
			}
		}
		return nil
	}
	_, callErr := awsSDKInvoke(service, action, request, authorize)
	if callErr == nil {
		cloudTrailRecordSchedulerFire(eventName, eventSource, "", "")
		return delivery.Delivered()
	}
	code := strings.TrimPrefix(callErr.Name, service+".")
	if eventName != "" && code != "AccessDeniedException" {
		cloudTrailRecordSchedulerFireErr(eventName, eventSource, "", "", code, callErr.Cause)
	}
	failure := ebTargetError{code, callErr.Cause}
	if code == "ThrottlingException" || code == "Throttling" || code == "InternalFailure" || code == "ServiceUnavailable" {
		return delivery.Retryable(failure)
	}
	return delivery.Permanent(failure)
}

// fireEventBridgeTarget puts one event on the bus, with the target's Input as
// its detail and its EventBridgeParameters as detail-type and source.
func fireEventBridgeTarget(t schedulerTarget) delivery.Outcome {
	busName := t.Arn[strings.Index(t.Arn, ":event-bus/")+len(":event-bus/"):]
	if bus, ok := ebBusByARN(t.Arn); ok {
		busName = bus.Name
	}
	status, body := callJSONHandler(handleEBPutEvents, map[string]any{"Entries": []map[string]any{{
		"EventBusName": busName,
		"Source":       t.EventBridgeParameters.Source,
		"DetailType":   t.EventBridgeParameters.DetailType,
		"Detail":       t.Input,
	}}})
	outcome := recordSchedulerFireResult("PutEvents", "events.amazonaws.com", "AWS::Events::EventBus", busName, status, body, false)
	if !outcome.OK() {
		return outcome
	}
	var result struct {
		Entries []struct {
			ErrorCode    string `json:"ErrorCode"`
			ErrorMessage string `json:"ErrorMessage"`
		} `json:"Entries"`
	}
	if json.Unmarshal(body, &result) != nil || len(result.Entries) != 1 {
		return delivery.Permanent(ebTargetError{"InternalFailure", "PutEvents answered without the entry's result"})
	}
	if entry := result.Entries[0]; entry.ErrorCode != "" {
		failure := ebTargetError{entry.ErrorCode, entry.ErrorMessage}
		if entry.ErrorCode == "InternalFailure" || entry.ErrorCode == "ThrottlingException" {
			return delivery.Retryable(failure)
		}
		return delivery.Permanent(failure)
	}
	return outcome
}

// schedulerFinishDelivery sends an invocation the target never accepted to
// the target's DeadLetterConfig queue, as the schedule's execution role.
func schedulerFinishDelivery(item delivery.Item[schedulerDelivery], reason delivery.Reason) {
	if reason == delivery.Succeeded {
		return
	}
	target := schedulerParseTarget(item.Payload.Target)
	cwEvalLogger.Info().Str("schedule", item.Payload.ScheduleArn).Str("target", target.Arn).Str("reason", string(reason)).
		Str("error", item.LastError).Msg("EventBridge Scheduler could not invoke its target")
	if target.DeadLetterConfig == nil || !strings.HasPrefix(target.DeadLetterConfig.Arn, "arn:aws:sqs:") {
		return
	}
	dlq := target.DeadLetterConfig.Arn
	if err := iamValidateServiceRole(target.RoleArn, "scheduler.amazonaws.com", map[string]string{"sqs:SendMessage": dlq}); err != nil {
		cwEvalLogger.Info().Str("schedule", item.Payload.ScheduleArn).Str("deadLetterQueue", dlq).Str("error", err.Error()).
			Msg("EventBridge Scheduler cannot send to its dead-letter queue")
		return
	}
	queue := snsTopicNameFromARN(dlq)
	if _, ok := sqsQueues.Get(queue); !ok {
		return
	}
	code, message, _ := strings.Cut(item.LastError, ": ")
	attributes := map[string]SQSMessageAttribute{
		"SCHEDULE_ARN":   {DataType: "String", StringValue: item.Payload.ScheduleArn},
		"TARGET_ARN":     {DataType: "String", StringValue: target.Arn},
		"ERROR_CODE":     {DataType: "String", StringValue: code},
		"ERROR_MESSAGE":  {DataType: "String", StringValue: message},
		"RETRY_ATTEMPTS": {DataType: "String", StringValue: strconv.Itoa(max(item.Attempts-1, 0))},
	}
	switch reason {
	case delivery.AttemptsExhausted:
		attributes["EXHAUSTED_RETRY_CONDITION"] = SQSMessageAttribute{DataType: "String", StringValue: "MaximumRetryAttempts"}
	case delivery.AgeExceeded:
		attributes["EXHAUSTED_RETRY_CONDITION"] = SQSMessageAttribute{DataType: "String", StringValue: "MaximumEventAgeInSeconds"}
	}
	sqsEnqueueBodyWithAttributes(queue, target.Input, attributes)
}

// callJSONHandler invokes an awsJson-style handler in-process with a JSON body
// and returns the handler's HTTP status and response body, so the caller can
// tell whether the downstream call actually succeeded (a scheduler fire must
// not record a phantom success when the target API rejected the request).
func callJSONHandler(h http.HandlerFunc, body map[string]any) (int, []byte) {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// awsJSONError extracts the (__type, message) pair from an awsJson error body.
// The short type ("InvalidParameterException") is the CloudTrail errorCode.
func awsJSONError(body []byte) (code, message string) {
	var e struct {
		Type    string `json:"__type"`
		TypeAlt string `json:"code"`
		Message string `json:"message"`
		MsgAlt  string `json:"Message"`
	}
	_ = json.Unmarshal(body, &e)
	code = e.Type
	if code == "" {
		code = e.TypeAlt
	}
	if i := strings.LastIndex(code, "#"); i >= 0 { // strip "com.amazonaws...#Type" prefix
		code = code[i+1:]
	}
	message = e.Message
	if message == "" {
		message = e.MsgAlt
	}
	return code, message
}

// awsXMLError extracts <Code>/<Message> from a query-protocol error response
// (SNS and other XML APIs).
func awsXMLError(body []byte) (code, message string) {
	var e struct {
		Code    string `xml:"Error>Code"`
		Message string `xml:"Error>Message"`
	}
	_ = xml.Unmarshal(body, &e)
	return e.Code, e.Message
}

// recordSchedulerFireResult records a fired target invocation, reflecting a
// failed downstream call honestly (errorCode/errorMessage) instead of a phantom
// success — the same class of silent-swallow bug for every target type — and
// reads the call's answer: throttling and server errors are retried, any other
// refusal is final.
func recordSchedulerFireResult(eventName, source, resType, resName string, status int, body []byte, xmlErr bool) delivery.Outcome {
	if status < 400 {
		cloudTrailRecordSchedulerFire(eventName, source, resType, resName)
		return delivery.Delivered()
	}
	var code, message string
	if xmlErr {
		code, message = awsXMLError(body)
	} else {
		code, message = awsJSONError(body)
	}
	cloudTrailRecordSchedulerFireErr(eventName, source, resType, resName, code, message)
	failure := ebTargetError{code, message}
	if status == http.StatusTooManyRequests || status >= 500 || code == "ThrottlingException" || code == "Throttling" {
		return delivery.Retryable(failure).WithStatus(status)
	}
	return delivery.Permanent(failure).WithStatus(status)
}

func fireECSTarget(clusterArn string, p *schedulerEcsParams) delivery.Outcome {
	count := p.TaskCount
	if count <= 0 {
		count = 1
	}
	body := map[string]any{
		"cluster":        clusterArn,
		"taskDefinition": p.TaskDefinitionArn,
		"count":          count,
		"launchType":     p.LaunchType,
		"group":          p.Group,
	}
	if p.NetworkConfiguration != nil && p.NetworkConfiguration.AwsvpcConfiguration != nil {
		a := p.NetworkConfiguration.AwsvpcConfiguration
		body["networkConfiguration"] = map[string]any{
			"awsvpcConfiguration": map[string]any{
				"subnets":        a.Subnets,
				"securityGroups": a.SecurityGroups,
				"assignPublicIp": a.AssignPublicIp,
			},
		}
	}
	// Record the RunTask call either way (real CloudTrail records the attempt),
	// but reflect a failed launch honestly with errorCode/errorMessage rather
	// than a phantom success — e.g. RunTask rejects a security group that does
	// not exist, so no task is created and none ever transitions to STOPPED.
	status, respBody := callJSONHandler(handleECSRunTask, body)
	outcome := recordSchedulerFireResult("RunTask", "ecs.amazonaws.com",
		"AWS::ECS::Cluster", cloudTrailShortName(clusterArn), status, respBody, false)
	if !outcome.OK() {
		return outcome
	}
	return ecsRunTaskFailuresOutcome(respBody)
}

func fireSQSTarget(t schedulerTarget) delivery.Outcome {
	name := t.Arn
	if i := strings.LastIndex(t.Arn, ":"); i >= 0 {
		name = t.Arn[i+1:]
	}
	request := map[string]any{"QueueUrl": sqsQueueURL(name), "MessageBody": t.Input}
	if t.SqsParameters != nil && t.SqsParameters.MessageGroupID != "" {
		request["MessageGroupId"] = t.SqsParameters.MessageGroupID
	}
	status, respBody := callJSONHandler(handleSQSSendMessage, request)
	return recordSchedulerFireResult("SendMessage", "sqs.amazonaws.com", "AWS::SQS::Queue", name, status, respBody, false)
}

func fireLambdaTarget(functionArn, input string) delivery.Outcome {
	name := functionArn
	if i := strings.Index(functionArn, ":function:"); i >= 0 {
		name = functionArn[i+len(":function:"):]
		if c := strings.IndexByte(name, ':'); c >= 0 {
			name = name[:c]
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions/"+url.PathEscape(functionArn)+"/invocations", strings.NewReader(input))
	req.SetPathValue("name", functionArn)
	// Amazon EventBridge Scheduler invokes AWS Lambda asynchronously, so the
	// function's own retries and destinations apply and a slow function does
	// not hold the firing loop.
	req.Header.Set("X-Amz-Invocation-Type", "Event")
	rec := httptest.NewRecorder()
	handleLambdaInvoke(rec, req)
	return recordSchedulerFireResult("Invoke", "lambda.amazonaws.com", "AWS::Lambda::Function", name, rec.Code, rec.Body.Bytes(), false)
}

func fireSNSTarget(topicArn, input string) delivery.Outcome {
	form := url.Values{"TopicArn": {topicArn}, "Message": {input}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleSNSPublish(rec, req)
	return recordSchedulerFireResult("Publish", "sns.amazonaws.com",
		"AWS::SNS::Topic", cloudTrailShortName(topicArn), rec.Code, rec.Body.Bytes(), true)
}

// fireKinesisTarget puts the input on the stream under the target's
// PartitionKey.
func fireKinesisTarget(streamArn, partitionKey, input string) delivery.Outcome {
	if _, _, err := kinesisAppendRecord("", streamArn, []byte(input), partitionKey, ""); err != nil {
		cloudTrailRecordSchedulerFireErr("PutRecord", "kinesis.amazonaws.com", "AWS::Kinesis::Stream",
			cloudTrailShortName(streamArn), "ResourceNotFoundException", "Stream "+streamArn+" not found")
		return delivery.Permanent(ebTargetError{"ResourceNotFoundException", "Stream " + streamArn + " not found"})
	}
	cloudTrailRecordSchedulerFire("PutRecord", "kinesis.amazonaws.com", "AWS::Kinesis::Stream", cloudTrailShortName(streamArn))
	return delivery.Delivered()
}

func fireStepFunctionsTarget(stateMachineArn, input string) delivery.Outcome {
	if input == "" {
		input = "{}"
	}
	execution, executionErr := sfnStartNestedExecution(stateMachineArn, sim.NewUUID(), input)
	if executionErr != nil {
		cloudTrailRecordSchedulerFireErr(
			"StartExecution",
			"states.amazonaws.com",
			"AWS::StepFunctions::StateMachine",
			cloudTrailShortName(stateMachineArn),
			executionErr.Name,
			executionErr.Cause,
		)
		return delivery.Permanent(ebTargetError{strings.TrimPrefix(executionErr.Name, "StepFunctions."), executionErr.Cause})
	}
	cloudTrailRecordSchedulerFire(
		"StartExecution",
		"states.amazonaws.com",
		"AWS::StepFunctions::StateMachine",
		cloudTrailShortName(execution.StateMachineArn),
	)
	return delivery.Delivered()
}

// cloudTrailRecordSchedulerFire records a CloudTrail event for a target the
// Scheduler firing loop invoked in-process. These invocations call the target
// handler directly (callJSONHandler / httptest), bypassing the central `POST /`
// recording middleware. Real CloudTrail records the downstream call (RunTask /
// SendMessage / Publish / Invoke) with `userIdentity.invokedBy =
// scheduler.amazonaws.com`.
func cloudTrailRecordSchedulerFire(eventName, source, resourceType, resourceName string) {
	cloudTrailRecordSchedulerFireErr(eventName, source, resourceType, resourceName, "", "")
}

// cloudTrailRecordSchedulerFireErr records a scheduler-fired target invocation,
// carrying errorCode/errorMessage when the downstream call failed.
func cloudTrailRecordSchedulerFireErr(eventName, source, resourceType, resourceName, errorCode, errorMessage string) {
	// A service-initiated DATA event (e.g. a scheduler-fired SQS SendMessage / SNS
	// Publish / Lambda Invoke) is still a data event — LookupEvents never returns
	// it, just as for a client-initiated one. Management targets (e.g. ECS
	// RunTask) are recorded with invokedBy=scheduler.amazonaws.com.
	if cloudTrailIsDataEvent(source, eventName) {
		return
	}
	var resources []CloudTrailResource
	if resourceName != "" {
		resources = []CloudTrailResource{{ResourceType: resourceType, ResourceName: resourceName}}
	}
	cloudTrailRecord(CloudTrailEvent{
		EventName:    eventName,
		EventSource:  source,
		InvokedBy:    "scheduler.amazonaws.com",
		Resources:    resources,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	})
}
