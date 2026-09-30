package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEventGridWebhook serves a webhook the way an Event Grid subscriber does:
// it answers the subscription validation handshake by echoing the event's
// validationCode, and hands every notification's events to the channel.
func newEventGridWebhook(t *testing.T) (*httptest.Server, chan []map[string]any) {
	t.Helper()
	deliveries := make(chan []map[string]any, 16)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var events []map[string]any
		if err := json.Unmarshal(body, &events); err != nil || len(events) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("aeg-event-type") == "SubscriptionValidation" {
			data, _ := events[0]["data"].(map[string]any)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"validationResponse": data["validationCode"]})
			return
		}
		deliveries <- events
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)
	return hook, deliveries
}

// A webhook subscription's create runs Event Grid's validation handshake: an
// endpoint that answers without the echo completes it by visiting the
// validationUrl, and an endpoint that refuses it fails the create with Url
// validation.
func TestEventGrid_WebhookValidationHandshakeSDK(t *testing.T) {
	rg := "sdk-eventgrid-handshake-rg"
	ensureRG(t, rg)
	cred := &fakeCredential{}
	topics, err := armeventgrid.NewTopicsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	subs, err := armeventgrid.NewEventSubscriptionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	topicPoller, err := topics.BeginCreateOrUpdate(ctx, rg, "handshake-topic", armeventgrid.Topic{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	topic, err := topicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	visited := make(chan error, 1)
	manual := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var events []struct {
			Data struct {
				ValidationURL string `json:"validationUrl"`
			} `json:"data"`
		}
		if json.NewDecoder(r.Body).Decode(&events) != nil || len(events) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		go func() {
			resp, err := http.Get(events[0].Data.ValidationURL)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("validationUrl answered %d", resp.StatusCode)
				}
			}
			visited <- err
		}()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(manual.Close)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(refusing.Close)

	create := func(name, endpoint string) (armeventgrid.EventSubscriptionsClientCreateOrUpdateResponse, error) {
		t.Helper()
		poller, err := subs.BeginCreateOrUpdate(ctx, *topic.ID, name, armeventgrid.EventSubscription{
			Properties: &armeventgrid.EventSubscriptionProperties{
				Destination: &armeventgrid.WebHookEventSubscriptionDestination{
					EndpointType: to.Ptr(armeventgrid.EndpointTypeWebHook),
					Properties:   &armeventgrid.WebHookEventSubscriptionDestinationProperties{EndpointURL: to.Ptr(endpoint)},
				},
			},
		}, nil)
		require.NoError(t, err)
		return poller.PollUntilDone(ctx, nil)
	}

	got, err := create("manual-sub", manual.URL)
	require.NoError(t, err)
	require.NoError(t, <-visited)
	assert.Equal(t, armeventgrid.EventSubscriptionProvisioningStateSucceeded, *got.Properties.ProvisioningState)

	_, err = create("refused-sub", refusing.URL)
	require.ErrorContains(t, err, "Url validation")
	failed, err := subs.Get(ctx, *topic.ID, "refused-sub", nil)
	require.NoError(t, err)
	assert.Equal(t, armeventgrid.EventSubscriptionProvisioningStateFailed, *failed.Properties.ProvisioningState)
}
