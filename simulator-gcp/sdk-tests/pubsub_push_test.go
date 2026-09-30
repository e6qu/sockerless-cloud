package gcp_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pubsub "google.golang.org/api/pubsub/v1"
)

// TestPubSub_PushSubscriptionDeliversSDK proves a push subscription created
// through the official client POSTs each published message to its
// pushEndpoint with an OIDC token for its service account, and that a
// rejected push is retried until the endpoint accepts it.
//
// The two messages share an ordering key, and Pub/Sub pushes a key's next
// message only once the previous one is acknowledged. The second message's
// push is therefore the proof that the accepted first push acknowledged it:
// the endpoint must see exactly the rejected attempt, the accepted retry and
// then the second message, with nothing pushed again in between.
func TestPubSub_PushSubscriptionDeliversSDK(t *testing.T) {
	svc := pubsubService(t)
	type pushed struct {
		auth string
		body map[string]any
	}
	received := make(chan pushed, 8)
	var accepted atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		received <- pushed{auth: r.Header.Get("Authorization"), body: body}
		if !accepted.Swap(true) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()

	const project = "test-project"
	topicName := "projects/" + project + "/topics/push-topic"
	subName := "projects/" + project + "/subscriptions/push-sub"
	_, err := svc.Projects.Topics.Create(topicName, &pubsub.Topic{Name: topicName}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Topics.Delete(topicName).Do() })
	_, err = svc.Projects.Subscriptions.Create(subName, &pubsub.Subscription{
		Topic: topicName,
		PushConfig: &pubsub.PushConfig{
			PushEndpoint: endpoint.URL + "/push",
			OidcToken:    &pubsub.OidcToken{ServiceAccountEmail: "pusher@" + project + ".iam.gserviceaccount.com"},
		},
		RetryPolicy:           &pubsub.RetryPolicy{MinimumBackoff: "1s", MaximumBackoff: "2s"},
		EnableMessageOrdering: true,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Subscriptions.Delete(subName).Do() })

	data := base64.StdEncoding.EncodeToString([]byte("pushed payload"))
	next := base64.StdEncoding.EncodeToString([]byte("next payload"))
	published, err := svc.Projects.Topics.Publish(topicName, &pubsub.PublishRequest{
		Messages: []*pubsub.PubsubMessage{
			{Data: data, Attributes: map[string]string{"origin": "sdk"}, OrderingKey: "push-order"},
			{Data: next, OrderingKey: "push-order"},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, published.MessageIds, 2)

	want := []struct{ id, data string }{
		{published.MessageIds[0], data},
		{published.MessageIds[0], data},
		{published.MessageIds[1], next},
	}
	for attempt, w := range want {
		select {
		case got := <-received:
			message, _ := got.body["message"].(map[string]any)
			assert.Equal(t, subName, got.body["subscription"])
			assert.Equal(t, w.id, message["messageId"], "push %d delivered the wrong message", attempt+1)
			assert.Equal(t, w.data, message["data"])
			assert.True(t, strings.HasPrefix(got.auth, "Bearer "), "push %d carried no OIDC token", attempt+1)
		case <-time.After(20 * time.Second):
			t.Fatalf("push %d never arrived", attempt+1)
		}
	}
}
