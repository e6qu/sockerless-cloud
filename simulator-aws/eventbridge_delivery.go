package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cron"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
	"github.com/e6qu/sockerless-cloud/sim/eventbus"
)

// Body is the event after the target's input transformation.
type ebTargetDelivery struct {
	RuleArn string
	Target  EBTarget
	Record  EBEventRecord
	Body    string
}

// One target of one rule.
type ebRoute struct {
	Rule   EBRule
	Target EBTarget
}

// The error code reported in the dead-letter message.
type ebTargetError struct {
	Code    string
	Message string
}

func (e ebTargetError) Error() string { return e.Code + ": " + e.Message }

var ebBus *eventbus.Bus[EBEventRecord, ebRoute, ebTargetDelivery]

func registerEventBridgeDelivery(srv *sim.Server) {
	store := sim.MakeStore[delivery.Item[ebTargetDelivery]](srv.DB(), "eventbridge_target_deliveries")
	if err := ebMigrateTargetDeliveryBodies(srv.DB(), store); err != nil {
		panic(err)
	}
	ebBus = eventbus.New(srv, eventbus.Config[EBEventRecord, ebRoute, ebTargetDelivery]{
		Subscriptions: ebBusRoutes,
		Admits: func(route ebRoute, record EBEventRecord) bool {
			return route.Rule.EventPattern != "" && ebEventPatternMatches(route.Rule.EventPattern, ebBuildEvent(record))
		},
		Transform: func(route ebRoute, records []EBEventRecord) (ebTargetDelivery, bool) {
			record := records[0]
			return ebTargetDelivery{RuleArn: route.Rule.Arn, Target: route.Target, Record: record, Body: ebApplyInput(route.Target, record)}, true
		},
		Archive: archiveEBEvent,
		Delivery: &eventbus.Delivery[ebTargetDelivery]{
			Name:   "EventBridge target deliveries",
			Store:  store,
			Policy: func(d ebTargetDelivery) delivery.Policy { return ebTargetRetryPolicy(d.Target) },
			Attempt: func(ctx context.Context, item *delivery.Item[ebTargetDelivery]) delivery.Outcome {
				return ebAttemptTarget(ctx, item.Payload)
			},
			Inline:     func(ebTargetDelivery) bool { return true },
			DeadLetter: ebDeadLetterTargetDelivery,
		},
	})
	ebBus.Resume()

	rules, targets := ebRules, ebTargets
	records := sim.MakeStore[cron.Record](srv.DB(), "eventbridge_rule_schedules")
	cron.NewTicker(records, func() []cron.Entry { return ebScheduledRuleEntries(rules, targets) }).
		Start(srv, "EventBridge scheduled rules", time.Second)
}

// Deliveries an earlier build persisted untransformed get the target's input
// transformation.
func ebMigrateTargetDeliveryBodies(db *sql.DB, store sim.Store[delivery.Item[ebTargetDelivery]]) error {
	return sim.Migrate(db, "eventbridge_target_deliveries/body", func() error {
		for _, item := range store.List() {
			item.Payload.Body = ebApplyInput(item.Payload.Target, item.Payload.Record)
			store.Put(item.ID, item)
		}
		return nil
	})
}

// Every target of every enabled rule on bus.
func ebBusRoutes(bus string) []ebRoute {
	var routes []ebRoute
	for _, rule := range ebRules.List() {
		if rule.EventBusName != bus || rule.State == "DISABLED" {
			continue
		}
		routes = append(routes, ebRuleRoutes(rule, ebTargets)...)
	}
	return routes
}

func ebRuleRoutes(rule EBRule, targets sim.Store[[]EBTarget]) []ebRoute {
	ruleTargets, _ := targets.Get(ebRuleKey(rule.EventBusName, rule.Name))
	routes := make([]ebRoute, 0, len(ruleTargets))
	for _, target := range ruleTargets {
		routes = append(routes, ebRoute{Rule: rule, Target: target})
	}
	return routes
}

// MaximumRetryAttempts defaults to 185 and MaximumEventAgeInSeconds to 86400,
// with exponential backoff.
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

// A refusal by the target service, such as a resource policy not admitting
// events.amazonaws.com or a missing target, fails without retry; throttling
// and server errors are retried.
func ebAttemptTarget(ctx context.Context, d ebTargetDelivery) delivery.Outcome {
	target, body := d.Target, d.Body
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

// With the attributes EventBridge documents.
func ebDeadLetterTargetDelivery(item delivery.Item[ebTargetDelivery], reason delivery.Reason) {
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

// Each occurrence sends a "Scheduled Event" from aws.events naming the rule.
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
				ebBus.DeliverTo(ebRuleRoutes(rule, targets), record)
			},
		})
	}
	return entries
}

// Returns the ValidationException message, or "".
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
