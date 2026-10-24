package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// ebTargetDelivery is one event Amazon EventBridge owes one rule target.
type ebTargetDelivery struct {
	RuleArn string
	Target  EBTarget
	Record  EBEventRecord
}

// ebTargetError is a failed target invocation with the error code EventBridge
// reports in the dead-letter message.
type ebTargetError struct {
	Code    string
	Message string
}

func (e ebTargetError) Error() string { return e.Code + ": " + e.Message }

var ebTargetDeliveries *delivery.Dispatcher[ebTargetDelivery]

func registerEventBridgeDelivery(srv *sim.Server) {
	store := sim.MakeStore[delivery.Item[ebTargetDelivery]](srv.DB(), "eventbridge_target_deliveries")
	ebTargetDeliveries = delivery.New(srv, "EventBridge target deliveries", store, delivery.Handler[ebTargetDelivery]{
		Policy: func(d ebTargetDelivery) delivery.Policy { return ebTargetRetryPolicy(d.Target) },
		Attempt: func(ctx context.Context, item *delivery.Item[ebTargetDelivery]) delivery.Outcome {
			return ebAttemptTarget(ctx, item.Payload)
		},
		Finish: ebFinishTargetDelivery,
	})
	ebTargetDeliveries.Resume()

	rules, targets := ebRules, ebTargets
	records := sim.MakeStore[cron.Record](srv.DB(), "eventbridge_rule_schedules")
	cron.NewTicker(records, func() []cron.Entry { return ebScheduledRuleEntries(rules, targets) }).
		Start(srv, "EventBridge scheduled rules", time.Second)
}

// ebTargetRetryPolicy reads the target's RetryPolicy: MaximumRetryAttempts
// (default 185) and MaximumEventAgeInSeconds (default 86400), retried with
// exponential backoff.
func ebTargetRetryPolicy(target EBTarget) delivery.Policy {
	policy := struct {
		MaximumRetryAttempts     *int `json:"MaximumRetryAttempts"`
		MaximumEventAgeInSeconds *int `json:"MaximumEventAgeInSeconds"`
	}{}
	if len(target.RetryPolicy) > 0 {
		_ = json.Unmarshal(target.RetryPolicy, &policy)
	}
	retries, age := 185, 86400
	if policy.MaximumRetryAttempts != nil {
		retries = *policy.MaximumRetryAttempts
	}
	if policy.MaximumEventAgeInSeconds != nil {
		age = *policy.MaximumEventAgeInSeconds
	}
	return delivery.Policy{
		MaxAttempts: retries + 1,
		MaxAge:      time.Duration(age) * time.Second,
		Backoff:     delivery.Exponential(time.Second, 5*time.Minute),
	}
}

func ebSubmitTargetDelivery(ruleArn string, target EBTarget, record EBEventRecord) {
	ebTargetDeliveries.SubmitAttempted(sim.NewUUID(), ebTargetDelivery{RuleArn: ruleArn, Target: target, Record: record})
}

// ebAttemptTarget delivers one event to one target through the target
// service's API, on the rule's behalf. A refusal the target service answers
// with, such as a resource policy that does not admit events.amazonaws.com or
// a target that does not exist, fails without retry, as EventBridge treats
// both; throttling and server errors are retried.
func ebAttemptTarget(ctx context.Context, d ebTargetDelivery) delivery.Outcome {
	target := d.Target
	body := ebApplyInput(target, d.Record)
	switch {
	case strings.HasPrefix(target.Arn, "arn:aws:sqs:"):
		return ebSendToQueue(ctx, d.RuleArn, target, d.Record, body)
	case strings.HasPrefix(target.Arn, "arn:aws:lambda:"):
		return ebInvokeFunction(ctx, d.RuleArn, target, body)
	case strings.HasPrefix(target.Arn, "arn:aws:sns:"):
		return ebPublishToTopic(ctx, d.RuleArn, target, body)
	case strings.HasPrefix(target.Arn, "arn:aws:states:"):
		return ebStartExecution(ctx, d.RuleArn, target, d.Record, body)
	case strings.HasPrefix(target.Arn, "arn:aws:logs:") && strings.Contains(target.Arn, ":log-group:"):
		return ebWriteToLogGroup(ctx, d.RuleArn, target, body)
	case strings.HasPrefix(target.Arn, "arn:aws:ecs:"):
		return ebRunTask(ctx, d.RuleArn, target, body)
	case strings.HasPrefix(target.Arn, "arn:aws:kinesis:"):
		return ebPutStreamRecord(ctx, d.RuleArn, target, d.Record, body)
	case strings.HasPrefix(target.Arn, "arn:aws:batch:"):
		return ebSubmitJob(ctx, d.RuleArn, target)
	case strings.HasPrefix(target.Arn, "arn:aws:codebuild:") && strings.Contains(target.Arn, ":project/"):
		return ebStartBuild(ctx, d.RuleArn, target, body)
	case strings.HasPrefix(target.Arn, "arn:aws:firehose:") && strings.Contains(target.Arn, ":deliverystream/"):
		return ebPutFirehoseRecord(ctx, d.RuleArn, target, body)
	case strings.HasPrefix(target.Arn, "arn:aws:ssm:") && strings.Contains(target.Arn, ":document/"):
		return ebSendCommand(ctx, d.RuleArn, target, body)
	case strings.HasPrefix(target.Arn, "arn:aws:events:") && strings.Contains(target.Arn, ":api-destination/"):
		return ebInvokeApiDestination(ctx, target, body)
	case strings.HasPrefix(target.Arn, "arn:aws:events:") && strings.Contains(target.Arn, ":event-bus/"):
		return ebPutOnBus(ctx, d.RuleArn, target, d.Record)
	default:
		return delivery.Permanent(ebTargetError{"UnsupportedTarget", "the simulator does not invoke targets of this service: " + target.Arn})
	}
}

