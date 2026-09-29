package gcp_sdk_test

import (
	"testing"
	"time"

	pubsubpb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestPubSub_GRPC_DeadLetterPolicy proves the subscription's dead-letter
// policy: every delivery reports its attempt number, and once the maximum is
// spent the message moves to the dead-letter topic with the attributes Pub/Sub
// adds to a forwarded message.
func TestPubSub_GRPC_DeadLetterPolicy(t *testing.T) {
	pub, sc := psRawClient(t)
	project := "projects/ps-grpc-dlq"
	topic, dlt := project+"/topics/work", project+"/topics/dead"
	for _, name := range []string{topic, dlt} {
		_, err := pub.CreateTopic(ctx, &pubsubpb.Topic{Name: name})
		require.NoError(t, err)
	}
	deadSub := project + "/subscriptions/dead"
	_, err := sc.CreateSubscription(ctx, &pubsubpb.Subscription{Name: deadSub, Topic: dlt})
	require.NoError(t, err)
	sub := project + "/subscriptions/work"
	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: sub, Topic: topic,
		DeadLetterPolicy: &pubsubpb.DeadLetterPolicy{DeadLetterTopic: dlt, MaxDeliveryAttempts: 5},
	})
	require.NoError(t, err)

	_, err = pub.Publish(ctx, &pubsubpb.PublishRequest{Topic: topic, Messages: []*pubsubpb.PubsubMessage{
		{Data: []byte("poison"), Attributes: map[string]string{"origin": "test"}},
	}})
	require.NoError(t, err)
	for attempt := int32(1); attempt <= 5; attempt++ {
		got := psPullAll(t, sc, sub, 1, 10*time.Second)
		require.Len(t, got, 1, "delivery attempt %d", attempt)
		require.Equal(t, attempt, got[0].GetDeliveryAttempt())
		_, err := sc.ModifyAckDeadline(ctx, &pubsubpb.ModifyAckDeadlineRequest{
			Subscription: sub, AckIds: []string{got[0].GetAckId()}, AckDeadlineSeconds: 0,
		})
		require.NoError(t, err)
	}

	dead := psPullAll(t, sc, deadSub, 1, 10*time.Second)
	require.Len(t, dead, 1, "the exhausted message must reach the dead-letter topic")
	msg := dead[0].GetMessage()
	require.Equal(t, "poison", string(msg.GetData()))
	require.Equal(t, "test", msg.GetAttributes()["origin"])
	require.Equal(t, "5", msg.GetAttributes()["CloudPubSubDeadLetterSourceDeliveryCount"])
	require.Equal(t, "work", msg.GetAttributes()["CloudPubSubDeadLetterSourceSubscription"])
	require.Equal(t, "ps-grpc-dlq", msg.GetAttributes()["CloudPubSubDeadLetterSourceSubscriptionProject"])
	require.Empty(t, psPullN(t, sc, sub, 1), "no sixth delivery attempt")
}

// TestPubSub_GRPC_FilterAndOrdering proves a subscription filter drops the
// messages it rejects and message ordering holds a key's later messages until
// the earlier one is acknowledged.
func TestPubSub_GRPC_FilterAndOrdering(t *testing.T) {
	pub, sc := psRawClient(t)
	project := "projects/ps-grpc-order"
	topic := project + "/topics/events"
	_, err := pub.CreateTopic(ctx, &pubsubpb.Topic{Name: topic})
	require.NoError(t, err)

	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{Name: project + "/subscriptions/bad", Topic: topic, Filter: `attributes.kind = `})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "a malformed filter is INVALID_ARGUMENT")

	sub := project + "/subscriptions/ordered"
	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: sub, Topic: topic, EnableMessageOrdering: true, Filter: `attributes.kind = "order"`,
	})
	require.NoError(t, err)
	order := map[string]string{"kind": "order"}
	_, err = pub.Publish(ctx, &pubsubpb.PublishRequest{Topic: topic, Messages: []*pubsubpb.PubsubMessage{
		{Data: []byte("k-1"), OrderingKey: "k", Attributes: order},
		{Data: []byte("noise"), OrderingKey: "k", Attributes: map[string]string{"kind": "noise"}},
		{Data: []byte("k-2"), OrderingKey: "k", Attributes: order},
	}})
	require.NoError(t, err)

	first := psPullN(t, sc, sub, 1)
	require.Len(t, first, 1)
	require.Equal(t, "k-1", string(first[0].GetMessage().GetData()))
	require.Equal(t, "k", first[0].GetMessage().GetOrderingKey())
	require.Empty(t, psPullN(t, sc, sub, 10), "k-2 waits behind the outstanding k-1")
	_, err = sc.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{Subscription: sub, AckIds: []string{first[0].GetAckId()}})
	require.NoError(t, err)

	second := psPullN(t, sc, sub, 10)
	require.Len(t, second, 1, "the filter drops the noise message")
	require.Equal(t, "k-2", string(second[0].GetMessage().GetData()))
}
