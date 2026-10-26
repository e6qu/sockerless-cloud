package gcp_sdk_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	pubsubpb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// newPSGRPCClient points the client at the simulator by endpoint rather than
// through the process-wide PUBSUB_EMULATOR_HOST.
func newPSGRPCClient(t *testing.T, project string) *pubsub.Client {
	t.Helper()
	ensureProject(t, project)
	c, err := pubsub.NewClient(ctx, project, append(simGRPCClientOptions(), option.WithTelemetryDisabled())...)
	if err != nil {
		t.Fatalf("pubsub.NewClient (gRPC): %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// psRawClient returns the raw Publisher and Subscriber clients, which drive the
// exact Pull, Acknowledge and ModifyAckDeadline contract without the
// high-level client's automatic modack.
func psRawClient(t *testing.T) (pubsubpb.PublisherClient, pubsubpb.SubscriberClient) {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, simGRPCDialOptions(simTokenSource())...)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return pubsubpb.NewPublisherClient(conn), pubsubpb.NewSubscriberClient(conn)
}

func psRawSubscriber(t *testing.T) pubsubpb.SubscriberClient {
	t.Helper()
	_, sc := psRawClient(t)
	return sc
}

func TestPubSub_GRPC_TopicAndSubscriptionCRUD(t *testing.T) {
	t.Parallel()
	c := newPSGRPCClient(t, "ps-grpc-proj")
	topicID := "crud-topic"

	topic := c.Topic(topicID)
	exists, err := topic.Exists(ctx)
	require.NoError(t, err, "Exists on absent topic")
	require.False(t, exists, "topic should not exist before create")

	_, err = c.CreateTopicWithConfig(ctx, topicID, &pubsub.TopicConfig{
		Labels: map[string]string{"env": "test"},
	})
	require.NoError(t, err, "CreateTopicWithConfig")

	exists, err = topic.Exists(ctx)
	require.NoError(t, err)
	require.True(t, exists, "topic should exist after create")

	cfg, err := topic.Config(ctx)
	require.NoError(t, err, "topic.Config (GetTopic)")
	require.Equal(t, topicID, topic.ID(), "topic.ID must match")
	require.Equal(t, "test", cfg.Labels["env"], "GetTopic must round-trip labels")

	// Only iterator.Done ends the walk; any other error is a failed RPC.
	it := c.Topics(ctx)
	var names []string
	for {
		tt, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err, "ListTopics iteration")
		names = append(names, tt.ID())
	}
	require.Contains(t, names, topicID, "ListTopics must include created topic")

	sub := c.Subscription("crud-sub")
	_, err = c.CreateSubscription(ctx, "crud-sub", pubsub.SubscriptionConfig{Topic: topic})
	require.NoError(t, err, "CreateSubscription")
	exists, err = sub.Exists(ctx)
	require.NoError(t, err)
	require.True(t, exists, "subscription should exist after create")

	subCfg, err := sub.Config(ctx)
	require.NoError(t, err, "subscription.Config (GetSubscription)")
	require.Equal(t, "crud-sub", sub.ID())
	// Pub/Sub applies the default 10s ack deadline and 7d retention.
	require.Equal(t, 10*time.Second, subCfg.AckDeadline, "default ack deadline must be honoured")
	require.NotZero(t, subCfg.RetentionDuration, "default message retention must be populated")

	subs := c.Subscriptions(ctx)
	var subNames []string
	for {
		ss, err := subs.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err, "ListSubscriptions iteration")
		subNames = append(subNames, ss.ID())
	}
	require.Contains(t, subNames, "crud-sub", "ListSubscriptions must include created subscription")

	require.NoError(t, sub.Delete(ctx), "DeleteSubscription")
	require.NoError(t, topic.Delete(ctx), "DeleteTopic")
}

