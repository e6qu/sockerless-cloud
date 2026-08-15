package azure_sdk_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/eventgrid/azeventgrid"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventGridPublishTransport terminates TLS in front of the simulator, the way
// a gateway does in front of a real service: the client speaks the https
// endpoint URL the azcore credential policies require before they will attach
// a key, a signature or a bearer token, and this transport forwards the
// request to the simulator's plain-HTTP listener with the X-Forwarded-Proto
// header that tells it which scheme the client used. That header is what makes
// the URL the simulator reconstructs — and therefore verifies a Shared Access
// Signature against — the same URL the client signed.
type eventGridPublishTransport struct{}

func (eventGridPublishTransport) Do(req *http.Request) (*http.Response, error) {
	forwarded := req.Clone(req.Context())
	target := *req.URL
	forwarded.Host = req.URL.Host
	target.Host = strings.TrimPrefix(baseURL, "http://")
	target.Scheme = "http"
	forwarded.URL = &target
	forwarded.Header.Set("X-Forwarded-Proto", req.URL.Scheme)
	return http.DefaultClient.Do(forwarded)
}

// eventGridPublisherOptions points the publisher client at the simulator
// through that gateway. The endpoint URL is the only coordinate that differs
// from a real Event Grid topic.
func eventGridPublisherOptions() *azeventgrid.ClientOptions {
	return &azeventgrid.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: eventGridPublishTransport{}},
	}
}

// eventGridPublishEndpoint returns a topic's advertised publish endpoint in
// the https spelling a publisher client uses.
func eventGridPublishEndpoint(t *testing.T, advertised string) string {
	t.Helper()
	parsed, err := url.Parse(advertised)
	require.NoError(t, err)
	parsed.Scheme = "https"
	return parsed.String()
}

// eventGridSASToken builds a Shared Access Signature the way Microsoft's
// published generators for Event Grid do: URL-encode the resource URI and the
// UTC expiry, sign "r=<resource>&e=<expiry>" with HMAC-SHA256 under the
// base64-decoded access key, and append the URL-encoded base64 signature.
func eventGridSASToken(t *testing.T, resource string, expiry time.Time, key string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(key)
	require.NoError(t, err)
	unsigned := "r=" + url.QueryEscape(resource) + "&e=" + url.QueryEscape(expiry.UTC().Format("1/2/2006 3:04:05 PM"))
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte(unsigned))
	return unsigned + "&s=" + url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

func eventGridTestEvent(id string) azeventgrid.Event {
	return azeventgrid.Event{
		ID:          to.Ptr(id),
		Subject:     to.Ptr("/sdk/auth"),
		EventType:   to.Ptr("sockerless.test"),
		EventTime:   to.Ptr(time.Now().UTC()),
		DataVersion: to.Ptr("1"),
		Data:        map[string]any{"ok": true},
	}
}

// assertEventGridUnauthorized checks that a publish was refused with Event
// Grid's 401 and its Unauthorized error code.
func assertEventGridUnauthorized(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err, "the publish must be refused")
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, http.StatusUnauthorized, respErr.StatusCode)
	assert.Equal(t, "Unauthorized", respErr.ErrorCode)
}

