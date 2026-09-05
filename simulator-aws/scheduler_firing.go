package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// EventBridge Scheduler firing engine. The CRUD surface in scheduler.go stores
// schedules; this loop evaluates the ScheduleExpression — `at(...)` one-time,
// `rate(N unit)` recurring, and `cron(...)` (see scheduler_cron.go) — and
// invokes the configured Target when due (ECS RunTask, Lambda Invoke, SQS
// SendMessage, SNS Publish) by calling the sim's own handlers in-process,
// exactly as real EventBridge Scheduler invokes the downstream service.

type schedulerFireRec struct {
	Next  time.Time
	Fired bool // one-time at() already fired
}

var (
	schedulerFireRecs sim.Store[schedulerFireRec]
	schedulerLoopOnce sync.Once
)

// startSchedulerFiringLoop launches the once-per-second evaluation loop.
func startSchedulerFiringLoop(srv *sim.Server) {
	schedulerLoopOnce.Do(func() {
		srv.StartBackground(func(ctx context.Context) {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					schedulerTick(time.Now().UTC())
				}
			}
		})
	})
}

func schedulerTick(now time.Time) {
	store := schedulerStore()
	for _, s := range store.List() {
		if s.State != "ENABLED" {
			continue
		}
		key := scheduleKey(s.GroupName, s.Name)
		if !schedulerDue(key, s, now) {
			continue
		}
		fireSchedule(s)
		schedulerAfterFire(key, s, now)
		if s.ActionAfterCompletion == "DELETE" && !schedulerRecurring(s.ScheduleExpression) {
			store.Delete(key)
			schedulerFireStore().Delete(key)
		}
	}
}

func schedulerDue(key string, s Schedule, now time.Time) bool {
	store := schedulerFireStore()
	rec, exists := store.Get(key)
	if !exists {
		next, ok := schedulerFirstFire(s, now)
		if !ok {
			return false
		}
		rec = schedulerFireRec{Next: next}
		store.Put(key, rec)
	}
	if rec.Fired {
		return false
	}
	return !now.Before(rec.Next)
}

func schedulerAfterFire(key string, s Schedule, now time.Time) {
	schedulerFireStore().Update(key, func(rec *schedulerFireRec) {
		switch {
		case schedulerRecurring(s.ScheduleExpression):
			if interval, ok := schedulerRateInterval(s.ScheduleExpression); ok {
				rec.Next = now.Add(interval)
			} else if next, ok := schedulerCronNext(s.ScheduleExpression, now); ok {
				rec.Next = next
			} else {
				rec.Fired = true
			}
		default:
			rec.Fired = true // one-time at()
		}
	})
}

func schedulerRecurring(expr string) bool {
	if _, ok := schedulerRateInterval(expr); ok {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(expr), "cron(")
}

// schedulerFirstFire computes the first fire time: at(...) → the timestamp;
// rate(...) → creation + interval; cron(...) → next match after now. False for
// unparseable expressions.
func schedulerFirstFire(s Schedule, now time.Time) (time.Time, bool) {
	expr := strings.TrimSpace(s.ScheduleExpression)
	if strings.HasPrefix(expr, "at(") {
		inner := strings.TrimSuffix(strings.TrimPrefix(expr, "at("), ")")
		if t, err := time.Parse("2006-01-02T15:04:05", inner); err == nil {
			return t.UTC(), true
		}
		return time.Time{}, false
	}
	if interval, ok := schedulerRateInterval(expr); ok {
		base := now
		if s.CreationDate > 0 {
			base = time.Unix(int64(s.CreationDate), 0).UTC()
		}
		return base.Add(interval), true
	}
	if next, ok := schedulerCronNext(expr, now); ok {
		return next, true
	}
	return time.Time{}, false
}

func schedulerRateInterval(expr string) (time.Duration, bool) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "rate(") {
		return 0, false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(expr, "rate("), ")")
	f := strings.Fields(inner)
	if len(f) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch {
	case strings.HasPrefix(f[1], "minute"):
		return time.Duration(n) * time.Minute, true
	case strings.HasPrefix(f[1], "hour"):
		return time.Duration(n) * time.Hour, true
	case strings.HasPrefix(f[1], "day"):
		return time.Duration(n) * 24 * time.Hour, true
	}
	return 0, false
}