func TestPubSub_GRPC_PublishPull(t *testing.T) {
	t.Parallel()
	c := newPSGRPCClient(t, "ps-grpc-proj")
	pc, sc := psRawClient(t)

	topic := c.Topic("pp-topic")
	_, err := c.CreateTopic(ctx, topic.ID())
	require.NoError(t, err)
	defer topic.Delete(ctx)

	subName := "projects/ps-grpc-proj/subscriptions/pp-sub"
	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: subName, Topic: topic.String(), AckDeadlineSeconds: 30,
	})
	require.NoError(t, err)
	defer sc.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})

	want := []struct {
		data string
		attr map[string]string
	}{
		{data: "one", attr: map[string]string{"k": "v1"}},
		{data: "two", attr: map[string]string{"k": "v2"}},
		{data: "three", attr: map[string]string{"k": "v3"}},
	}
	var pubMsgs []*pubsubpb.PubsubMessage
	for _, w := range want {
		pubMsgs = append(pubMsgs, &pubsubpb.PubsubMessage{Data: []byte(w.data), Attributes: w.attr})
	}
	pres, err := pc.Publish(ctx, &pubsubpb.PublishRequest{Topic: topic.String(), Messages: pubMsgs})
	require.NoError(t, err, "Publish")
	require.Len(t, pres.GetMessageIds(), len(want), "Publish must assign one id per message")

	got := psPullAll(t, sc, subName, len(want), 10*time.Second)
	require.Len(t, got, len(want), "must receive every published message")

	gotData := map[string]string{}
	gotAttrs := map[string]map[string]string{}
	var gotIds []string
	for _, rm := range got {
		gotData[string(rm.GetMessage().GetData())] = rm.GetMessage().GetMessageId()
		gotAttrs[string(rm.GetMessage().GetData())] = rm.GetMessage().GetAttributes()
		gotIds = append(gotIds, rm.GetMessage().GetMessageId())
	}
	for _, w := range want {
		require.Contains(t, gotData, w.data, "message data %q must be delivered", w.data)
		require.Equal(t, w.attr, gotAttrs[w.data], "attributes for %q must round-trip", w.data)
	}
	require.ElementsMatch(t, pres.GetMessageIds(), gotIds, "assigned message ids must be returned")

	var ackIDs []string
	for _, rm := range got {
		ackIDs = append(ackIDs, rm.GetAckId())
	}
	_, err = sc.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{Subscription: subName, AckIds: ackIDs})
	require.NoError(t, err, "Acknowledge")

	// A subsequent pull returns nothing — acked messages are not redelivered.
	redrain := psPullN(t, sc, subName, 10)
	require.Empty(t, redrain, "acked messages must not be redelivered")
}

// TestPubSub_GRPC_AckDeadlineRedelivery: a pulled message left unacknowledged
// returns to the queue when its ack deadline expires and is redelivered.
func TestPubSub_GRPC_AckDeadlineRedelivery(t *testing.T) {
	t.Parallel()
	c := newPSGRPCClient(t, "ps-grpc-proj")
	_, sc := psRawClient(t)

	topic := c.Topic("rd-topic")
	_, err := c.CreateTopic(ctx, topic.ID())
	require.NoError(t, err)
	defer topic.Delete(ctx)

	// The shortest ack deadline a subscription takes.
	subName := "projects/ps-grpc-proj/subscriptions/rd-sub"
	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: subName, Topic: topic.String(), AckDeadlineSeconds: 10,
	})
	require.NoError(t, err)
	defer sc.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})

	_, err = topic.Publish(ctx, &pubsub.Message{Data: []byte("will-redeliver")}).Get(ctx)
	require.NoError(t, err, "Publish")

	first := psPullN(t, sc, subName, 1)
	require.Len(t, first, 1, "first pull must return the published message")
	require.Equal(t, "will-redeliver", string(first[0].GetMessage().GetData()))

	// A StreamingPull holds open until the service redelivers the message after its
	// 10-second deadline.
	got := psStreamUntil(t, psOpenStreamingPull(t, sc, subName), "will-redeliver")
	require.Len(t, got, 1, "the stream delivers only the redelivered message")
	redelivered := got[0]
	require.NotEqual(t, first[0].GetAckId(), redelivered.GetAckId(), "redelivery must carry a fresh ackId")
}

