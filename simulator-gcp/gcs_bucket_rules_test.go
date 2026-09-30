package main

import (
	"net/http"
	"testing"
)

func gcsErrorReason(t *testing.T, body map[string]any) (message, reason string) {
	t.Helper()
	envelope, _ := body["error"].(map[string]any)
	errs, _ := envelope["errors"].([]any)
	if len(errs) != 1 {
		t.Fatalf("error envelope %v carries no single errors[] entry", body)
	}
	first, _ := errs[0].(map[string]any)
	message, _ = envelope["message"].(string)
	reason, _ = first["reason"].(string)
	return message, reason
}

// Cloud Storage refuses to delete a bucket that holds a live object with 409
// conflict and keeps both; a bucket whose objects are only soft-deleted deletes.
func TestGCSDeleteBucketRefusesLiveObjects(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "storage.googleapis.com"
	gcpHostOK(t, srv, host, http.MethodPost, "/storage/v1/b?project=p", `{"name":"full"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/upload/storage/v1/b/full/o?uploadType=media&name=a.txt", "payload")

	code, body := gcpHostCall(t, srv, host, http.MethodDelete, "/storage/v1/b/full", ``)
	if code != http.StatusConflict {
		t.Fatalf("deleting a bucket that holds an object answered %d: %v", code, body)
	}
	if message, reason := gcsErrorReason(t, body); reason != "conflict" || message != "The bucket you tried to delete is not empty." {
		t.Fatalf("error = %q / %q", message, reason)
	}
	gcpHostOK(t, srv, host, http.MethodGet, "/storage/v1/b/full", ``)
	gcpHostOK(t, srv, host, http.MethodGet, "/storage/v1/b/full/o/a.txt", ``)

	if code, _ := gcpHostCall(t, srv, host, http.MethodDelete, "/storage/v1/b/full/o/a.txt", ``); code != http.StatusNoContent {
		t.Fatalf("delete object answered %d", code)
	}
	if code, body := gcpHostCall(t, srv, host, http.MethodDelete, "/storage/v1/b/full", ``); code != http.StatusNoContent {
		t.Fatalf("deleting a bucket holding only soft-deleted objects answered %d: %v", code, body)
	}
}

// A notification configuration names a well-formed Pub/Sub topic that exists
// and that Cloud Storage's service agent may publish to.
func TestGCSNotificationTopicValidated(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "storage.googleapis.com"
	gcpHostOK(t, srv, host, http.MethodPost, "/storage/v1/b?project=p", `{"name":"watched"}`)
	create := func(topic string) (int, map[string]any) {
		return gcpHostCall(t, srv, host, http.MethodPost, "/storage/v1/b/watched/notificationConfigs", `{"topic":"`+topic+`"}`)
	}

	for _, malformed := range []string{
		"projects/p/topics/events",
		"//pubsub.googleapis.com/projects/p/subscriptions/events",
		"//pubsub.googleapis.com/projects/p/topics/9events",
		"//pubsub.googleapis.com/projects/p/topics/goog-events",
	} {
		code, body := create(malformed)
		if code != http.StatusBadRequest {
			t.Fatalf("topic %q answered %d: %v", malformed, code, body)
		}
		if _, reason := gcsErrorReason(t, body); reason != "invalid" {
			t.Fatalf("topic %q reason = %q", malformed, reason)
		}
	}

	const topic = "//pubsub.googleapis.com/projects/p/topics/events"
	const refusal = "The service account 'service-p@gs-project-accounts.iam.gserviceaccount.com' does not have permission to publish messages to to the Cloud Pub/Sub topic '" + topic + "', or that topic does not exist."
	code, body := create(topic)
	if message, reason := gcsErrorReason(t, body); code != http.StatusForbidden || reason != "forbidden" || message != refusal {
		t.Fatalf("an absent topic answered %d %q %q", code, reason, message)
	}

	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPut, "/v1/projects/p/topics/events", `{}`)
	code, body = create(topic)
	if message, reason := gcsErrorReason(t, body); code != http.StatusForbidden || reason != "forbidden" || message != refusal {
		t.Fatalf("a topic the service agent may not publish to answered %d %q %q", code, reason, message)
	}

	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPost, "/v1/projects/p/topics/events:setIamPolicy",
		`{"policy":{"bindings":[{"role":"roles/pubsub.publisher","members":["serviceAccount:service-p@gs-project-accounts.iam.gserviceaccount.com"]}]}}`)
	if code, body := create(topic); code != http.StatusOK {
		t.Fatalf("a topic the service agent may publish to answered %d: %v", code, body)
	}
	if count := len(gcsBucketNotifications("watched")); count != 1 {
		t.Fatalf("the bucket holds %d notification configurations, want only the accepted one", count)
	}
}
