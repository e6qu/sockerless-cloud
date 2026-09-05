package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// eventGridSASFor builds a Shared Access Signature exactly as Microsoft's
// published C# and Python generators do: URL-encode the resource URI and the
// UTC expiry, sign the resulting "r=<resource>&e=<expiry>" string with
// HMAC-SHA256 under the base64-DECODED access key, and append the URL-encoded
// base64 signature as `s`.
func eventGridSASFor(t *testing.T, resource string, expiry time.Time, key string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("decode access key: %v", err)
	}
	encodedResource := url.QueryEscape(resource)
	encodedExpiry := url.QueryEscape(expiry.UTC().Format("1/2/2006 3:04:05 PM"))
	unsigned := "r=" + encodedResource + "&e=" + encodedExpiry
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte(unsigned))
	return unsigned + "&s=" + url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

// eventGridPublishBody is one well-formed EventGridEvent batch.
func eventGridPublishBody(id string) string {
	return `[{"id":"` + id + `","eventType":"sockerless.test","subject":"/auth","eventTime":"2026-06-02T00:00:00Z","data":{"ok":true},"dataVersion":"1"}]`
}

// eventGridPublishRequest addresses a publishing resource's advertised
// endpoint. The request carries the endpoint host so the shared-host data
// plane routes it, which is what a real publisher's URL resolves to.
func eventGridPublishRequest(t *testing.T, endpoint, body string) *http.Request {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint %q: %v", endpoint, err)
	}
	req := eventGridTestRequest(http.MethodPost, endpoint+"?api-version=2018-01-01", body)
	req.Host = parsed.Host
	return req
}

// eventGridCreatePublishTopic creates a custom topic and returns its resource
// ID, advertised endpoint, and the two access keys the control plane serves.
func eventGridCreatePublishTopic(t *testing.T, srv *sim.Server, name string, properties string) (string, string, map[string]string) {
	t.Helper()
	base := "http://localhost:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/" + name
	body := `{"location":"eastus"}`
	if properties != "" {
		body = `{"location":"eastus","properties":` + properties + `}`
	}
	data := serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, base+"?api-version=2021-12-01", body), http.StatusCreated)
	var topic EventGridTopic
	if err := json.Unmarshal(data, &topic); err != nil {
		t.Fatalf("decode topic: %v", err)
	}
	endpoint, _ := topic.Properties["endpoint"].(string)
	if endpoint == "" {
		t.Fatal("created topic advertised no endpoint")
	}
	return topic.ID, endpoint, eventGridTestListKeys(t, srv, topic.ID)
}

// eventGridPublishStatus serves one publish and returns its status and body,
// so a test can assert on a refusal without failing the helper.
func eventGridPublishStatus(t *testing.T, srv *sim.Server, req *http.Request) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read publish response: %v", err)
	}
	return resp.StatusCode, data
}

// assertEventGridUnauthorized checks the 401 body Event Grid answers: an
// Unauthorized error carrying the message, with a details array repeating both.
func assertEventGridUnauthorized(t *testing.T, status int, body []byte, wantMessage string) {
	t.Helper()
	if status != http.StatusUnauthorized {
		t.Fatalf("publish status = %d, want 401: %s", status, body)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode 401 body %s: %v", body, err)
	}
	if envelope.Error.Code != "Unauthorized" {
		t.Fatalf("error code = %q, want Unauthorized", envelope.Error.Code)
	}
	if envelope.Error.Message != wantMessage {
		t.Fatalf("error message = %q, want %q", envelope.Error.Message, wantMessage)
	}
	if len(envelope.Error.Details) != 1 {
		t.Fatalf("error details length = %d, want 1: %s", len(envelope.Error.Details), body)
	}
	if envelope.Error.Details[0].Code != "Unauthorized" || envelope.Error.Details[0].Message != wantMessage {
		t.Fatalf("error details = %+v, want the error's own code and message", envelope.Error.Details[0])
	}
}

