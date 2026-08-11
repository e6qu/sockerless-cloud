package azure_cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventGridCLI_TopicSubscriptionPublish(t *testing.T) {
	deliveries := make(chan []map[string]any, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var events []map[string]any
		require.NoError(t, json.Unmarshal(body, &events))
		deliveries <- events
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)

	topicURL := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.EventGrid/topics/cli-topic?api-version=2021-12-01"
	out := runCLI(t, azRest("PUT", topicURL, `{"location":"eastus","tags":{"env":"test"}}`))
	var topic struct {
		ID         string `json:"id"`
		Properties struct {
			Endpoint string `json:"endpoint"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &topic))
	require.NotEmpty(t, topic.ID)
	require.NotEmpty(t, topic.Properties.Endpoint)
	t.Cleanup(func() {
		runCLI(t, azRest("DELETE", topicURL, ""))
	})
	keysURL := baseURL + topic.ID + "/listKeys?api-version=2021-12-01"
	out = runCLI(t, azRest("POST", keysURL, ""))
	var keys struct {
		Key1 string `json:"key1"`
		Key2 string `json:"key2"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &keys))
	assert.Len(t, keys.Key1, 44)
	assert.Len(t, keys.Key2, 44)

	subURL := baseURL + topic.ID + "/providers/Microsoft.EventGrid/eventSubscriptions/cli-sub?api-version=2021-12-01"
	body := `{"properties":{"destination":{"endpointType":"WebHook","properties":{"endpointUrl":"` + hook.URL + `"}},"eventDeliverySchema":"EventGridSchema"}}`
	runCLI(t, azRest("PUT", subURL, body))

	select {
	case <-deliveries:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Event Grid subscription validation delivery")
	}

	listURL := baseURL + topic.ID + "/providers/Microsoft.EventGrid/eventSubscriptions?api-version=2021-12-01"
	out = runCLI(t, azRest("GET", listURL, ""))
	var list struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	require.Len(t, list.Value, 1)
	assert.Equal(t, "cli-sub", list.Value[0].Name)

	publishBody := `[{"id":"cli-evt","eventType":"sockerless.cli","subject":"/cli","eventTime":"2026-05-27T00:00:00Z","data":{"ok":true},"dataVersion":"1"}]`
	// The topic endpoint is a `<topic>.eventgrid.localhost` subdomain URL; the
	// sim routes /api/events by the Host header (.eventgrid.), so publish to the
	// loopback base URL with Host set to the subdomain. This avoids depending on
	// `*.localhost` resolving to 127.0.0.1, which Linux does but macOS does not.
	ep, err := url.Parse(topic.Properties.Endpoint)
	require.NoError(t, err)
	runCLI(t, azRest("POST", baseURL+ep.Path+"?api-version=2018-01-01", publishBody,
		"--headers", "Content-Type=application/json", "Host="+ep.Host))

	select {
	case events := <-deliveries:
		require.Len(t, events, 1)
		assert.Equal(t, "cli-evt", events[0]["id"])
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Event Grid event delivery")
	}
}

