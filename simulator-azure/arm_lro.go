package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// AsyncOperationStatus is the ARM operation-status envelope a polled
// Azure-AsyncOperation URL returns — `{"id":...,"name":...,"status":...}`
// is what `armappcontainers` (and every azcore poller) reads. ID is
// derived from the request path at read time so one stored record serves
// both the operationStatuses and operationResults routes.
type AsyncOperationStatus struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status"` // InProgress / Succeeded / Failed
	StartTime string `json:"startTime,omitempty"`
	EndTime   string `json:"endTime,omitempty"`
	// Error carries ARM's failed-operation error member
	// (`{"error":{"code":...,"message":...}}`); present only on Failed
	// operations, exactly as real Azure Resource Manager emits it.
	Error *AsyncOperationError `json:"error,omitempty"`
	// Result is what the Location poll answers with once an operation whose
	// final state comes via Location succeeds: Azure Resource Manager serves
	// the operation's own result there rather than the status envelope, and a
	// paged operation's client reads its first page out of it. It is stored so
	// it survives a restart alongside the operation it belongs to, and never
	// appears in the operationStatuses envelope.
	Result json.RawMessage `json:"result,omitempty"`
}

// AsyncOperationError is the error member of a Failed ARM operation envelope.
type AsyncOperationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details carries the nested per-resource reasons an Azure Resource
	// Manager error can hold, which is how a move validation failure reports
	// which resource it refused and why.
	Details []map[string]any `json:"details,omitempty"`
}

var azureAsyncOps sim.Store[AsyncOperationStatus]

func registerAzureAsyncOperations(srv *sim.Server) {
	azureAsyncOps = sim.MakeStore[AsyncOperationStatus](srv.DB(), "azure_async_ops")
	// An operation completes via an in-process goroutine, so a persisted row
	// still InProgress after a restart can never complete — its goroutine died
	// with the previous process. Real ARM fails an operation its backend lost
	// rather than leaving pollers hanging forever; flip such rows to Failed
	// with an error envelope saying so.
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
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{provider}/locations/{location}/operationStatuses/{opId}", handleAzureAsyncOperationStatus)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{provider}/locations/{location}/operationResults/{opId}", handleAzureAsyncOperationStatus)
}

// issueAzureAsyncOperation records an operation whose work the simulator
// finishes in-process while it accepts the request: the work runs before the
// operation is recorded, so the first status read reports its outcome.
func issueAzureAsyncOperation(complete func()) string {
	return issueAzureAsyncOperationOutcome(func() *AsyncOperationError {
		if complete != nil {
			complete()
		}
		return nil
	})
}

// issueAzureAsyncOperationOutcome is the failable form: the completion
// callback decides the operation's terminal state. A nil return marks the
// operation Succeeded; a non-nil error marks it Failed with ARM's
// failed-operation error envelope, exactly as real Azure Resource Manager
// reports a long-running operation whose backend work failed.
func issueAzureAsyncOperationOutcome(complete func() *AsyncOperationError) string {
	return issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
		if complete == nil {
			return nil, nil
		}
		return nil, complete()
	})
}

// issueAzureAsyncOperationResult is the form for an operation whose final
// state comes via Location: the completion callback returns the payload the
// Location poll answers with, which is the operation's result rather than its
// status envelope.
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

// startAzureAsyncOperationOutcome records an operation InProgress and runs its
// work in the background: the operation reports InProgress until the work
// returns, then Succeeded, or Failed with the error the work returned.
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

// azureAsyncOperationRetryAfter is the Retry-After ARM attaches to a response
// about an operation still running. The specifications type the header as
// whole seconds, azcore ignores zero and falls back to its 30-second default,
// and ARM sends 1 for its quickest operations (armnetwork's recorded public IP
// create), so 1 is the soonest a client can learn that the work finished. ARM
// sends none once the operation is terminal.
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

func azureCurrentRequestURL(r *http.Request) string {
	return fmt.Sprintf("%s://%s%s", azureRequestScheme(r), r.Host, r.URL.RequestURI())
}

func writeAzureAsyncCreateHeaders(w http.ResponseWriter, opID, opURL, locationURL string) {
	w.Header().Set("Azure-AsyncOperation", opURL)
	w.Header().Set("Location", locationURL)
	setAzureAsyncOperationRetryAfter(w, opID)
}

func handleAzureAsyncOperationStatus(w http.ResponseWriter, r *http.Request) {
	opID := sim.PathParam(r, "opId")
	op, ok := azureAsyncOps.Get(opID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Operation %q not found.", opID)
		return
	}
	op.ID = strings.Replace(r.URL.Path, "/operationResults/", "/operationStatuses/", 1)
	if op.Status == "InProgress" {
		w.Header().Set("Retry-After", azureAsyncOperationRetryAfter)
		// The operationResults route is ARM's Location-poll target: while the
		// operation runs it answers 202 Accepted with no body; the envelope is
		// the operationStatuses route's contract.
		if strings.Contains(r.URL.Path, "/operationResults/") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	// An operation that recorded a result serves it from the Location route,
	// which is where a final-state-via-location client reads the operation's
	// own payload. The status envelope never carries it.
	if strings.Contains(r.URL.Path, "/operationResults/") && len(op.Result) > 0 {
		sim.WriteJSON(w, http.StatusOK, op.Result)
		return
	}
	op.Result = nil
	sim.WriteJSON(w, http.StatusOK, op)
}
