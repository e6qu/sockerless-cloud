package main

import (
	"net/http"
	"testing"
	"time"
)

func sqsLongPollReceive(t *testing.T, url string, wait int) chan []map[string]any {
	t.Helper()
	got := make(chan []map[string]any, 1)
	go func() {
		code, out := sqsCall(t, handleSQSReceiveMessage, map[string]any{
			"QueueUrl": url, "WaitTimeSeconds": wait, "VisibilityTimeout": 30,
		})
		if code != http.StatusOK {
			t.Errorf("ReceiveMessage: %d %v", code, out)
			got <- nil
			return
		}
		var msgs []map[string]any
		for _, m := range out["Messages"].([]any) {
			msgs = append(msgs, m.(map[string]any))
		}
		got <- msgs
	}()
	return got
}

func TestSQSSendSignalsWaitingReceive(t *testing.T) {
	sqsTestStore(t)
	url := sqsMustCreate(t, "signal-q", nil)
	arrival := sqsQueueSignal("signal-q")
	select {
	case <-arrival:
		t.Fatal("the queue signalled before anything was sent")
	default:
	}
	if code, out := sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "hello"}); code != http.StatusOK {
		t.Fatalf("SendMessage: %d %v", code, out)
	}
	select {
	case <-arrival:
	default:
		t.Fatal("SendMessage did not signal the queue's waiting receives")
	}
}

func TestSQSLongPollWakesOnSend(t *testing.T) {
	sqsTestStore(t)
	url := sqsMustCreate(t, "wake-on-send", nil)
	start := time.Now()
	got := sqsLongPollReceive(t, url, 20)
	if code, out := sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "arrived"}); code != http.StatusOK {
		t.Fatalf("SendMessage: %d %v", code, out)
	}
	msgs := <-got
	if len(msgs) != 1 || msgs[0]["Body"] != "arrived" {
		t.Fatalf("long poll returned %v, want the sent message", msgs)
	}
	if elapsed := time.Since(start); elapsed >= 20*time.Second {
		t.Fatalf("long poll returned after %s, at its deadline rather than on the send", elapsed)
	}
}

func TestSQSLongPollWakesWhenDelayEnds(t *testing.T) {
	sqsTestStore(t)
	url := sqsMustCreate(t, "wake-on-delay", nil)
	sent := time.Now()
	if code, out := sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "delayed", "DelaySeconds": 1}); code != http.StatusOK {
		t.Fatalf("SendMessage: %d %v", code, out)
	}
	msgs := <-sqsLongPollReceive(t, url, 20)
	if len(msgs) != 1 || msgs[0]["Body"] != "delayed" {
		t.Fatalf("long poll returned %v, want the delayed message", msgs)
	}
	// The queue keeps times in milliseconds, so the delay can end up to a
	// millisecond before a full second has passed.
	if elapsed := time.Since(sent); elapsed < time.Second-time.Millisecond || elapsed >= 20*time.Second {
		t.Fatalf("delayed message arrived after %s, want when its one-second delay ended", elapsed)
	}
}

func TestSQSLongPollWakesWhenVisibilityTimeoutEnds(t *testing.T) {
	sqsTestStore(t)
	url := sqsMustCreate(t, "wake-on-visibility", nil)
	if code, out := sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "leased"}); code != http.StatusOK {
		t.Fatalf("SendMessage: %d %v", code, out)
	}
	if msgs := sqsReceive(t, url, 1, 1); len(msgs) != 1 {
		t.Fatalf("first receive returned %v", msgs)
	}
	received := time.Now()
	msgs := <-sqsLongPollReceive(t, url, 20)
	if len(msgs) != 1 || msgs[0]["Body"] != "leased" {
		t.Fatalf("long poll returned %v, want the message whose visibility timeout ended", msgs)
	}
	if elapsed := time.Since(received); elapsed >= 20*time.Second {
		t.Fatalf("message reappeared after %s, want when its one-second visibility timeout ended", elapsed)
	}
}

func TestSQSLongPollWakesOnVisibilityChange(t *testing.T) {
	sqsTestStore(t)
	url := sqsMustCreate(t, "wake-on-change", nil)
	if code, out := sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "released"}); code != http.StatusOK {
		t.Fatalf("SendMessage: %d %v", code, out)
	}
	first := sqsReceive(t, url, 1, 600)
	if len(first) != 1 {
		t.Fatalf("first receive returned %v", first)
	}
	got := sqsLongPollReceive(t, url, 20)
	if code, out := sqsCall(t, handleSQSChangeMessageVisibility, map[string]any{
		"QueueUrl": url, "ReceiptHandle": first[0]["ReceiptHandle"], "VisibilityTimeout": 0,
	}); code != http.StatusOK {
		t.Fatalf("ChangeMessageVisibility: %d %v", code, out)
	}
	if msgs := <-got; len(msgs) != 1 || msgs[0]["Body"] != "released" {
		t.Fatalf("long poll returned %v, want the released message", msgs)
	}
}
