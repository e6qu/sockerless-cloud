package aws_sdk_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func eventbridgeClient() *eventbridge.Client {
	return eventbridge.NewFromConfig(sdkConfig(), func(o *eventbridge.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func assertEventBridgeSQSEnvelope(t *testing.T, body, source, detailType string, detail map[string]any) {
	t.Helper()
	var delivered map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &delivered))
	assert.Equal(t, source, delivered["source"])
	assert.Equal(t, detailType, delivered["detail-type"])
	assert.Equal(t, detail, delivered["detail"])
	assert.NotEmpty(t, delivered["id"])
	assert.NotEmpty(t, delivered["time"])
}

func TestEventBridge_BusArchiveReplaySDK(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()
	sqsC := sqsClient()

	busName := "eb-sdk-bus"
	createBus, err := eb.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{
		Name:        aws.String(busName),
		Description: aws.String("sdk bus"),
		Tags:        []ebtypes.Tag{{Key: aws.String("env"), Value: aws.String("sdk")}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(createBus.EventBusArn))
	t.Cleanup(func() {
		_, _ = eb.DeleteArchive(ctx, &eventbridge.DeleteArchiveInput{ArchiveName: aws.String("eb-sdk-archive")})
		_, _ = eb.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String(busName)})
	})

	describeBus, err := eb.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{Name: aws.String(busName)})
	require.NoError(t, err)
	assert.Equal(t, busName, aws.ToString(describeBus.Name))
	assert.Equal(t, aws.ToString(createBus.EventBusArn), aws.ToString(describeBus.Arn))

	buses, err := eb.ListEventBuses(ctx, &eventbridge.ListEventBusesInput{NamePrefix: aws.String("eb-sdk")})
	require.NoError(t, err)
	require.Len(t, buses.EventBuses, 1)
	assert.Equal(t, busName, aws.ToString(buses.EventBuses[0].Name))

	_, err = eb.PutPermission(ctx, &eventbridge.PutPermissionInput{
		EventBusName: aws.String(busName),
		StatementId:  aws.String("sdk-permission"),
		Action:       aws.String("events:PutEvents"),
		Principal:    aws.String("123456789012"),
	})
	require.NoError(t, err)
	describeBus, err = eb.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{Name: aws.String(busName)})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(describeBus.Policy))
	var policy struct {
		Statement []struct {
			Sid string `json:"Sid"`
		} `json:"Statement"`
	}
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(describeBus.Policy)), &policy))
	require.Len(t, policy.Statement, 1)
	assert.Equal(t, "sdk-permission", policy.Statement[0].Sid)

	_, err = eb.RemovePermission(ctx, &eventbridge.RemovePermissionInput{
		EventBusName: aws.String(busName),
		StatementId:  aws.String("sdk-permission"),
	})
	require.NoError(t, err)
	describeBus, err = eb.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{Name: aws.String(busName)})
	require.NoError(t, err)
	assert.Empty(t, aws.ToString(describeBus.Policy))

	createArchive, err := eb.CreateArchive(ctx, &eventbridge.CreateArchiveInput{
		ArchiveName:    aws.String("eb-sdk-archive"),
		EventSourceArn: createBus.EventBusArn,
		Description:    aws.String("sdk archive"),
		EventPattern:   aws.String(`{"source":["sockerless.archive"]}`),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(createArchive.ArchiveArn))
	assert.Equal(t, ebtypes.ArchiveStateEnabled, createArchive.State)

	archive, err := eb.DescribeArchive(ctx, &eventbridge.DescribeArchiveInput{ArchiveName: aws.String("eb-sdk-archive")})
	require.NoError(t, err)
	assert.Equal(t, "eb-sdk-archive", aws.ToString(archive.ArchiveName))
	assert.Equal(t, aws.ToString(createBus.EventBusArn), aws.ToString(archive.EventSourceArn))

	archives, err := eb.ListArchives(ctx, &eventbridge.ListArchivesInput{EventSourceArn: createBus.EventBusArn})
	require.NoError(t, err)
	require.Len(t, archives.Archives, 1)
	// List entries are the summary Archive shape, identity and state only;
	// DescribeArchive carries ArchiveArn, Description and EventPattern.
	assert.Equal(t, "eb-sdk-archive", aws.ToString(archives.Archives[0].ArchiveName))
	assert.Equal(t, aws.ToString(createBus.EventBusArn), aws.ToString(archives.Archives[0].EventSourceArn))
	assert.Equal(t, ebtypes.ArchiveStateEnabled, archives.Archives[0].State)
	assert.NotNil(t, archives.Archives[0].CreationTime)

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("eb-sdk-replay-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })
	attrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	queueARN := attrs.Attributes["QueueArn"]
	replayRule, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String("eb-sdk-replay-rule"),
		EventBusName: aws.String(busName),
		EventPattern: aws.String(`{"source":["sockerless.archive"]}`),
	})
	require.NoError(t, err)
	setEBQueuePolicy(t, sqsC, q.QueueUrl, queueARN, aws.ToString(replayRule.RuleArn))
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
			EventBusName: aws.String(busName),
			Rule:         aws.String("eb-sdk-replay-rule"),
			Ids:          []string{"queue"},
		})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{
			EventBusName: aws.String(busName),
			Name:         aws.String("eb-sdk-replay-rule"),
		})
	})
	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		EventBusName: aws.String(busName),
		Rule:         aws.String("eb-sdk-replay-rule"),
		Targets: []ebtypes.Target{{
			Id:  aws.String("queue"),
			Arn: aws.String(queueARN),
		}},
	})
	require.NoError(t, err)

	_, err = eb.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String(busName),
			Source:       aws.String("sockerless.archive"),
			DetailType:   aws.String("example"),
			Detail:       aws.String(`{"replayed":true}`),
		}},
	})
	require.NoError(t, err)

	original, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            q.QueueUrl,
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	require.Len(t, original.Messages, 1)
	assertEventBridgeSQSEnvelope(t, aws.ToString(original.Messages[0].Body),
		"sockerless.archive", "example", map[string]any{"replayed": true})
	_, err = sqsC.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      q.QueueUrl,
		ReceiptHandle: original.Messages[0].ReceiptHandle,
	})
	require.NoError(t, err)

	start := time.Now().Add(-time.Hour)
	end := time.Now().Add(time.Hour)
	replay, err := eb.StartReplay(ctx, &eventbridge.StartReplayInput{
		ReplayName:     aws.String("eb-sdk-replay"),
		EventSourceArn: createArchive.ArchiveArn,
		EventStartTime: aws.Time(start),
		EventEndTime:   aws.Time(end),
		Destination: &ebtypes.ReplayDestination{
			Arn: createBus.EventBusArn,
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(replay.ReplayArn))

	describeReplay, err := eb.DescribeReplay(ctx, &eventbridge.DescribeReplayInput{ReplayName: aws.String("eb-sdk-replay")})
	require.NoError(t, err)
	assert.Equal(t, ebtypes.ReplayStateCompleted, describeReplay.State)

	replays, err := eb.ListReplays(ctx, &eventbridge.ListReplaysInput{EventSourceArn: createArchive.ArchiveArn})
	require.NoError(t, err)
	require.Len(t, replays.Replays, 1)
	// List entries are the summary Replay shape, identity, state and timestamps;
	// DescribeReplay carries ReplayArn and Description.
	assert.Equal(t, "eb-sdk-replay", aws.ToString(replays.Replays[0].ReplayName))
	assert.Equal(t, aws.ToString(createArchive.ArchiveArn), aws.ToString(replays.Replays[0].EventSourceArn))
	assert.Equal(t, ebtypes.ReplayStateCompleted, replays.Replays[0].State)
	assert.NotNil(t, replays.Replays[0].ReplayStartTime)

	received, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            q.QueueUrl,
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	require.Len(t, received.Messages, 1)
	assertEventBridgeSQSEnvelope(t, aws.ToString(received.Messages[0].Body),
		"sockerless.archive", "example", map[string]any{"replayed": true})
}

func TestEventBridge_RuleTargetPutEventsSDK(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()
	sqsC := sqsClient()

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("eb-sdk-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })

	attrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	queueARN := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)

	pattern := `{"source":["sockerless.test"],"detail-type":["example"]}`
	putRule, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String("eb-sdk-rule"),
		EventPattern: aws.String(pattern),
		Tags:         []ebtypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(putRule.RuleArn))

	// EventBridge delivers as events.amazonaws.com on the rule's behalf, so the
	// queue grants that service sqs:SendMessage for the rule's ARN.
	setEBQueuePolicy(t, sqsC, q.QueueUrl, queueARN, aws.ToString(putRule.RuleArn))
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
			Rule: aws.String("eb-sdk-rule"),
			Ids:  []string{"queue"},
		})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String("eb-sdk-rule")})
	})

	describe, err := eb.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String("eb-sdk-rule")})
	require.NoError(t, err)
	assert.Equal(t, "eb-sdk-rule", aws.ToString(describe.Name))
	assert.Equal(t, pattern, aws.ToString(describe.EventPattern))

	rules, err := eb.ListRules(ctx, &eventbridge.ListRulesInput{NamePrefix: aws.String("eb-sdk")})
	require.NoError(t, err)
	require.Len(t, rules.Rules, 1)
	assert.Equal(t, "eb-sdk-rule", aws.ToString(rules.Rules[0].Name))

	tags, err := eb.ListTagsForResource(ctx, &eventbridge.ListTagsForResourceInput{
		ResourceARN: putRule.RuleArn,
	})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "env", aws.ToString(tags.Tags[0].Key))
	assert.Equal(t, "test", aws.ToString(tags.Tags[0].Value))

	_, err = eb.TagResource(ctx, &eventbridge.TagResourceInput{
		ResourceARN: putRule.RuleArn,
		Tags:        []ebtypes.Tag{{Key: aws.String("owner"), Value: aws.String("sdk")}},
	})
	require.NoError(t, err)
	tags, err = eb.ListTagsForResource(ctx, &eventbridge.ListTagsForResourceInput{
		ResourceARN: putRule.RuleArn,
	})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 2)

	_, err = eb.UntagResource(ctx, &eventbridge.UntagResourceInput{
		ResourceARN: putRule.RuleArn,
		TagKeys:     []string{"owner"},
	})
	require.NoError(t, err)
	tags, err = eb.ListTagsForResource(ctx, &eventbridge.ListTagsForResourceInput{
		ResourceARN: putRule.RuleArn,
	})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)

	_, err = eb.DisableRule(ctx, &eventbridge.DisableRuleInput{Name: aws.String("eb-sdk-rule")})
	require.NoError(t, err)
	describe, err = eb.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String("eb-sdk-rule")})
	require.NoError(t, err)
	assert.Equal(t, ebtypes.RuleStateDisabled, describe.State)

	_, err = eb.EnableRule(ctx, &eventbridge.EnableRuleInput{Name: aws.String("eb-sdk-rule")})
	require.NoError(t, err)
	describe, err = eb.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: aws.String("eb-sdk-rule")})
	require.NoError(t, err)
	assert.Equal(t, ebtypes.RuleStateEnabled, describe.State)

	targets, err := eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String("eb-sdk-rule"),
		Targets: []ebtypes.Target{{
			Id:  aws.String("queue"),
			Arn: aws.String(queueARN),
		}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 0, targets.FailedEntryCount)

	listTargets, err := eb.ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{
		Rule: aws.String("eb-sdk-rule"),
	})
	require.NoError(t, err)
	require.Len(t, listTargets.Targets, 1)
	assert.Equal(t, queueARN, aws.ToString(listTargets.Targets[0].Arn))

	putEvents, err := eb.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{{
			Source:     aws.String("sockerless.test"),
			DetailType: aws.String("example"),
			Detail:     aws.String(`{"ok":true}`),
		}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 0, putEvents.FailedEntryCount)
	require.Len(t, putEvents.Entries, 1)
	require.NotEmpty(t, aws.ToString(putEvents.Entries[0].EventId))

	received, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            q.QueueUrl,
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	require.Len(t, received.Messages, 1)
	assertEventBridgeSQSEnvelope(t, aws.ToString(received.Messages[0].Body),
		"sockerless.test", "example", map[string]any{"ok": true})
}

