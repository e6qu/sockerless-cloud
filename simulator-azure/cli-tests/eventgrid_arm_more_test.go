package azure_cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const egMoreAPI = "2022-06-15"

func egURL(t *testing.T, resourcePath string) string {
	t.Helper()
	return baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.EventGrid/" + resourcePath + "?api-version=" + egMoreAPI
}

// TestEventGridCLI_PartnerFamily exercises the Microsoft.EventGrid Partner
// Events control plane (registrations, namespaces + channels + keys,
// configurations + authorization, verified partners) through the az CLI.
func TestEventGridCLI_PartnerFamily(t *testing.T) {
	// Partner registration: create → get → list-by-subscription → delete.
	regURL := egURL(t, "partnerRegistrations/cli-partner-reg")
	out := runCLI(t, azRest("PUT", regURL, `{"location":"global","properties":{}}`))
	var reg struct {
		ID         string `json:"id"`
		Properties struct {
			PartnerRegistrationImmutableID string `json:"partnerRegistrationImmutableId"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &reg))
	require.NotEmpty(t, reg.ID)
	assert.NotEmpty(t, reg.Properties.PartnerRegistrationImmutableID)
	t.Cleanup(func() { runCLI(t, azRest("DELETE", regURL, "")) })
	runCLI(t, azRest("GET", regURL, ""))
	regListURL := baseURL + "/subscriptions/" + subscriptionID + "/providers/Microsoft.EventGrid/partnerRegistrations?api-version=" + egMoreAPI
	out = runCLI(t, azRest("GET", regListURL, ""))
	assert.Contains(t, out, "cli-partner-reg")

	// Partner namespace: create → listKeys → regenerateKey → channel CRUD →
	// list-by-subscription.
	nsURL := egURL(t, "partnerNamespaces/cli-partner-ns")
	runCLI(t, azRest("PUT", nsURL, `{"location":"eastus","properties":{}}`))
	t.Cleanup(func() { runCLI(t, azRest("DELETE", nsURL, "")) })
	keysURL := egURL(t, "partnerNamespaces/cli-partner-ns/listKeys")
	out = runCLI(t, azRest("POST", keysURL, ""))
	var keys struct{ Key1, Key2 string }
	require.NoError(t, json.Unmarshal([]byte(out), &keys))
	assert.Len(t, keys.Key1, 44)
	regenURL := egURL(t, "partnerNamespaces/cli-partner-ns/regenerateKey")
	origKey1 := keys.Key1
	out = runCLI(t, azRest("POST", regenURL, `{"keyName":"key1"}`))
	require.NoError(t, json.Unmarshal([]byte(out), &keys))
	assert.Len(t, keys.Key1, 44)
	assert.NotEqual(t, origKey1, keys.Key1) // key1 rotated; key2 unchanged

	chURL := egURL(t, "partnerNamespaces/cli-partner-ns/channels/cli-channel")
	runCLI(t, azRest("PUT", chURL, `{"properties":{"channelType":"PartnerTopic"}}`))
	runCLI(t, azRest("GET", chURL, ""))
	chListURL := egURL(t, "partnerNamespaces/cli-partner-ns/channels")
	out = runCLI(t, azRest("GET", chListURL, ""))
	assert.Contains(t, out, "cli-channel")
	runCLI(t, azRest("POST", egURL(t, "partnerNamespaces/cli-partner-ns/channels/cli-channel/getFullUrl"), ""))
	runCLI(t, azRest("DELETE", chURL, ""))

	nsListURL := baseURL + "/subscriptions/" + subscriptionID + "/providers/Microsoft.EventGrid/partnerNamespaces?api-version=" + egMoreAPI
	out = runCLI(t, azRest("GET", nsListURL, ""))
	assert.Contains(t, out, "cli-partner-ns")

	// Partner configuration (the per-resource-group default singleton):
	// create → authorizePartner → unauthorizePartner → list-by-subscription.
	cfgURL := egURL(t, "partnerConfigurations/default")
	runCLI(t, azRest("PUT", cfgURL, `{"properties":{"partnerAuthorization":{"defaultMaximumExpirationTimeInDays":7,"authorizedPartnersList":[]}}}`))
	t.Cleanup(func() { runCLI(t, azRest("DELETE", cfgURL, "")) })
	authURL := egURL(t, "partnerConfigurations/default/authorizePartner")
	partner := `{"partnerRegistrationImmutableId":"` + reg.Properties.PartnerRegistrationImmutableID + `","partnerName":"cli-partner-reg"}`
	out = runCLI(t, azRest("POST", authURL, partner))
	assert.Contains(t, out, "cli-partner-reg")
	out = runCLI(t, azRest("GET", cfgURL, ""))
	assert.Contains(t, out, "cli-partner-reg")
	unauthURL := egURL(t, "partnerConfigurations/default/unauthorizePartner")
	out = runCLI(t, azRest("POST", unauthURL, partner))
	assert.NotContains(t, out, `"partnerName": "cli-partner-reg"`)
	cfgListURL := baseURL + "/subscriptions/" + subscriptionID + "/providers/Microsoft.EventGrid/partnerConfigurations?api-version=" + egMoreAPI
	runCLI(t, azRest("GET", cfgListURL, ""))

	// Verified partners (tenant-level catalog).
	vpListURL := baseURL + "/providers/Microsoft.EventGrid/verifiedPartners?api-version=" + egMoreAPI
	out = runCLI(t, azRest("GET", vpListURL, ""))
	assert.Contains(t, out, "Auth0")
	vpURL := baseURL + "/providers/Microsoft.EventGrid/verifiedPartners/Auth0?api-version=" + egMoreAPI
	out = runCLI(t, azRest("GET", vpURL, ""))
	assert.Contains(t, out, "Auth0")
}

// TestEventGridCLI_NestedEventSubscriptionsAndRegistries exercises the
// 2022-06-15 nested (non-provider) per-resource EventSubscriptions URL form
// plus the operations / topic-type registries through the az CLI.
func TestEventGridCLI_NestedEventSubscriptionsAndRegistries(t *testing.T) {
	// Operations + topic types.
	opsURL := baseURL + "/providers/Microsoft.EventGrid/operations?api-version=" + egMoreAPI
	out := runCLI(t, azRest("GET", opsURL, ""))
	assert.Contains(t, out, "Microsoft.EventGrid/topics/read")
	ttURL := baseURL + "/providers/Microsoft.EventGrid/topicTypes?api-version=" + egMoreAPI
	out = runCLI(t, azRest("GET", ttURL, ""))
	assert.Contains(t, out, "Microsoft.Storage.StorageAccounts")
	runCLI(t, azRest("GET", baseURL+"/providers/Microsoft.EventGrid/topicTypes/Microsoft.Storage.StorageAccounts?api-version="+egMoreAPI, ""))
	etURL := baseURL + "/providers/Microsoft.EventGrid/topicTypes/Microsoft.Storage.StorageAccounts/eventTypes?api-version=" + egMoreAPI
	out = runCLI(t, azRest("GET", etURL, ""))
	assert.Contains(t, out, "Microsoft.Storage.BlobCreated")

	// Topic + nested event subscription (topics/{topic}/eventSubscriptions/{name}).
	topicURL := egURL(t, "topics/cli-more-topic")
	runCLI(t, azRest("PUT", topicURL, `{"location":"eastus"}`))
	t.Cleanup(func() { runCLI(t, azRest("DELETE", topicURL, "")) })
	subURL := egURL(t, "topics/cli-more-topic/eventSubscriptions/cli-nested-sub")
	subBody := `{"properties":{"destination":{"endpointType":"WebHook","properties":{"endpointUrl":"https://example.com/hook"}},"eventDeliverySchema":"EventGridSchema"}}`
	runCLI(t, azRest("PUT", subURL, subBody))
	out = runCLI(t, azRest("GET", subURL, ""))
	assert.Contains(t, out, "cli-nested-sub")
	runCLI(t, azRest("PATCH", subURL, `{"labels":["green"]}`))
	out = runCLI(t, azRest("POST", egURL(t, "topics/cli-more-topic/eventSubscriptions/cli-nested-sub/getFullUrl"), ""))
	assert.Contains(t, out, "https://example.com/hook")
	runCLI(t, azRest("POST", egURL(t, "topics/cli-more-topic/eventSubscriptions/cli-nested-sub/getDeliveryAttributes"), ""))
	out = runCLI(t, azRest("GET", egURL(t, "topics/cli-more-topic/eventSubscriptions"), ""))
	assert.Contains(t, out, "cli-nested-sub")
	runCLI(t, azRest("DELETE", subURL, ""))

	// Subscription-/regional-/topic-type-scoped event subscription list views.
	for _, u := range []string{
		baseURL + "/subscriptions/" + subscriptionID + "/providers/Microsoft.EventGrid/eventSubscriptions?api-version=" + egMoreAPI,
		baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.EventGrid/eventSubscriptions?api-version=" + egMoreAPI,
		baseURL + "/subscriptions/" + subscriptionID + "/providers/Microsoft.EventGrid/locations/eastus/eventSubscriptions?api-version=" + egMoreAPI,
		baseURL + "/subscriptions/" + subscriptionID + "/providers/Microsoft.EventGrid/topicTypes/Microsoft.EventGrid.Topics/eventSubscriptions?api-version=" + egMoreAPI,
	} {
		out = runCLI(t, azRest("GET", u, ""))
		assert.True(t, strings.Contains(out, `"value"`), "list view returns a value array: %s", u)
	}
}