// TestPubSub_GRPC_ModifyAckDeadline: one message's deadline is cut to a second
// and then extended; a sentinel pulled after it is cut to a second and left, so
// it expires no earlier than the first message's short deadline. When the
// stream redelivers the sentinel, the first message's short deadline has
// passed, and the extension must have held it. A zero deadline then returns it
// on the same stream at once.
func TestPubSub_GRPC_ModifyAckDeadline(t *testing.T) {
	t.Parallel()
	c := newPSGRPCClient(t, "ps-grpc-proj")
	sc := psRawSubscriber(t)

	topic := c.Topic("mod-topic")
	_, err := c.CreateTopic(ctx, topic.ID())
	require.NoError(t, err)
	defer topic.Delete(ctx)

	subName := "projects/ps-grpc-proj/subscriptions/mod-sub"
	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: subName, Topic: topic.String(), AckDeadlineSeconds: 10,
	})
	require.NoError(t, err)
	defer sc.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})

	modack := func(ackID string, seconds int32) {
		t.Helper()
		_, err := sc.ModifyAckDeadline(ctx, &pubsubpb.ModifyAckDeadlineRequest{
			Subscription: subName, AckIds: []string{ackID}, AckDeadlineSeconds: seconds,
		})
		require.NoError(t, err, "ModifyAckDeadline to %ds", seconds)
	}
	publishAndPull := func(data string) *pubsubpb.ReceivedMessage {
		t.Helper()
		_, err := topic.Publish(ctx, &pubsub.Message{Data: []byte(data)}).Get(ctx)
		require.NoError(t, err)
		pulled := psPullN(t, sc, subName, 1)
		require.Len(t, pulled, 1, "pull must return the published message")
		require.Equal(t, data, string(pulled[0].GetMessage().GetData()))
		return pulled[0]
	}

	extended := publishAndPull("mod-test")
	modack(extended.GetAckId(), 1)
	modack(extended.GetAckId(), 60)

	sentinel := publishAndPull("mod-sentinel")
	modack(sentinel.GetAckId(), 1)

	stream := psOpenStreamingPull(t, sc, subName)
	before := psStreamUntil(t, stream, "mod-sentinel")
	for _, m := range before {
		require.NotEqual(t, "mod-test", string(m.GetMessage().GetData()),
			"a message whose deadline was extended must not be redelivered when its earlier deadline passes")
	}

	// A zero deadline expires the message immediately, so the open stream
	// redelivers it with a fresh ackId.
	modack(extended.GetAckId(), 0)
	redelivered := psStreamUntil(t, stream, "mod-test")
	last := redelivered[len(redelivered)-1]
	require.NotEqual(t, extended.GetAckId(), last.GetAckId(), "redelivery carries a fresh ackId")
}

// psOpenStreamingPull's stream closes when the test ends or its minute is up.
func psOpenStreamingPull(t *testing.T, sc pubsubpb.SubscriberClient, sub string) pubsubpb.Subscriber_StreamingPullClient {
	t.Helper()
	streamCtx, cancel := context.WithTimeout(ctx, time.Minute)
	t.Cleanup(cancel)
	stream, err := sc.StreamingPull(streamCtx)
	require.NoError(t, err, "StreamingPull")
	require.NoError(t, stream.Send(&pubsubpb.StreamingPullRequest{
		Subscription: sub, StreamAckDeadlineSeconds: 60,
	}))
	return stream
}

// psStreamUntil returns every message received up to and including the batch
// carrying data. Recv blocks until the service delivers; the stream's context
// bounds the wait.
func psStreamUntil(t *testing.T, stream pubsubpb.Subscriber_StreamingPullClient, data string) []*pubsubpb.ReceivedMessage {
	t.Helper()
	var got []*pubsubpb.ReceivedMessage
	for {
		resp, err := stream.Recv()
		require.NoError(t, err, "StreamingPull Recv waiting for %q", data)
		var match *pubsubpb.ReceivedMessage
		for _, m := range resp.GetReceivedMessages() {
			if string(m.GetMessage().GetData()) == data && match == nil {
				match = m
				continue
			}
			got = append(got, m)
		}
		if match != nil {
			return append(got, match)
		}
	}
}

