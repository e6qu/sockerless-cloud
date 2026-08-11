package gcp_sdk_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/pubsub"
	pubsubpb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// newPSGRPCClient builds the high-level cloud.google.com/go/pubsub client over
// gRPC (its only transport), pointed at the simulator's Pub/Sub gRPC data plane
// via PUBSUB_EMULATOR_HOST — the same coordinate the client uses to reach
// Google's own Pub/Sub emulator.
func newPSGRPCClient(t *testing.T, project string) *pubsub.Client {
	t.Helper()
	t.Setenv("PUBSUB_EMULATOR_HOST", grpcAddr)
	c, err := pubsub.NewClient(ctx, project,
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithTelemetryDisabled(),
	)
	if err != nil {
		t.Fatalf("pubsub.NewClient (gRPC): %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// psRawClient dials the simulator directly and returns raw Publisher/Subscriber
// gRPC clients. This is how the precise Pull / Acknowledge / ModifyAckDeadline
// contract is exercised without the high-level client's automatic modack flow.
func psRawClient(t *testing.T) (pubsubpb.PublisherClient, pubsubpb.SubscriberClient) {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return pubsubpb.NewPublisherClient(conn), pubsubpb.NewSubscriberClient(conn)
}

// psRawSubscriber returns just the raw Subscriber client for tests that only
// exercise the data-plane read side.
func psRawSubscriber(t *testing.T) pubsubpb.SubscriberClient {
	t.Helper()
	_, sc := psRawClient(t)
	return sc
}

// TestPubSub_GRPC_TopicAndSubscriptionCRUD exercises the full topic + subscription
// CRUD round-trip over gRPC via the high-level client.
func TestPubSub_GRPC_TopicAndSubscriptionCRUD(t *testing.T) {
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

	// Get with config carries the stored labels back.
	cfg, err := topic.Config(ctx)
	require.NoError(t, err, "topic.Config (GetTopic)")
	require.Equal(t, topicID, topic.ID(), "topic.ID must match")
	require.Equal(t, "test", cfg.Labels["env"], "GetTopic must round-trip labels")

	// ListTopics includes the new topic.
	it := c.Topics(ctx)
	var names []string
	for {
		tt, err := it.Next()
		if err != nil {
			break
		}
		names = append(names, tt.ID())
	}
	require.Contains(t, names, topicID, "ListTopics must include created topic")

	// CreateSubscription + Get + List + Delete.
	sub := c.Subscription("crud-sub")
	_, err = c.CreateSubscription(ctx, "crud-sub", pubsub.SubscriptionConfig{Topic: topic})
	require.NoError(t, err, "CreateSubscription")
	exists, err = sub.Exists(ctx)
	require.NoError(t, err)
	require.True(t, exists, "subscription should exist after create")

	subCfg, err := sub.Config(ctx)
	require.NoError(t, err, "subscription.Config (GetSubscription)")
	require.Equal(t, "crud-sub", sub.ID())
	// The default 10s ack deadline + 7d retention are applied by the sim.
	require.Equal(t, 10*time.Second, subCfg.AckDeadline, "default ack deadline must be honoured")
	require.NotZero(t, subCfg.RetentionDuration, "default message retention must be populated")

	subs := c.Subscriptions(ctx)
	var subNames []string
	for {
		ss, err := subs.Next()
		if err != nil {
			break
		}
		subNames = append(subNames, ss.ID())
	}
	require.Contains(t, subNames, "crud-sub", "ListSubscriptions must include created subscription")

	require.NoError(t, sub.Delete(ctx), "DeleteSubscription")
	require.NoError(t, topic.Delete(ctx), "DeleteTopic")
}

// TestPubSub_GRPC_PublishPull exercises the data-plane write then read: publish a
// batch with data + attributes, raw Pull the messages back, assert fidelity, then
// Acknowledge and confirm the queue is drained.
func TestPubSub_GRPC_PublishPull(t *testing.T) {
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

	// Pull returns all published messages (loop until the batch is drained).
	got := psPullAll(t, sc, subName, len(want), 10*time.Second)
	require.Len(t, got, len(want), "must receive every published message")

	// Assert data + attributes survive the proto ↔ REST-store round-trip.
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
	sort.Strings(gotIds)
	require.ElementsMatch(t, pres.GetMessageIds(), gotIds, "assigned message ids must be returned")

	// Acknowledge the pulled messages.
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

// TestPubSub_GRPC_AckDeadlineRedelivery proves at-least-once delivery: a pulled
// message that is NOT acknowledged is returned to the queue by the ack-deadline
// sweeper and redelivered on the next pull.
func TestPubSub_GRPC_AckDeadlineRedelivery(t *testing.T) {
	c := newPSGRPCClient(t, "ps-grpc-proj")
	_, sc := psRawClient(t)

	topic := c.Topic("rd-topic")
	_, err := c.CreateTopic(ctx, topic.ID())
	require.NoError(t, err)
	defer topic.Delete(ctx)

	// Short ack deadline so the sweeper requeues promptly.
	subName := "projects/ps-grpc-proj/subscriptions/rd-sub"
	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: subName, Topic: topic.String(), AckDeadlineSeconds: 2,
	})
	require.NoError(t, err)
	defer sc.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})

	_, err = topic.Publish(ctx, &pubsub.Message{Data: []byte("will-redeliver")}).Get(ctx)
	require.NoError(t, err, "Publish")

	// Pull the message and deliberately do NOT ack it.
	first := psPullN(t, sc, subName, 1)
	require.Len(t, first, 1, "first pull must return the published message")
	require.Equal(t, "will-redeliver", string(first[0].GetMessage().GetData()))

	// The sweeper runs every 1s; the deadline is 2s. Poll-until the message is
	// redelivered with a fresh ackId.
	var redelivered *pubsubpb.ReceivedMessage
	require.Eventually(t, func() bool {
		got := psPullN(t, sc, subName, 1)
		if len(got) == 0 {
			return false
		}
		redelivered = got[0]
		return true
	}, 15*time.Second, 500*time.Millisecond, "unacked message must be redelivered after ack deadline")
	require.Equal(t, "will-redeliver", string(redelivered.GetMessage().GetData()), "redelivered payload must match")
	require.NotEqual(t, first[0].GetAckId(), redelivered.GetAckId(), "redelivery must carry a fresh ackId")
}

