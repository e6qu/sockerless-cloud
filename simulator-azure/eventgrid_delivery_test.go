package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

type eventGridHook struct {
	server   *httptest.Server
	received chan []map[string]any
	headers  chan http.Header
	attempts atomic.Int32
}

// newEventGridHook answers the validation handshake with 200 and every
// notification with status.
func newEventGridHook(t *testing.T, status int) *eventGridHook {
	t.Helper()
	hook := &eventGridHook{received: make(chan []map[string]any, 16), headers: make(chan http.Header, 16)}
	hook.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("aeg-event-type") == "SubscriptionValidation" {
			w.WriteHeader(http.StatusOK)
			return
		}
		hook.attempts.Add(1)
		var events []map[string]any
		_ = json.Unmarshal(body, &events)
		hook.received <- events
		hook.headers <- r.Header.Clone()
		w.WriteHeader(status)
	}))
	t.Cleanup(hook.server.Close)
	return hook
}

func (h *eventGridHook) next(t *testing.T) ([]map[string]any, http.Header) {
	t.Helper()
	select {
	case events := <-h.received:
		return events, <-h.headers
	case <-time.After(10 * time.Second):
		t.Fatal("no delivery reached the webhook")
		return nil, nil
	}
}

// eventGridTopicWithSubscription creates a topic and one webhook
// subscription with the given extra properties, returning the topic.
func eventGridTopicWithSubscription(t *testing.T, srv *sim.Server, name, hookURL string, extra map[string]any) EventGridTopic {
	t.Helper()
	topicURL := "http://localhost:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/" + name + "?api-version=2021-12-01"
	data := serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, topicURL, `{"location":"eastus"}`), http.StatusCreated)
	var topic EventGridTopic
	if err := json.Unmarshal(data, &topic); err != nil {
		t.Fatal(err)
	}
	props := map[string]any{"destination": map[string]any{"endpointType": "WebHook", "properties": map[string]any{"endpointUrl": hookURL}}}
	for k, v := range extra {
		props[k] = v
	}
	body, _ := json.Marshal(map[string]any{"properties": props})
	subURL := "http://localhost:4568" + topic.ID + "/providers/Microsoft.EventGrid/eventSubscriptions/sub1?api-version=2021-12-01"
	serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, subURL, string(body)), http.StatusCreated)
	return topic
}

func eventGridPublish(t *testing.T, srv *sim.Server, topic EventGridTopic, events string) {
	t.Helper()
	endpoint, _ := topic.Properties["endpoint"].(string)
	req := eventGridTestRequest(http.MethodPost, endpoint+"?api-version=2018-01-01", events)
	req.Header.Set(eventGridKeyHeader, eventGridTestListKeys(t, srv, topic.ID)["key1"])
	serveEventGridTestRequest(t, srv, req, http.StatusOK)
}

