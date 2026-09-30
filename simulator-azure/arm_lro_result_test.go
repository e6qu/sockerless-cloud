package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// An operation whose final state comes via Location answers with its own
// result at that URL — the collection, the resource, whatever the operation
// produces — and not with the status envelope. These tests hold the operation
// store to that, because the difference is what decides whether a generated
// client can read the operation at all: on a synchronous answer azcore selects
// its no-op poller, which overwrites the response the client pre-built, and for
// an operation whose result type is a pager that leaves a pager with a nil
// handler behind.

const (
	lroTestResultPath = "/subscriptions/00000000-0000-0000-0000-000000000001/providers/" +
		"Microsoft.Web/locations/eastus/operationResults/"
	lroTestStatusPath = "/subscriptions/00000000-0000-0000-0000-000000000001/providers/" +
		"Microsoft.Web/locations/eastus/operationStatuses/"
)

func azureLROTestStore(t *testing.T) {
	t.Helper()
	azureAsyncOps = sim.MakeStore[AsyncOperationStatus](nil, "azure_async_ops")
	t.Cleanup(bg.Await)
}

// pollAzureOperation drives one poll of an operation URL, the way a client
// following Azure-AsyncOperation or Location does.
func pollAzureOperation(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.SetPathValue("subscriptionId", "00000000-0000-0000-0000-000000000001")
	request.SetPathValue("provider", "Microsoft.Web")
	request.SetPathValue("location", "eastus")
	request.SetPathValue("opId", pathOperationID(path))
	recorder := httptest.NewRecorder()
	if strings.Contains(path, "/operationResults/") {
		handleAzureOperationResults(recorder, request)
	} else {
		handleAzureOperationStatuses(recorder, request)
	}
	return recorder
}

func pathOperationID(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func decodeOperationEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return envelope
}

// TestLocationPollServesTheOperationResult covers the contract the App Service
// Environment lifecycle operations depend on: while the operation runs its
// Location answers 202 with no body and a Retry-After, and once it succeeds the
// same URL answers with the payload the operation recorded and no Retry-After.
func TestLocationPollServesTheOperationResult(t *testing.T) {
	azureLROTestStore(t)
	payload := json.RawMessage(`{"value":[{"name":"app-one"}]}`)
	release := make(chan struct{})
	opID := startAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		<-release
		return payload, nil
	})

	running := pollAzureOperation(t, lroTestResultPath+opID)
	require.Equal(t, http.StatusAccepted, running.Code,
		"a running operation's Location answers 202 with no result yet")
	require.Empty(t, running.Body.Bytes())
	require.Equal(t, "1", running.Header().Get("Retry-After"))

	close(release)
	bg.Await()

	done := pollAzureOperation(t, lroTestResultPath+opID)
	require.Equal(t, http.StatusOK, done.Code, done.Body.String())
	require.JSONEq(t, string(payload), done.Body.String(),
		"the Location poll must answer with the operation's result, not its status envelope")
	require.Empty(t, done.Header().Get("Retry-After"), "ARM sends no Retry-After for a terminal operation")
}

// TestStatusPollFollowsTheBackgroundWork holds the status envelope to the work
// behind it: InProgress with a Retry-After while the work runs, Succeeded with
// startTime and endTime once it returns, and never before.
func TestStatusPollFollowsTheBackgroundWork(t *testing.T) {
	azureLROTestStore(t)
	release := make(chan struct{})
	opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		<-release
		return nil
	})

	running := pollAzureOperation(t, lroTestStatusPath+opID)
	require.Equal(t, "InProgress", decodeOperationEnvelope(t, running)["status"])
	require.Equal(t, "1", running.Header().Get("Retry-After"))
	again := pollAzureOperation(t, lroTestStatusPath+opID)
	require.Equal(t, "InProgress", decodeOperationEnvelope(t, again)["status"],
		"the operation stays InProgress for as long as its work runs")

	close(release)
	bg.Await()

	done := pollAzureOperation(t, lroTestStatusPath+opID)
	envelope := decodeOperationEnvelope(t, done)
	require.Equal(t, "Succeeded", envelope["status"])
	require.NotEmpty(t, envelope["startTime"])
	require.NotEmpty(t, envelope["endTime"])
	require.Empty(t, done.Header().Get("Retry-After"))
}

// TestBackgroundWorkFailureFailsTheOperation checks that the error the work
// returns is the error the operation reports.
func TestBackgroundWorkFailureFailsTheOperation(t *testing.T) {
	azureLROTestStore(t)
	opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		return &AsyncOperationError{Code: "AllocationFailed", Message: "no host"}
	})
	bg.Await()

	envelope := decodeOperationEnvelope(t, pollAzureOperation(t, lroTestStatusPath+opID))
	require.Equal(t, "Failed", envelope["status"])
	failure, ok := envelope["error"].(map[string]any)
	require.True(t, ok, "a failed operation reports the error envelope: %v", envelope)
	require.Equal(t, "AllocationFailed", failure["code"])
	require.Equal(t, "no host", failure["message"])
}

// TestInstantaneousOperationIsSettledOnFirstRead covers the operations whose
// work the simulator finishes while it accepts the request: the first status
// read reports the outcome, and the accepting response advertises no
// Retry-After, so azcore's poller reads the status at once instead of waiting.
func TestInstantaneousOperationIsSettledOnFirstRead(t *testing.T) {
	azureLROTestStore(t)
	ran := false
	opID := issueAzureAsyncOperation(func() { ran = true })
	require.True(t, ran, "the work runs before the operation is recorded")

	accepted := httptest.NewRecorder()
	writeAzureAsyncCreateHeaders(accepted, opID, "https://status", "https://result")
	require.Equal(t, "https://status", accepted.Header().Get("Azure-AsyncOperation"))
	require.Equal(t, "https://result", accepted.Header().Get("Location"))
	require.Empty(t, accepted.Header().Get("Retry-After"))

	envelope := decodeOperationEnvelope(t, pollAzureOperation(t, lroTestStatusPath+opID))
	require.Equal(t, "Succeeded", envelope["status"])
}

// TestRunningOperationAdvertisesRetryAfterOnAcceptance is the counterpart: an
// operation still running when the request is answered tells the client how
// long to wait before its first poll.
func TestRunningOperationAdvertisesRetryAfterOnAcceptance(t *testing.T) {
	azureLROTestStore(t)
	release := make(chan struct{})
	opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		<-release
		return nil
	})
	accepted := httptest.NewRecorder()
	writeAzureAsyncCreateHeaders(accepted, opID, "https://status", "https://result")
	require.Equal(t, "1", accepted.Header().Get("Retry-After"))
	close(release)
}

// TestStatusPollNeverCarriesTheResult is the discriminator: the operationStatuses
// route is the status envelope's own contract, and leaking the result into it
// would let a client that polls the wrong URL appear to work.
func TestStatusPollNeverCarriesTheResult(t *testing.T) {
	azureLROTestStore(t)
	opID := issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		return json.RawMessage(`{"value":[{"name":"app-one"}]}`), nil
	})

	envelope := decodeOperationEnvelope(t, pollAzureOperation(t, lroTestStatusPath+opID))
	require.Equal(t, "Succeeded", envelope["status"])
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

	envelope := decodeOperationEnvelope(t, pollAzureOperation(t, lroTestResultPath+opID))
	require.Equal(t, "Failed", envelope["status"])
	require.NotContains(t, envelope, "value",
		"a failed operation has no result to serve")
	failure, ok := envelope["error"].(map[string]any)
	require.True(t, ok, "a failed operation reports the error envelope: %v", envelope)
	require.Equal(t, "OperationFailed", failure["code"])
}