func TestPubSub_GRPC_StreamingPull(t *testing.T) {
	t.Parallel()
	c := newPSGRPCClient(t, "ps-grpc-proj")
	topic := c.Topic("sp-topic")
	_, err := c.CreateTopic(ctx, topic.ID())
	require.NoError(t, err)
	defer topic.Delete(ctx)

	sub := c.Subscription("sp-sub")
	_, err = c.CreateSubscription(ctx, "sp-sub", pubsub.SubscriptionConfig{Topic: topic})
	require.NoError(t, err)
	defer sub.Delete(ctx)

	want := []string{"alpha", "beta", "gamma"}
	for _, p := range want {
		r := topic.Publish(ctx, &pubsub.Message{Data: []byte(p)})
		_, err := r.Get(ctx)
		require.NoError(t, err, "Publish %s", p)
	}

	// Receive dispatches each message to the handler on its own goroutine,
	// which acks it and hands its payload to the test.
	received := make(chan string, len(want))
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	receiveDone := make(chan error, 1)
	go func() {
		receiveDone <- sub.Receive(rctx, func(_ context.Context, m *pubsub.Message) {
			m.Ack()
			select {
			case received <- string(m.Data):
			case <-rctx.Done():
			}
		})
	}()

	var got []string
	for len(got) < len(want) {
		select {
		case data := <-received:
			got = append(got, data)
		case err := <-receiveDone:
			t.Fatalf("Receive returned after %q of %q: %v", got, want, err)
		}
	}
	require.ElementsMatch(t, want, got, "StreamingPull must deliver every published message once")

	cancel()
	require.NoError(t, <-receiveDone)
}

func TestPubSub_GRPC_SeekSnapshot(t *testing.T) {
	t.Parallel()
	c := newPSGRPCClient(t, "ps-grpc-proj")
	sc := psRawSubscriber(t)

	topic := c.Topic("sk-topic")
	_, err := c.CreateTopic(ctx, topic.ID())
	require.NoError(t, err)
	defer topic.Delete(ctx)

	subName := "projects/ps-grpc-proj/subscriptions/sk-sub"
	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: subName, Topic: topic.String(), AckDeadlineSeconds: 60,
	})
	require.NoError(t, err)
	defer sc.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})

	want := []string{"s1", "s2", "s3"}
	for _, p := range want {
		_, err = topic.Publish(ctx, &pubsub.Message{Data: []byte(p)}).Get(ctx)
		require.NoError(t, err, "Publish %s", p)
	}

	snapName := "projects/ps-grpc-proj/snapshots/sk-snap"
	_, err = sc.CreateSnapshot(ctx, &pubsubpb.CreateSnapshotRequest{
		Name: snapName, Subscription: subName,
	})
	require.NoError(t, err, "CreateSnapshot")
	defer sc.DeleteSnapshot(ctx, &pubsubpb.DeleteSnapshotRequest{Snapshot: snapName})

	for _, rm := range psPullAll(t, sc, subName, len(want), 10*time.Second) {
		_, err := sc.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{Subscription: subName, AckIds: []string{rm.GetAckId()}})
		require.NoError(t, err, "Acknowledge before seek")
	}
	require.Empty(t, psPullN(t, sc, subName, 10), "queue must be empty after acking all")

	_, err = sc.Seek(ctx, &pubsubpb.SeekRequest{
		Subscription: subName,
		Target:       &pubsubpb.SeekRequest_Snapshot{Snapshot: snapName},
	})
	require.NoError(t, err, "Seek to snapshot")

	replayed := psPullAll(t, sc, subName, len(want), 10*time.Second)
	require.Len(t, replayed, len(want), "seek must replay the full backlog")
	gotSet := map[string]bool{}
	for _, rm := range replayed {
		gotSet[string(rm.GetMessage().GetData())] = true
	}
	for _, w := range want {
		require.True(t, gotSet[w], "seek must redeliver %q", w)
	}
}

