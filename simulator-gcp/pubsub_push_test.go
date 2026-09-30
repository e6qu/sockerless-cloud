package main

import (
	"context"
	"encoding/base64"
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
)

type pushedRequest struct {
	header http.Header
	body   map[string]any
}

func pushReceiver(t *testing.T, status int) (*httptest.Server, chan pushedRequest, *atomic.Int32) {
	t.Helper()
	received := make(chan pushedRequest, 32)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		count.Add(1)
		received <- pushedRequest{header: r.Header.Clone(), body: body}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, received, &count
}

func gcpCall(t *testing.T, srv *sim.Server, method, path, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s → %d: %s", method, path, rec.Code, rec.Body.String())
	}
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func buildPushTestSimulator(t *testing.T) *sim.Server {
	t.Helper()
	bg.Await()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(bg.Await)
	t.Cleanup(srv.StopBackground)
	return srv
}

func awaitPush(t *testing.T, received chan pushedRequest) pushedRequest {
	t.Helper()
	select {
	case r := <-received:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("nothing was pushed")
		return pushedRequest{}
	}
}

// TestPubSubPushDeliversWithOIDCTokenAndAcknowledges proves a push
// subscription POSTs the push envelope with an OIDC token for its service
// account, and that a 2xx acknowledges the message.
func TestPubSubPushDeliversWithOIDCTokenAndAcknowledges(t *testing.T) {
	srv := buildPushTestSimulator(t)
	receiver, received, _ := pushReceiver(t, http.StatusNoContent)
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/topics/orders", `{}`)
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/subscriptions/orders-push", `{"topic":"projects/p/topics/orders",
		"pushConfig":{"pushEndpoint":"`+receiver.URL+`/push","oidcToken":{"serviceAccountEmail":"pusher@p.iam.gserviceaccount.com","audience":"https://orders.example"}}}`)
	data := base64.StdEncoding.EncodeToString([]byte("hello"))
	published := gcpCall(t, srv, http.MethodPost, "/v1/projects/p/topics/orders:publish", `{"messages":[{"data":"`+data+`","attributes":{"k":"v"}}]}`)
	ids, _ := published["messageIds"].([]any)

	psPushSweep(context.Background())
	pushed := awaitPush(t, received)
	message, _ := pushed.body["message"].(map[string]any)
	if pushed.body["subscription"] != "projects/p/subscriptions/orders-push" || message["data"] != data ||
		message["messageId"] != ids[0] || message["attributes"].(map[string]any)["k"] != "v" {
		t.Fatalf("push body = %v", pushed.body)
	}
	if _, has := pushed.body["deliveryAttempt"]; has {
		t.Error("deliveryAttempt was sent without a dead-letter policy")
	}
	token := strings.TrimPrefix(pushed.header.Get("Authorization"), "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("Authorization = %q, want a bearer JWT", pushed.header.Get("Authorization"))
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var decoded map[string]any
	if err := json.Unmarshal(claims, &decoded); err != nil || decoded["aud"] != "https://orders.example" ||
		decoded["email"] != "pusher@p.iam.gserviceaccount.com" {
		t.Fatalf("OIDC claims = %s", claims)
	}
	bg.Await()
	if q, _ := psQueues.Get("projects/p/subscriptions/orders-push"); len(q.Queue.Messages) != 0 {
		t.Fatalf("%d messages unacknowledged after the endpoint acknowledged", len(q.Queue.Messages))
	}
}

// TestPubSubPushDeadLettersAfterMaxDeliveryAttempts proves a rejected push is
// retried under the retry policy and, after maxDeliveryAttempts, forwarded to
// the dead-letter topic with the attributes Pub/Sub adds.
func TestPubSubPushDeadLettersAfterMaxDeliveryAttempts(t *testing.T) {
	srv := buildPushTestSimulator(t)
	receiver, received, count := pushReceiver(t, http.StatusInternalServerError)
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/topics/jobs", `{}`)
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/topics/jobs-dead", `{}`)
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/subscriptions/jobs-dead-pull", `{"topic":"projects/p/topics/jobs-dead"}`)
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/subscriptions/jobs-push", `{"topic":"projects/p/topics/jobs",
		"pushConfig":{"pushEndpoint":"`+receiver.URL+`"},
		"retryPolicy":{"minimumBackoff":"0.05s","maximumBackoff":"0.2s"},
		"deadLetterPolicy":{"deadLetterTopic":"projects/p/topics/jobs-dead","maxDeliveryAttempts":5}}`)
	gcpCall(t, srv, http.MethodPost, "/v1/projects/p/topics/jobs:publish", `{"messages":[{"data":"`+base64.StdEncoding.EncodeToString([]byte("x"))+`"}]}`)
	psPushSweep(context.Background())
	for attempt := 1; attempt <= 5; attempt++ {
		pushed := awaitPush(t, received)
		if pushed.body["deliveryAttempt"] != float64(attempt) {
			t.Fatalf("attempt %d reported deliveryAttempt %v", attempt, pushed.body["deliveryAttempt"])
		}
	}
	// Drain the fifth rejection's negative acknowledgement, then sweep at the
	// instant the retry policy's maximum backoff has run out: the exhausted
	// message is due for the dead-letter topic by then.
	bg.Await()
	psSweepDeadLetters(time.Now().Add(200 * time.Millisecond))
	dead := pullNow(t, "projects/p/subscriptions/jobs-dead-pull")
	if n := count.Load(); n != 5 {
		t.Fatalf("endpoint saw %d attempts, want maxDeliveryAttempts=5", n)
	}
	attributes := dead[0].Message.Attributes
	if attributes["CloudPubSubDeadLetterSourceDeliveryCount"] != "5" ||
		attributes["CloudPubSubDeadLetterSourceSubscription"] != "jobs-push" ||
		attributes["CloudPubSubDeadLetterSourceSubscriptionProject"] != "p" ||
		attributes["CloudPubSubDeadLetterSourceTopicPublishTime"] == "" {
		t.Fatalf("dead-letter attributes = %v", attributes)
	}
}

// pullNow pulls once from the subscription and fails the test when nothing
// is there.
func pullNow(t *testing.T, subscription string) []psDelivered {
	t.Helper()
	got, err := psDequeue(subscription, 10, 0)
	if err != nil {
		t.Fatalf("pull %s: %v", subscription, err)
	}
	if len(got) == 0 {
		t.Fatalf("nothing was available on %s", subscription)
	}
	return got
}

// TestPubSubPushAndPullShareDeliveryAttempts proves a subscription switched
// between push and pull with modifyPushConfig keeps its backlog and counts
// every delivery attempt, pushed or pulled, toward one total.
func TestPubSubPushAndPullShareDeliveryAttempts(t *testing.T) {
	srv := buildPushTestSimulator(t)
	// The endpoint rejects every push and holds its answer to the second
	// until the test has switched the subscription to pull, so no third push
	// can start while the switch is in flight.
	failed := make(chan pushedRequest, 4)
	release := make(chan struct{})
	var rejected atomic.Int32
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		failed <- pushedRequest{header: r.Header.Clone(), body: body}
		if rejected.Add(1) == 2 {
			<-release
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(failing.Close)
	accepting, accepted, _ := pushReceiver(t, http.StatusOK)
	const sub = "projects/p/subscriptions/switch"
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/topics/switch", `{}`)
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/topics/switch-dead", `{}`)
	gcpCall(t, srv, http.MethodPut, "/v1/"+sub, `{"topic":"projects/p/topics/switch",
		"pushConfig":{"pushEndpoint":"`+failing.URL+`"},
		"retryPolicy":{"minimumBackoff":"0s","maximumBackoff":"0s"},
		"deadLetterPolicy":{"deadLetterTopic":"projects/p/topics/switch-dead","maxDeliveryAttempts":10}}`)
	gcpCall(t, srv, http.MethodPost, "/v1/projects/p/topics/switch:publish", `{"messages":[{"data":"`+base64.StdEncoding.EncodeToString([]byte("x"))+`"}]}`)
	psPushSweep(context.Background())
	for attempt := 1; attempt <= 2; attempt++ {
		if got := awaitPush(t, failed).body["deliveryAttempt"]; got != float64(attempt) {
			t.Fatalf("push %d reported deliveryAttempt %v", attempt, got)
		}
	}
	gcpCall(t, srv, http.MethodPost, "/v1/"+sub+":modifyPushConfig", `{"pushConfig":{}}`)
	close(release)
	bg.Await()

	pulled := pullNow(t, sub)
	if len(pulled) != 1 || pulled[0].DeliveryAttempt != 3 {
		t.Fatalf("pull after two pushes = %+v, want the message at deliveryAttempt 3", pulled)
	}
	gcpCall(t, srv, http.MethodPost, "/v1/"+sub+":modifyAckDeadline", `{"ackIds":["`+pulled[0].AckID+`"],"ackDeadlineSeconds":0}`)
	gcpCall(t, srv, http.MethodPost, "/v1/"+sub+":modifyPushConfig", `{"pushConfig":{"pushEndpoint":"`+accepting.URL+`"}}`)
	psPushSweep(context.Background())
	if got := awaitPush(t, accepted).body["deliveryAttempt"]; got != float64(4) {
		t.Fatalf("push after the pull reported deliveryAttempt %v, want 4", got)
	}
	bg.Await()
	if q, _ := psQueues.Get(sub); len(q.Queue.Messages) != 0 {
		t.Fatalf("%d messages unacknowledged after the endpoint acknowledged", len(q.Queue.Messages))
	}
}

// TestEventarcPubSubTriggerDeliversCloudEvents proves a Pub/Sub trigger whose
// destination is a Cloud Run service gets the push subscription Eventarc
// creates, and that a published message reaches the service as a binary-mode
// CloudEvent.
func TestEventarcPubSubTriggerDeliversCloudEvents(t *testing.T) {
	srv := buildPushTestSimulator(t)
	receiver, received, _ := pushReceiver(t, http.StatusOK)
	crv2Services.Put("projects/p/locations/us-central1/services/handler", ServiceV2{
		Name: "projects/p/locations/us-central1/services/handler", URI: receiver.URL,
	})
	gcpCall(t, srv, http.MethodPut, "/v1/projects/p/topics/events", `{}`)
	gcpCall(t, srv, http.MethodPost, "/v1/projects/p/locations/us-central1/triggers?triggerId=on-event", `{
		"eventFilters":[{"attribute":"type","value":"google.cloud.pubsub.topic.v1.messagePublished"}],
		"destination":{"cloudRun":{"service":"handler","region":"us-central1","path":"/events"}},
		"transport":{"pubsub":{"topic":"projects/p/topics/events"}},
		"serviceAccount":"invoker@p.iam.gserviceaccount.com"}`)
	trigger, ok := eventarcTriggers.Get(eventarcTriggerKey("p", "us-central1", "on-event"))
	if !ok {
		t.Fatal("trigger not stored")
	}
	pubsub, _ := trigger.Transport["pubsub"].(map[string]any)
	subscription, _ := pubsub["subscription"].(string)
	if !strings.HasPrefix(subscription, "projects/p/subscriptions/eventarc-us-central1-on-event-sub-") {
		t.Fatalf("transport.pubsub = %v", pubsub)
	}
	gcpCall(t, srv, http.MethodPost, "/v1/projects/p/topics/events:publish", `{"messages":[{"data":"`+base64.StdEncoding.EncodeToString([]byte("{}"))+`"}]}`)
	psPushSweep(context.Background())
	pushed := awaitPush(t, received)
	if pushed.header.Get("ce-type") != "google.cloud.pubsub.topic.v1.messagePublished" ||
		pushed.header.Get("ce-source") != "//pubsub.googleapis.com/projects/p/topics/events" ||
		pushed.header.Get("ce-specversion") != "1.0" || pushed.header.Get("ce-id") == "" ||
		!strings.HasPrefix(pushed.header.Get("Authorization"), "Bearer ") {
		t.Fatalf("CloudEvent headers = %v", pushed.header)
	}
	if pushed.body["subscription"] != subscription {
		t.Fatalf("CloudEvent data = %v", pushed.body)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/projects/p/locations/us-central1/triggers/on-event", nil)
	srv.ServeHTTP(httptest.NewRecorder(), req)
	if _, still := psSubscriptions.Get(subscription); still {
		t.Fatal("the trigger's push subscription outlived the trigger")
	}
}
