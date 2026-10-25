package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAzureReadRequiredJSONRefusesEmptyAndMalformedBodies(t *testing.T) {
	var v struct {
		Kty string `json:"kty"`
	}
	if err := azureReadRequiredJSON(httptest.NewRequest(http.MethodPost, "/", nil), &v); !errors.Is(err, io.EOF) {
		t.Fatalf("empty body: err = %v, want io.EOF", err)
	}
	if err := azureReadRequiredJSON(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"kty":`)), &v); err == nil {
		t.Fatal("truncated body: err = nil, want a parse error")
	}
	if err := azureReadRequiredJSON(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"kty":"RSA"} trailing`)), &v); err == nil {
		t.Fatal("body with trailing data: err = nil, want a parse error")
	}
	if err := azureReadRequiredJSON(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"kty":"RSA"}`)), &v); err != nil || v.Kty != "RSA" {
		t.Fatalf("valid body: err = %v, kty = %q", err, v.Kty)
	}
}

func TestEventGridRegenerateKeyRefusesMalformedBody(t *testing.T) {
	srv := newEventGridTestServer(t)
	base := "http://127.0.0.1:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/eg-keys"
	serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, base+"?api-version=2021-12-01",
		`{"location":"eastus"}`), http.StatusCreated)

	data := serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPost, base+"/regenerateKey?api-version=2021-12-01",
		`{"keyName":`), http.StatusBadRequest)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Error.Code != "InvalidRequestContent" {
		t.Fatalf("error = %s, want code InvalidRequestContent", data)
	}

	serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPost, base+"/regenerateKey?api-version=2021-12-01",
		`{"keyName":"key2"}`), http.StatusOK)
}

func TestEventGridDeleteAnswersThroughTheSharedARMShape(t *testing.T) {
	srv := newEventGridTestServer(t)
	base := "http://127.0.0.1:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/systemTopics/st"
	serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodPut, base+"?api-version=2021-12-01",
		`{"location":"global","properties":{"source":"/subscriptions/sub","topicType":"Microsoft.Resources.Subscriptions"}}`), http.StatusCreated)
	serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodDelete, base+"?api-version=2021-12-01", ""), http.StatusNoContent)
	data := serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodDelete, base+"?api-version=2021-12-01", ""), http.StatusNotFound)
	var envelope struct {
		Error struct {
			Code, Message string
		} `json:"error"`
	}
	want := `system topic "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/systemTopics/st" not found`
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Error.Code != "ResourceNotFound" || envelope.Error.Message != want {
		t.Fatalf("second delete = %s, want ResourceNotFound %s", data, want)
	}
	data = serveEventGridTestRequest(t, srv, eventGridTestRequest(http.MethodGet,
		"http://127.0.0.1:4568/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/systemTopics?api-version=2021-12-01", ""), http.StatusOK)
	if strings.TrimSpace(string(data)) != `{"value":[]}` {
		t.Fatalf("list after delete = %s, want an empty value array", data)
	}
}
