package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	pspb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type pulledMessage struct {
	ackID, data string
	published   string
	attributes  map[string]string
}

// psPullREST pulls what the subscription has ready, payloads decoded.
func psPullREST(t *testing.T, srv *sim.Server, sub string) []pulledMessage {
	t.Helper()
	out := gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPost, "/v1/"+sub+":pull", `{"maxMessages":10}`)
	received, _ := out["receivedMessages"].([]any)
	var msgs []pulledMessage
	for _, raw := range received {
		entry, _ := raw.(map[string]any)
		message, _ := entry["message"].(map[string]any)
		data, err := base64.StdEncoding.DecodeString(message["data"].(string))
		if err != nil {
			t.Fatalf("message data: %v", err)
		}
		attrs := map[string]string{}
		if a, ok := message["attributes"].(map[string]any); ok {
			for k, v := range a {
				attrs[k], _ = v.(string)
			}
		}
		msgs = append(msgs, pulledMessage{
			ackID: entry["ackId"].(string), data: string(data),
			published: message["publishTime"].(string), attributes: attrs,
		})
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].data < msgs[j].data })
	return msgs
}

func psAckREST(t *testing.T, srv *sim.Server, sub string, msgs []pulledMessage) {
	t.Helper()
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ackID)
	}
	body, err := json.Marshal(map[string]any{"ackIds": ids})
	if err != nil {
		t.Fatal(err)
	}
	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPost, "/v1/"+sub+":acknowledge", string(body))
}

func psPublishREST(t *testing.T, srv *sim.Server, topic, data string) {
	t.Helper()
	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPost, "/v1/"+topic+":publish",
		`{"messages":[{"data":"`+base64.StdEncoding.EncodeToString([]byte(data))+`"}]}`)
}

// A subscription with retainAckedMessages keeps what it acknowledged for its
// messageRetentionDuration, so a seek to a time marks the acknowledged
// messages published from then on unacknowledged again. One without it has
// nothing to replay.
func TestPubSubSeekReplaysRetainedAcknowledgedMessages(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "pubsub.googleapis.com"
	gcpHostOK(t, srv, host, http.MethodPut, "/v1/projects/p/topics/retain", `{}`)
	gcpHostOK(t, srv, host, http.MethodPut, "/v1/projects/p/subscriptions/retaining", `{"topic":"projects/p/topics/retain","retainAckedMessages":true}`)
	gcpHostOK(t, srv, host, http.MethodPut, "/v1/projects/p/subscriptions/plain", `{"topic":"projects/p/topics/retain"}`)

	psPublishREST(t, srv, "projects/p/topics/retain", "before")
	time.Sleep(5 * time.Millisecond) // separate the publish-time milliseconds
	psPublishREST(t, srv, "projects/p/topics/retain", "after")

	var seekTo string
	for _, sub := range []string{"projects/p/subscriptions/retaining", "projects/p/subscriptions/plain"} {
		got := psPullREST(t, srv, sub)
		if len(got) != 2 || got[0].data != "after" || got[1].data != "before" {
			t.Fatalf("%s pulled %v", sub, got)
		}
		seekTo = got[0].published
		psAckREST(t, srv, sub, got)
		if again := psPullREST(t, srv, sub); len(again) != 0 {
			t.Fatalf("%s redelivered acknowledged messages %v", sub, again)
		}
		gcpHostOK(t, srv, host, http.MethodPost, "/v1/"+sub+":seek", `{"time":"`+seekTo+`"}`)
	}

	replayed := psPullREST(t, srv, "projects/p/subscriptions/retaining")
	if len(replayed) != 1 || replayed[0].data != "after" {
		t.Fatalf("the seek replayed %v, want only the message published at the seek time", replayed)
	}
	if none := psPullREST(t, srv, "projects/p/subscriptions/plain"); len(none) != 0 {
		t.Fatalf("a subscription that does not retain acknowledged messages replayed %v", none)
	}
}

