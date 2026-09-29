package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

func runtimeAPICall(handler http.HandlerFunc, requestID string, body *strings.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/2018-06-01/runtime/invocation/"+requestID+"/response", body)
	request.SetPathValue("id", requestID)
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func expectRuntimeAPIError(t *testing.T, name string, recorder *httptest.ResponseRecorder, errorType, message string) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("%s answered %s: %v", name, recorder.Body.String(), err)
	}
	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Content-Type") != "application/json" || len(document) != 2 ||
		document["errorType"] != errorType || document["errorMessage"] != message {
		t.Fatalf("%s = %d %s %s, want 400 application/json {errorMessage: %q, errorType: %q}",
			name, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String(), message, errorType)
	}
}

// The Runtime API answers a runtime with its own ErrorResponse document,
// {errorMessage, errorType}, as AWS Lambda's runtime interface does — never an
// AWS service error with __type and message.
func TestLambdaRuntimeAPIAnswersWithItsOwnErrorShape(t *testing.T) {
	inv := &lambdaInvocation{
		RequestID:   "8476a536-e9f4-11e8-9739-2dfe598c3fcd",
		initialized: make(chan struct{}),
		done:        make(chan struct{}),
	}
	sidecar := &runtimeAPISidecar{inv: inv}

	for name, handler := range map[string]http.HandlerFunc{"response": sidecar.handleResponse, "error": sidecar.handleInvocationError} {
		expectRuntimeAPIError(t, name+" for another request ID",
			runtimeAPICall(handler, "not-the-invocation", strings.NewReader(`{}`)), "InvalidRequestID", "Invalid request ID")

		truncated := httptest.NewRequest(http.MethodPost, "/", iotest.ErrReader(errors.New("connection reset")))
		truncated.SetPathValue("id", inv.RequestID)
		recorder := httptest.NewRecorder()
		handler(recorder, truncated)
		expectRuntimeAPIError(t, name+" with a truncated body", recorder, "TruncatedHTTPRequest", "HTTP request detected as truncated")
	}
	initError := httptest.NewRecorder()
	sidecar.handleInitError(initError, httptest.NewRequest(http.MethodPost, "/", iotest.ErrReader(errors.New("connection reset"))))
	expectRuntimeAPIError(t, "init error with a truncated body", initError, "TruncatedHTTPRequest", "HTTP request detected as truncated")
	select {
	case <-inv.done:
		t.Fatal("a refused call completed the invocation")
	default:
	}

	accepted := runtimeAPICall(sidecar.handleResponse, inv.RequestID, strings.NewReader(`{"ok":true}`))
	if accepted.Code != http.StatusAccepted || accepted.Body.String() != `{"status":"OK"}` || string(inv.response) != `{"ok":true}` {
		t.Fatalf("response = %d %s recorded %s, want 202 {\"status\":\"OK\"}", accepted.Code, accepted.Body.String(), inv.response)
	}
}