// ebFinishTargetDelivery sends an event the target never accepted to the
// target's DeadLetterConfig queue with the attributes EventBridge documents.
func ebFinishTargetDelivery(item delivery.Item[ebTargetDelivery], reason delivery.Reason) {
	if reason == delivery.Succeeded {
		return
	}
	d := item.Payload
	var dlq struct {
		Arn string `json:"Arn"`
	}
	if len(d.Target.DeadLetterConfig) > 0 {
		_ = json.Unmarshal(d.Target.DeadLetterConfig, &dlq)
	}
	cwEvalLogger.Info().Str("rule", d.RuleArn).Str("target", d.Target.Arn).Str("reason", string(reason)).
		Str("error", item.LastError).Msg("EventBridge could not deliver an event to its target")
	if !strings.HasPrefix(dlq.Arn, "arn:aws:sqs:") {
		return
	}
	src := iamServiceSource{Service: "events.amazonaws.com", SourceArn: d.RuleArn, SourceAccount: awsAccountID()}
	if !iamAuthorizeServiceDelivery(dlq.Arn, "sqs:SendMessage", src) {
		return
	}
	queue := snsTopicNameFromARN(dlq.Arn)
	if _, ok := sqsQueues.Get(queue); !ok {
		return
	}
	code, message, _ := strings.Cut(item.LastError, ": ")
	attributes := map[string]SQSMessageAttribute{
		"RULE_ARN":       {DataType: "String", StringValue: d.RuleArn},
		"TARGET_ARN":     {DataType: "String", StringValue: d.Target.Arn},
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
	event, err := json.Marshal(ebBuildEvent(d.Record))
	if err != nil {
		return
	}
	sqsEnqueueBodyWithAttributes(queue, string(event), attributes)
}

// ebScheduledRuleEntries fires every enabled rule with a ScheduleExpression:
// each occurrence sends a "Scheduled Event" from aws.events, naming the rule
// in its resources, to that rule's targets.
func ebScheduledRuleEntries(rules sim.Store[EBRule], targets sim.Store[[]EBTarget]) []cron.Entry {
	var entries []cron.Entry
	for _, rule := range rules.List() {
		if rule.ScheduleExpression == "" {
			continue
		}
		plan, err := parseAWSSchedule(rule.ScheduleExpression, "", false)
		if err != nil {
			continue
		}
		key := ebRuleKey(rule.EventBusName, rule.Name)
		entries = append(entries, cron.Entry{
			Key:    key,
			Spec:   rule.ScheduleExpression,
			Paused: rule.State != "ENABLED",
			First: func(now time.Time) (time.Time, bool) {
				if plan.rate > 0 && rule.CreatedAt > 0 {
					return plan.next(time.Unix(rule.CreatedAt, 0).UTC())
				}
				return plan.next(now)
			},
			Next: plan.next,
			Fire: func(scheduled time.Time) {
				record := EBEventRecord{
					ID:         sim.NewUUID(),
					Source:     "aws.events",
					DetailType: "Scheduled Event",
					Detail:     "{}",
					Time:       scheduled.Unix(),
					Resources:  []string{rule.Arn},
				}
				ruleTargets, _ := targets.Get(key)
				for _, target := range ruleTargets {
					ebSubmitTargetDelivery(rule.Arn, target, record)
				}
			},
		})
	}
	return entries
}

// ebRuleScheduleProblem applies the PutRule rules for ScheduleExpression,
// returning the ValidationException message or "".
func ebRuleScheduleProblem(expr, pattern, bus string) string {
	switch {
	case expr == "" && pattern == "":
		return "Parameter(s) EventPattern or ScheduleExpression must be specified."
	case expr == "":
		return ""
	case ebBusName(bus) != "default":
		return "ScheduleExpression is supported only on the default event bus."
	}
	if _, err := parseAWSSchedule(expr, "", false); err != nil {
		return "Parameter ScheduleExpression is not valid."
	}
	return ""
}
