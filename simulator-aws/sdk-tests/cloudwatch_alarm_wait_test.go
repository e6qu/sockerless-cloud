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

// These helpers wait for the state a test depends on and return as soon as it
// holds: a fixed sleep costs its whole length every run and can still end
// early on a loaded machine.

// cloudWatchProbeID gives each alarm probe its own namespace and alarm name, so
// one probe's leftover metric data cannot stand in for another's.
var cloudWatchProbeID atomic.Int64

func nextCloudWatchProbeID() int64 { return cloudWatchProbeID.Add(1) }

const (
	// alarmWaitTimeout is paid only when something is broken.
	alarmWaitTimeout = 30 * time.Second
	alarmWaitPoll    = 25 * time.Millisecond
	// alarmQuietWindow is how long a queue is watched for an unwanted message; the
	// watch fails as soon as one arrives.
	alarmQuietWindow = 2 * time.Second
)

// awaitSubscription waits until a topic lists the subscription, so the alarm
// that follows targets a topic that can deliver.
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

func awaitAlarmState(ctx context.Context, t *testing.T, cw *cloudwatch.Client, alarmName string, want cwtypes.StateValue, why string) cwtypes.MetricAlarm {
	t.Helper()
	out, err := cloudwatch.NewAlarmExistsWaiter(cw, func(o *cloudwatch.AlarmExistsWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(ctx, &cloudwatch.DescribeAlarmsInput{
		AlarmNames: []string{alarmName},
		StateValue: want,
	}, alarmWaitTimeout)
	require.NoError(t, err, why)
	require.Len(t, out.MetricAlarms, 1, why)
	return out.MetricAlarms[0]
}

func awaitQueueMessages(ctx context.Context, t *testing.T, sqsC *sqs.Client, queueURL *string, want int) *sqs.ReceiveMessageOutput {
	t.Helper()
	received := &sqs.ReceiveMessageOutput{}
	deadline := time.Now().Add(alarmWaitTimeout)
	for len(received.Messages) < want && time.Now().Before(deadline) {
		out, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            queueURL,
			MaxNumberOfMessages: int32(want - len(received.Messages)),
			WaitTimeSeconds:     int32(min(20, max(1, time.Until(deadline)/time.Second))),
		})
		require.NoError(t, err)
		received.Messages = append(received.Messages, out.Messages...)
	}
	require.Len(t, received.Messages, want, "the queue never received %d message(s)", want)
	return received
}

// awaitRecordedTransition waits until the alarm's history shows CloudWatch
// moved it into want. DescribeAlarms derives state from the metric data at
// once, while the transition and its action dispatch are the service's own and
// show in the history; a datapoint put before the previous transition is
// recorded yields a notification that skipped a state.
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

// requireQueueStaysEmpty confirms an alarm that already reached ALARM delivered
// nothing, failing as soon as a message arrives.
func requireQueueStaysEmpty(ctx context.Context, t *testing.T, sqsC *sqs.Client, queueURL *string, why string) {
	t.Helper()
	out, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:        queueURL,
		WaitTimeSeconds: int32(alarmQuietWindow / time.Second),
	})
	require.NoError(t, err)
	require.Empty(t, out.Messages, why)
}
