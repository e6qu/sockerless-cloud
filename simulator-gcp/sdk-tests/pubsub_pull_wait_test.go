package gcp_sdk_test

import (
	"encoding/base64"
	"testing"

	vkit "cloud.google.com/go/pubsub/apiv1"
	pubsubpb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// psGapicClients builds the generated cloud.google.com/go/pubsub/apiv1
// Publisher and Subscriber clients against the simulator's gRPC endpoint.
func psGapicClients(t *testing.T) (*vkit.PublisherClient, *vkit.SubscriberClient) {
	t.Helper()
	opts := []option.ClientOption{
		option.WithEndpoint(grpcAddr),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithTelemetryDisabled(),
	}
	pub, err := vkit.NewPublisherClient(ctx, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { pub.Close() })
	sub, err := vkit.NewSubscriberClient(ctx, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { sub.Close() })
	return pub, sub
}

type psPullResult struct {
	resp *pubsubpb.PullResponse
	err  error
}

// TestPubSub_GRPC_PullWaitsForPublish starts a unary Pull on an empty
// subscription without returnImmediately and receives the messages a later
// Publish delivers, at most maxMessages of them.
func TestPubSub_GRPC_PullWaitsForPublish(t *testing.T) {
	pub, sub := psGapicClients(t)
	topic := "projects/ps-pull-wait/topics/" + uniqueName("wait-topic")
	subName := "projects/ps-pull-wait/subscriptions/" + uniqueName("wait-sub")
	_, err := pub.CreateTopic(ctx, &pubsubpb.Topic{Name: topic})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{Topic: topic}) })
	_, err = sub.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subName, Topic: topic, AckDeadlineSeconds: 60})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName}) })

	empty, err := sub.Pull(ctx, &pubsubpb.PullRequest{Subscription: subName, MaxMessages: 10, ReturnImmediately: true})
	require.NoError(t, err)
	require.Empty(t, empty.GetReceivedMessages(), "returnImmediately answers an empty subscription with no messages")

	pulled := make(chan psPullResult, 1)
	go func() {
		resp, err := sub.Pull(ctx, &pubsubpb.PullRequest{Subscription: subName, MaxMessages: 2})
		pulled <- psPullResult{resp, err}
	}()

	_, err = pub.Publish(ctx, &pubsubpb.PublishRequest{Topic: topic, Messages: []*pubsubpb.PubsubMessage{
		{Data: []byte("first")}, {Data: []byte("second")}, {Data: []byte("third")},
	}})
	require.NoError(t, err)

	got := <-pulled
	require.NoError(t, got.err)
	require.Len(t, got.resp.GetReceivedMessages(), 2, "a Pull returns at most maxMessages")
	assert.Equal(t, "first", string(got.resp.GetReceivedMessages()[0].GetMessage().GetData()))
	assert.Equal(t, "second", string(got.resp.GetReceivedMessages()[1].GetMessage().GetData()))

	rest, err := sub.Pull(ctx, &pubsubpb.PullRequest{Subscription: subName, MaxMessages: 10})
	require.NoError(t, err)
	require.Len(t, rest.GetReceivedMessages(), 1)
	assert.Equal(t, "third", string(rest.GetReceivedMessages()[0].GetMessage().GetData()))
}

// TestPubSub_GRPC_PullWaitsForNack holds a Pull open while the only message is
// leased, and receives the message once a negative acknowledgement releases it.
func TestPubSub_GRPC_PullWaitsForNack(t *testing.T) {
	pub, sub := psGapicClients(t)
	topic := "projects/ps-pull-wait/topics/" + uniqueName("nack-topic")
	subName := "projects/ps-pull-wait/subscriptions/" + uniqueName("nack-sub")
	_, err := pub.CreateTopic(ctx, &pubsubpb.Topic{Name: topic})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{Topic: topic}) })
	_, err = sub.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subName, Topic: topic, AckDeadlineSeconds: 600})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName}) })

	_, err = pub.Publish(ctx, &pubsubpb.PublishRequest{Topic: topic, Messages: []*pubsubpb.PubsubMessage{{Data: []byte("leased")}}})
	require.NoError(t, err)
	first, err := sub.Pull(ctx, &pubsubpb.PullRequest{Subscription: subName, MaxMessages: 1})
	require.NoError(t, err)
	require.Len(t, first.GetReceivedMessages(), 1)

	pulled := make(chan psPullResult, 1)
	go func() {
		resp, err := sub.Pull(ctx, &pubsubpb.PullRequest{Subscription: subName, MaxMessages: 1})
		pulled <- psPullResult{resp, err}
	}()

	require.NoError(t, sub.ModifyAckDeadline(ctx, &pubsubpb.ModifyAckDeadlineRequest{
		Subscription: subName, AckIds: []string{first.GetReceivedMessages()[0].GetAckId()}, AckDeadlineSeconds: 0,
	}))

	got := <-pulled
	require.NoError(t, got.err)
	require.Len(t, got.resp.GetReceivedMessages(), 1)
	assert.Equal(t, "leased", string(got.resp.GetReceivedMessages()[0].GetMessage().GetData()))
	assert.NotEqual(t, first.GetReceivedMessages()[0].GetAckId(), got.resp.GetReceivedMessages()[0].GetAckId(),
		"a redelivery carries a new ack id")
}

// TestPubSub_REST_PullWaitsForPublish starts projects.subscriptions.pull on an
// empty subscription without returnImmediately and receives the message a
// later projects.topics.publish delivers.
func TestPubSub_REST_PullWaitsForPublish(t *testing.T) {
	svc := pubsubService(t)
	topic := "projects/ps-pull-wait/topics/" + uniqueName("rest-wait-topic")
	subName := "projects/ps-pull-wait/subscriptions/" + uniqueName("rest-wait-sub")
	_, err := svc.Projects.Topics.Create(topic, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Topics.Delete(topic).Do() })
	_, err = svc.Projects.Subscriptions.Create(subName, &pubsub.Subscription{Topic: topic, AckDeadlineSeconds: 60}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Subscriptions.Delete(subName).Do() })

	empty, err := svc.Projects.Subscriptions.Pull(subName, &pubsub.PullRequest{MaxMessages: 10, ReturnImmediately: true}).Do()
	require.NoError(t, err)
	require.Empty(t, empty.ReceivedMessages)

	type result struct {
		resp *pubsub.PullResponse
		err  error
	}
	pulled := make(chan result, 1)
	go func() {
		resp, err := svc.Projects.Subscriptions.Pull(subName, &pubsub.PullRequest{MaxMessages: 10}).Context(ctx).Do()
		pulled <- result{resp, err}
	}()

	_, err = svc.Projects.Topics.Publish(topic, &pubsub.PublishRequest{Messages: []*pubsub.PubsubMessage{
		{Data: base64.StdEncoding.EncodeToString([]byte("over REST"))},
	}}).Do()
	require.NoError(t, err)

	got := <-pulled
	require.NoError(t, got.err)
	require.Len(t, got.resp.ReceivedMessages, 1)
	data, err := base64.StdEncoding.DecodeString(got.resp.ReceivedMessages[0].Message.Data)
	require.NoError(t, err)
	assert.Equal(t, "over REST", string(data))
}