// ackDeadlineSeconds holds 10 to 600 seconds at create and at update, and 0
// takes the 10-second default.
func TestPubSubAckDeadlineRange(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "pubsub.googleapis.com"
	gcpHostOK(t, srv, host, http.MethodPut, "/v1/projects/p/topics/deadline", `{}`)
	for _, seconds := range []string{"5", "601"} {
		code, out := gcpHostCall(t, srv, host, http.MethodPut, "/v1/projects/p/subscriptions/bad-"+seconds,
			`{"topic":"projects/p/topics/deadline","ackDeadlineSeconds":`+seconds+`}`)
		if code != http.StatusBadRequest {
			t.Fatalf("ackDeadlineSeconds %s answered %d %v", seconds, code, out)
		}
	}
	sub := gcpHostOK(t, srv, host, http.MethodPut, "/v1/projects/p/subscriptions/ok", `{"topic":"projects/p/topics/deadline"}`)
	if sub["ackDeadlineSeconds"] != float64(10) {
		t.Fatalf("default ack deadline = %v", sub["ackDeadlineSeconds"])
	}
	if code, _ := gcpHostCall(t, srv, host, http.MethodPatch, "/v1/projects/p/subscriptions/ok",
		`{"subscription":{"ackDeadlineSeconds":700},"updateMask":"ackDeadlineSeconds"}`); code != http.StatusBadRequest {
		t.Fatalf("an update past 600 seconds answered %d", code)
	}

	grpcSub := &pubsubSubscriberGRPC{}
	updated, err := grpcSub.UpdateSubscription(context.Background(), &pspb.UpdateSubscriptionRequest{
		Subscription: &pspb.Subscription{Name: "projects/p/subscriptions/ok", AckDeadlineSeconds: 45},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"ack_deadline_seconds"}},
	})
	if err != nil || updated.GetAckDeadlineSeconds() != 45 {
		t.Fatalf("a snake_case FieldMask update answered %v, %v", updated, err)
	}
	_, err = grpcSub.UpdateSubscription(context.Background(), &pspb.UpdateSubscriptionRequest{
		Subscription: &pspb.Subscription{Name: "projects/p/subscriptions/ok"},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"no_such_field"}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("an update naming no field answered %v", err)
	}
}

