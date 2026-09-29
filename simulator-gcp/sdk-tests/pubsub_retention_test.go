package gcp_sdk_test

import (
	"sort"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	pubsubpb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func psPayloads(msgs []*pubsubpb.ReceivedMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, string(m.GetMessage().GetData()))
	}
	sort.Strings(out)
	return out
}

func psAckAll(t *testing.T, sc pubsubpb.SubscriberClient, sub string, msgs []*pubsubpb.ReceivedMessage) {
	t.Helper()
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.GetAckId())
	}
	_, err := sc.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{Subscription: sub, AckIds: ids})
	require.NoError(t, err)
}

// TestPubSub_GRPC_SeekReplaysRetainedAcknowledgedMessages proves a
// subscription with retainAckedMessages keeps what it acknowledged: a seek to
// a time marks the retained messages published from then on unacknowledged
// again, and leaves the earlier ones acknowledged. The same seek on a
// subscription that does not retain acknowledged messages replays nothing.
func TestPubSub_GRPC_SeekReplaysRetainedAcknowledgedMessages(t *testing.T) {
	c := newPSGRPCClient(t, "ps-retain-proj")
	_, sc := psRawClient(t)
	topic, err := c.CreateTopic(ctx, "retain-topic")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, topic.Delete(ctx)) })

	retaining := "projects/ps-retain-proj/subscriptions/retaining"
	plain := "projects/ps-retain-proj/subscriptions/plain"
	for _, sub := range []*pubsubpb.Subscription{
		{Name: retaining, Topic: topic.String(), RetainAckedMessages: true},
		{Name: plain, Topic: topic.String()},
	} {
		_, err := sc.CreateSubscription(ctx, sub)
		require.NoError(t, err)
		name := sub.GetName()
		t.Cleanup(func() {
			_, err := sc.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: name})
			assert.NoError(t, err)
		})
	}

	for _, data := range []string{"before", "after-1", "after-2"} {
		_, err = topic.Publish(ctx, &pubsub.Message{Data: []byte(data)}).Get(ctx)
		require.NoError(t, err)
	}

	// The seek targets the first "after" message's publish time, and a seek
	// replays every retained message published at or after it — "before" too
	// when it shares that millisecond — so the expectation comes from the
	// publish times the pull returned.
	var seekTo time.Time
	var wantReplayed []string
	for _, sub := range []string{retaining, plain} {
		got := psPullAll(t, sc, sub, 3, 10*time.Second)
		require.Equal(t, []string{"after-1", "after-2", "before"}, psPayloads(got))
		if seekTo.IsZero() {
			for _, m := range got {
				published := m.GetMessage().GetPublishTime().AsTime()
				if string(m.GetMessage().GetData()) != "before" && (seekTo.IsZero() || published.Before(seekTo)) {
					seekTo = published
				}
			}
			for _, m := range got {
				if !m.GetMessage().GetPublishTime().AsTime().Before(seekTo) {
					wantReplayed = append(wantReplayed, string(m.GetMessage().GetData()))
				}
			}
			sort.Strings(wantReplayed)
		}
		psAckAll(t, sc, sub, got)
		require.Empty(t, psPullN(t, sc, sub, 3), "%s: everything was acknowledged", sub)

		_, err = sc.Seek(ctx, &pubsubpb.SeekRequest{
			Subscription: sub,
			Target:       &pubsubpb.SeekRequest_Time{Time: timestamppb.New(seekTo)},
		})
		require.NoError(t, err)
	}

	replayed := psPullAll(t, sc, retaining, len(wantReplayed), 10*time.Second)
	assert.Equal(t, wantReplayed, psPayloads(replayed),
		"the seek replays the retained messages published from the seek time on")
	psAckAll(t, sc, retaining, replayed)
	assert.Empty(t, psPullN(t, sc, plain, 3), "a subscription that does not retain acknowledged messages has nothing to replay")
}

// TestPubSub_GRPC_AckDeadlineRange proves the 10–600 second range Pub/Sub
// declares for ackDeadlineSeconds holds at create and at update, and that an
// update names its fields the way the client library sends a FieldMask.
func TestPubSub_GRPC_AckDeadlineRange(t *testing.T) {
	c := newPSGRPCClient(t, "ps-deadline-proj")
	_, sc := psRawClient(t)
	topic, err := c.CreateTopic(ctx, "deadline-topic")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, topic.Delete(ctx)) })

	for _, seconds := range []int32{5, 601} {
		_, err := sc.CreateSubscription(ctx, &pubsubpb.Subscription{
			Name: "projects/ps-deadline-proj/subscriptions/out-of-range", Topic: topic.String(), AckDeadlineSeconds: seconds,
		})
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "ackDeadlineSeconds %d", seconds)
	}

	sub, err := c.CreateSubscription(ctx, "in-range", pubsub.SubscriptionConfig{Topic: topic, AckDeadline: 10 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sub.Delete(ctx)) })

	updated, err := sub.Update(ctx, pubsub.SubscriptionConfigToUpdate{AckDeadline: 45 * time.Second})
	require.NoError(t, err)
	assert.Equal(t, 45*time.Second, updated.AckDeadline)
	cfg, err := sub.Config(ctx)
	require.NoError(t, err)
	assert.Equal(t, 45*time.Second, cfg.AckDeadline, "the update the client library sent was applied")

	_, err = sc.UpdateSubscription(ctx, &pubsubpb.UpdateSubscriptionRequest{
		Subscription: &pubsubpb.Subscription{Name: sub.String(), AckDeadlineSeconds: 700},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"ack_deadline_seconds"}},
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	cfg, err = sub.Config(ctx)
	require.NoError(t, err)
	assert.Equal(t, 45*time.Second, cfg.AckDeadline, "a refused update leaves the subscription as it was")
}
