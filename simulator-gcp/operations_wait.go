package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	sim "github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/signals"
)

// gcpOperationDone announces an operation's name when it reaches done:
// gcpFinishOperation for long-running operations, computeOpFinish for Compute
// Engine operations.
var gcpOperationDone = signals.New[string]()

// gcpFinishOperation completes an unfinished operation in crOperations: settle
// fills in the result, the record turns done, and every WaitOperation blocked
// on it returns. An operation already done keeps the result it has.
func gcpFinishOperation(name string, settle func(*Operation)) {
	if crOperations == nil {
		return
	}
	crOperations.Update(name, func(op *Operation) {
		if op.Done {
			return
		}
		settle(op)
		op.Done = true
	})
	gcpOperationDone.Announce(name)
}

// gcpAwaitOperation implements google.longrunning.Operations.WaitOperation: it
// blocks until the operation is done, the caller's timeout elapses, or the
// caller goes away, then returns the operation as it stands. A zero timeout
// waits for as long as the caller's connection does, which is what the method
// documents for a request that leaves it blank.
func gcpAwaitOperation(ctx context.Context, name string, timeout time.Duration) (Operation, bool) {
	if timeout > 0 {
		sim.DeclareWait(ctx, timeout)
	} else {
		sim.DeclareOpenEndedWait(ctx)
	}
	done := gcpOperationDone.Changed(name)
	op, ok := gcpLookupOperation(name)
	if !ok || op.Done {
		gcpOperationDone.Announce(name)
		return op, ok
	}
	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-ctx.Done():
	case <-done:
	case <-expired:
	}
	return gcpLookupOperation(name)
}

// gcpWaitOperationTimeout reads the timeout a REST WaitOperationRequest body
// carries, a google.protobuf.Duration in its JSON spelling such as "30s".
func gcpWaitOperationTimeout(r *http.Request) (time.Duration, error) {
	var req struct {
		Timeout string `json:"timeout"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("invalid WaitOperationRequest body: %v", err)
		}
	}
	if req.Timeout == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(req.Timeout)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid timeout %q", req.Timeout)
	}
	return d, nil
}