// TestEventGridPublisher_SDKCredentialContract drives the Event Grid publish
// data plane through the official publisher client's three credentials. The
// key and the signature are the ones the control plane's listKeys serves, so
// this is the end-to-end proof that those keys are load-bearing rather than
// decorative: a caller with no credential, with the wrong key, or with an
// expired or foreign-resource signature is refused with the service's own 401.
func TestEventGridPublisher_SDKCredentialContract(t *testing.T) {
	rg := "sdk-eg-auth-rg"
	ensureRG(t, rg)

	topics, err := armeventgrid.NewTopicsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const topicName = "sdk-eg-auth"
	poller, err := topics.BeginCreateOrUpdate(ctx, rg, topicName, armeventgrid.Topic{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.Endpoint)
	advertised := *created.Properties.Endpoint
	endpoint := eventGridPublishEndpoint(t, advertised)
	t.Cleanup(func() {
		if deletePoller, err := topics.BeginDelete(ctx, rg, topicName, nil); err == nil {
			_, _ = deletePoller.PollUntilDone(ctx, nil)
		}
	})

	keys, err := topics.ListSharedAccessKeys(ctx, rg, topicName, nil)
	require.NoError(t, err)
	require.NotNil(t, keys.Key1)
	require.NotNil(t, keys.Key2)

	publish := func(t *testing.T, client *azeventgrid.Client, id string) error {
		t.Helper()
		_, err := client.PublishEvents(ctx, []azeventgrid.Event{eventGridTestEvent(id)}, nil)
		return err
	}

	t.Run("access key", func(t *testing.T) {
		client, err := azeventgrid.NewClientWithSharedKeyCredential(endpoint,
			azcore.NewKeyCredential(*keys.Key1), eventGridPublisherOptions())
		require.NoError(t, err)
		require.NoError(t, publish(t, client, "sdk-key1"))

		second, err := azeventgrid.NewClientWithSharedKeyCredential(endpoint,
			azcore.NewKeyCredential(*keys.Key2), eventGridPublisherOptions())
		require.NoError(t, err)
		require.NoError(t, publish(t, second, "sdk-key2"))
	})

	t.Run("wrong access key", func(t *testing.T) {
		client, err := azeventgrid.NewClientWithSharedKeyCredential(endpoint,
			azcore.NewKeyCredential(base64.StdEncoding.EncodeToString(make([]byte, 32))), eventGridPublisherOptions())
		require.NoError(t, err)
		assertEventGridUnauthorized(t, publish(t, client, "sdk-wrong-key"))
	})

	t.Run("shared access signature", func(t *testing.T) {
		token := eventGridSASToken(t, endpoint, time.Now().Add(time.Hour), *keys.Key1)
		client, err := azeventgrid.NewClientWithSAS(endpoint, azcore.NewSASCredential(token), eventGridPublisherOptions())
		require.NoError(t, err)
		require.NoError(t, publish(t, client, "sdk-sas"))
	})

	t.Run("expired shared access signature", func(t *testing.T) {
		token := eventGridSASToken(t, endpoint, time.Now().Add(-time.Minute), *keys.Key1)
		client, err := azeventgrid.NewClientWithSAS(endpoint, azcore.NewSASCredential(token), eventGridPublisherOptions())
		require.NoError(t, err)
		assertEventGridUnauthorized(t, publish(t, client, "sdk-sas-expired"))
	})

	t.Run("shared access signature for another resource", func(t *testing.T) {
		token := eventGridSASToken(t, eventGridEndpointForTopic(t, endpoint, "sdk-eg-elsewhere"),
			time.Now().Add(time.Hour), *keys.Key1)
		client, err := azeventgrid.NewClientWithSAS(endpoint, azcore.NewSASCredential(token), eventGridPublisherOptions())
		require.NoError(t, err)
		assertEventGridUnauthorized(t, publish(t, client, "sdk-sas-foreign"))
	})

	t.Run("microsoft entra access token", func(t *testing.T) {
		// The publisher client requests the https://eventgrid.azure.net/.default
		// scope, so the token the simulator's own OAuth2 endpoint mints carries
		// the Event Grid data-plane audience.
		client, err := azeventgrid.NewClient(endpoint, &fakeCredential{}, eventGridPublisherOptions())
		require.NoError(t, err)
		require.NoError(t, publish(t, client, "sdk-entra"))
	})

	t.Run("no credential", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, advertised+"?api-version=2018-01-01", nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("regenerated key stops authorizing", func(t *testing.T) {
		stale, err := azeventgrid.NewClientWithSharedKeyCredential(endpoint,
			azcore.NewKeyCredential(*keys.Key1), eventGridPublisherOptions())
		require.NoError(t, err)
		staleToken := eventGridSASToken(t, endpoint, time.Now().Add(time.Hour), *keys.Key1)

		regenPoller, err := topics.BeginRegenerateKey(ctx, rg, topicName,
			armeventgrid.TopicRegenerateKeyRequest{KeyName: to.Ptr("key1")}, nil)
		require.NoError(t, err)
		rotated, err := regenPoller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, rotated.Key1)
		require.NotEqual(t, *keys.Key1, *rotated.Key1)

		assertEventGridUnauthorized(t, publish(t, stale, "sdk-stale-key"))

		staleSAS, err := azeventgrid.NewClientWithSAS(endpoint, azcore.NewSASCredential(staleToken), eventGridPublisherOptions())
		require.NoError(t, err)
		assertEventGridUnauthorized(t, publish(t, staleSAS, "sdk-stale-sas"))

		fresh, err := azeventgrid.NewClientWithSharedKeyCredential(endpoint,
			azcore.NewKeyCredential(*rotated.Key1), eventGridPublisherOptions())
		require.NoError(t, err)
		require.NoError(t, publish(t, fresh, "sdk-fresh-key"))
	})
}

// eventGridEndpointForTopic rewrites an advertised endpoint onto another
// topic name, which is how a token signed for a different resource is built.
func eventGridEndpointForTopic(t *testing.T, endpoint, topic string) string {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	require.NoError(t, err)
	_, rest, ok := strings.Cut(parsed.Host, ".")
	require.True(t, ok, "endpoint host must carry the topic label: %s", parsed.Host)
	parsed.Host = topic + "." + rest
	return parsed.String()
}
