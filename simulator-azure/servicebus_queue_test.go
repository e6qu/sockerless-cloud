package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func newServiceBusQueueTestStores(t *testing.T) {
	t.Helper()
	newServiceBusMoveTestStores(t)
	saved := sbQueueDurable
	sbQueueDurable = sim.MakeStore[sbQueueRecord](nil, "test_sb_queue_messages")
	t.Cleanup(func() { sbQueueDurable = saved })
	id := sbNamespaceID("sub", "rg", "ns")
	sbNamespaces.Put(id, SBNamespace{ID: id, Name: "ns"})
}

func sbTestQueue(name string, props map[string]any) {
	id := sbAdminQueueID("ns", name)
	sbQueues.Put(id, SBQueue{ID: id, Name: name, Properties: props})
}

func TestServiceBusParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"PT1M":                       time.Minute,
		"PT30S":                      30 * time.Second,
		"PT0.5S":                     500 * time.Millisecond,
		"P1DT2H":                     26 * time.Hour,
		"P10675199DT2H48M5.4775807S": math.MaxInt64,
	} {
		if got, ok := sbParseDuration(in); !ok || got != want {
			t.Errorf("%s = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "P", "PT", "1M", "PT1X"} {
		if _, ok := sbParseDuration(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestServiceBusLockDurationAndDeliveryCount(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", map[string]any{"lockDuration": "PT5S", "maxDeliveryCount": float64(2)})
	sbSend("ns", "q", sbOutgoing{payload: sbPayload{Body: []byte("m")}})

	got, lock := sbReceive("ns", "q", "", 1, true)
	if lock != 5*time.Second || len(got) != 1 || got[0].Deliveries != 1 {
		t.Fatalf("first receive = %+v lock %v", got, lock)
	}
	if until := time.UnixMilli(got[0].AvailableAt); time.Until(until) > 5*time.Second+time.Millisecond || time.Until(until) < 4*time.Second {
		t.Fatalf("lock held until %v, want lockDuration from now", until)
	}
	if err := sbSettle("ns", "q", got[0].Receipt, sbSettlement{kind: sbAbandon}); err != nil {
		t.Fatal(err)
	}
	if err := sbSettle("ns", "q", got[0].Receipt, sbSettlement{kind: sbComplete}); err != errSBLockLost {
		t.Fatalf("completing an abandoned lock = %v, want lock lost", err)
	}
	second, _ := sbReceive("ns", "q", "", 1, true)
	if len(second) != 1 || second[0].Deliveries != 2 {
		t.Fatalf("second receive = %+v", second)
	}
	if err := sbSettle("ns", "q", second[0].Receipt, sbSettlement{kind: sbAbandon}); err != nil {
		t.Fatal(err)
	}
	if third, _ := sbReceive("ns", "q", "", 1, true); len(third) != 0 {
		t.Fatalf("a third delivery past maxDeliveryCount 2: %+v", third)
	}
	dead, _ := sbReceive("ns", sbDeadLetterPath("q"), "", 1, false)
	if len(dead) != 1 || dead[0].Payload.DeadLetterReason != "MaxDeliveryCountExceeded" || string(dead[0].Payload.Body) != "m" {
		t.Fatalf("dead-letter sub-queue = %+v", dead)
	}
}

func TestServiceBusDuplicateDetection(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", map[string]any{"requiresDuplicateDetection": true, "duplicateDetectionHistoryTimeWindow": "PT1M"})
	sbTestQueue("plain", nil)
	for range 2 {
		sbSend("ns", "q", sbOutgoing{messageID: "same", payload: sbPayload{Body: []byte("x")}})
		sbSend("ns", "plain", sbOutgoing{messageID: "same", payload: sbPayload{Body: []byte("x")}})
	}
	if total, _, _ := sbQueueCounts("ns", "q"); total != 1 {
		t.Fatalf("queue with duplicate detection holds %d messages", total)
	}
	if total, _, _ := sbQueueCounts("ns", "plain"); total != 2 {
		t.Fatalf("queue without duplicate detection holds %d messages", total)
	}
}

func TestServiceBusTopicDuplicateDetectionAndFanOut(t *testing.T) {
	newServiceBusQueueTestStores(t)
	topicID := sbAdminTopicID("ns", "t")
	sbTopics.Put(topicID, SBTopic{ID: topicID, Name: "t", Properties: map[string]any{"requiresDuplicateDetection": true}})
	for _, s := range []string{"a", "b"} {
		id := sbAdminSubscriptionID("ns", "t", s)
		sbSubscriptions.Put(id, SBSubscription{ID: id, Name: s})
	}
	if reached := sbSend("ns", "t", sbOutgoing{messageID: "m1"}); len(reached) != 2 {
		t.Fatalf("fan-out reached %v", reached)
	}
	if reached := sbSend("ns", "t", sbOutgoing{messageID: "m1"}); len(reached) != 0 {
		t.Fatalf("a duplicate reached %v", reached)
	}
}

func TestServiceBusExpiryDeadLettersWhenConfigured(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", map[string]any{"defaultMessageTimeToLive": "PT1S", "deadLetteringOnMessageExpiration": true})
	sbSend("ns", "q", sbOutgoing{messageID: "old"})
	sbQueueDurable.Update(sbQueueKey("ns", "q"), func(rec *sbQueueRecord) {
		rec.Queue.Messages[0].ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	})
	if got, _ := sbReceive("ns", "q", "", 1, true); len(got) != 0 {
		t.Fatalf("an expired message was delivered: %+v", got)
	}
	if _, _, dead := sbQueueCounts("ns", "q"); dead != 1 {
		t.Fatalf("dead-letter count = %d", dead)
	}
}

func TestServiceBusDeferAndReceiveBySequenceNumber(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbSend("ns", "q", sbOutgoing{messageID: "d"})
	got, _ := sbReceive("ns", "q", "", 1, true)
	if err := sbSettle("ns", "q", got[0].Receipt, sbSettlement{kind: sbDefer}); err != nil {
		t.Fatal(err)
	}
	if again, _ := sbReceive("ns", "q", "", 1, true); len(again) != 0 {
		t.Fatalf("a deferred message was received again: %+v", again)
	}
	deferred := sbReceiveDeferred("ns", "q", []uint64{got[0].Seq})
	if len(deferred) != 1 || deferred[0].ID != "d" {
		t.Fatalf("receive by sequence number = %+v", deferred)
	}
	if peeked := sbPeek("ns", "q", 0, 10); len(peeked) != 1 {
		t.Fatalf("peek = %+v", peeked)
	}
}

func TestServiceBusRESTPeekLockUnlockAndRenew(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", map[string]any{"lockDuration": "PT30S"})
	send := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/q/messages", strings.NewReader("hello"))
	req.Header.Set("BrokerProperties", `{"MessageId":"m-1","Label":"greeting"}`)
	handleSBRESTDataPlane(send, req, "ns")
	if send.Code != http.StatusCreated {
		t.Fatalf("send = %d %s", send.Code, send.Body)
	}

	lock := httptest.NewRecorder()
	handleSBRESTDataPlane(lock, httptest.NewRequest(http.MethodPost, "/q/messages/head", nil), "ns")
	if lock.Code != http.StatusCreated || lock.Body.String() != "hello" {
		t.Fatalf("peek-lock = %d %q", lock.Code, lock.Body)
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(lock.Header().Get("BrokerProperties")), &props); err != nil {
		t.Fatal(err)
	}
	if props["MessageId"] != "m-1" || props["Label"] != "greeting" || props["DeliveryCount"] != float64(1) || props["LockToken"] == nil {
		t.Fatalf("BrokerProperties = %v", props)
	}
	location := lock.Header().Get("Location")
	target := location[strings.Index(location, "/q/"):]

	renew := httptest.NewRecorder()
	handleSBRESTDataPlane(renew, httptest.NewRequest(http.MethodPost, target, nil), "ns")
	var renewed map[string]string
	if err := json.Unmarshal([]byte(renew.Header().Get("BrokerProperties")), &renewed); err != nil {
		t.Fatal(err)
	}
	if until, err := http.ParseTime(renewed["LockedUntilUtc"]); renew.Code != http.StatusOK || renew.Body.Len() != 0 || err != nil || !until.After(time.Now()) {
		t.Fatalf("renew-lock = %d %q, BrokerProperties %v", renew.Code, renew.Body, renewed)
	}
	unlock := httptest.NewRecorder()
	handleSBRESTDataPlane(unlock, httptest.NewRequest(http.MethodPut, target, nil), "ns")
	if unlock.Code != http.StatusOK || unlock.Body.Len() != 0 {
		t.Fatalf("unlock = %d %q", unlock.Code, unlock.Body)
	}
	gone := httptest.NewRecorder()
	handleSBRESTDataPlane(gone, httptest.NewRequest(http.MethodDelete, target, nil), "ns")
	if gone.Code != http.StatusGone {
		t.Fatalf("completing an unlocked message = %d, want 410", gone.Code)
	}
	again := httptest.NewRecorder()
	handleSBRESTDataPlane(again, httptest.NewRequest(http.MethodDelete, "/q/messages/head", nil), "ns")
	if again.Code != http.StatusOK || again.Body.String() != "hello" || !strings.Contains(again.Header().Get("BrokerProperties"), `"DeliveryCount":2`) {
		t.Fatalf("receive after unlock = %d %q %s", again.Code, again.Body, again.Header().Get("BrokerProperties"))
	}
}

func TestServiceBusAMQPDispositionMapping(t *testing.T) {
	cases := []struct {
		state any
		want  sbSettleKind
	}{
		{amqpDescribed{code: amqpDescAccepted, value: []any{}}, sbComplete},
		{amqpDescribed{code: amqpDescReleased, value: []any{}}, sbRelease},
		{amqpDescribed{code: amqpDescModified, value: []any{false, false}}, sbAbandon},
		{amqpDescribed{code: amqpDescModified, value: []any{false, true}}, sbDefer},
		{amqpDescribed{code: amqpDescRejected, value: []any{amqpDescribed{code: amqpDescError, value: []any{
			amqpSymbol("com.microsoft:dead-letter"), nil,
			map[any]any{amqpSymbol("DeadLetterReason"): "bad", amqpSymbol("DeadLetterErrorDescription"): "worse"},
		}}}}, sbDeadLetterIt},
	}
	for _, c := range cases {
		got, ok := sbSettlementFromState(c.state)
		if !ok || got.kind != c.want {
			t.Errorf("%+v = %+v %v, want %v", c.state, got, ok, c.want)
		}
		if c.want == sbDeadLetterIt && (got.deadLetterReason != "bad" || got.deadLetterErrorDescription != "worse") {
			t.Errorf("rejected outcome lost its reason: %+v", got)
		}
	}
	token := "00112233-4455-6677-8899-aabbccddeeff"
	tag := sbLockTokenTag(token)
	if len(tag) != 16 || tag[0] != 0x33 || tag[3] != 0x00 || tag[4] != 0x55 || tag[6] != 0x77 || tag[8] != 0x88 {
		t.Fatalf("delivery tag %x is not the .NET byte order", tag)
	}
	if id, ok := sbLockTokenUUID(token); !ok || sbLockTokenString(id) != token {
		t.Fatal("lock token does not round-trip")
	}
}

// A topic keeps a message only in its subscriptions: with none, the message
// is accepted and kept nowhere, and a topic's size is the size of what its
// subscriptions hold.
func TestServiceBusTopicKeepsMessagesOnlyInSubscriptions(t *testing.T) {
	newServiceBusQueueTestStores(t)
	topicID := sbAdminTopicID("ns", "t")
	topic := SBTopic{ID: topicID, Name: "t"}
	sbTopics.Put(topicID, topic)
	if reached := sbSend("ns", "t", sbOutgoing{payload: sbPayload{Body: []byte("nobody")}}); len(reached) != 0 {
		t.Fatalf("a topic without subscriptions kept the message in %v", reached)
	}
	if total, _, _ := sbQueueCounts("ns", "t"); total != 0 {
		t.Fatalf("the topic's own path holds %d messages", total)
	}
	for _, s := range []string{"a", "b"} {
		id := sbAdminSubscriptionID("ns", "t", s)
		sbSubscriptions.Put(id, SBSubscription{ID: id, Name: s})
	}
	sbSend("ns", "t", sbOutgoing{payload: sbPayload{Body: []byte("12345")}})
	desc := sbAdminTopicDescriptionFor("ns", "t", topic)
	if *desc.SubscriptionCount != 2 || *desc.SizeInBytes != 10 {
		t.Fatalf("topic description: %d subscriptions, %d bytes; want 2 and 10", *desc.SubscriptionCount, *desc.SizeInBytes)
	}
	if got := sbQueueBytes("ns", "t/a"); got != 5 {
		t.Fatalf("subscription a holds %d bytes, want 5", got)
	}
}
