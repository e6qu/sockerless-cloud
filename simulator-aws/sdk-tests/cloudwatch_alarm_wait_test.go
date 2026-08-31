package aws_sdk_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/require"
)

// Waiting on a condition, not on a clock. The alarm tests used to sleep a
// fixed two or three seconds at each step; a fixed wait is wrong in both
// directions — it costs its full length on every run, and on a loaded machine
// it can still elapse before the thing it waits for has happened, which is a
// flake that looks like a delivery bug. Each helper below waits for the state
// the test actually depends on and returns the moment it holds.

// cloudWatchProbeID hands each alarm probe a namespace and alarm name nothing
// else uses, so metric data left behind by one cannot stand in for another's.
var cloudWatchProbeID atomic.Int64

func nextCloudWatchProbeID() int64 { return cloudWatchProbeID.Add(1) }

const (
	// How long a condition is given to become true before the test calls it a
	// failure. Generous, because it is only ever paid when something is
	// genuinely broken.
	alarmWaitTimeout = 30 * time.Second
	alarmWaitPoll    = 25 * time.Millisecond
	// How long a queue is watched to confirm nothing arrives. A negative
	// observation has to spend its window, but it ends early — and loudly — the
	// moment a message shows up.
	alarmQuietWindow = 2 * time.Second
)

// awaitSubscription waits until a topic reports the subscription just made, so
// the alarm that follows is created against a topic the evaluator can already
// see delivering.
func awaitSubscription(ctx context.Context, t *testing.T, snsC *sns.Client, topicARN, endpoint string) {
	t.Helper()
	require.Eventually(t, func() bool {
		listed, err := snsC.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{
			TopicArn: aws.String(topicARN),
		})
		if err != nil {
			return false
		}
		for _, subscription := range listed.Subscriptions {
			if aws.ToString(subscription.Endpoint) == endpoint {
				return true
			}
		}
		return false
	}, alarmWaitTimeout, alarmWaitPoll, "the topic never reported its subscription to %s", endpoint)
}

// awaitQueueMessages waits for a queue to hold the messages an alarm action
// delivers, and hands them to the assertions that read them.
func awaitQueueMessages(ctx context.Context, t *testing.T, sqsC *sqs.Client, queueURL *string, want int) *sqs.ReceiveMessageOutput {
	t.Helper()
	var received *sqs.ReceiveMessageOutput
	require.Eventually(t, func() bool {
		out, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: queueURL, MaxNumberOfMessages: int32(want),
		})
		if err != nil || len(out.Messages) < want {
			return false
		}
		received = out
		return true
	}, alarmWaitTimeout, alarmWaitPoll, "the queue never received %d message(s)", want)
	return received
}

// awaitRecordedTransition waits until the alarm's history shows CloudWatch
// having moved it into the wanted state.
//
// DescribeAlarms reports the state derived from the metric data, which is the
// answer as soon as the data is there; the transition — and the action dispatch
// that rides it — is the service's own, and the history is where a client can
// see it has happened. A test that puts its next datapoint before the previous
// transition is recorded gets a notification for a transition that skipped a
// state, which looks like a dispatch bug and is not one.
func awaitRecordedTransition(ctx context.Context, t *testing.T, cw *cloudwatch.Client, alarmName, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		history, err := cw.DescribeAlarmHistory(ctx, &cloudwatch.DescribeAlarmHistoryInput{
			AlarmName:       aws.String(alarmName),
			HistoryItemType: cwtypes.HistoryItemTypeStateUpdate,
		})
		if err != nil {
			return false
		}
		for _, item := range history.AlarmHistoryItems {
			var recorded struct {
				NewState string `json:"newState"`
			}
			if json.Unmarshal([]byte(aws.ToString(item.HistoryData)), &recorded) != nil {
				continue
			}
			if recorded.NewState == want {
				return true
			}
		}
		return false
	}, alarmWaitTimeout, alarmWaitPoll, "the alarm never recorded a transition into %s", want)
}

// requireQueueStaysEmpty confirms an alarm delivered nothing. The evaluator has
// already run by the time this is called — the alarm reached ALARM — so this
// watches for a delivery that must not happen, and fails as soon as one does.
func requireQueueStaysEmpty(ctx context.Context, t *testing.T, sqsC *sqs.Client, queueURL *string, why string) {
	t.Helper()
	deadline := time.Now().Add(alarmQuietWindow)
	for time.Now().Before(deadline) {
		out, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: queueURL})
		require.NoError(t, err)
		require.Empty(t, out.Messages, why)
		time.Sleep(alarmWaitPoll)
	}
}