func TestEventGridCLI_DomainAndSystemTopic(t *testing.T) {
	domainURL := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.EventGrid/domains/cli-domain?api-version=2021-12-01"
	out := runCLI(t, azRest("PUT", domainURL, `{"location":"eastus","properties":{"publicNetworkAccess":"Enabled"}}`))
	var domain struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties struct {
			Endpoint string `json:"endpoint"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &domain))
	require.NotEmpty(t, domain.ID)
	assert.Equal(t, "cli-domain", domain.Name)
	require.Contains(t, domain.Properties.Endpoint, "/api/events")
	t.Cleanup(func() {
		runCLI(t, azRest("DELETE", domainURL, ""))
	})

	keysURL := baseURL + domain.ID + "/listKeys?api-version=2021-12-01"
	out = runCLI(t, azRest("POST", keysURL, ""))
	var keys struct {
		Key1 string `json:"key1"`
		Key2 string `json:"key2"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &keys))
	assert.Len(t, keys.Key1, 44)
	assert.Len(t, keys.Key2, 44)

	domainTopicURL := baseURL + domain.ID + "/topics/cli-domain-topic?api-version=2021-12-01"
	out = runCLI(t, azRest("PUT", domainTopicURL, ""))
	var domainTopic struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &domainTopic))
	assert.Equal(t, "cli-domain-topic", domainTopic.Name)
	t.Cleanup(func() {
		runCLI(t, azRest("DELETE", domainTopicURL, ""))
	})

	out = runCLI(t, azRest("GET", baseURL+domain.ID+"/topics?api-version=2021-12-01", ""))
	var domainTopicList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &domainTopicList))
	require.Len(t, domainTopicList.Value, 1)
	assert.Equal(t, "cli-domain-topic", domainTopicList.Value[0].Name)

	systemTopicURL := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.EventGrid/systemTopics/cli-system-topic?api-version=2021-12-01"
	source := "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.Storage/storageAccounts/clistorage"
	out = runCLI(t, azRest("PUT", systemTopicURL, `{"location":"eastus","properties":{"source":"`+source+`","topicType":"Microsoft.Storage.StorageAccounts"}}`))
	var systemTopic struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties struct {
			TopicType string `json:"topicType"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &systemTopic))
	require.NotEmpty(t, systemTopic.ID)
	assert.Equal(t, "Microsoft.Storage.StorageAccounts", systemTopic.Properties.TopicType)
	t.Cleanup(func() {
		runCLI(t, azRest("DELETE", systemTopicURL, ""))
	})

	subURL := baseURL + systemTopic.ID + "/eventSubscriptions/cli-system-sub?api-version=2021-12-01"
	body := `{"properties":{"destination":{"endpointType":"WebHook","properties":{"endpointUrl":"http://127.0.0.1:1"}},"eventDeliverySchema":"EventGridSchema"}}`
	runCLI(t, azRest("PUT", subURL, body))

	out = runCLI(t, azRest("GET", baseURL+systemTopic.ID+"/eventSubscriptions?api-version=2021-12-01", ""))
	var subList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &subList))
	require.Len(t, subList.Value, 1)
	assert.Equal(t, "cli-system-sub", subList.Value[0].Name)
}

func TestEventGridCLI_PartnerTopicLifecycle(t *testing.T) {
	partnerTopicURL := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.EventGrid/partnerTopics/cli-partner-topic?api-version=2022-06-15"
	out := runCLI(t, azRest("PUT", partnerTopicURL, `{"location":"eastus","properties":{"partnerRegistrationImmutableId":"registration-id","source":"partner-source"}}`))
	var partnerTopic struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties struct {
			ActivationState string `json:"activationState"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &partnerTopic))
	require.NotEmpty(t, partnerTopic.ID)
	assert.Equal(t, "cli-partner-topic", partnerTopic.Name)
	assert.Equal(t, "NeverActivated", partnerTopic.Properties.ActivationState)
	t.Cleanup(func() {
		runCLI(t, azRest("DELETE", partnerTopicURL, ""))
	})

	out = runCLI(t, azRest("GET", baseURL+"/subscriptions/"+subscriptionID+"/providers/Microsoft.EventGrid/partnerTopics?api-version=2022-06-15", ""))
	var partnerTopicList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &partnerTopicList))
	require.Len(t, partnerTopicList.Value, 1)
	assert.Equal(t, "cli-partner-topic", partnerTopicList.Value[0].Name)

	out = runCLI(t, azRest("PATCH", partnerTopicURL, `{"tags":{"env":"test"}}`))
	var patchedPartnerTopic struct {
		Tags map[string]string `json:"tags"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &patchedPartnerTopic))
	assert.Equal(t, "test", patchedPartnerTopic.Tags["env"])

	out = runCLI(t, azRest("POST", baseURL+partnerTopic.ID+"/activate?api-version=2022-06-15", ""))
	require.NoError(t, json.Unmarshal([]byte(out), &partnerTopic))
	assert.Equal(t, "Activated", partnerTopic.Properties.ActivationState)

	subURL := baseURL + partnerTopic.ID + "/providers/Microsoft.EventGrid/eventSubscriptions/cli-partner-sub?api-version=2022-06-15"
	body := `{"properties":{"destination":{"endpointType":"WebHook","properties":{"endpointUrl":"http://127.0.0.1:1"}},"eventDeliverySchema":"EventGridSchema"}}`
	runCLI(t, azRest("PUT", subURL, body))
	out = runCLI(t, azRest("GET", baseURL+partnerTopic.ID+"/providers/Microsoft.EventGrid/eventSubscriptions?api-version=2022-06-15", ""))
	var subList struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &subList))
	require.Len(t, subList.Value, 1)
	assert.Equal(t, "cli-partner-sub", subList.Value[0].Name)

	out = runCLI(t, azRest("POST", baseURL+partnerTopic.ID+"/deactivate?api-version=2022-06-15", ""))
	require.NoError(t, json.Unmarshal([]byte(out), &partnerTopic))
	assert.Equal(t, "Deactivated", partnerTopic.Properties.ActivationState)
}
