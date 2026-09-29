package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func sqsTestStore(t *testing.T) {
	t.Helper()
	saved := sqsQueues
	t.Cleanup(func() { sqsQueues = saved })
	sqsQueues = sim.MakeStore[SQSQueue](nil, "test_sqs_queues")
}

func sqsCall(t *testing.T, h http.HandlerFunc, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

func sqsMustCreate(t *testing.T, name string, attrs map[string]string) string {
	t.Helper()
	code, out := sqsCall(t, handleSQSCreateQueue, map[string]any{"QueueName": name, "Attributes": attrs})
	if code != http.StatusOK {
		t.Fatalf("CreateQueue %s: %d %v", name, code, out)
	}
	return out["QueueUrl"].(string)
}

func sqsReceive(t *testing.T, url string, max, visibility int) []map[string]any {
	t.Helper()
	code, out := sqsCall(t, handleSQSReceiveMessage, map[string]any{
		"QueueUrl": url, "MaxNumberOfMessages": max, "VisibilityTimeout": visibility, "WaitTimeSeconds": 0,
		"MessageSystemAttributeNames": []string{"All"},
	})
	if code != http.StatusOK {
		t.Fatalf("ReceiveMessage: %d %v", code, out)
	}
	var msgs []map[string]any
	for _, m := range out["Messages"].([]any) {
		msgs = append(msgs, m.(map[string]any))
	}
	return msgs
}

func sqsBodies(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m["Body"].(string))
	}
	return out
}

func TestSQSFIFODeduplicatesAndBlocksGroups(t *testing.T) {
	sqsTestStore(t)
	url := sqsMustCreate(t, "orders.fifo", map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"})

	send := func(body, group string) map[string]any {
		code, out := sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": body, "MessageGroupId": group})
		if code != http.StatusOK {
			t.Fatalf("SendMessage: %d %v", code, out)
		}
		return out
	}
	first := send("a1", "a")
	if again := send("a1", "a"); again["MessageId"] != first["MessageId"] || again["SequenceNumber"] != first["SequenceNumber"] {
		t.Fatalf("resend inside the deduplication interval = %v, want %v", again, first)
	}
	send("b1", "b")
	send("a2", "a")

	got := sqsReceive(t, url, 1, 30)
	if bodies := sqsBodies(got); len(bodies) != 1 || bodies[0] != "a1" {
		t.Fatalf("first receive = %v", bodies)
	}
	if attrs := got[0]["Attributes"].(map[string]any); attrs["SequenceNumber"] != first["SequenceNumber"] || attrs["MessageGroupId"] != "a" {
		t.Fatalf("system attributes = %v", attrs)
	}
	if bodies := sqsBodies(sqsReceive(t, url, 10, 30)); len(bodies) != 1 || bodies[0] != "b1" {
		t.Fatalf("group a stays blocked behind its in-flight message; got %v", bodies)
	}
	code, _ := sqsCall(t, handleSQSDeleteMessage, map[string]any{"QueueUrl": url, "ReceiptHandle": got[0]["ReceiptHandle"]})
	if code != http.StatusOK {
		t.Fatalf("DeleteMessage: %d", code)
	}
	if bodies := sqsBodies(sqsReceive(t, url, 10, 30)); len(bodies) != 1 || bodies[0] != "a2" {
		t.Fatalf("after deleting a1 = %v", bodies)
	}
}

func TestSQSRedrivesAfterMaxReceiveCount(t *testing.T) {
	sqsTestStore(t)
	dlq := sqsMustCreate(t, "dlq", nil)
	src := sqsMustCreate(t, "src", map[string]string{
		"RedrivePolicy": `{"deadLetterTargetArn":"` + sqsQueueARN("dlq") + `","maxReceiveCount":"2"}`,
	})
	sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": src, "MessageBody": "poison"})
	for i := range 2 {
		if got := sqsReceive(t, src, 1, 0); len(got) != 1 {
			t.Fatalf("receive %d = %v", i+1, got)
		}
	}
	if got := sqsReceive(t, src, 1, 0); len(got) != 0 {
		t.Fatalf("third receive delivered %v instead of redriving", got)
	}
	got := sqsReceive(t, dlq, 1, 30)
	if bodies := sqsBodies(got); len(bodies) != 1 || bodies[0] != "poison" {
		t.Fatalf("dead-letter queue = %v", bodies)
	}
	if attrs := got[0]["Attributes"].(map[string]any); attrs["ApproximateReceiveCount"] != "1" {
		t.Fatalf("the receive count restarts on the dead-letter queue: %v", attrs)
	}
}

func TestSQSVisibilityAndReceiptHandles(t *testing.T) {
	sqsTestStore(t)
	url := sqsMustCreate(t, "work", map[string]string{"DelaySeconds": "0"})
	sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "job"})
	sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "later", "DelaySeconds": 60})

	_, attrs := sqsCall(t, handleSQSGetQueueAttributes, map[string]any{"QueueUrl": url, "AttributeNames": []string{"All"}})
	a := attrs["Attributes"].(map[string]any)
	if a["ApproximateNumberOfMessages"] != "1" || a["ApproximateNumberOfMessagesDelayed"] != "1" {
		t.Fatalf("attributes = %v", a)
	}

	got := sqsReceive(t, url, 10, 0)
	if len(got) != 1 {
		t.Fatalf("receive = %v", got)
	}
	handle := got[0]["ReceiptHandle"]
	code, out := sqsCall(t, handleSQSChangeMessageVisibility, map[string]any{"QueueUrl": url, "ReceiptHandle": handle, "VisibilityTimeout": 30})
	if code != http.StatusBadRequest {
		t.Fatalf("ChangeMessageVisibility on an expired lease = %d %v", code, out)
	}
	code, _ = sqsCall(t, handleSQSChangeMessageVisibility, map[string]any{"QueueUrl": url, "ReceiptHandle": "bogus", "VisibilityTimeout": 30})
	if code != http.StatusBadRequest {
		t.Fatalf("ChangeMessageVisibility with an unknown handle = %d", code)
	}

	// The receipt handle outlives the visibility timeout until the message is
	// received again.
	code, out = sqsCall(t, handleSQSDeleteMessageBatch, map[string]any{"QueueUrl": url, "Entries": []map[string]any{{"Id": "1", "ReceiptHandle": handle}}})
	if code != http.StatusOK || len(out["Successful"].([]any)) != 1 {
		t.Fatalf("DeleteMessageBatch = %d %v", code, out)
	}
	_, attrs = sqsCall(t, handleSQSGetQueueAttributes, map[string]any{"QueueUrl": url})
	if a := attrs["Attributes"].(map[string]any); a["ApproximateNumberOfMessages"] != "0" || a["ApproximateNumberOfMessagesNotVisible"] != "0" {
		t.Fatalf("attributes after delete = %v", a)
	}
}

func TestSQSRetentionDropsExpiredMessages(t *testing.T) {
	sqsTestStore(t)
	url := sqsMustCreate(t, "short", map[string]string{"MessageRetentionPeriod": "60"})
	sqsCall(t, handleSQSSendMessage, map[string]any{"QueueUrl": url, "MessageBody": "old"})
	sqsQueues.Update("short", func(q *SQSQueue) {
		q.Messages.Messages[0].EnqueuedAt = time.Now().Add(-time.Minute).UnixMilli()
	})
	if got := sqsReceive(t, url, 10, 30); len(got) != 0 {
		t.Fatalf("a message past MessageRetentionPeriod was delivered: %v", got)
	}
	q, _ := sqsQueues.Get("short")
	if len(q.Messages.Messages) != 0 {
		t.Fatal("expired message stayed stored")
	}
}