// TestEventBridge_TestEventPatternSDK expects TestEventPattern to share the
// live rules' evaluator: a matching event is true, and one with the right
// source but an out-of-range detail value false.
func TestEventBridge_TestEventPatternSDK(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()

	pattern := `{"source":["sockerless.tep"],"detail":{"code":[{"numeric":[">",200]}]}}`

	match, err := eb.TestEventPattern(ctx, &eventbridge.TestEventPatternInput{
		EventPattern: aws.String(pattern),
		Event:        aws.String(`{"id":"1","account":"000000000000","source":"sockerless.tep","detail-type":"job","detail":{"code":500}}`),
	})
	require.NoError(t, err)
	assert.True(t, match.Result)

	noMatch, err := eb.TestEventPattern(ctx, &eventbridge.TestEventPatternInput{
		EventPattern: aws.String(pattern),
		Event:        aws.String(`{"id":"2","account":"000000000000","source":"sockerless.tep","detail-type":"job","detail":{"code":100}}`),
	})
	require.NoError(t, err)
	assert.False(t, noMatch.Result)

	// A pattern with a scalar where an array belongs is rejected.
	_, err = eb.TestEventPattern(ctx, &eventbridge.TestEventPatternInput{
		EventPattern: aws.String(`{"source":"sockerless.tep"}`),
		Event:        aws.String(`{"id":"3","account":"000000000000","source":"sockerless.tep"}`),
	})
	requireAWSErrorCode(t, err, "InvalidEventPatternException")
}

