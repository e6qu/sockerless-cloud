package main

import (
	"testing"
	"time"
)

func TestServiceBusScheduleAndCancelOnAQueue(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", nil)
	seqs := sbSchedule("ns", "q", []sbOutgoing{
		{payload: sbPayload{Body: []byte("later")}, delay: time.Hour},
		{payload: sbPayload{Body: []byte("also later")}, delay: time.Hour},
	})
	if len(seqs) != 2 || seqs[0] == seqs[1] {
		t.Fatalf("schedule-message returned %v, want two sequence numbers", seqs)
	}
	if n := sbScheduledCount("ns", "q"); n != 2 {
		t.Fatalf("scheduled count %d, want 2", n)
	}
	if got, _ := sbReceive("ns", "q", "", 10, false); len(got) != 0 {
		t.Fatalf("a message scheduled an hour out was received: %+v", got)
	}
	sbCancelScheduled("ns", "q", seqs[:1])
	if n := sbScheduledCount("ns", "q"); n != 1 {
		t.Fatalf("scheduled count after cancelling one: %d, want 1", n)
	}
	rec, _ := sbQueueDurable.Get(sbQueueKey("ns", "q"))
	if len(rec.Queue.Messages) != 1 || string(rec.Queue.Messages[0].Payload.Body) != "also later" {
		t.Fatalf("the queue holds %+v after the cancel", rec.Queue.Messages)
	}
}

func TestServiceBusScheduleAndCancelOnATopic(t *testing.T) {
	newServiceBusQueueTestStores(t)
	topicID := sbAdminTopicID("ns", "t")
	sbTopics.Put(topicID, SBTopic{ID: topicID, Name: "t"})
	for _, s := range []string{"a", "b"} {
		id := sbAdminSubscriptionID("ns", "t", s)
		sbSubscriptions.Put(id, SBSubscription{ID: id, Name: s})
	}
	seqs := sbSchedule("ns", "t", []sbOutgoing{{payload: sbPayload{Body: []byte("x")}, delay: time.Hour}})
	if len(seqs) != 1 {
		t.Fatalf("schedule-message returned %v", seqs)
	}
	if n := sbTopicScheduledCount("ns", "t", []string{"t/a", "t/b"}); n != 1 {
		t.Fatalf("topic scheduled count %d, want 1", n)
	}
	if a, b := sbScheduledCount("ns", "t/a"), sbScheduledCount("ns", "t/b"); a != 1 || b != 1 {
		t.Fatalf("subscriptions hold %d and %d scheduled messages, want 1 each", a, b)
	}
	sbCancelScheduled("ns", "t", seqs)
	if a, b := sbScheduledCount("ns", "t/a"), sbScheduledCount("ns", "t/b"); a != 0 || b != 0 {
		t.Fatalf("cancel left %d and %d scheduled messages", a, b)
	}
}

// A session-enabled queue hands a session's messages only to the receiver
// holding its lock; the next-session accept takes the earliest session whose
// lock is free.
func TestServiceBusSessionsLockAndState(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("sq", map[string]any{"requiresSession": true, "lockDuration": "PT30S"})
	if !sbSettings("ns", "sq").requiresSession {
		t.Fatal("requiresSession was not read from the queue")
	}
	for _, m := range []struct{ session, body string }{{"s1", "a"}, {"s2", "b"}, {"s1", "c"}} {
		sbSend("ns", "sq", sbOutgoing{sessionID: m.session, payload: sbPayload{Body: []byte(m.body)}})
	}

	first, until, ok, err := sbAcceptSession("ns", "sq", "", "owner-1")
	if err != nil || !ok || first != "s1" {
		t.Fatalf("next session = %q %v %v, want s1", first, ok, err)
	}
	if time.Until(until) < 25*time.Second {
		t.Fatalf("session locked until %v, want lockDuration from now", until)
	}
	second, _, ok, err := sbAcceptSession("ns", "sq", "", "owner-2")
	if err != nil || !ok || second != "s2" {
		t.Fatalf("second next session = %q %v %v, want s2 while s1 is locked", second, ok, err)
	}
	if _, _, _, err := sbAcceptSession("ns", "sq", "s1", "owner-2"); err != errSBSessionLocked {
		t.Fatalf("accepting a locked session: %v, want %v", err, errSBSessionLocked)
	}
	if _, _, ok, _ := sbAcceptSession("ns", "sq", "", "owner-3"); ok {
		t.Fatal("a third receiver accepted a session while both are locked")
	}

	got, _ := sbReceive("ns", "sq", first, 10, false)
	if len(got) != 2 || string(got[0].Payload.Body) != "a" || string(got[1].Payload.Body) != "c" {
		t.Fatalf("session s1 received %+v, want a then c", got)
	}

	sbSetSessionState("ns", "sq", "s1", []byte("checkpoint"))
	if state := string(sbSessionState("ns", "sq", "s1")); state != "checkpoint" {
		t.Fatalf("session state %q", state)
	}
	if _, err := sbRenewSessionLock("ns", "sq", "s1"); err != nil {
		t.Fatalf("renewing a held session lock: %v", err)
	}
	sbReleaseSession("ns", "sq", "s1", "owner-1")
	if sbSessionHeld("ns", "sq", "s1", "owner-1") {
		t.Fatal("a released session is still held")
	}
	if _, err := sbRenewSessionLock("ns", "sq", "s1"); err != errSBSessionLockLost {
		t.Fatalf("renewing a released session lock: %v, want %v", err, errSBSessionLockLost)
	}
	if again, _, ok, err := sbAcceptSession("ns", "sq", "s1", "owner-3"); err != nil || !ok || again != "s1" {
		t.Fatalf("accepting a released session by name = %q %v %v", again, ok, err)
	}
	if state := string(sbSessionState("ns", "sq", "s1")); state != "checkpoint" {
		t.Fatalf("session state after a new receiver took the lock: %q", state)
	}
}

func TestSBAMQPRefusesAReceiverOfTheWrongSessionKind(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("sq", map[string]any{"requiresSession": true})
	sbTestQueue("plain", nil)
	c, transport := sbFlowTestConn(t)

	detachError := func(source any) string {
		t.Helper()
		sbFlowTestSend(t, c, amqpDescAttach, []any{"r", uint32(0), true, uint8(1), uint8(0), source, encodeTarget("r")})
		for _, f := range sbFlowTestFrames(t, transport) {
			if f.desc == amqpDescDetach {
				e, _ := field(f.fields, 2).(amqpDescribed)
				fields, _ := e.value.([]any)
				return asString(field(fields, 0))
			}
		}
		return ""
	}
	if got := detachError(encodeSource("sq")); got != "amqp:not-allowed" {
		t.Fatalf("a sessionless receiver on a session queue was answered with %q", got)
	}
	sessionSource := amqpDescribed{code: amqpDescSource, value: []any{"plain", uint32(0), amqpSymbol("session-end"), nil, nil, nil, nil,
		map[any]any{amqpSymbol(sbAMQPSessionFilterName): amqpDescribed{code: sbAMQPSessionFilterCode, value: nil}}}}
	if session, ok := sbAMQPSessionFilter(sessionSource); !ok || session != "" {
		t.Fatalf("session filter read as %q %v", session, ok)
	}
	if got := detachError(sessionSource); got != "amqp:not-allowed" {
		t.Fatalf("a session receiver on a sessionless queue was answered with %q", got)
	}
}