// TestEventGridPublishRefusesAnUnauthenticatedCaller pins the credential
// contract of the publish data plane: a request with no credential, a wrong
// key, a tampered or expired signature, and a signature for another resource
// are all refused with Event Grid's own 401, while both key slots, both key
// header spellings, and a well-formed signature publish.
func TestEventGridPublishRefusesAnUnauthenticatedCaller(t *testing.T) {
	srv := newEventGridTestServer(t)
	_, endpoint, keys := eventGridCreatePublishTopic(t, srv, "eg-auth", "")
	host := mustParseHost(t, endpoint)

	t.Run("no credential", func(t *testing.T) {
		status, body := eventGridPublishStatus(t, srv, eventGridPublishRequest(t, endpoint, eventGridPublishBody("e1")))
		assertEventGridUnauthorized(t, status, body, eventGridMissingCredentialMessage)
	})

	t.Run("wrong key", func(t *testing.T) {
		req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e2"))
		req.Header.Set(eventGridKeyHeader, base64.StdEncoding.EncodeToString(make([]byte, 32)))
		status, body := eventGridPublishStatus(t, srv, req)
		assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))
	})

	for _, slot := range []string{"key1", "key2"} {
		t.Run("key header "+slot, func(t *testing.T) {
			req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e3-"+slot))
			req.Header.Set(eventGridKeyHeader, keys[slot])
			if status, body := eventGridPublishStatus(t, srv, req); status != http.StatusOK {
				t.Fatalf("publish with %s status = %d, want 200: %s", slot, status, body)
			}
		})
	}

	t.Run("key query parameter", func(t *testing.T) {
		req := eventGridTestRequest(http.MethodPost,
			endpoint+"?api-version=2018-01-01&"+eventGridKeyHeader+"="+url.QueryEscape(keys["key1"]),
			eventGridPublishBody("e4"))
		req.Host = host
		if status, body := eventGridPublishStatus(t, srv, req); status != http.StatusOK {
			t.Fatalf("publish with key query parameter status = %d, want 200: %s", status, body)
		}
	})

	t.Run("shared access signature", func(t *testing.T) {
		token := eventGridSASFor(t, endpoint, time.Now().Add(time.Hour), keys["key1"])
		req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e5"))
		req.Header.Set(eventGridTokenHeader, token)
		if status, body := eventGridPublishStatus(t, srv, req); status != http.StatusOK {
			t.Fatalf("publish with aeg-sas-token status = %d, want 200: %s", status, body)
		}

		authorized := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e6"))
		authorized.Header.Set("Authorization", eventGridSASScheme+" "+token)
		if status, body := eventGridPublishStatus(t, srv, authorized); status != http.StatusOK {
			t.Fatalf("publish with Authorization SharedAccessSignature status = %d, want 200: %s", status, body)
		}
	})

	t.Run("signature signed with the second key", func(t *testing.T) {
		token := eventGridSASFor(t, endpoint, time.Now().Add(time.Hour), keys["key2"])
		req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e7"))
		req.Header.Set(eventGridTokenHeader, token)
		if status, body := eventGridPublishStatus(t, srv, req); status != http.StatusOK {
			t.Fatalf("publish signed with key2 status = %d, want 200: %s", status, body)
		}
	})

	t.Run("expired signature", func(t *testing.T) {
		token := eventGridSASFor(t, endpoint, time.Now().Add(-time.Minute), keys["key1"])
		req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e8"))
		req.Header.Set(eventGridTokenHeader, token)
		status, body := eventGridPublishStatus(t, srv, req)
		assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))
	})

	t.Run("tampered signature", func(t *testing.T) {
		token := eventGridSASFor(t, endpoint, time.Now().Add(time.Hour), keys["key1"])
		tampered := token[:len(token)-4] + "AAA%3D"
		req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e9"))
		req.Header.Set(eventGridTokenHeader, tampered)
		status, body := eventGridPublishStatus(t, srv, req)
		assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))
	})

	t.Run("signature covering another resource", func(t *testing.T) {
		// Signed correctly, with this topic's own key, but for a resource URI
		// that does not prefix the request: the token authorizes nothing here.
		token := eventGridSASFor(t, "http://other-topic.eventgrid.localhost:4568/api/events",
			time.Now().Add(time.Hour), keys["key1"])
		req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e10"))
		req.Header.Set(eventGridTokenHeader, token)
		status, body := eventGridPublishStatus(t, srv, req)
		assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))
	})

	t.Run("malformed signature", func(t *testing.T) {
		req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("e11"))
		req.Header.Set(eventGridTokenHeader, "not-a-shared-access-signature")
		status, body := eventGridPublishStatus(t, srv, req)
		assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))
	})

	// The body is only read once the credential is accepted, so an
	// unauthenticated caller learns nothing about the event schema.
	t.Run("credential is checked before the body", func(t *testing.T) {
		req := eventGridPublishRequest(t, endpoint, `{"not":"an array"}`)
		status, body := eventGridPublishStatus(t, srv, req)
		assertEventGridUnauthorized(t, status, body, eventGridMissingCredentialMessage)
	})

}

