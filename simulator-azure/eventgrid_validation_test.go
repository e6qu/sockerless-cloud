package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// eventGridCreateWebhookSubscription creates a topic and a webhook
// subscription on it, returning the subscription URL and the operation URL the
// create advertised.
func eventGridCreateWebhookSubscription(t *testing.T, srv *sim.Server, topic, hookURL, schema string) (string, string) {
	t.Helper()
	topicURL := "http://localhost:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/" + topic + "?api-version=2022-06-15"
	serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, topicURL, `{"location":"eastus"}`), http.StatusCreated)
	subURL := strings.Replace(topicURL, "?", "/providers/Microsoft.EventGrid/eventSubscriptions/sub1?", 1)
	body := `{"properties":{"destination":{"endpointType":"WebHook","properties":{"endpointUrl":"` + hookURL + `"}},"eventDeliverySchema":"` + schema + `"}}`
	req := eventGridTestRequest(http.MethodPut, subURL, body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create subscription: %d %s", rec.Code, rec.Body)
	}
	var created EventGridEventSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if state := created.Properties["provisioningState"]; state != "Creating" {
		t.Fatalf("a webhook subscription answers provisioningState %v while its handshake runs, want Creating", state)
	}
	opURL := rec.Header().Get("Azure-AsyncOperation")
	if !strings.Contains(opURL, "/providers/Microsoft.EventGrid/locations/eastus/operationStatuses/") {
		t.Fatalf("Azure-AsyncOperation = %q, want the location's operationStatuses URL", opURL)
	}
	return subURL, opURL
}

func eventGridSubscriptionState(t *testing.T, srv *sim.Server, subURL string) string {
	t.Helper()
	data := serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodGet, subURL, ""), http.StatusOK)
	var es EventGridEventSubscription
	if err := json.Unmarshal(data, &es); err != nil {
		t.Fatal(err)
	}
	state, _ := es.Properties["provisioningState"].(string)
	return state
}

func eventGridOperation(t *testing.T, srv *sim.Server, opURL string) AsyncOperationStatus {
	t.Helper()
	data := serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodGet, opURL, ""), http.StatusOK)
	var op AsyncOperationStatus
	if err := json.Unmarshal(data, &op); err != nil {
		t.Fatal(err)
	}
	return op
}

// An endpoint that answers the SubscriptionValidationEvent without echoing its
// code leaves the subscription AwaitingManualAction, receiving nothing, until
// its validationUrl is visited.
func TestEventGridWebhookManualValidation(t *testing.T) {
	srv := newEventGridTestServer(t)
	validationURLs := make(chan string, 1)
	notifications := make(chan struct{}, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("aeg-event-type") == "SubscriptionValidation" {
			var events []struct {
				EventType string `json:"eventType"`
				Data      struct {
					ValidationURL string `json:"validationUrl"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &events); err != nil || len(events) != 1 || events[0].EventType != "Microsoft.EventGrid.SubscriptionValidationEvent" {
				t.Errorf("validation request body %s", body)
			}
			validationURLs <- events[0].Data.ValidationURL
			w.WriteHeader(http.StatusOK)
			return
		}
		notifications <- struct{}{}
	}))
	t.Cleanup(hook.Close)

	subURL, opURL := eventGridCreateWebhookSubscription(t, srv, "eg-manual", hook.URL, "EventGridSchema")
	validationURL := <-validationURLs
	bg.Await()
	if state := eventGridSubscriptionState(t, srv, subURL); state != "AwaitingManualAction" {
		t.Fatalf("subscription reads %q after a handshake without the echo, want AwaitingManualAction", state)
	}
	if op := eventGridOperation(t, srv, opURL); op.Status != "InProgress" {
		t.Fatalf("operation reads %q while the subscription awaits manual validation, want InProgress", op.Status)
	}
	eventGridSubmit("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/eg-manual",
		[]json.RawMessage{json.RawMessage(`{"id":"early"}`)})
	bg.Await()
	if len(notifications) != 0 {
		t.Fatal("an unvalidated subscription received an event")
	}

	parsed, err := url.Parse(validationURL)
	if err != nil {
		t.Fatalf("validationUrl %q: %v", validationURL, err)
	}
	if parsed.Host != "localhost:4568" || parsed.Query().Get("id") == "" {
		t.Fatalf("validationUrl %q is not served by the simulator", validationURL)
	}
	forged := *parsed
	query := forged.Query()
	query.Set("token", "forged")
	forged.RawQuery = query.Encode()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, forged.String(), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a validationUrl with a forged token answered %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, validationURL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET validationUrl: %d %s", rec.Code, rec.Body)
	}
	if state := eventGridSubscriptionState(t, srv, subURL); state != "Succeeded" {
		t.Fatalf("subscription reads %q after its validationUrl was visited, want Succeeded", state)
	}
	if op := eventGridOperation(t, srv, opURL); op.Status != "Succeeded" {
		t.Fatalf("operation reads %q after manual validation, want Succeeded", op.Status)
	}
}

// An endpoint that refuses the handshake fails the subscription and its
// operation with Event Grid's Url validation error.
func TestEventGridWebhookValidationFailure(t *testing.T) {
	srv := newEventGridTestServer(t)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(hook.Close)

	subURL, opURL := eventGridCreateWebhookSubscription(t, srv, "eg-refused", hook.URL, "EventGridSchema")
	bg.Await()
	if state := eventGridSubscriptionState(t, srv, subURL); state != "Failed" {
		t.Fatalf("subscription reads %q after the endpoint refused the handshake, want Failed", state)
	}
	op := eventGridOperation(t, srv, opURL)
	if op.Status != "Failed" || op.Error == nil || op.Error.Code != "Url validation" || !strings.Contains(op.Error.Message, hook.URL) {
		t.Fatalf("operation after a refused handshake = %+v, want Failed with Url validation naming the endpoint", op)
	}
}

// A CloudEvents subscription validates through the abuse-protection OPTIONS
// handshake: an endpoint allowing Event Grid's origin succeeds.
func TestEventGridCloudEventsAbuseProtectionHandshake(t *testing.T) {
	srv := newEventGridTestServer(t)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions || r.Header.Get("WebHook-Request-Origin") != "eventgrid.azure.net" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("WebHook-Allowed-Origin", "eventgrid.azure.net")
	}))
	t.Cleanup(hook.Close)

	subURL, opURL := eventGridCreateWebhookSubscription(t, srv, "eg-cloudevents", hook.URL, "CloudEventSchemaV1_0")
	bg.Await()
	if state := eventGridSubscriptionState(t, srv, subURL); state != "Succeeded" {
		t.Fatalf("CloudEvents subscription reads %q after its endpoint allowed the origin, want Succeeded", state)
	}
	if op := eventGridOperation(t, srv, opURL); op.Status != "Succeeded" {
		t.Fatalf("operation = %+v, want Succeeded", op)
	}
}
