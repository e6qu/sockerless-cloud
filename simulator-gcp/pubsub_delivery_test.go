package main

import (
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func psTestStores(t *testing.T) {
	t.Helper()
	topics, subs, queues, snaps, backlogs := psTopics, psSubscriptions, psQueues, psSnapshots, psSnapshotBacklogs
	t.Cleanup(func() {
		psTopics, psSubscriptions, psQueues, psSnapshots, psSnapshotBacklogs = topics, subs, queues, snaps, backlogs
	})
	psTopics = sim.MakeStore[PSTopic](nil, "test_ps_topics")
	psSubscriptions = sim.MakeStore[PSSubscription](nil, "test_ps_subscriptions")
	psQueues = sim.MakeStore[psQueue](nil, "test_ps_queues")
	psSnapshots = sim.MakeStore[PSSnapshot](nil, "test_ps_snapshots")
	psSnapshotBacklogs = sim.MakeStore[[]PSMessage](nil, "test_ps_snapshot_backlogs")
}

func psTestSubscription(t *testing.T, s PSSubscription) {
	t.Helper()
	if s.AckDeadlineSeconds == 0 {
		s.AckDeadlineSeconds = 10
	}
	if err := psValidateSubscription(s); err != nil {
		t.Fatalf("subscription %s: %v", s.Name, err)
	}
	psSubscriptions.Put(s.Name, s)
}

func psData(ds []psDelivered) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Message.Data)
	}
	return out
}

func psEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPubSubFilterGrammar(t *testing.T) {
	attrs := map[string]string{"domain": "com", "iana.org/language_tag": "en", "region": "us-east1"}
	cases := map[string]bool{
		``:                                                         true,
		`attributes:domain`:                                        true,
		`attributes:missing`:                                       false,
		`NOT attributes:missing`:                                   true,
		`-attributes:domain`:                                       false,
		`attributes.domain = "com"`:                                true,
		`attributes.domain != "com"`:                               false,
		`attributes.missing != "com"`:                              true,
		`attributes."iana.org/language_tag" = "en"`:                true,
		`hasPrefix(attributes.region, "us-")`:                      true,
		`hasPrefix(attributes.missing, "us-")`:                     false,
		`attributes:domain AND attributes.region = "eu"`:           false,
		`attributes:domain OR attributes.region = "eu"`:            true,
		`(attributes:domain OR attributes:x) AND NOT attributes:y`: true,
	}
	for filter, want := range cases {
		n, err := psParseFilter(filter)
		if err != nil {
			t.Fatalf("%q: %v", filter, err)
		}
		if got := n.Eval(psFilterDoc(attrs)); got != want {
			t.Errorf("%q = %v, want %v", filter, got, want)
		}
	}
	for _, bad := range []string{
		`attributes:a AND attributes:b OR attributes:c`,
		`attributes.a = unquoted`,
		`labels.a = "x"`,
		`hasPrefix(attributes.a "x")`,
		`(attributes:a`,
	} {
		if _, err := psParseFilter(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestPubSubFilterAcknowledgesRejectedMessagesOnArrival(t *testing.T) {
	psTestStores(t)
	psTopics.Put("projects/p/topics/t", PSTopic{Name: "projects/p/topics/t"})
	psTestSubscription(t, PSSubscription{Name: "projects/p/subscriptions/s", Topic: "projects/p/topics/t", Filter: `attributes.kind = "news"`})
	if _, err := psPublishMessages("projects/p/topics/t", []PSMessage{
		{Data: "sport", Attributes: map[string]string{"kind": "sport"}},
		{Data: "news", Attributes: map[string]string{"kind": "news"}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := psDequeue("projects/p/subscriptions/s", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	psEqual(t, psData(got), []string{"news"})
}

func TestPubSubDeadLetterPolicyForwardsAfterMaxDeliveryAttempts(t *testing.T) {
	psTestStores(t)
	psTopics.Put("projects/p/topics/t", PSTopic{Name: "projects/p/topics/t"})
	psTopics.Put("projects/p/topics/dlt", PSTopic{Name: "projects/p/topics/dlt"})
	psTestSubscription(t, PSSubscription{Name: "projects/p/subscriptions/dl", Topic: "projects/p/topics/dlt"})
	psTestSubscription(t, PSSubscription{
		Name: "projects/p/subscriptions/s", Topic: "projects/p/topics/t",
		DeadLetterPolicy: &PSDeadLetterPolicy{DeadLetterTopic: "projects/p/topics/dlt", MaxDeliveryAttempts: 5},
	})
	if _, err := psPublishMessages("projects/p/topics/t", []PSMessage{{Data: "poison", Attributes: map[string]string{"a": "b"}}}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 5; attempt++ {
		got, err := psDequeue("projects/p/subscriptions/s", 1, 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("attempt %d: %v %v", attempt, got, err)
		}
		if got[0].DeliveryAttempt != attempt {
			t.Fatalf("attempt %d reported deliveryAttempt %d", attempt, got[0].DeliveryAttempt)
		}
		psModifyAckDeadline("projects/p/subscriptions/s", []string{got[0].AckID}, 0)
	}
	if got, _ := psDequeue("projects/p/subscriptions/s", 1, 0); len(got) != 0 {
		t.Fatalf("a sixth delivery attempt was made: %v", got)
	}
	dead, err := psDequeue("projects/p/subscriptions/dl", 10, 0)
	if err != nil || len(dead) != 1 {
		t.Fatalf("dead-letter subscription = %v %v", dead, err)
	}
	attrs := dead[0].Message.Attributes
	if dead[0].Message.Data != "poison" || attrs["a"] != "b" ||
		attrs["CloudPubSubDeadLetterSourceDeliveryCount"] != "5" ||
		attrs["CloudPubSubDeadLetterSourceSubscription"] != "s" ||
		attrs["CloudPubSubDeadLetterSourceSubscriptionProject"] != "p" ||
		attrs["CloudPubSubDeadLetterSourceTopicPublishTime"] == "" {
		t.Fatalf("forwarded message = %+v", dead[0].Message)
	}
	if dead[0].DeliveryAttempt != 0 {
		t.Fatal("deliveryAttempt set on a subscription without a dead-letter policy")
	}
}

func TestPubSubDeadLetterSweeperForwardsExhaustedMessages(t *testing.T) {
	psTestStores(t)
	psTopics.Put("projects/p/topics/t", PSTopic{Name: "projects/p/topics/t"})
	psTopics.Put("projects/p/topics/dlt", PSTopic{Name: "projects/p/topics/dlt"})
	psTestSubscription(t, PSSubscription{Name: "projects/p/subscriptions/dl", Topic: "projects/p/topics/dlt"})
	sub := PSSubscription{
		Name: "projects/p/subscriptions/s", Topic: "projects/p/topics/t",
		DeadLetterPolicy: &PSDeadLetterPolicy{DeadLetterTopic: "projects/p/topics/dlt", MaxDeliveryAttempts: 5},
	}
	psTestSubscription(t, sub)
	if _, err := psPublishMessages("projects/p/topics/t", []PSMessage{{Data: "m"}}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		got := must(psDequeue(sub.Name, 1, 0))
		psModifyAckDeadline(sub.Name, []string{got[0].AckID}, 0)
	}
	psSweepDeadLetters(time.Now())
	psEqual(t, psData(must(psDequeue("projects/p/subscriptions/dl", 10, 0))), []string{"m"})
}

func TestPubSubOrderingKeyDeliversInOrder(t *testing.T) {
	psTestStores(t)
	psTopics.Put("projects/p/topics/t", PSTopic{Name: "projects/p/topics/t"})
	psTestSubscription(t, PSSubscription{Name: "projects/p/subscriptions/s", Topic: "projects/p/topics/t", EnableMessageOrdering: true})
	if _, err := psPublishMessages("projects/p/topics/t", []PSMessage{
		{Data: "k1-a", OrderingKey: "k1"},
		{Data: "k2-a", OrderingKey: "k2"},
		{Data: "k1-b", OrderingKey: "k1"},
		{Data: "free"},
	}); err != nil {
		t.Fatal(err)
	}
	first, _ := psDequeue("projects/p/subscriptions/s", 1, 0)
	psEqual(t, psData(first), []string{"k1-a"})
	// k1-b waits behind the outstanding k1-a; the other key and the
	// unordered message flow.
	psEqual(t, psData(must(psDequeue("projects/p/subscriptions/s", 10, 0))), []string{"k2-a", "free"})
	psModifyAckDeadline("projects/p/subscriptions/s", []string{first[0].AckID}, 0)
	// A nacked message is redelivered before the rest of its key.
	psEqual(t, psData(must(psDequeue("projects/p/subscriptions/s", 10, 0))), []string{"k1-a", "k1-b"})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestPubSubRetryPolicyBacksOffRedelivery(t *testing.T) {
	sub := PSSubscription{RetryPolicy: &PSRetryPolicy{MinimumBackoff: "10s", MaximumBackoff: "35s"}}
	pol := psPolicy(sub)
	for deliveries, want := range map[int]time.Duration{1: 10 * time.Second, 2: 20 * time.Second, 3: 35 * time.Second, 9: 35 * time.Second} {
		if got := pol.Backoff(deliveries); got != want {
			t.Errorf("backoff after %d deliveries = %v, want %v", deliveries, got, want)
		}
	}
	psTestStores(t)
	psTopics.Put("projects/p/topics/t", PSTopic{Name: "projects/p/topics/t"})
	sub.Name, sub.Topic = "projects/p/subscriptions/s", "projects/p/topics/t"
	psTestSubscription(t, sub)
	if _, err := psPublishMessages("projects/p/topics/t", []PSMessage{{Data: "m"}}); err != nil {
		t.Fatal(err)
	}
	got := must(psDequeue(sub.Name, 1, 0))
	psModifyAckDeadline(sub.Name, []string{got[0].AckID}, 0)
	if again := must(psDequeue(sub.Name, 1, 0)); len(again) != 0 {
		t.Fatalf("a nacked message was redelivered inside its retry backoff: %v", again)
	}
}

func TestPubSubValidatesDeliverySettings(t *testing.T) {
	psTestStores(t)
	for _, s := range []PSSubscription{
		{Filter: `attributes.a = `},
		{DeadLetterPolicy: &PSDeadLetterPolicy{DeadLetterTopic: "projects/p/topics/missing"}},
		{DeadLetterPolicy: &PSDeadLetterPolicy{MaxDeliveryAttempts: 4}},
		{RetryPolicy: &PSRetryPolicy{MinimumBackoff: "700s"}},
		{RetryPolicy: &PSRetryPolicy{MinimumBackoff: "60s", MaximumBackoff: "30s"}},
	} {
		if err := psValidateSubscription(s); err == nil {
			t.Errorf("%+v validated", s)
		}
	}
}

func TestPubSubPullOnADetachedSubscriptionFails(t *testing.T) {
	psTestStores(t)
	psSubscriptions.Put("projects/p/subscriptions/s", PSSubscription{Name: "projects/p/subscriptions/s", Detached: true})
	_, err := psDequeue("projects/p/subscriptions/s", 1, 0)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("pull on a detached subscription = %v", err)
	}
}

func TestPubSubSeekToTimeAcknowledgesEarlierMessages(t *testing.T) {
	psTestStores(t)
	psTopics.Put("projects/p/topics/t", PSTopic{Name: "projects/p/topics/t"})
	psTestSubscription(t, PSSubscription{Name: "projects/p/subscriptions/s", Topic: "projects/p/topics/t"})
	if _, err := psPublishMessages("projects/p/topics/t", []PSMessage{{Data: "old"}}); err != nil {
		t.Fatal(err)
	}
	psSeekTime("projects/p/subscriptions/s", time.Now().Add(time.Second))
	if got := must(psDequeue("projects/p/subscriptions/s", 10, 0)); len(got) != 0 {
		t.Fatalf("seek past a message left it unacknowledged: %v", got)
	}
}
