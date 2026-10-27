package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/signals"
)

// AsyncOperationStatus is the envelope a polled Azure-AsyncOperation URL
// returns, which every azcore poller reads. ID derives from the request path at
// read time, so one record serves both operationStatuses and operationResults.
type AsyncOperationStatus struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status"` // InProgress / Succeeded / Failed
	StartTime string `json:"startTime,omitempty"`
	EndTime   string `json:"endTime,omitempty"`
	// Error is present only on Failed operations.
	Error *AsyncOperationError `json:"error,omitempty"`
	// Result is what the Location poll answers once an operation whose final state
	// comes via Location succeeds: Azure Resource Manager serves the operation's
	// result there, and a paged operation's client reads its first page from it.
	// It persists with the operation and never appears in the status envelope.
	Result json.RawMessage `json:"result,omitempty"`
}

type AsyncOperationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details carries nested per-resource reasons; a move validation failure
	// reports which resource it refused and why.
	Details []map[string]any `json:"details,omitempty"`
}

var (
	azureAsyncOps sim.Store[AsyncOperationStatus]
	// azureAsyncOpChanges announces an operation's ID on every write of it.
	azureAsyncOpChanges = signals.New[string]()
)

func registerAzureAsyncOperations(srv *sim.Server) {
	azureAsyncOps = sim.Announcing(sim.MakeStore[AsyncOperationStatus](srv.DB(), "azure_async_ops"), azureAsyncOpChanges)
	// Operations complete in in-process goroutines, so a row still InProgress
	// after a restart lost its goroutine. Azure Resource Manager fails an
	// operation its backend lost rather than leaving pollers hanging; do the same.
	for _, op := range azureAsyncOps.List() {
		if op.Status != "InProgress" {
			continue
		}
		azureAsyncOps.Update(op.Name, func(stale *AsyncOperationStatus) {
			if stale.Status != "InProgress" {
				return
			}
			stale.Status = "Failed"
			stale.EndTime = time.Now().UTC().Format(time.RFC3339Nano)
			stale.Error = &AsyncOperationError{
				Code:    "OperationInterrupted",
				Message: "The operation was interrupted by a service restart before it completed and cannot be resumed. Retry the request.",
			}
		})
	}
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{provider}/locations/{location}/operationStatuses/{opId}", handleAzureOperationStatuses)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{provider}/locations/{location}/operationResults/{opId}", handleAzureOperationResults)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/operations/{opId}", handleComputeOperation)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Cache/locations/{location}/asyncOperations/{operationId}", handleCacheAsyncOperation)
}

func handleAzureOperationStatuses(w http.ResponseWriter, r *http.Request) {
	serveAzureAsyncOperation(w, r, sim.PathParam(r, "opId"), false, r.URL.Path)
}

func handleCacheAsyncOperation(w http.ResponseWriter, r *http.Request) {
	serveAzureAsyncOperation(w, r, sim.PathParam(r, "operationId"), false, r.URL.Path)
}

func handleAzureOperationResults(w http.ResponseWriter, r *http.Request) {
	serveAzureAsyncOperation(w, r, sim.PathParam(r, "opId"), true, strings.Replace(r.URL.Path, "/operationResults/", "/operationStatuses/", 1))
}

// handleComputeOperation serves both Compute polls from one URL: the status
// envelope, which carries no id, and with monitor=true the result.
func handleComputeOperation(w http.ResponseWriter, r *http.Request) {
	serveAzureAsyncOperation(w, r, sim.PathParam(r, "opId"), r.URL.Query().Get("monitor") == "true", "")
}

// issueAzureAsyncOperation runs the work before recording the operation, so
// the first status read reports its outcome.
func issueAzureAsyncOperation(complete func()) string {
	return issueAzureAsyncOperationOutcome(func() *AsyncOperationError {
		if complete != nil {
			complete()
		}
		return nil
	})
}

// issueAzureAsyncOperationOutcome: a nil return marks the operation Succeeded;
// an error marks it Failed with that error.
func issueAzureAsyncOperationOutcome(complete func() *AsyncOperationError) string {
	return issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		if complete == nil {
			return nil, nil
		}
		return nil, complete()
	})
}

// issueAzureAsyncOperationResult is for an operation whose final state comes
// via Location: complete returns the payload the Location poll answers.
func issueAzureAsyncOperationResult(complete func() (json.RawMessage, *AsyncOperationError)) string {
	opID := sim.NewUUID()
	op := AsyncOperationStatus{
		Name:      opID,
		StartTime: time.Now().UTC().Format(time.RFC3339Nano),
	}
	var result json.RawMessage
	var opErr *AsyncOperationError
	if complete != nil {
		result, opErr = complete()
	}
	settleAzureAsyncOperation(&op, result, opErr)
	azureAsyncOps.Put(opID, op)
	return opID
}

