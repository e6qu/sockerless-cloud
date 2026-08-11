package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// GetLogEvents returns a bounded page anchored at the end of the stream the
// caller asked for. With startFromHead unset — the documented default, and what
// every CLI read that does not pass it sends — that end is the tail, so an
// unbounded page is not a harmless superset: a reader of a busy stream is shown
// the beginning of history where the service would have shown them the latest
// lines, and concludes the workload is producing nothing.
func TestGetLogEventsReturnsABoundedPageFromTheRequestedEnd(t *testing.T) {
	_, jsonRouter, _ := buildConformanceSimulator(t)
	handler, ok := jsonRouter.Handler("Logs_20140328.GetLogEvents")
	if !ok {
		t.Fatal("GetLogEvents is not served")
	}

	base := time.Now().Add(-time.Hour).UnixMilli()
	events := make([]CWLogEvent, 0, cwGetLogEventsMaxEvents+500)
	for i := range cap(events) {
		events = append(events, CWLogEvent{
			Timestamp:     base + int64(i)*10,
			Message:       fmt.Sprintf("line-%05d", i),
			IngestionTime: base + int64(i)*10,
		})
	}
	cwLogGroups.Put("g", CWLogGroup{LogGroupName: "g", Arn: cwLogGroupArn("g")})
	cwLogEvents.Put(cwEventsKey("g", "s"), events)
	newest := events[len(events)-1].Message
	oldest := events[0].Message

	get := func(body string) (status int, out struct {
		Events  []CWLogEvent `json:"events"`
		Message string       `json:"message"`
	}) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("X-Amz-Target", "Logs_20140328.GetLogEvents")
		w := httptest.NewRecorder()
		handler(w, r)
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
		return w.Code, out
	}

	t.Run("no limit still pages, and the page holds the newest events", func(t *testing.T) {
		_, out := get(`{"logGroupName":"g","logStreamName":"s"}`)
		if len(out.Events) > cwGetLogEventsMaxEvents {
			t.Fatalf("returned %d events, more than the %d-event page bound",
				len(out.Events), cwGetLogEventsMaxEvents)
		}
		if len(out.Events) == 0 {
			t.Fatal("returned no events")
		}
		if got := out.Events[len(out.Events)-1].Message; got != newest {
			t.Errorf("last event = %q, want the stream's newest %q", got, newest)
		}
		if out.Events[0].Message == oldest {
			t.Errorf("the default page started at the stream's oldest event %q — "+
				"startFromHead is unset, so the page is anchored at the tail", oldest)
		}
	})

	t.Run("startFromHead anchors the same page at the beginning", func(t *testing.T) {
		_, out := get(`{"logGroupName":"g","logStreamName":"s","startFromHead":true}`)
		if len(out.Events) == 0 || out.Events[0].Message != oldest {
			t.Fatalf("first event = %q, want the stream's oldest %q", out.Events[0].Message, oldest)
		}
		if len(out.Events) > cwGetLogEventsMaxEvents {
			t.Errorf("returned %d events, more than the %d-event page bound",
				len(out.Events), cwGetLogEventsMaxEvents)
		}
	})

	t.Run("an explicit limit takes the newest that many", func(t *testing.T) {
		_, out := get(`{"logGroupName":"g","logStreamName":"s","limit":10}`)
		if len(out.Events) != 10 {
			t.Fatalf("returned %d events, want 10", len(out.Events))
		}
		if got := out.Events[9].Message; got != newest {
			t.Errorf("last event = %q, want %q", got, newest)
		}
	})

	t.Run("events within a page stay in ascending timestamp order", func(t *testing.T) {
		_, out := get(`{"logGroupName":"g","logStreamName":"s","limit":50}`)
		for i := 1; i < len(out.Events); i++ {
			if out.Events[i].Timestamp < out.Events[i-1].Timestamp {
				t.Fatalf("event %d is older than the one before it", i)
			}
		}
	})

	t.Run("a limit above the service maximum is rejected", func(t *testing.T) {
		status, out := get(`{"logGroupName":"g","logStreamName":"s","limit":10001}`)
		if status != 400 {
			t.Fatalf("status = %d, want 400", status)
		}
		if !strings.Contains(out.Message, "limit") {
			t.Errorf("error message %q should name the limit constraint", out.Message)
		}
	})
}

// The 1 MB payload bound applies before the event-count bound, so a stream of
// large messages returns fewer than the maximum event count.
func TestGetLogEventsDefaultPageHonoursThePayloadBound(t *testing.T) {
	big := strings.Repeat("x", 64*1024)
	events := make([]CWLogEvent, 64)
	for i := range events {
		events[i] = CWLogEvent{Timestamp: int64(i), Message: big}
	}
	got := cwGetLogEventsDefaultPage(events)
	if got >= len(events) {
		t.Fatalf("page = %d events, want fewer than %d — 64 x 64 KiB exceeds the 1 MiB bound",
			got, len(events))
	}
	if got == 0 {
		t.Fatal("page = 0 events; a page always returns at least one event")
	}
}
