package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// An operation whose final state comes via Location answers with its own
// result at that URL — the collection, the resource, whatever the operation
// produces — and not with the status envelope. These tests hold the operation
// store to that, because the difference is what decides whether a generated
// client can read the operation at all: on a synchronous answer azcore selects
// its no-op poller, which overwrites the response the client pre-built, and for
// an operation whose result type is a pager that leaves a pager with a nil
// handler behind.

func azureLROTestStore(t *testing.T) {
	t.Helper()
	azureAsyncOps = sim.MakeStore[AsyncOperationStatus](nil, "azure_async_ops")
}

// pollAzureOperation drives one poll of an operation URL and returns the status
// code and body, the way a client following Location does.
func pollAzureOperation(t *testing.T, path string) (int, []byte) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.SetPathValue("subscriptionId", "00000000-0000-0000-0000-000000000001")
	request.SetPathValue("provider", "Microsoft.Web")
	request.SetPathValue("location", "eastus")
	request.SetPathValue("opId", pathOperationID(path))
	recorder := httptest.NewRecorder()
	handleAzureAsyncOperationStatus(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func pathOperationID(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// TestLocationPollServesTheOperationResult covers the contract the App Service
// Environment lifecycle operations depend on: while the operation runs its
// Location answers 202 with no body, and once it succeeds the same URL answers
// with the payload the operation recorded.
func TestLocationPollServesTheOperationResult(t *testing.T) {
	azureLROTestStore(t)
	payload := json.RawMessage(`{"value":[{"name":"app-one"}]}`)
	opID := issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		return payload, nil
	})

	resultPath := "/subscriptions/00000000-0000-0000-0000-000000000001/providers/" +
		"Microsoft.Web/locations/eastus/operationResults/" + opID
	code, body := pollAzureOperation(t, resultPath)
	require.Equal(t, http.StatusAccepted, code,
		"a running operation's Location answers 202 with no result yet")
	require.Empty(t, body)

	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body = pollAzureOperation(t, resultPath)
		if code == http.StatusOK {
			break
		}
		require.Equal(t, http.StatusAccepted, code, string(body))
		require.True(t, time.Now().Before(deadline), "the operation never completed")
		time.Sleep(20 * time.Millisecond)
	}
	require.JSONEq(t, string(payload), string(body),
		"the Location poll must answer with the operation's result, not its status envelope")
}

// TestStatusPollNeverCarriesTheResult is the discriminator: the operationStatuses
// route is the status envelope's own contract, and leaking the result into it
// would let a client that polls the wrong URL appear to work.
func TestStatusPollNeverCarriesTheResult(t *testing.T) {
	azureLROTestStore(t)
	opID := issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		return json.RawMessage(`{"value":[{"name":"app-one"}]}`), nil
	})

	statusPath := "/subscriptions/00000000-0000-0000-0000-000000000001/providers/" +
		"Microsoft.Web/locations/eastus/operationStatuses/" + opID
	var envelope map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := pollAzureOperation(t, statusPath)
		require.Equal(t, http.StatusOK, code, string(body))
		require.NoError(t, json.Unmarshal(body, &envelope))
		if envelope["status"] == "Succeeded" {
			break
		}
		require.Equal(t, "InProgress", envelope["status"])
		require.True(t, time.Now().Before(deadline), "the operation never completed")
		time.Sleep(20 * time.Millisecond)
	}
	require.NotContains(t, envelope, "result",
		"the status envelope carries the operation's state, never its result")
	require.NotContains(t, envelope, "value")
	require.Contains(t, envelope, "id")
	require.Contains(t, envelope, "name")
}

// TestFailedOperationCarriesNoResult holds the failure path to the same line: a
// failed operation reports the error envelope and no payload, so a client
// cannot read a result out of an operation that produced none.
func TestFailedOperationCarriesNoResult(t *testing.T) {
	azureLROTestStore(t)
	opID := issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		return json.RawMessage(`{"value":[]}`), &AsyncOperationError{
			Code: "OperationFailed", Message: "the operation failed",
		}
	})

	resultPath := "/subscriptions/00000000-0000-0000-0000-000000000001/providers/" +
		"Microsoft.Web/locations/eastus/operationResults/" + opID
	deadline := time.Now().Add(5 * time.Second)
	var envelope map[string]any
	for {
		code, body := pollAzureOperation(t, resultPath)
		if code == http.StatusOK {
			require.NoError(t, json.Unmarshal(body, &envelope))
			if envelope["status"] == "Failed" {
				break
			}
		}
		require.True(t, time.Now().Before(deadline), "the operation never settled")
		time.Sleep(20 * time.Millisecond)
	}
	require.NotContains(t, envelope, "value",
		"a failed operation has no result to serve")
	failure, ok := envelope["error"].(map[string]any)
	require.True(t, ok, "a failed operation reports the error envelope: %v", envelope)
	require.Equal(t, "OperationFailed", failure["code"])
}