// startAzureAsyncOperationOutcome runs the work in the background; the
// operation reports InProgress until it returns.
func startAzureAsyncOperationOutcome(work func() *AsyncOperationError) string {
	return startAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		return nil, work()
	})
}

// startAzureAsyncOperationResult is the background form of
// issueAzureAsyncOperationResult.
func startAzureAsyncOperationResult(work func() (json.RawMessage, *AsyncOperationError)) string {
	opID := sim.NewUUID()
	azureAsyncOps.Put(opID, AsyncOperationStatus{
		Name:      opID,
		Status:    "InProgress",
		StartTime: time.Now().UTC().Format(time.RFC3339Nano),
	})
	bg.Go(func() {
		result, opErr := work()
		azureAsyncOps.Update(opID, func(op *AsyncOperationStatus) {
			settleAzureAsyncOperation(op, result, opErr)
		})
	})
	return opID
}

// azureAwaitAsyncOperation returns once operation opID is no longer
// InProgress, or is not one of azureAsyncOps' operations, or ctx ends.
func azureAwaitAsyncOperation(ctx context.Context, opID string) error {
	return azureAsyncOpChanges.Await(ctx, opID, func() bool {
		op, ok := azureAsyncOps.Get(opID)
		return !ok || op.Status != "InProgress"
	})
}

func settleAzureAsyncOperation(op *AsyncOperationStatus, result json.RawMessage, opErr *AsyncOperationError) {
	op.Status = "Succeeded"
	op.Result = result
	if opErr != nil {
		op.Status = "Failed"
		op.Error = opErr
		op.Result = nil
	}
	op.EndTime = time.Now().UTC().Format(time.RFC3339Nano)
}

// The specifications type Retry-After as whole seconds, azcore ignores zero
// and falls back to 30 seconds, and Azure Resource Manager sends 1 for its
// quickest operations, so 1 is the soonest a client learns the work finished.
// No Retry-After once the operation is terminal.
const azureAsyncOperationRetryAfter = "1"

// setAzureAsyncOperationRetryAfter advertises the poll interval on a response
// about opID only while that operation is still running.
func setAzureAsyncOperationRetryAfter(w http.ResponseWriter, opID string) {
	if op, ok := azureAsyncOps.Get(opID); ok && op.Status == "InProgress" {
		w.Header().Set("Retry-After", azureAsyncOperationRetryAfter)
	}
}

func azureAsyncOperationHeader(r *http.Request, sub, provider, location, kind, opID, apiVersion string) string {
	scheme := azureRequestScheme(r)
	if apiVersion == "" {
		apiVersion = "2024-01-01"
	}
	if kind == "" {
		kind = "operationStatuses"
	}
	return fmt.Sprintf("%s://%s/subscriptions/%s/providers/%s/locations/%s/%s/%s?api-version=%s",
		scheme, r.Host, sub, provider, location, kind, opID, apiVersion)
}

// computeOperationURLs: the operations URL is the Azure-AsyncOperation, and
// the same URL with monitor=true is the Location.
func computeOperationURLs(r *http.Request, sub, location, opID string) (asyncOperation, monitor string) {
	apiVersion := r.URL.Query().Get("api-version")
	if apiVersion == "" {
		apiVersion = "2024-07-01"
	}
	base := fmt.Sprintf("%s://%s/subscriptions/%s/providers/Microsoft.Compute/locations/%s/operations/%s",
		azureRequestScheme(r), r.Host, sub, location, opID)
	return base + "?api-version=" + apiVersion, base + "?monitor=true&api-version=" + apiVersion
}

func azureCurrentRequestURL(r *http.Request) string {
	return fmt.Sprintf("%s://%s%s", azureRequestScheme(r), r.Host, r.URL.RequestURI())
}

func writeAzureAsyncCreateHeaders(w http.ResponseWriter, opID, opURL, locationURL string) {
	w.Header().Set("Azure-AsyncOperation", opURL)
	w.Header().Set("Location", locationURL)
	setAzureAsyncOperationRetryAfter(w, opID)
}

// serveAzureAsyncOperation: a status poll answers the envelope, with id when
// the provider's envelope has one; a result poll answers 202 while the
// operation runs and its result once it ends.
func serveAzureAsyncOperation(w http.ResponseWriter, r *http.Request, opID string, resultPoll bool, id string) {
	op, ok := azureAsyncOps.Get(opID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Operation %q not found.", opID)
		return
	}
	op.ID = id
	if op.Status == "InProgress" {
		w.Header().Set("Retry-After", azureAsyncOperationRetryAfter)
		if resultPoll {
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	if resultPoll && len(op.Result) > 0 {
		sim.WriteJSON(w, http.StatusOK, op.Result)
		return
	}
	op.Result = nil
	sim.WriteJSON(w, http.StatusOK, op)
}
