package azure_sdk_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus"
	"github.com/stretchr/testify/require"
)

// The Service Bus and Event Hubs data planes authenticate every caller with a
// Shared Access Signature signed by an authorization rule's key. Tests
// therefore provision the namespace through ARM — which auto-provisions
// RootManageSharedAccessKey exactly as the real services do — and read that
// rule's real key material through listKeys. No test may hardcode key
// material: a token signed with an invented key is precisely what the data
// plane must reject.

const messagingRootRule = "RootManageSharedAccessKey"

var (
	messagingKeysMu sync.Mutex
	messagingKeys   = map[string]string{}
)

// serviceBusNamespaceKey provisions a Service Bus namespace and returns the
// primary key of its root authorization rule.
func serviceBusNamespaceKey(t *testing.T, namespace string) string {
	t.Helper()
	return messagingNamespaceKey(t, "servicebus/"+namespace, func(rg string) string {
		client, err := armservicebus.NewNamespacesClient(subscriptionID, &fakeCredential{}, clientOpts())
		require.NoError(t, err)
		poller, err := client.BeginCreateOrUpdate(ctx, rg, namespace, armservicebus.SBNamespace{
			Location: to.Ptr("eastus"),
			SKU: &armservicebus.SBSKU{
				Name: to.Ptr(armservicebus.SKUNameStandard),
				Tier: to.Ptr(armservicebus.SKUTierStandard),
			},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		keys, err := client.ListKeys(ctx, rg, namespace, messagingRootRule, nil)
		require.NoError(t, err)
		require.NotNil(t, keys.PrimaryKey)
		return *keys.PrimaryKey
	})
}

// eventHubsNamespaceKey provisions an Event Hubs namespace and returns the
// primary key of its root authorization rule.
func eventHubsNamespaceKey(t *testing.T, namespace string) string {
	t.Helper()
	return messagingNamespaceKey(t, "eventhub/"+namespace, func(rg string) string {
		client, err := armeventhub.NewNamespacesClient(subscriptionID, &fakeCredential{}, clientOpts())
		require.NoError(t, err)
		poller, err := client.BeginCreateOrUpdate(ctx, rg, namespace, armeventhub.EHNamespace{
			Location: to.Ptr("eastus"),
			SKU: &armeventhub.SKU{
				Name: to.Ptr(armeventhub.SKUNameStandard),
				Tier: to.Ptr(armeventhub.SKUTierStandard),
			},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		keys, err := client.ListKeys(ctx, rg, namespace, messagingRootRule, nil)
		require.NoError(t, err)
		require.NotNil(t, keys.PrimaryKey)
		return *keys.PrimaryKey
	})
}

// messagingNamespaceKey memoizes provisioning per namespace so parallel tests
// sharing a namespace provision it once.
func messagingNamespaceKey(t *testing.T, cacheKey string, provision func(rg string) string) string {
	t.Helper()
	messagingKeysMu.Lock()
	defer messagingKeysMu.Unlock()
	if key, ok := messagingKeys[cacheKey]; ok {
		return key
	}
	rg := "messaging-dataplane-rg"
	ensureRG(t, rg)
	key := provision(rg)
	messagingKeys[cacheKey] = key
	return key
}

// messagingConnectionString builds the connection string an AMQP or admin
// client uses, carrying the namespace's real key material.
func messagingConnectionString(namespace, host, key string, extra ...string) string {
	conn := fmt.Sprintf("Endpoint=sb://%s/;SharedAccessKeyName=%s;SharedAccessKey=%s",
		host, messagingRootRule, key)
	for _, part := range extra {
		conn += ";" + part
	}
	return conn
}

// messagingSASToken signs the Authorization header value a Service Bus HTTP
// caller presents, using the same construction the official SDKs use: the
// audience is URL-escaped and lowercased, and the signature is the base64
// HMAC-SHA256 of "<audience>\n<expiry>".
func messagingSASToken(keyName, key, audience string) string {
	escaped := strings.ToLower(url.QueryEscape(audience))
	expiry := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(escaped + "\n" + expiry))
	signature := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return fmt.Sprintf("SharedAccessSignature sr=%s&sig=%s&se=%s&skn=%s",
		escaped, signature, expiry, keyName)
}

// serviceBusHTTPAuthorization returns the Authorization header value for a
// Service Bus HTTP request against a namespace, signed with that namespace's
// real root key.
func serviceBusHTTPAuthorization(t *testing.T, namespace string) string {
	t.Helper()
	key := serviceBusNamespaceKey(t, namespace)
	return messagingSASToken(messagingRootRule, key,
		"https://"+namespace+".servicebus.localhost/")
}