func TestEventBridge_ListRuleNamesByTargetSDK(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()

	targetARN := "arn:aws:sqs:us-east-1:000000000000:eb-sdk-lrnbt-target"
	_, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String("eb-sdk-lrnbt-rule"),
		EventPattern: aws.String(`{"source":["sockerless.lrnbt"]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
			Rule: aws.String("eb-sdk-lrnbt-rule"),
			Ids:  []string{"target"},
		})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String("eb-sdk-lrnbt-rule")})
	})
	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String("eb-sdk-lrnbt-rule"),
		Targets: []ebtypes.Target{{
			Id:  aws.String("target"),
			Arn: aws.String(targetARN),
		}},
	})
	require.NoError(t, err)

	out, err := eb.ListRuleNamesByTarget(ctx, &eventbridge.ListRuleNamesByTargetInput{
		TargetArn: aws.String(targetARN),
	})
	require.NoError(t, err)
	require.Contains(t, out.RuleNames, "eb-sdk-lrnbt-rule")

	none, err := eb.ListRuleNamesByTarget(ctx, &eventbridge.ListRuleNamesByTargetInput{
		TargetArn: aws.String(targetARN + "-unrelated"),
	})
	require.NoError(t, err)
	assert.NotContains(t, none.RuleNames, "eb-sdk-lrnbt-rule")
}

// TestEventBridge_UpdateEventBusSDK expects UpdateEventBus to change a custom
// bus's Description and DeadLetterConfig, as DescribeEventBus shows.
func TestEventBridge_UpdateEventBusSDK(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()

	busName := "eb-sdk-update-bus"
	created, err := eb.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{
		Name:        aws.String(busName),
		Description: aws.String("initial"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String(busName)})
	})

	dlqARN := "arn:aws:sqs:us-east-1:000000000000:eb-sdk-update-dlq"
	updated, err := eb.UpdateEventBus(ctx, &eventbridge.UpdateEventBusInput{
		Name:             aws.String(busName),
		Description:      aws.String("updated description"),
		DeadLetterConfig: &ebtypes.DeadLetterConfig{Arn: aws.String(dlqARN)},
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(created.EventBusArn), aws.ToString(updated.Arn))
	assert.Equal(t, "updated description", aws.ToString(updated.Description))
	require.NotNil(t, updated.DeadLetterConfig)
	assert.Equal(t, dlqARN, aws.ToString(updated.DeadLetterConfig.Arn))

	describe, err := eb.DescribeEventBus(ctx, &eventbridge.DescribeEventBusInput{Name: aws.String(busName)})
	require.NoError(t, err)
	assert.Equal(t, "updated description", aws.ToString(describe.Description))
}

// TestEventBridge_ContentFilterPatternSDK matches the nested "detail" object
// with the {"prefix":...} and {"numeric":[...]} matchers: a matching event is
// delivered and one with the right source but wrong detail is not.
func TestEventBridge_ContentFilterPatternSDK(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()
	sqsC := sqsClient()

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("eb-sdk-content-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })
	attrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	queueARN := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)

	// Nested keys AND together; each value array ORs its matchers.
	pattern := `{
		"source": ["sockerless.content"],
		"detail": {
			"state": [{"prefix": "run"}],
			"code": [{"numeric": [">", 200]}]
		}
	}`
	contentRule, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String("eb-sdk-content-rule"),
		EventPattern: aws.String(pattern),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
			Rule: aws.String("eb-sdk-content-rule"),
			Ids:  []string{"queue"},
		})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String("eb-sdk-content-rule")})
	})
	setEBQueuePolicy(t, sqsC, q.QueueUrl, queueARN, aws.ToString(contentRule.RuleArn))
	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String("eb-sdk-content-rule"),
		Targets: []ebtypes.Target{{
			Id:  aws.String("queue"),
			Arn: aws.String(queueARN),
		}},
	})
	require.NoError(t, err)

	matchDetail := `{"state":"running","code":500}`
	// Same source, but neither the prefix nor the numeric matcher matches.
	rejectDetail := `{"state":"queued","code":100}`

	_, err = eb.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{
			{
				Source:     aws.String("sockerless.content"),
				DetailType: aws.String("job"),
				Detail:     aws.String(matchDetail),
			},
			{
				Source:     aws.String("sockerless.content"),
				DetailType: aws.String("job"),
				Detail:     aws.String(rejectDetail),
			},
		},
	})
	require.NoError(t, err)

	// Only the matching event arrives.
	var bodies []string
	out, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            q.QueueUrl,
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     20,
	})
	require.NoError(t, err)
	for _, m := range out.Messages {
		bodies = append(bodies, aws.ToString(m.Body))
		_, _ = sqsC.DeleteMessage(ctx, &sqs.DeleteMessageInput{
			QueueUrl:      q.QueueUrl,
			ReceiptHandle: m.ReceiptHandle,
		})
	}

	require.Len(t, bodies, 1, "only the matching event must be delivered")
	assertEventBridgeSQSEnvelope(t, bodies[0], "sockerless.content", "job",
		map[string]any{"state": "running", "code": float64(500)})
}

// setEBQueuePolicy grants events.amazonaws.com sqs:SendMessage on the queue
// when aws:SourceArn is ruleArn, the policy AWS requires before EventBridge
// delivers to a queue.
func setEBQueuePolicy(t *testing.T, sqsC *sqs.Client, queueURL *string, queueARN, ruleArn string) {
	t.Helper()
	policy := fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [{
			"Sid": "AllowEventBridge",
			"Effect": "Allow",
			"Principal": {"Service": "events.amazonaws.com"},
			"Action": "sqs:SendMessage",
			"Resource": %q,
			"Condition": {"ArnEquals": {"aws:SourceArn": %q}}
		}]
	}`, queueARN, ruleArn)
	_, err := sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   queueURL,
		Attributes: map[string]string{"Policy": policy},
	})
	require.NoError(t, err)
}

