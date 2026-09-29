package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// awsJSONCall invokes one awsJson operation through the router a client
// reaches, returning the status and the decoded body.
func awsJSONCall(t *testing.T, router *AWSRouter, target string, body any) (int, map[string]any) {
	t.Helper()
	handler, ok := router.Handler(target)
	if !ok {
		t.Fatalf("%s is not served", target)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
	r.Header.Set("X-Amz-Target", target)
	r.Header.Set("Content-Type", "application/x-amz-json-1.0")
	w := httptest.NewRecorder()
	handler(w, r)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s answered %d with undecodable %q", target, w.Code, w.Body.String())
		}
	}
	return w.Code, out
}

// testSQSQueue creates a queue and returns its URL and ARN.
func testSQSQueue(t *testing.T, router *AWSRouter, name string) (string, string) {
	t.Helper()
	status, out := awsJSONCall(t, router, "AmazonSQS.CreateQueue", map[string]any{"QueueName": name})
	if status != 200 {
		t.Fatalf("CreateQueue %s: %d %v", name, status, out)
	}
	url, _ := out["QueueUrl"].(string)
	return url, "arn:aws:sqs:" + awsRegion() + ":" + awsAccountID() + ":" + name
}

type receivedSQSMessage struct {
	Body       string
	Attributes map[string]string
}

// awaitSQSMessage long-polls the queue until a message arrives.
func awaitSQSMessage(t *testing.T, router *AWSRouter, queueURL string, within time.Duration) receivedSQSMessage {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		status, out := awsJSONCall(t, router, "AmazonSQS.ReceiveMessage", map[string]any{
			"QueueUrl": queueURL, "WaitTimeSeconds": 1, "MessageAttributeNames": []string{"All"},
		})
		if status != 200 {
			t.Fatalf("ReceiveMessage: %d %v", status, out)
		}
		messages, _ := out["Messages"].([]any)
		if len(messages) == 0 {
			continue
		}
		message, _ := messages[0].(map[string]any)
		received := receivedSQSMessage{Attributes: map[string]string{}}
		received.Body, _ = message["Body"].(string)
		attributes, _ := message["MessageAttributes"].(map[string]any)
		for name, value := range attributes {
			typed, _ := value.(map[string]any)
			received.Attributes[name], _ = typed["StringValue"].(string)
		}
		return received
	}
	t.Fatalf("no message reached %s within %s", queueURL, within)
	return receivedSQSMessage{}
}

// sqsQueueEmpty reports whether a receive that waits one second finds nothing.
func sqsQueueEmpty(t *testing.T, router *AWSRouter, queueURL string) bool {
	t.Helper()
	_, out := awsJSONCall(t, router, "AmazonSQS.ReceiveMessage", map[string]any{"QueueUrl": queueURL, "WaitTimeSeconds": 1})
	messages, _ := out["Messages"].([]any)
	return len(messages) == 0
}