// TestPubSub_GRPC_ModifyAckDeadline proves that extending an inflight message's
// deadline keeps it from being redelivered before the extended deadline elapses.
func TestPubSub_GRPC_ModifyAckDeadline(t *testing.T) {
	c := newPSGRPCClient(t, "ps-grpc-proj")
	sc := psRawSubscriber(t)

	topic := c.Topic("mod-topic")
	_, err := c.CreateTopic(ctx, topic.ID())
	require.NoError(t, err)
	defer topic.Delete(ctx)

	subName := "projects/ps-grpc-proj/subscriptions/mod-sub"
	_, err = sc.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name: subName, Topic: topic.String(), AckDeadlineSeconds: 2,
	})
	require.NoError(t, err)
	defer sc.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})

	_, err = topic.Publish(ctx, &pubsub.Message{Data: []byte("mod-test")}).Get(ctx)
	require.NoError(t, err)

	pulled := psPullN(t, sc, subName, 1)
	require.Len(t, pulled, 1, "pull must return the published message")

	// Extend the deadline by 30s immediately.
	_, err = sc.ModifyAckDeadline(ctx, &pubsubpb.ModifyAckDeadlineRequest{
		Subscription: subName, AckIds: []string{pulled[0].GetAckId()}, AckDeadlineSeconds: 30,
	})
	require.NoError(t, err, "ModifyAckDeadline")

	// Within the original short deadline window, the message must NOT be
	// redelivered because the deadline was extended.
	time.Sleep(4 * time.Second)
	require.Empty(t, psPullN(t, sc, subName, 1), "extended message must not be redelivered before the new deadline")
}

// TestPubSub_GRPC_StreamingPull drives the high-level client's Receive path
// (StreamingPull) and asserts it receives the published messages, then acks them
// via the handler so they are not redelivered.
func TestPubSub_GRPC_StreamingPull(t *testing.T) {
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

	// Receive dispatches each message to the handler on its own goroutine. Record
	// the payloads and ack as each arrives.
	received := struct {
		sync.Mutex
		m map[string]bool
	}{m: map[string]bool{}}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = sub.Receive(rctx, func(_ context.Context, m *pubsub.Message) {
			received.Lock()
			received.m[string(m.Data)] = true
			received.Unlock()
			m.Ack()
		})
	}()

	require.Eventually(t, func() bool {
		received.Lock()
		defer received.Unlock()
		if len(received.m) != len(want) {
			return false
		}
		for _, w := range want {
			if !received.m[w] {
				return false
			}
		}
		return true
	}, 15*time.Second, 200*time.Millisecond, "StreamingPull must deliver every published message")

	cancel()
}

// TestPubSub_GRPC_SeekSnapshot publishes a batch, snapshots the backlog, pulls +
// acks everything, then seeks to the snapshot and confirms all messages are
// replayed.
func TestPubSub_GRPC_SeekSnapshot(t *testing.T) {
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

	// Snapshot the full backlog before any are acked.
	snapName := "projects/ps-grpc-proj/snapshots/sk-snap"
	_, err = sc.CreateSnapshot(ctx, &pubsubpb.CreateSnapshotRequest{
		Name: snapName, Subscription: subName,
	})
	require.NoError(t, err, "CreateSnapshot")
	defer sc.DeleteSnapshot(ctx, &pubsubpb.DeleteSnapshotRequest{Snapshot: snapName})

	// Pull + ack everything so the queue is drained.
	for _, rm := range psPullAll(t, sc, subName, len(want), 10*time.Second) {
		_, err := sc.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{Subscription: subName, AckIds: []string{rm.GetAckId()}})
		require.NoError(t, err, "Acknowledge before seek")
	}
	require.Empty(t, psPullN(t, sc, subName, 10), "queue must be empty after acking all")

	// Seek to the snapshot and confirm the backlog is replayed.
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

// TestPubSub_GRPC_Schema exercises SchemaService CRUD + ValidateSchema.
func TestPubSub_GRPC_Schema(t *testing.T) {
	newPSGRPCClient(t, "ps-grpc-proj") // ensures the emulator env is set + project wiring
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

// psPullN performs a single unary Pull and returns at most max received messages.
func psPullN(t *testing.T, sc pubsubpb.SubscriberClient, sub string, max int) []*pubsubpb.ReceivedMessage {
	t.Helper()
	resp, err := sc.Pull(ctx, &pubsubpb.PullRequest{Subscription: sub, MaxMessages: int32(max), ReturnImmediately: true})
	require.NoError(t, err, "Pull")
	return resp.GetReceivedMessages()
}

// psPullAll repeatedly pulls until count messages have been collected or the
// timeout elapses. Used because the sim may return a partial batch per Pull.
func psPullAll(t *testing.T, sc pubsubpb.SubscriberClient, sub string, count int, timeout time.Duration) []*pubsubpb.ReceivedMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var out []*pubsubpb.ReceivedMessage
	for time.Now().Before(deadline) && len(out) < count {
		got := psPullN(t, sc, sub, count-len(out))
		out = append(out, got...)
		if len(got) == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return out
}