// TestEventBridge_SQSDeliveryAuthorizationSDK expects a queue whose policy
// admits events.amazonaws.com for the rule's ARN to receive the event, and a
// queue without a policy or with one naming another SourceArn to receive
// nothing: AWS drops an unauthorized delivery.
func TestEventBridge_SQSDeliveryAuthorizationSDK(t *testing.T) {
	t.Parallel()
	eb := eventbridgeClient()
	sqsC := sqsClient()

	queueARNOf := func(t *testing.T, name string) (*string, string) {
		t.Helper()
		q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name)})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })
		attrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       q.QueueUrl,
			AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
		})
		require.NoError(t, err)
		arn := attrs.Attributes["QueueArn"]
		require.NotEmpty(t, arn)
		return q.QueueUrl, arn
	}

	allowedURL, allowedARN := queueARNOf(t, "eb-auth-allowed-q")
	noPolicyURL, noPolicyARN := queueARNOf(t, "eb-auth-nopolicy-q")
	wrongURL, wrongARN := queueARNOf(t, "eb-auth-wrongarn-q")

	pattern := `{"source":["sockerless.auth"],"detail-type":["example"]}`
	putRule, err := eb.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String("eb-auth-rule"),
		EventPattern: aws.String(pattern),
	})
	require.NoError(t, err)
	ruleArn := aws.ToString(putRule.RuleArn)
	require.NotEmpty(t, ruleArn)
	t.Cleanup(func() {
		_, _ = eb.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{
			Rule: aws.String("eb-auth-rule"),
			Ids:  []string{"allowed", "nopolicy", "wrong"},
		})
		_, _ = eb.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String("eb-auth-rule")})
	})

	// The wrong policy names another SourceArn, so its condition fails.
	setEBQueuePolicy(t, sqsC, allowedURL, allowedARN, ruleArn)
	setEBQueuePolicy(t, sqsC, wrongURL, wrongARN, ruleArn+"-different")

	_, err = eb.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule: aws.String("eb-auth-rule"),
		Targets: []ebtypes.Target{
			{Id: aws.String("allowed"), Arn: aws.String(allowedARN)},
			{Id: aws.String("nopolicy"), Arn: aws.String(noPolicyARN)},
			{Id: aws.String("wrong"), Arn: aws.String(wrongARN)},
		},
	})
	require.NoError(t, err)

	_, err = eb.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []ebtypes.PutEventsRequestEntry{{
			Source:     aws.String("sockerless.auth"),
			DetailType: aws.String("example"),
			Detail:     aws.String(`{"ok":true}`),
		}},
	})
	require.NoError(t, err)

	received, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            allowedURL,
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     1,
	})
	require.NoError(t, err)
	require.Len(t, received.Messages, 1)
	assertEventBridgeSQSEnvelope(t, aws.ToString(received.Messages[0].Body),
		"sockerless.auth", "example", map[string]any{"ok": true})

	for name, url := range map[string]*string{"nopolicy": noPolicyURL, "wrong": wrongURL} {
		out, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            url,
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
		})
		require.NoError(t, err)
		assert.Empty(t, out.Messages, "queue %q must not receive an unauthorized delivery", name)
	}
}
