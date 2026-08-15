package azure_cli_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the Event Grid publish data plane's credential contract.
// The access keys `az eventgrid topic key list` serves are the ones the
// publish accepts, and a caller with no credential, the wrong key, or a
// tampered or expired Shared Access Signature is refused. Routes exercised:
//
//	POST /api/events (Host-routed custom-topic publish)
//	POST …/providers/Microsoft.EventGrid/topics/{topicName}/listKeys
//	POST …/providers/Microsoft.EventGrid/topics/{topicName}/regenerateKey

// eventGridCLISASToken builds a Shared Access Signature the way Microsoft's
// published generators do: URL-encode the resource URI and the UTC expiry,
// sign "r=<resource>&e=<expiry>" with HMAC-SHA256 under the base64-decoded
// access key, and append the URL-encoded base64 signature.
func eventGridCLISASToken(t *testing.T, resource string, expiry time.Time, key string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(key)
	require.NoError(t, err)
	unsigned := "r=" + url.QueryEscape(resource) + "&e=" + url.QueryEscape(expiry.UTC().Format("1/2/2006 3:04:05 PM"))
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte(unsigned))
	return unsigned + "&s=" + url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

func TestEventGridCLI_PublishCredentialContract(t *testing.T) {
	topicURL := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.EventGrid/topics/cli-auth-topic?api-version=2021-12-01"
	out := runCLI(t, azRest("PUT", topicURL, `{"location":"eastus"}`))
	var topic struct {
		ID         string `json:"id"`
		Properties struct {
			Endpoint string `json:"endpoint"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &topic))
	require.NotEmpty(t, topic.Properties.Endpoint)
	t.Cleanup(func() { runCLI(t, azRest("DELETE", topicURL, "")) })

	keysURL := baseURL + topic.ID + "/listKeys?api-version=2021-12-01"
	var keys struct {
		Key1 string `json:"key1"`
		Key2 string `json:"key2"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, azRest("POST", keysURL, ""))), &keys))
	require.NotEmpty(t, keys.Key1)

	endpoint, err := url.Parse(topic.Properties.Endpoint)
	require.NoError(t, err)
	body := `[{"id":"cli-auth-evt","eventType":"sockerless.cli","subject":"/cli","eventTime":"2026-05-27T00:00:00Z","data":{"ok":true},"dataVersion":"1"}]`
	// The topic endpoint is a `<topic>.eventgrid.localhost` subdomain URL; the
	// sim routes /api/events by the Host header, so publish to the loopback
	// base URL with Host set to the subdomain. That avoids depending on
	// `*.localhost` resolving to 127.0.0.1, which Linux does and macOS does not.
	publish := func(headers ...string) []string {
		return append([]string{"--headers", "Content-Type=application/json", "Host=" + endpoint.Host}, headers...)
	}
	publishURL := baseURL + endpoint.Path + "?api-version=2018-01-01"

	// No credential at all: the service names both header spellings it accepts.
	failure := runStorageCLIExpectFailure(t, azRest("POST", publishURL, body, publish()...))
	assert.Contains(t, failure, "Unauthorized")
	assert.Contains(t, failure, "aeg-sas-token, aeg-sas-key")

	// A key that matches neither slot.
	failure = runStorageCLIExpectFailure(t, azRest("POST", publishURL, body,
		publish("aeg-sas-key="+base64.StdEncoding.EncodeToString(make([]byte, 32)))...))
	assert.Contains(t, failure, "is not authorized for")

	// An expired signature, and a signature whose bytes were altered.
	expired := eventGridCLISASToken(t, topic.Properties.Endpoint, time.Now().Add(-time.Minute), keys.Key1)
	failure = runStorageCLIExpectFailure(t, azRest("POST", publishURL, body, publish("aeg-sas-token="+expired)...))
	assert.Contains(t, failure, "is not authorized for")

	valid := eventGridCLISASToken(t, topic.Properties.Endpoint, time.Now().Add(time.Hour), keys.Key1)
	failure = runStorageCLIExpectFailure(t, azRest("POST", publishURL, body,
		publish("aeg-sas-token="+valid[:len(valid)-4]+"AAA%3D")...))
	assert.Contains(t, failure, "is not authorized for")

	// Both key slots and a well-formed signature publish.
	runCLI(t, azRest("POST", publishURL, body, publish("aeg-sas-key="+keys.Key1)...))
	runCLI(t, azRest("POST", publishURL, body, publish("aeg-sas-key="+keys.Key2)...))
	runCLI(t, azRest("POST", publishURL, body, publish("aeg-sas-token="+valid)...))

	// Regenerating key1 invalidates it and every signature derived from it,
	// while the untouched key2 keeps publishing.
	regenURL := baseURL + topic.ID + "/regenerateKey?api-version=2021-12-01"
	var rotated struct {
		Key1 string `json:"key1"`
		Key2 string `json:"key2"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, azRest("POST", regenURL, `{"keyName":"key1"}`))), &rotated))
	require.NotEqual(t, keys.Key1, rotated.Key1)
	assert.Equal(t, keys.Key2, rotated.Key2)

	failure = runStorageCLIExpectFailure(t, azRest("POST", publishURL, body, publish("aeg-sas-key="+keys.Key1)...))
	assert.Contains(t, failure, "is not authorized for")
	failure = runStorageCLIExpectFailure(t, azRest("POST", publishURL, body, publish("aeg-sas-token="+valid)...))
	assert.Contains(t, failure, "is not authorized for")
	runCLI(t, azRest("POST", publishURL, body, publish("aeg-sas-key="+rotated.Key1)...))
	runCLI(t, azRest("POST", publishURL, body, publish("aeg-sas-key="+rotated.Key2)...))
}
