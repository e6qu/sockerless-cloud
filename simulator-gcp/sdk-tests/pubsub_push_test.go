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
		RetryPolicy: &pubsub.RetryPolicy{MinimumBackoff: "1s", MaximumBackoff: "2s"},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Subscriptions.Delete(subName).Do() })

	data := base64.StdEncoding.EncodeToString([]byte("pushed payload"))
	published, err := svc.Projects.Topics.Publish(topicName, &pubsub.PublishRequest{
		Messages: []*pubsub.PubsubMessage{{Data: data, Attributes: map[string]string{"origin": "sdk"}}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, published.MessageIds, 1)

	for attempt := 1; attempt <= 2; attempt++ {
		select {
		case got := <-received:
			message, _ := got.body["message"].(map[string]any)
			assert.Equal(t, subName, got.body["subscription"])
			assert.Equal(t, data, message["data"])
			assert.Equal(t, published.MessageIds[0], message["messageId"])
			assert.True(t, strings.HasPrefix(got.auth, "Bearer "), "attempt %d carried no OIDC token", attempt)
		case <-time.After(20 * time.Second):
			t.Fatalf("push attempt %d never arrived", attempt)
		}
	}
	select {
	case extra := <-received:
		t.Fatalf("an acknowledged message was pushed again: %v", extra.body)
	case <-time.After(3 * time.Second):
	}
}
