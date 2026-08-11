package azure_sdk_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Service Bus and Event Hubs data planes must refuse every token they
// cannot verify against a real authorization rule's current key. These are the
// negative controls for that: without them a passing suite would only prove
// the happy path, and an implementation that accepted any syntactically valid
// token would look just as green.

// sasTokenSignedWith builds a Shared Access Signature over an audience with a
// caller-chosen key, key name and expiry, so a test can present a token that
// is well-formed but must not authenticate.
func sasTokenSignedWith(keyName, key, audience string, expiresAt time.Time) string {
	escaped := strings.ToLower(url.QueryEscape(audience))
	expiry := strconv.FormatInt(expiresAt.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(escaped + "\n" + expiry))
	signature := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return fmt.Sprintf("SharedAccessSignature sr=%s&sig=%s&se=%s&skn=%s",
		escaped, signature, expiry, keyName)
}

// serviceBusHTTPStatus issues a Service Bus HTTP data-plane request with a
// caller-supplied Authorization header and returns the status and body.
func serviceBusHTTPStatus(t *testing.T, namespace, authorization string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/authqueue/messages", strings.NewReader("body"))
	require.NoError(t, err)
	req.Host = namespace + ".servicebus.localhost"
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// TestServiceBusHTTP_RefusesUnverifiableTokens drives the Service Bus HTTP
// data plane with tokens that must not authenticate, and one that must.
func TestServiceBusHTTP_RefusesUnverifiableTokens(t *testing.T) {
	namespace := "sdk-sb-authns"
	realKey := serviceBusNamespaceKey(t, namespace)
	audience := "https://" + namespace + ".servicebus.localhost/"

	t.Run("no authorization header", func(t *testing.T) {
		status, body := serviceBusHTTPStatus(t, namespace, "")
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Contains(t, body, "<Code>401</Code>", body)
		assert.Contains(t, body, "MalformedToken", body)
	})

	t.Run("garbage authorization header", func(t *testing.T) {
		status, body := serviceBusHTTPStatus(t, namespace, "Bearer not-a-sas-token")
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Contains(t, body, "MalformedToken", body)
	})

	t.Run("wrong key", func(t *testing.T) {
		wrong := base64.StdEncoding.EncodeToString([]byte("this-is-not-the-namespace-key!!!"))
		token := sasTokenSignedWith(messagingRootRule, wrong, audience, time.Now().Add(time.Hour))
		status, body := serviceBusHTTPStatus(t, namespace, token)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Contains(t, body, "InvalidSignature", body)
	})

	t.Run("unknown rule name", func(t *testing.T) {
		token := sasTokenSignedWith("NoSuchRule", realKey, audience, time.Now().Add(time.Hour))
		status, body := serviceBusHTTPStatus(t, namespace, token)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Contains(t, body, "NoSuchRule", body)
	})

	t.Run("expired token", func(t *testing.T) {
		token := sasTokenSignedWith(messagingRootRule, realKey, audience, time.Now().Add(-time.Minute))
		status, body := serviceBusHTTPStatus(t, namespace, token)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Contains(t, body, "ExpiredToken", body)
	})

	t.Run("token for another namespace", func(t *testing.T) {
		otherKey := serviceBusNamespaceKey(t, "sdk-sb-authns-other")
		token := sasTokenSignedWith(messagingRootRule, otherKey, audience, time.Now().Add(time.Hour))
		status, body := serviceBusHTTPStatus(t, namespace, token)
		assert.Equal(t, http.StatusUnauthorized, status,
			"a token signed by another namespace's key must not authenticate")
		assert.Contains(t, body, "InvalidSignature", body)
	})

	t.Run("valid token", func(t *testing.T) {
		token := sasTokenSignedWith(messagingRootRule, realKey, audience, time.Now().Add(time.Hour))
		status, body := serviceBusHTTPStatus(t, namespace, token)
		assert.Equal(t, http.StatusCreated, status,
			"a token signed with the rule's current key must authenticate: %s", body)
	})
}

// TestServiceBusHTTP_RotatedKeyInvalidatesToken ties key rotation to
// authentication: after regenerateKeys, a token signed with the key that was
// replaced stops authenticating and one signed with the new key works. This is
// what makes rotation meaningful — a simulator that ignored rotation would
// keep accepting the old token forever.
func TestServiceBusHTTP_RotatedKeyInvalidatesToken(t *testing.T) {
	rg := "messaging-dataplane-rg"
	namespace := "sdk-sb-rotateauth"
	ensureRG(t, rg)

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

	audience := "https://" + namespace + ".servicebus.localhost/"
	before, err := client.ListKeys(ctx, rg, namespace, messagingRootRule, nil)
	require.NoError(t, err)
	oldToken := sasTokenSignedWith(messagingRootRule, *before.PrimaryKey, audience, time.Now().Add(time.Hour))

	status, body := serviceBusHTTPStatus(t, namespace, oldToken)
	require.Equal(t, http.StatusCreated, status, "the pre-rotation key must authenticate: %s", body)

	// Rotate the primary key. The secondary is untouched, so a token signed
	// with it keeps working — the staged-rotation guarantee operators rely on.
	secondaryToken := sasTokenSignedWith(messagingRootRule, *before.SecondaryKey, audience, time.Now().Add(time.Hour))
	_, err = client.RegenerateKeys(ctx, rg, namespace, messagingRootRule, armservicebus.RegenerateAccessKeyParameters{
		KeyType: to.Ptr(armservicebus.KeyTypePrimaryKey),
	}, nil)
	require.NoError(t, err)

	status, body = serviceBusHTTPStatus(t, namespace, oldToken)
	assert.Equal(t, http.StatusUnauthorized, status,
		"a token signed with the replaced primary key must stop authenticating: %s", body)
	assert.Contains(t, body, "InvalidSignature", body)

	status, body = serviceBusHTTPStatus(t, namespace, secondaryToken)
	assert.Equal(t, http.StatusCreated, status,
		"the untouched secondary key must keep authenticating through a primary rotation: %s", body)

	after, err := client.ListKeys(ctx, rg, namespace, messagingRootRule, nil)
	require.NoError(t, err)
	newToken := sasTokenSignedWith(messagingRootRule, *after.PrimaryKey, audience, time.Now().Add(time.Hour))
	status, body = serviceBusHTTPStatus(t, namespace, newToken)
	assert.Equal(t, http.StatusCreated, status,
		"a token signed with the rotated primary key must authenticate: %s", body)
}

// TestServiceBusAMQP_RefusesUnverifiableToken drives the AMQP transport with a
// connection string carrying key material no authorization rule holds. The CBS
// put-token handshake must refuse it, and the official client must surface the
// failure rather than proceeding to send.
func TestServiceBusAMQP_RefusesUnverifiableToken(t *testing.T) {
	namespace := "sdk-sb-amqpauth"
	// Provision the namespace so the only thing wrong is the key.
	serviceBusNamespaceKey(t, namespace)

	wrongKey := base64.StdEncoding.EncodeToString([]byte("this-is-not-the-namespace-key!!!"))
	conn := messagingConnectionString(namespace, namespace+".servicebus.localhost", wrongKey)
	client, err := azservicebus.NewClientFromConnectionString(conn, &azservicebus.ClientOptions{
		CustomEndpoint: sbAMQPEndpoint,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- test-only self-signed simulator certificate.
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close(ctx) })

	sender, err := client.NewSender("authqueue", nil)
	if err == nil {
		err = sender.SendMessage(ctx, &azservicebus.Message{Body: []byte("must not be delivered")}, nil)
	}
	require.Error(t, err, "a connection whose key no authorization rule holds must not send")
	assert.Contains(t, strings.ToLower(err.Error()), "unauthorized",
		"the failure must name the unauthorized-access condition: %v", err)
}