// TestEventGridRegenerateKeyInvalidatesTheCredentialsItReplaces proves the
// rotation contract the data plane now enforces: the key and every signature
// derived from it stop authenticating the moment the slot is regenerated,
// while the untouched slot and the newly issued key keep working.
func TestEventGridRegenerateKeyInvalidatesTheCredentialsItReplaces(t *testing.T) {
	srv := newEventGridTestServer(t)
	topicID, endpoint, keys := eventGridCreatePublishTopic(t, srv, "eg-rotate", "")
	host := mustParseHost(t, endpoint)

	staleToken := eventGridSASFor(t, endpoint, time.Now().Add(time.Hour), keys["key1"])

	regenURL := "http://localhost:4568" + topicID + "/regenerateKey?api-version=2021-12-01"
	data := serveEventGridTestRequest(t, srv,
		eventGridTestRequest(http.MethodPost, regenURL, `{"keyName":"key1"}`), http.StatusOK)
	var rotated map[string]string
	if err := json.Unmarshal(data, &rotated); err != nil {
		t.Fatalf("decode regenerateKey: %v", err)
	}
	if rotated["key1"] == keys["key1"] {
		t.Fatal("regenerateKey returned the key it was asked to replace")
	}
	if rotated["key2"] != keys["key2"] {
		t.Fatal("regenerateKey rotated the slot it was not asked to")
	}

	stale := eventGridPublishRequest(t, endpoint, eventGridPublishBody("r1"))
	stale.Header.Set(eventGridKeyHeader, keys["key1"])
	status, body := eventGridPublishStatus(t, srv, stale)
	assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))

	staleSAS := eventGridPublishRequest(t, endpoint, eventGridPublishBody("r2"))
	staleSAS.Header.Set(eventGridTokenHeader, staleToken)
	status, body = eventGridPublishStatus(t, srv, staleSAS)
	assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))

	fresh := eventGridPublishRequest(t, endpoint, eventGridPublishBody("r3"))
	fresh.Header.Set(eventGridKeyHeader, rotated["key1"])
	if status, body := eventGridPublishStatus(t, srv, fresh); status != http.StatusOK {
		t.Fatalf("publish with the regenerated key status = %d, want 200: %s", status, body)
	}

	untouched := eventGridPublishRequest(t, endpoint, eventGridPublishBody("r4"))
	untouched.Header.Set(eventGridKeyHeader, keys["key2"])
	if status, body := eventGridPublishStatus(t, srv, untouched); status != http.StatusOK {
		t.Fatalf("publish with the untouched key2 status = %d, want 200: %s", status, body)
	}
}