// A Cloud Storage notification configuration publishes each change to an
// object in its bucket to its topic, as the JSON_API_V1 object resource with
// the attributes Cloud Storage sets, filtered by event type and name prefix.
func TestGCSNotificationsPublishObjectChanges(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "storage.googleapis.com"
	for _, topic := range []string{"gcs-events", "other"} {
		gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPut, "/v1/projects/p/topics/"+topic, `{}`)
		gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPost, "/v1/projects/p/topics/"+topic+":setIamPolicy",
			`{"policy":{"bindings":[{"role":"roles/pubsub.publisher","members":["serviceAccount:service-p@gs-project-accounts.iam.gserviceaccount.com"]}]}}`)
	}
	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPut, "/v1/projects/p/subscriptions/gcs-events", `{"topic":"projects/p/topics/gcs-events"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/storage/v1/b?project=p", `{"name":"watched"}`)
	first := gcpHostOK(t, srv, host, http.MethodPost, "/storage/v1/b/watched/notificationConfigs",
		`{"topic":"//pubsub.googleapis.com/projects/p/topics/gcs-events","payload_format":"JSON_API_V1","event_types":["OBJECT_FINALIZE","OBJECT_DELETE"],"object_name_prefix":"in/","custom_attributes":{"team":"data"}}`)
	second := gcpHostOK(t, srv, host, http.MethodPost, "/storage/v1/b/watched/notificationConfigs",
		`{"topic":"//pubsub.googleapis.com/projects/p/topics/other"}`)
	if code, _ := gcpHostCall(t, srv, host, http.MethodDelete, "/storage/v1/b/watched/notificationConfigs/"+second["id"].(string), ``); code != http.StatusNoContent {
		t.Fatalf("delete notification answered %d", code)
	}
	third := gcpHostOK(t, srv, host, http.MethodPost, "/storage/v1/b/watched/notificationConfigs",
		`{"topic":"//pubsub.googleapis.com/projects/p/topics/other"}`)
	if third["id"] == first["id"] {
		t.Fatalf("a new configuration reused the id %v of one that exists", first["id"])
	}
	listed := gcpHostOK(t, srv, host, http.MethodGet, "/storage/v1/b/watched/notificationConfigs", ``)
	if items, _ := listed["items"].([]any); len(items) != 2 {
		t.Fatalf("the bucket lists %v", listed)
	}

	gcpHostOK(t, srv, host, http.MethodPost, "/upload/storage/v1/b/watched/o?uploadType=media&name=in/a.txt", "payload")
	gcpHostOK(t, srv, host, http.MethodPost, "/upload/storage/v1/b/watched/o?uploadType=media&name=out/b.txt", "ignored")
	gcpHostOK(t, srv, host, http.MethodPatch, "/storage/v1/b/watched/o/in%2Fa.txt", `{"metadata":{"k":"v"}}`)
	if code, _ := gcpHostCall(t, srv, host, http.MethodDelete, "/storage/v1/b/watched/o/in%2Fa.txt", ``); code != http.StatusNoContent {
		t.Fatalf("delete object answered %d", code)
	}

	got := psPullREST(t, srv, "projects/p/subscriptions/gcs-events")
	events := map[string]pulledMessage{}
	for _, m := range got {
		events[m.attributes["eventType"]] = m
	}
	if len(got) != 2 || len(events) != 2 {
		t.Fatalf("published %v, want one OBJECT_FINALIZE and one OBJECT_DELETE for in/a.txt", got)
	}
	for eventType, m := range events {
		if m.attributes["objectId"] != "in/a.txt" || m.attributes["bucketId"] != "watched" || m.attributes["team"] != "data" ||
			m.attributes["payloadFormat"] != "JSON_API_V1" || m.attributes["notificationConfig"] != "projects/_/buckets/watched/notificationConfigs/"+first["id"].(string) {
			t.Fatalf("%s attributes = %v", eventType, m.attributes)
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(m.data), &object); err != nil || object["name"] != "in/a.txt" || object["size"] != "7" {
			t.Fatalf("%s payload = %s (%v)", eventType, m.data, err)
		}
	}
}

// A Cloud Storage trigger delivers the object's finalize as the CloudEvent
// Eventarc sends, through a notification configuration Eventarc puts on the
// bucket; deleting the trigger removes it. A trigger whose destination Cloud
// Run service does not exist is refused.
func TestEventarcStorageTriggerDeliversCloudEvents(t *testing.T) {
	srv := buildPushTestSimulator(t)
	receiver, received, _ := pushReceiver(t, http.StatusOK)
	crv2Services.Put("projects/p/locations/us-central1/services/handler", ServiceV2{
		Name: "projects/p/locations/us-central1/services/handler", URI: receiver.URL,
	})
	const host = "eventarc.googleapis.com"

	if code, out := gcpHostCall(t, srv, host, http.MethodPost, "/v1/projects/p/locations/us-central1/triggers?triggerId=nowhere", `{
		"eventFilters":[{"attribute":"type","value":"google.cloud.pubsub.topic.v1.messagePublished"}],
		"destination":{"cloudRun":{"service":"absent","region":"us-central1"}}}`); code != http.StatusBadRequest {
		t.Fatalf("a trigger naming a missing Cloud Run service answered %d %v", code, out)
	}
	if code, out := gcpHostCall(t, srv, host, http.MethodPost, "/v1/projects/p/locations/us-central1/triggers?triggerId=no-bucket", `{
		"eventFilters":[{"attribute":"type","value":"google.cloud.storage.object.v1.finalized"},{"attribute":"bucket","value":"absent"}],
		"destination":{"cloudRun":{"service":"handler","region":"us-central1"}}}`); code != http.StatusBadRequest {
		t.Fatalf("a trigger naming a missing bucket answered %d %v", code, out)
	}

	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=p", `{"name":"uploads"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v1/projects/p/locations/us-central1/triggers?triggerId=on-upload", `{
		"eventFilters":[{"attribute":"type","value":"google.cloud.storage.object.v1.finalized"},{"attribute":"bucket","value":"uploads"}],
		"destination":{"cloudRun":{"service":"handler","region":"us-central1","path":"/storage"}},
		"serviceAccount":"invoker@p.iam.gserviceaccount.com"}`)
	if configs := gcsBucketNotifications("uploads"); len(configs) != 1 || configs[0].EventTypes[0] != "OBJECT_FINALIZE" {
		t.Fatalf("the trigger's bucket holds notification configurations %v", configs)
	}
	uploadTrigger, _ := eventarcTriggers.Get(eventarcTriggerKey("p", "us-central1", "on-upload"))
	transport, _ := uploadTrigger.Transport["pubsub"].(map[string]any)
	createdTopic, _ := transport["topic"].(string)
	if _, ok := psTopics.Get(createdTopic); !ok || !strings.HasPrefix(createdTopic, "projects/p/topics/eventarc-us-central1-on-upload-") {
		t.Fatalf("Eventarc created no transport topic: %v", transport)
	}

	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/upload/storage/v1/b/uploads/o?uploadType=media&name=photo.jpg", "jpeg bytes")
	psPushSweep(context.Background())
	pushed := awaitPush(t, received)
	if pushed.header.Get("ce-type") != "google.cloud.storage.object.v1.finalized" ||
		pushed.header.Get("ce-source") != "//storage.googleapis.com/projects/_/buckets/uploads" ||
		pushed.header.Get("ce-subject") != "objects/photo.jpg" || pushed.header.Get("ce-bucket") != "uploads" ||
		pushed.header.Get("ce-specversion") != "1.0" || pushed.header.Get("ce-id") == "" ||
		!strings.HasPrefix(pushed.header.Get("Authorization"), "Bearer ") {
		t.Fatalf("CloudEvent headers = %v", pushed.header)
	}
	if pushed.body["name"] != "photo.jpg" || pushed.body["bucket"] != "uploads" || pushed.body["size"] != "10" {
		t.Fatalf("CloudEvent data = %v", pushed.body)
	}

	gcpHostOK(t, srv, host, http.MethodDelete, "/v1/projects/p/locations/us-central1/triggers/on-upload", ``)
	if configs := gcsBucketNotifications("uploads"); len(configs) != 0 {
		t.Fatalf("the deleted trigger left notification configurations %v", configs)
	}
	if _, still := psTopics.Get(createdTopic); still {
		t.Fatalf("the transport topic %s Eventarc created outlived its trigger", createdTopic)
	}
}

// entries:copy copies the entries a log bucket stores — the ones the scope's
// sinks route to it — that the filter matches into a Cloud Storage bucket, and
// reports how many it copied.
func TestLoggingEntriesCopy(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	bucket := "projects/p/locations/global/buckets/archive"
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/p/locations/global/buckets?bucketId=archive", `{}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/p/sinks", `{"name":"to-archive",
		"destination":"logging.googleapis.com/`+bucket+`","filter":"logName=\"projects/p/logs/app\"",
		"exclusions":[{"name":"noise","filter":"textPayload:\"noise\""}]}`)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=p", `{"name":"copies"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/p/logs/app","timestamp":"2026-09-01T10:15:00Z","severity":"ERROR","textPayload":"disk full"},
		{"logName":"projects/p/logs/app","timestamp":"2026-09-01T11:05:00Z","severity":"INFO","textPayload":"started"},
		{"logName":"projects/p/logs/app","timestamp":"2026-09-01T10:20:00Z","severity":"ERROR","textPayload":"noise burst"},
		{"logName":"projects/p/logs/other","timestamp":"2026-09-01T10:30:00Z","severity":"ERROR","textPayload":"not routed"},
		{"logName":"projects/q/logs/app","timestamp":"2026-09-01T10:30:00Z","severity":"ERROR","textPayload":"another project"}]}`)

	op := gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:copy",
		`{"name":"`+bucket+`","filter":"severity>=ERROR","destination":"storage.googleapis.com/copies"}`)
	response, _ := op["response"].(map[string]any)
	if response["logEntriesCopiedCount"] != "1" {
		t.Fatalf("copy response = %v", response)
	}
	metadata, _ := op["metadata"].(map[string]any)
	if metadata["source"] != bucket || metadata["destination"] != "storage.googleapis.com/copies" || metadata["verb"] != "copy" {
		t.Fatalf("copy metadata = %v", metadata)
	}
	name, _ := op["name"].(string)
	if fetched := gcpHostOK(t, srv, host, http.MethodGet, "/v2/"+name, ``); fetched["done"] != true {
		t.Fatalf("operations.get %s answered %v", name, fetched)
	}
	data, err := GCSObjectBytes("copies", "app/2026/09/01/10:00:00_10:59:59_S0.json")
	if err != nil {
		t.Fatalf("exported object: %v", err)
	}
	var entry LogEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil || entry.TextPayload != "disk full" {
		t.Fatalf("exported %q (%v)", data, err)
	}

	for body, want := range map[string]int{
		`{"name":"projects/p/locations/global/buckets/absent","destination":"storage.googleapis.com/copies"}`: http.StatusNotFound,
		`{"name":"` + bucket + `","destination":"storage.googleapis.com/absent"}`:                             http.StatusNotFound,
		`{"name":"` + bucket + `","destination":"bigquery.googleapis.com/projects/p/datasets/d"}`:             http.StatusBadRequest,
	} {
		if code, _ := gcpHostCall(t, srv, host, http.MethodPost, "/v2/entries:copy", body); code != want {
			t.Errorf("copy %s answered %d, want %d", body, code, want)
		}
	}
}