type schedulerTarget struct {
	Arn           string              `json:"Arn"`
	Input         string              `json:"Input"`
	EcsParameters *schedulerEcsParams `json:"EcsParameters"`
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

// fireSchedule dispatches a due schedule to its Target by invoking the sim's
// own handler for the target service in-process.
func fireSchedule(s Schedule) {
	var t schedulerTarget
	if err := json.Unmarshal(s.Target, &t); err != nil || t.Arn == "" {
		return
	}
	switch {
	case strings.Contains(t.Arn, ":ecs:") && t.EcsParameters != nil:
		fireECSTarget(t.Arn, t.EcsParameters)
	case strings.Contains(t.Arn, ":lambda:"):
		fireLambdaTarget(t.Arn, t.Input)
	case strings.Contains(t.Arn, ":sqs:"):
		fireSQSTarget(t.Arn, t.Input)
	case strings.Contains(t.Arn, ":sns:"):
		fireSNSTarget(t.Arn, t.Input)
	case strings.Contains(t.Arn, ":states:"):
		fireStepFunctionsTarget(t.Arn, t.Input)
	}
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
// success — the same class of silent-swallow bug for every target type.
func recordSchedulerFireResult(eventName, source, resType, resName string, status int, body []byte, xmlErr bool) {
	if status >= 400 {
		var code, message string
		if xmlErr {
			code, message = awsXMLError(body)
		} else {
			code, message = awsJSONError(body)
		}
		cloudTrailRecordSchedulerFireErr(eventName, source, resType, resName, code, message)
		return
	}
	cloudTrailRecordSchedulerFire(eventName, source, resType, resName)
}

func fireECSTarget(clusterArn string, p *schedulerEcsParams) {
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
	recordSchedulerFireResult("RunTask", "ecs.amazonaws.com",
		"AWS::ECS::Cluster", cloudTrailShortName(clusterArn), status, respBody, false)
}

func fireSQSTarget(queueArn, input string) {
	name := queueArn
	if i := strings.LastIndex(queueArn, ":"); i >= 0 {
		name = queueArn[i+1:]
	}
	status, respBody := callJSONHandler(handleSQSSendMessage, map[string]any{
		"QueueUrl":    sqsQueueURL(name),
		"MessageBody": input,
	})
	recordSchedulerFireResult("SendMessage", "sqs.amazonaws.com", "AWS::SQS::Queue", name, status, respBody, false)
}

func fireLambdaTarget(functionArn, input string) {
	name := functionArn
	if i := strings.Index(functionArn, ":function:"); i >= 0 {
		name = functionArn[i+len(":function:"):]
		if c := strings.IndexByte(name, ':'); c >= 0 { // strip :version/:alias
			name = name[:c]
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions/"+name+"/invocations", strings.NewReader(input))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handleLambdaInvoke(rec, req)
	// A function that runs but returns an error still 200s (X-Amz-Function-Error)
	// — that's a successful Invoke. Only an Invoke-API failure (e.g. 404
	// ResourceNotFound) is a failed fire.
	recordSchedulerFireResult("Invoke", "lambda.amazonaws.com", "AWS::Lambda::Function", name, rec.Code, rec.Body.Bytes(), false)
}

func fireSNSTarget(topicArn, input string) {
	form := url.Values{"TopicArn": {topicArn}, "Message": {input}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleSNSPublish(rec, req)
	recordSchedulerFireResult("Publish", "sns.amazonaws.com",
		"AWS::SNS::Topic", cloudTrailShortName(topicArn), rec.Code, rec.Body.Bytes(), true)
}

func fireStepFunctionsTarget(stateMachineArn, input string) {
	if input == "" {
		input = "{}"
	}
	execution, executionErr := sfnStartNestedExecution(stateMachineArn, generateUUID(), input)
	if executionErr != nil {
		cloudTrailRecordSchedulerFireErr(
			"StartExecution",
			"states.amazonaws.com",
			"AWS::StepFunctions::StateMachine",
			cloudTrailShortName(stateMachineArn),
			executionErr.Name,
			executionErr.Cause,
		)
		return
	}
	cloudTrailRecordSchedulerFire(
		"StartExecution",
		"states.amazonaws.com",
		"AWS::StepFunctions::StateMachine",
		cloudTrailShortName(execution.StateMachineArn),
	)
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