// TestEventGridPublishAcceptsAMicrosoftEntraAccessToken covers the third
// credential: a bearer token minted for the Event Grid data-plane audience
// publishes, while a token minted for Azure Resource Manager does not.
func TestEventGridPublishAcceptsAMicrosoftEntraAccessToken(t *testing.T) {
	srv := newEventGridTestServer(t)
	_, endpoint, keys := eventGridCreatePublishTopic(t, srv, "eg-entra", "")
	host := mustParseHost(t, endpoint)

	now := time.Now().UTC()
	eventGridToken, err := mintAzureSimJWT("tenant-xyz", eventGridEntraAudience, now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint Event Grid audience token: %v", err)
	}
	armToken, err := mintAzureSimJWT("tenant-xyz", defaultAzureTokenAudience, now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint Azure Resource Manager audience token: %v", err)
	}

	req := eventGridPublishRequest(t, endpoint, eventGridPublishBody("b1"))
	req.Header.Set("Authorization", eventGridBearerScheme+" "+eventGridToken)
	if status, body := eventGridPublishStatus(t, srv, req); status != http.StatusOK {
		t.Fatalf("publish with an Event Grid bearer token status = %d, want 200: %s", status, body)
	}

	wrongAudience := eventGridPublishRequest(t, endpoint, eventGridPublishBody("b2"))
	wrongAudience.Header.Set("Authorization", eventGridBearerScheme+" "+armToken)
	status, body := eventGridPublishStatus(t, srv, wrongAudience)
	assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))

	garbage := eventGridPublishRequest(t, endpoint, eventGridPublishBody("b3"))
	garbage.Header.Set("Authorization", eventGridBearerScheme+" not.a.jwt")
	status, body = eventGridPublishStatus(t, srv, garbage)
	assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))

	// The key path is unaffected by the presence of the bearer scheme support.
	keyed := eventGridPublishRequest(t, endpoint, eventGridPublishBody("b4"))
	keyed.Header.Set(eventGridKeyHeader, keys["key1"])
	if status, body := eventGridPublishStatus(t, srv, keyed); status != http.StatusOK {
		t.Fatalf("publish with the access key status = %d, want 200: %s", status, body)
	}
}

// TestEventGridDisableLocalAuthRefusesKeysAndSignatures covers the topic
// property that turns key-based authentication off: with it set the keys the
// control plane still serves authorize nothing, and Microsoft Entra ID is the
// only credential the publish accepts.
func TestEventGridDisableLocalAuthRefusesKeysAndSignatures(t *testing.T) {
	srv := newEventGridTestServer(t)
	_, endpoint, keys := eventGridCreatePublishTopic(t, srv, "eg-nolocal", `{"disableLocalAuth":true}`)
	host := mustParseHost(t, endpoint)

	keyed := eventGridPublishRequest(t, endpoint, eventGridPublishBody("d1"))
	keyed.Header.Set(eventGridKeyHeader, keys["key1"])
	status, body := eventGridPublishStatus(t, srv, keyed)
	assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))

	signed := eventGridPublishRequest(t, endpoint, eventGridPublishBody("d2"))
	signed.Header.Set(eventGridTokenHeader, eventGridSASFor(t, endpoint, time.Now().Add(time.Hour), keys["key1"]))
	status, body = eventGridPublishStatus(t, srv, signed)
	assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))

	now := time.Now().UTC()
	token, err := mintAzureSimJWT("tenant-xyz", eventGridEntraAudience, now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint Event Grid audience token: %v", err)
	}
	bearer := eventGridPublishRequest(t, endpoint, eventGridPublishBody("d3"))
	bearer.Header.Set("Authorization", eventGridBearerScheme+" "+token)
	if status, body := eventGridPublishStatus(t, srv, bearer); status != http.StatusOK {
		t.Fatalf("publish with Microsoft Entra ID status = %d, want 200: %s", status, body)
	}
}

