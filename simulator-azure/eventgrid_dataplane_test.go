package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

func newEventGridTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new azure sim server: %v", err)
	}
	registerEventGrid(srv)
	return srv
}

func eventGridTestRequest(method, rawURL, body string) *http.Request {
	req := httptest.NewRequest(method, rawURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func serveEventGridTestRequest(t *testing.T, srv *sim.Server, req *http.Request, want int) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s status = %d, want %d: %s", req.Method, req.URL.String(), resp.StatusCode, want, data)
	}
	return data
}

func TestEventGridTopicEndpointUsesSharedHostDataPlane(t *testing.T) {
	srv := newEventGridTestServer(t)
	base := "http://127.0.0.1:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/eg-topic"

	body := `{"location":"eastus","properties":{"inputSchema":"EventGridSchema"}}`
	data := serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, base+"?api-version=2021-12-01", body), http.StatusCreated)

	var topic EventGridTopic
	if err := json.Unmarshal(data, &topic); err != nil {
		t.Fatalf("decode create topic: %v", err)
	}
	endpoint, _ := topic.Properties["endpoint"].(string)
	if endpoint != "http://eg-topic.eventgrid.localhost:4568/api/events" {
		t.Fatalf("create endpoint = %q, want shared host-dispatched endpoint", endpoint)
	}
	assertNoEventGridLocalListenerEndpoint(t, endpoint)

	req := eventGridTestRequest(http.MethodGet, strings.Replace(base, "127.0.0.1", "localhost", 1)+"?api-version=2021-12-01", "")
	data = serveEventGridTestRequest(t, srv, req, http.StatusOK)
	if err := json.Unmarshal(data, &topic); err != nil {
		t.Fatalf("decode get topic: %v", err)
	}
	endpoint, _ = topic.Properties["endpoint"].(string)
	if endpoint != "http://eg-topic.eventgrid.localhost:4568/api/events" {
		t.Fatalf("get endpoint = %q, want shared host-dispatched endpoint", endpoint)
	}
	assertNoEventGridLocalListenerEndpoint(t, endpoint)

	listURL := "http://localhost:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics?api-version=2021-12-01"
	data = serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodGet, listURL, ""), http.StatusOK)
	var list struct {
		Value []EventGridTopic `json:"value"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("decode topic list: %v", err)
	}
	if len(list.Value) != 1 {
		t.Fatalf("topic list length = %d, want 1", len(list.Value))
	}
	listEndpoint, _ := list.Value[0].Properties["endpoint"].(string)
	if listEndpoint != "http://eg-topic.eventgrid.localhost:4568/api/events" {
		t.Fatalf("list endpoint = %q, want shared host-dispatched endpoint", listEndpoint)
	}
	assertNoEventGridLocalListenerEndpoint(t, listEndpoint)
}

func TestEventGridAdvertisedEndpointPublishesThroughSharedHostDataPlane(t *testing.T) {
	srv := newEventGridTestServer(t)

	deliveries := make(chan []map[string]any, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read webhook body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var events []map[string]any
		if err := json.Unmarshal(body, &events); err != nil {
			t.Errorf("decode webhook events: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deliveries <- events
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)

	topicURL := "http://localhost:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/eg-publish?api-version=2021-12-01"
	data := serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, topicURL, `{"location":"eastus"}`), http.StatusCreated)
	var topic EventGridTopic
	if err := json.Unmarshal(data, &topic); err != nil {
		t.Fatalf("decode topic: %v", err)
	}
	endpoint, _ := topic.Properties["endpoint"].(string)
	assertNoEventGridLocalListenerEndpoint(t, endpoint)

	subURL := "http://localhost:4568" + topic.ID + "/providers/Microsoft.EventGrid/eventSubscriptions/sub1?api-version=2021-12-01"
	subBody := `{"properties":{"destination":{"endpointType":"WebHook","properties":{"endpointUrl":"` + hook.URL + `"}},"eventDeliverySchema":"EventGridSchema"}}`
	serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, subURL, subBody), http.StatusCreated)

	select {
	case <-deliveries:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscription validation delivery")
	}

	publishURL, err := url.Parse(endpoint + "?api-version=2018-01-01")
	if err != nil {
		t.Fatalf("parse endpoint %q: %v", endpoint, err)
	}
	req := eventGridTestRequest(http.MethodPost, publishURL.String(), `[{"id":"evt-1","eventType":"sockerless.test","subject":"/topic","eventTime":"2026-06-02T00:00:00Z","data":{"ok":true},"dataVersion":"1"}]`)
	serveEventGridTestRequest(t, srv, req, http.StatusOK)

	select {
	case events := <-deliveries:
		if len(events) != 1 {
			t.Fatalf("delivered events length = %d, want 1", len(events))
		}
		if events[0]["id"] != "evt-1" {
			t.Fatalf("delivered event id = %v, want evt-1", events[0]["id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for published event delivery")
	}
}

func assertNoEventGridLocalListenerEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint %q: %v", endpoint, err)
	}
	if u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" {
		t.Fatalf("endpoint %q leaked a simulator-local listener host", endpoint)
	}
}