func TestEventGridDeliversPerEventWithTopicHeadersAndFilter(t *testing.T) {
	srv := newEventGridTestServer(t)
	hook := newEventGridHook(t, http.StatusOK)
	topic := eventGridTopicWithSubscription(t, srv, "eg-filtered", hook.server.URL, map[string]any{
		"filter": map[string]any{"includedEventTypes": []string{"order.created"}, "subjectBeginsWith": "/Orders/"},
	})
	eventGridPublish(t, srv, topic, `[
		{"id":"a","eventType":"order.created","subject":"/orders/1","eventTime":"2026-06-02T00:00:00Z","data":{},"dataVersion":"2"},
		{"id":"b","eventType":"order.deleted","subject":"/orders/1","eventTime":"2026-06-02T00:00:00Z","data":{},"dataVersion":"2"},
		{"id":"c","eventType":"order.created","subject":"/users/1","eventTime":"2026-06-02T00:00:00Z","data":{},"dataVersion":"2"},
		{"id":"d","eventType":"ORDER.CREATED","subject":"/orders/2","eventTime":"2026-06-02T00:00:00Z","data":{},"dataVersion":"2"}]`)
	seen := map[string]bool{}
	for range 2 {
		events, header := hook.next(t)
		if len(events) != 1 {
			t.Fatalf("a delivery carried %d events, want one per request", len(events))
		}
		seen[events[0]["id"].(string)] = true
		if events[0]["topic"] != topic.ID || events[0]["metadataVersion"] != "1" {
			t.Errorf("event carries topic %v metadataVersion %v", events[0]["topic"], events[0]["metadataVersion"])
		}
		if header.Get("aeg-event-type") != "Notification" || header.Get("aeg-subscription-name") != "SUB1" ||
			header.Get("aeg-delivery-count") != "0" || header.Get("aeg-data-version") != "2" {
			t.Errorf("delivery headers = %v", header)
		}
	}
	if !seen["a"] || !seen["d"] {
		t.Fatalf("delivered %v, want a and d", seen)
	}
	select {
	case extra := <-hook.received:
		t.Fatalf("the filter let %v through", extra)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestEventGridBatchesUpToMaxEventsPerBatch(t *testing.T) {
	es := EventGridEventSubscription{Properties: map[string]any{"destination": map[string]any{
		"properties": map[string]any{"maxEventsPerBatch": float64(2), "preferredBatchSizeInKilobytes": float64(1)},
	}}}
	small := json.RawMessage(`{"id":"x"}`)
	big := json.RawMessage(`{"id":"` + strings.Repeat("y", 1100) + `"}`)
	batches := eventGridBatches(es, []json.RawMessage{small, small, small, big, small})
	var sizes []int
	for _, b := range batches {
		sizes = append(sizes, len(b))
	}
	if len(sizes) != 4 || sizes[0] != 2 || sizes[1] != 1 || sizes[2] != 1 || sizes[3] != 1 {
		t.Fatalf("batch sizes = %v, want [2 1 1 1]", sizes)
	}
}

func TestEventGridRetryScheduleAndStatusWaits(t *testing.T) {
	for retry, want := range map[int]time.Duration{1: 10 * time.Second, 2: 30 * time.Second, 4: 5 * time.Minute, 10: 12 * time.Hour, 20: 12 * time.Hour} {
		if got := eventGridRetrySchedule(retry); got != want {
			t.Errorf("retry %d waits %v, want %v", retry, got, want)
		}
	}
	if eventGridStatusWait(delivery.Outcome{Status: 404}) != 5*time.Minute ||
		eventGridStatusWait(delivery.Outcome{Status: 503}) != 30*time.Second ||
		eventGridStatusWait(delivery.Outcome{Status: 500}) != 0 {
		t.Fatal("status waits differ from the documented ones")
	}
}

// TestEventGridDeadLettersToBlobStorage proves both dead-letter paths: a 400
// is not retried when a dead-letter destination exists, and a retryable
// failure dead-letters once maxDeliveryAttempts is spent. Each lands as a blob
// under <TOPIC>/<SUBSCRIPTION>/ carrying why it was dead-lettered.
func TestEventGridDeadLettersToBlobStorage(t *testing.T) {
	srv := newEventGridTestServer(t)
	registerBlobDataPlane(srv)
	blobContainersData.Put(blobContainerKey("dlacct", "deadletters"), BlobContainerData{Account: "dlacct", Name: "deadletters"})
	deadLetter := map[string]any{
		"endpointType": "StorageBlob",
		"properties": map[string]any{
			"resourceId":        "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/dlacct",
			"blobContainerName": "deadletters",
		},
	}
	for _, c := range []struct {
		topic, reason string
		status        int
		attempts      int
	}{
		{"eg-rejects", "NonRetriableFailure", http.StatusBadRequest, 1},
		{"eg-fails", "MaxDeliveryAttemptsExceeded", http.StatusInternalServerError, 1},
	} {
		hook := newEventGridHook(t, c.status)
		topic := eventGridTopicWithSubscription(t, srv, c.topic, hook.server.URL, map[string]any{
			"deadLetterDestination": deadLetter,
			"retryPolicy":           map[string]any{"maxDeliveryAttempts": 1, "eventTimeToLiveInMinutes": 60},
		})
		eventGridPublish(t, srv, topic, `[{"id":"`+c.topic+`","eventType":"t","subject":"/s","eventTime":"2026-06-02T00:00:00Z","data":{"n":1},"dataVersion":"1"}]`)
		hook.next(t)
		// The attempt that answered and its dead-lettering run in one piece of
		// background work.
		bg.Await()
		var blob BlobObject
		for _, b := range blobsUnderPrefix("dlacct", "deadletters", strings.ToUpper(c.topic)+"/SUB1/") {
			blob = b
		}
		if blob.Name == "" {
			t.Fatalf("%s: no dead-letter blob was written", c.topic)
		}
		_, reader, err := blobOpen(blob)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(reader)
		_ = reader.Close()
		var events []map[string]any
		if err := json.Unmarshal(body, &events); err != nil || len(events) != 1 {
			t.Fatalf("%s: dead-letter blob %q", c.topic, body)
		}
		event := events[0]
		if event["id"] != c.topic || event["deadLetterReason"] != c.reason ||
			event["deliveryAttempts"] != float64(c.attempts) || event["lastHttpStatusCode"] != float64(c.status) {
			t.Errorf("%s: dead-lettered event = %v", c.topic, event)
		}
		if n := hook.attempts.Load(); n != int32(c.attempts) {
			t.Errorf("%s: webhook saw %d attempts, want %d", c.topic, n, c.attempts)
		}
	}
}
