package azure_sdk_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An event subscription's advanced filters decide which published events reach
// its endpoint: the filters are ANDed, the values of one filter ORed.
func TestEventGrid_AdvancedFiltersSelectDeliveredEventsSDK(t *testing.T) {
	rg := "sdk-eventgrid-filter-rg"
	ensureRG(t, rg)

	deliveries := make(chan map[string]any, 8)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var events []map[string]any
		if err := json.Unmarshal(body, &events); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("aeg-event-type") == "SubscriptionValidation" {
			data, _ := events[0]["data"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{"validationResponse": data["validationCode"]})
			return
		}
		for _, e := range events {
			deliveries <- e
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)

	cred := &fakeCredential{}
	topics, err := armeventgrid.NewTopicsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	subs, err := armeventgrid.NewEventSubscriptionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	topicName := "sdk-filter-topic"
	topicPoller, err := topics.BeginCreateOrUpdate(ctx, rg, topicName, armeventgrid.Topic{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	topicResp, err := topicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	keysResp, err := topics.ListSharedAccessKeys(ctx, rg, topicName, nil)
	require.NoError(t, err)

	scope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.EventGrid/topics/" + topicName
	subPoller, err := subs.BeginCreateOrUpdate(ctx, scope, "filtered", armeventgrid.EventSubscription{
		Properties: &armeventgrid.EventSubscriptionProperties{
			Destination: &armeventgrid.WebHookEventSubscriptionDestination{
				EndpointType: to.Ptr(armeventgrid.EndpointTypeWebHook),
				Properties:   &armeventgrid.WebHookEventSubscriptionDestinationProperties{EndpointURL: to.Ptr(hook.URL)},
			},
			EventDeliverySchema: to.Ptr(armeventgrid.EventDeliverySchemaEventGridSchema),
			Filter: &armeventgrid.EventSubscriptionFilter{
				AdvancedFilters: []armeventgrid.AdvancedFilterClassification{
					&armeventgrid.NumberGreaterThanAdvancedFilter{
						OperatorType: to.Ptr(armeventgrid.AdvancedFilterOperatorTypeNumberGreaterThan),
						Key:          to.Ptr("data.counter"),
						Value:        to.Ptr[float64](10),
					},
					&armeventgrid.StringInAdvancedFilter{
						OperatorType: to.Ptr(armeventgrid.AdvancedFilterOperatorTypeStringIn),
						Key:          to.Ptr("data.color"),
						Values:       []*string{to.Ptr("RED"), to.Ptr("blue")},
					},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = subPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	publish := func(id string, counter int, color string) {
		t.Helper()
		body, err := json.Marshal([]map[string]any{{
			"id": id, "eventType": "sockerless.filter", "subject": "/filter", "eventTime": "2026-09-29T00:00:00Z",
			"dataVersion": "1", "data": map[string]any{"counter": counter, "color": color},
		}})
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, *topicResp.Properties.Endpoint+"?api-version=2018-01-01", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("aeg-sas-key", *keysResp.Key1)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, string(respBody))
	}
	publish("too-small", 5, "red")
	publish("wrong-color", 20, "green")
	publish("admitted", 20, "red")

	select {
	case event := <-deliveries:
		assert.Equal(t, "admitted", event["id"], "only the event both filters admit is delivered")
	case <-time.After(30 * time.Second):
		t.Fatal("the event both advanced filters admit was never delivered")
	}
	select {
	case event := <-deliveries:
		t.Fatalf("a filtered event was delivered: %v", event)
	default:
	}
}