// TestEventGridDomainPublishAuthenticatesAndRoutesByDomainTopic covers the
// second publish path: a domain advertises its own endpoint, serves its own
// two keys, refuses an unauthenticated publish the same way a custom topic
// does, and routes each authenticated event to the domain topic it names.
func TestEventGridDomainPublishAuthenticatesAndRoutesByDomainTopic(t *testing.T) {
	srv := newEventGridTestServer(t)

	deliveries := make(chan []map[string]any, 8)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var events []map[string]any
		if err := json.Unmarshal(body, &events); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deliveries <- events
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)

	domainBase := "http://localhost:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/domains/eg-domain"
	data := serveEventGridTestRequest(t, srv,
		eventGridTestRequest(http.MethodPut, domainBase+"?api-version=2021-12-01", `{"location":"eastus"}`), http.StatusCreated)
	var domain EventGridTopic
	if err := json.Unmarshal(data, &domain); err != nil {
		t.Fatalf("decode domain: %v", err)
	}
	endpoint, _ := domain.Properties["endpoint"].(string)
	if endpoint == "" {
		t.Fatal("created domain advertised no endpoint")
	}
	host := mustParseHost(t, endpoint)
	keys := eventGridTestListKeys(t, srv, domain.ID)

	serveEventGridTestRequest(t, srv,
		eventGridTestRequest(http.MethodPut, domainBase+"/topics/orders?api-version=2021-12-01", ""), http.StatusCreated)
	subURL := domainBase + "/topics/orders/providers/Microsoft.EventGrid/eventSubscriptions/orders-sub?api-version=2021-12-01"
	subBody := `{"properties":{"destination":{"endpointType":"WebHook","properties":{"endpointUrl":"` + hook.URL + `"}},"eventDeliverySchema":"EventGridSchema"}}`
	serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, subURL, subBody), http.StatusCreated)
	select {
	case <-deliveries:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the subscription validation delivery")
	}

	domainEvent := func(id, topic string) string {
		return fmt.Sprintf(`[{"id":%q,"topic":%q,"eventType":"sockerless.test","subject":"/orders","eventTime":"2026-06-02T00:00:00Z","data":{"ok":true},"dataVersion":"1"}]`, id, topic)
	}

	unauthenticated := eventGridPublishRequest(t, endpoint, domainEvent("dm1", "orders"))
	status, body := eventGridPublishStatus(t, srv, unauthenticated)
	assertEventGridUnauthorized(t, status, body, eventGridMissingCredentialMessage)

	wrongKey := eventGridPublishRequest(t, endpoint, domainEvent("dm2", "orders"))
	wrongKey.Header.Set(eventGridKeyHeader, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	status, body = eventGridPublishStatus(t, srv, wrongKey)
	assertEventGridUnauthorized(t, status, body, eventGridNotAuthorizedMessage(host))

	// A domain event must name the domain topic it belongs to.
	missingTopic := eventGridPublishRequest(t, endpoint, eventGridPublishBody("dm3"))
	missingTopic.Header.Set(eventGridKeyHeader, keys["key1"])
	if status, body := eventGridPublishStatus(t, srv, missingTopic); status != http.StatusBadRequest {
		t.Fatalf("domain publish without a topic member status = %d, want 400: %s", status, body)
	}

	signed := eventGridPublishRequest(t, endpoint, domainEvent("dm4", "orders"))
	signed.Header.Set(eventGridTokenHeader, eventGridSASFor(t, endpoint, time.Now().Add(time.Hour), keys["key2"]))
	if status, body := eventGridPublishStatus(t, srv, signed); status != http.StatusOK {
		t.Fatalf("domain publish with a signature status = %d, want 200: %s", status, body)
	}
	select {
	case events := <-deliveries:
		if len(events) != 1 || events[0]["id"] != "dm4" {
			t.Fatalf("delivered %v, want the event published to the orders domain topic", events)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the domain-routed delivery")
	}
}

func mustParseHost(t *testing.T, endpoint string) string {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint %q: %v", endpoint, err)
	}
	return parsed.Host
}
