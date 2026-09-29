package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/eventarc/apiv1/eventarcpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type deliveredEvent struct {
	header http.Header
	body   []byte
}

// eventReceiver is the HTTP endpoint an Eventarc trigger delivers to. It
// acknowledges every event and hands it to the test.
func eventReceiver(t *testing.T) (string, <-chan deliveredEvent) {
	t.Helper()
	events := make(chan deliveredEvent, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		events <- deliveredEvent{header: r.Header.Clone(), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server.URL, events
}

// TestEventarc_StorageTriggerDeliversObjectFinalized creates a Cloud Storage
// trigger on a bucket and writes an object into it: the destination receives
// the google.cloud.storage.object.v1.finalized CloudEvent naming the bucket and
// object, carrying the object resource as its data. An object written to
// another bucket reaches it not at all.
func TestEventarc_StorageTriggerDeliversObjectFinalized(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	name := parent + "/triggers/storage-trigger"
	svc := storageService(t)
	mustCreateBucket(t, svc, "eventarc-source-bucket")
	mustCreateBucket(t, svc, "eventarc-other-bucket")
	endpoint, events := eventReceiver(t)

	create, err := client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
		Parent:    parent,
		TriggerId: "storage-trigger",
		Trigger: &eventarcpb.Trigger{
			EventFilters: []*eventarcpb.EventFilter{
				{Attribute: "type", Value: "google.cloud.storage.object.v1.finalized"},
				{Attribute: "bucket", Value: "eventarc-source-bucket"},
			},
			Destination: &eventarcpb.Destination{
				Descriptor_: &eventarcpb.Destination_HttpEndpoint{HttpEndpoint: &eventarcpb.HttpEndpoint{Uri: endpoint}},
			},
		},
	})
	require.NoError(t, err)
	created, err := create.Wait(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, created.GetTransport().GetPubsub().GetSubscription(), "Eventarc provisions the transport it delivers through")
	t.Cleanup(func() {
		op, err := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: name})
		if assert.NoError(t, err, "delete %s", name) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", name)
		}
	})

	gcs := storageClient(t)
	w := gcs.Bucket("eventarc-other-bucket").Object("ignored.txt").NewWriter(ctx)
	_, err = w.Write([]byte("not watched"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	w = gcs.Bucket("eventarc-source-bucket").Object("reports/day-1.txt").NewWriter(ctx)
	_, err = w.Write([]byte("hello eventarc"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	var event deliveredEvent
	select {
	case event = <-events:
	case <-time.After(30 * time.Second):
		t.Fatal("no CloudEvent reached the destination")
	}
	assert.Equal(t, "1.0", event.header.Get("ce-specversion"))
	assert.Equal(t, "google.cloud.storage.object.v1.finalized", event.header.Get("ce-type"))
	assert.Equal(t, "//storage.googleapis.com/projects/_/buckets/eventarc-source-bucket", event.header.Get("ce-source"))
	assert.Equal(t, "objects/reports/day-1.txt", event.header.Get("ce-subject"))
	assert.NotEmpty(t, event.header.Get("ce-id"))
	assert.True(t, strings.HasPrefix(event.header.Get("Content-Type"), "application/json"))
	var object struct {
		Bucket string `json:"bucket"`
		Name   string `json:"name"`
		Size   string `json:"size"`
	}
	require.NoError(t, json.Unmarshal(event.body, &object))
	assert.Equal(t, "eventarc-source-bucket", object.Bucket)
	assert.Equal(t, "reports/day-1.txt", object.Name)
	assert.Equal(t, "14", object.Size)

	select {
	case extra := <-events:
		t.Fatalf("a second event arrived: %s %s", extra.header.Get("ce-subject"), extra.body)
	default:
	}
}

// TestEventarc_TriggerNeedsItsDestination proves Eventarc refuses a trigger
// whose destination Cloud Run service, or whose Cloud Storage bucket, does not
// exist.
func TestEventarc_TriggerNeedsItsDestination(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"

	_, err := client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
		Parent:    parent,
		TriggerId: "no-service-trigger",
		Trigger: &eventarcpb.Trigger{
			EventFilters: []*eventarcpb.EventFilter{{Attribute: "type", Value: "google.cloud.pubsub.topic.v1.messagePublished"}},
			Destination: &eventarcpb.Destination{
				Descriptor_: &eventarcpb.Destination_CloudRun{
					CloudRun: &eventarcpb.CloudRun{Service: "no-such-service", Region: "us-central1"},
				},
			},
		},
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a trigger naming a missing Cloud Run service: %v", err)

	endpoint, _ := eventReceiver(t)
	_, err = client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
		Parent:    parent,
		TriggerId: "no-bucket-trigger",
		Trigger: &eventarcpb.Trigger{
			EventFilters: []*eventarcpb.EventFilter{
				{Attribute: "type", Value: "google.cloud.storage.object.v1.finalized"},
				{Attribute: "bucket", Value: "no-such-bucket"},
			},
			Destination: &eventarcpb.Destination{
				Descriptor_: &eventarcpb.Destination_HttpEndpoint{HttpEndpoint: &eventarcpb.HttpEndpoint{Uri: endpoint}},
			},
		},
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a trigger naming a missing bucket: %v", err)

	_, err = client.GetTrigger(ctx, &eventarcpb.GetTriggerRequest{Name: parent + "/triggers/no-service-trigger"})
	assert.Equal(t, codes.NotFound, status.Code(err), "a refused trigger is not stored")
}