func TestPubSub_GRPC_Schema(t *testing.T) {
	t.Parallel()
	newPSGRPCClient(t, "ps-grpc-proj")
	conn, err := grpc.NewClient(grpcAddr, simGRPCDialOptions(simTokenSource())...)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	sc := pubsubpb.NewSchemaServiceClient(conn)

	const def = `syntax = "proto3"; message User { string name = 1; }`
	schemaName := "projects/ps-grpc-proj/schemas/user-schema"
	created, err := sc.CreateSchema(ctx, &pubsubpb.CreateSchemaRequest{
		Parent:   "projects/ps-grpc-proj",
		SchemaId: "user-schema",
		Schema:   &pubsubpb.Schema{Type: pubsubpb.Schema_PROTOCOL_BUFFER, Definition: def},
	})
	require.NoError(t, err, "CreateSchema")
	require.Equal(t, schemaName, created.GetName(), "CreateSchema must return the full resource name")
	require.NotEmpty(t, created.GetRevisionId(), "CreateSchema must assign a revision id")

	got, err := sc.GetSchema(ctx, &pubsubpb.GetSchemaRequest{Name: schemaName})
	require.NoError(t, err, "GetSchema")
	require.Equal(t, def, got.GetDefinition(), "GetSchema must round-trip the definition")

	_, err = sc.ValidateSchema(ctx, &pubsubpb.ValidateSchemaRequest{
		Parent: "projects/ps-grpc-proj",
		Schema: &pubsubpb.Schema{Type: pubsubpb.Schema_PROTOCOL_BUFFER, Definition: def},
	})
	require.NoError(t, err, "ValidateSchema")

	list, err := sc.ListSchemas(ctx, &pubsubpb.ListSchemasRequest{Parent: "projects/ps-grpc-proj"})
	require.NoError(t, err, "ListSchemas")
	var found bool
	for _, s := range list.GetSchemas() {
		if s.GetName() == schemaName {
			found = true
		}
	}
	require.True(t, found, "ListSchemas must include the created schema")

	_, err = sc.DeleteSchema(ctx, &pubsubpb.DeleteSchemaRequest{Name: schemaName})
	require.NoError(t, err, "DeleteSchema")
}

func psPullN(t *testing.T, sc pubsubpb.SubscriberClient, sub string, max int) []*pubsubpb.ReceivedMessage {
	t.Helper()
	resp, err := sc.Pull(ctx, &pubsubpb.PullRequest{Subscription: sub, MaxMessages: int32(max), ReturnImmediately: true})
	require.NoError(t, err, "Pull")
	return resp.GetReceivedMessages()
}

// psPullAll receives count messages over a StreamingPull whose flow control
// admits exactly count outstanding, so the service delivers them as they become
// available and no more. The messages stay leased for the stream's deadline,
// to be acked or nacked by ackId.
func psPullAll(t *testing.T, sc pubsubpb.SubscriberClient, sub string, count int, timeout time.Duration) []*pubsubpb.ReceivedMessage {
	t.Helper()
	streamCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stream, err := sc.StreamingPull(streamCtx)
	require.NoError(t, err, "StreamingPull")
	require.NoError(t, stream.Send(&pubsubpb.StreamingPullRequest{
		Subscription:             sub,
		StreamAckDeadlineSeconds: 60,
		MaxOutstandingMessages:   int64(count),
	}))
	var out []*pubsubpb.ReceivedMessage
	for len(out) < count {
		resp, err := stream.Recv()
		require.NoError(t, err, "StreamingPull on %s delivered %d of %d messages", sub, len(out), count)
		out = append(out, resp.GetReceivedMessages()...)
	}
	return out
}

// TestPubSubGRPC_ListRefusesAMalformedPage: a page token the service did not
// issue, or a negative page size, is INVALID_ARGUMENT, not a silent restart.
func TestPubSubGRPC_ListRefusesAMalformedPage(t *testing.T) {
	t.Parallel()
	pub, _ := psRawClient(t)
	project := "projects/" + uniquename.New("ps-grpc-paging")
	ensureProject(t, strings.TrimPrefix(project, "projects/"))
	for _, id := range []string{"a", "b", "c"} {
		_, err := pub.CreateTopic(ctx, &pubsubpb.Topic{Name: project + "/topics/" + id})
		require.NoError(t, err)
	}
	first, err := pub.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: project, PageSize: 2})
	require.NoError(t, err)
	require.Len(t, first.GetTopics(), 2)
	rest, err := pub.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: project, PageSize: 2, PageToken: first.GetNextPageToken()})
	require.NoError(t, err)
	require.Len(t, rest.GetTopics(), 1)

	_, err = pub.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: project, PageToken: "not-a-token"})
	requireGRPCCode(t, err, codes.InvalidArgument)
	_, err = pub.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: project, PageToken: "99"})
	requireGRPCCode(t, err, codes.InvalidArgument)
	_, err = pub.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: project, PageSize: -1})
	requireGRPCCode(t, err, codes.InvalidArgument)
}
