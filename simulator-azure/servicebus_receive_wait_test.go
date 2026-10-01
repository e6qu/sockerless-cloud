package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

func sbRESTReceive(method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handleSBRESTDataPlane(rec, httptest.NewRequest(method, target, nil), "ns")
	return rec
}

func sbRESTSend(t *testing.T, target, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	handleSBRESTDataPlane(rec, httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)), "ns")
	if rec.Code != http.StatusCreated {
		t.Fatalf("send = %d %s", rec.Code, rec.Body)
	}
}

func TestServiceBusRESTReceiveWaitsForASend(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", nil)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- sbRESTReceive(http.MethodDelete, "/q/messages/head?timeout=60") }()
	sbRESTSend(t, "/q/messages", "late")
	rec := <-done
	if rec.Code != http.StatusOK || rec.Body.String() != "late" {
		t.Fatalf("receive = %d %q, want 200 \"late\"", rec.Code, rec.Body)
	}
}

func TestServiceBusRESTPeekLockWaitsForTheLockToExpire(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", map[string]any{"lockDuration": "PT1S"})
	sbRESTSend(t, "/q/messages", "held")
	first := sbRESTReceive(http.MethodPost, "/q/messages/head?timeout=0")
	if first.Code != http.StatusCreated || first.Body.String() != "held" {
		t.Fatalf("first peek-lock = %d %q", first.Code, first.Body)
	}
	locked := time.Now()
	again := sbRESTReceive(http.MethodPost, "/q/messages/head?timeout=60")
	if again.Code != http.StatusCreated || again.Body.String() != "held" ||
		!strings.Contains(again.Header().Get("BrokerProperties"), `"DeliveryCount":2`) {
		t.Fatalf("peek-lock after the lock expired = %d %q %s", again.Code, again.Body, again.Header().Get("BrokerProperties"))
	}
	if waited := time.Since(locked); waited < 900*time.Millisecond {
		t.Fatalf("peek-lock returned the message after %v, before its 1s lock ran out", waited)
	}
}

func TestServiceBusRESTPeekLockWaitsForARenewedLockToExpire(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", map[string]any{"lockDuration": "PT1S"})
	sbRESTSend(t, "/q/messages", "renewed")
	first := sbRESTReceive(http.MethodPost, "/q/messages/head?timeout=0")
	location := first.Header().Get("Location")
	if first.Code != http.StatusCreated || location == "" || first.Body.String() != "renewed" {
		t.Fatalf("first peek-lock = %d %q", first.Code, first.Body)
	}
	renewed := time.Now()
	if renew := sbRESTReceive(http.MethodPost, location[strings.Index(location, "/q/"):]); renew.Code != http.StatusOK || renew.Body.Len() != 0 {
		t.Fatalf("renew-lock = %d %q, want 200 with no body", renew.Code, renew.Body)
	}
	again := sbRESTReceive(http.MethodPost, "/q/messages/head?timeout=60")
	if again.Code != http.StatusCreated || again.Body.String() != "renewed" {
		t.Fatalf("peek-lock after the renewed lock expired = %d %q", again.Code, again.Body)
	}
	if waited := time.Since(renewed); waited < 900*time.Millisecond {
		t.Fatalf("peek-lock returned the message after %v, before its renewed lock ran out", waited)
	}
}

func TestServiceBusRESTReceiveWaitsForAScheduledMessage(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", nil)
	sbEnqueue("ns", "q", sbOutgoing{payload: sbPayload{Body: []byte("later")}, messageID: "m", delay: time.Second}, time.Now())
	rec := sbRESTReceive(http.MethodDelete, "/q/messages/head?timeout=60")
	if rec.Code != http.StatusOK || rec.Body.String() != "later" {
		t.Fatalf("receive = %d %q, want the scheduled message", rec.Code, rec.Body)
	}
}

// A restarted simulator loads scheduled messages without the timers that
// would have announced them.
func TestServiceBusRESTReceiveWaitsForAScheduledMessageAfterARestart(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", nil)
	now := time.Now()
	var rec sbQueueRecord
	rec.Queue.Enqueue(sbPayload{Body: []byte("stored")}, msgq.EnqueueOpts{ID: "m", Delay: time.Second}, sbSettings("ns", "q").policy(), now)
	sbQueueDurable.Put(sbQueueKey("ns", "q"), rec)
	sbRearmAvailability(now)
	got := sbRESTReceive(http.MethodDelete, "/q/messages/head?timeout=60")
	if got.Code != http.StatusOK || got.Body.String() != "stored" {
		t.Fatalf("receive = %d %q, want the stored scheduled message", got.Code, got.Body)
	}
}

func TestServiceBusRESTReceiveAnswers204AfterTheTimeout(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", nil)
	start := time.Now()
	rec := sbRESTReceive(http.MethodDelete, "/q/messages/head?timeout=1")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("receive on an empty queue = %d %q, want 204 with no body", rec.Code, rec.Body)
	}
	if waited := time.Since(start); waited < time.Second {
		t.Fatalf("receive answered 204 after %v, before its 1s timeout", waited)
	}
	if rec := sbRESTReceive(http.MethodDelete, "/q/messages/head?timeout=0"); rec.Code != http.StatusNoContent {
		t.Fatalf("receive with timeout=0 = %d, want 204", rec.Code)
	}
	if rec := sbRESTReceive(http.MethodDelete, "/q/messages/head?timeout=soon"); rec.Code != http.StatusBadRequest {
		t.Fatalf("receive with timeout=soon = %d, want 400", rec.Code)
	}
}

func TestServiceBusRESTReceiveEndsWithItsCaller(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := sbAwaitReceive(ctx, "ns", "q", time.Hour, false); got != nil {
		t.Fatalf("receive for a gone caller = %v", got)
	}
}
