package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/sim"
)

// gcpOperationWaiters holds, per unfinished operation a client waits on, the
// channel closed when the operation reaches done: gcpFinishOperation for
// long-running operations, computeOpFinish for Compute Engine operations.
var gcpOperationWaiters = struct {
	sync.Mutex
	done map[string]chan struct{}
}{done: map[string]chan struct{}{}}

func gcpOperationDoneSignal(name string) <-chan struct{} {
	gcpOperationWaiters.Lock()
	defer gcpOperationWaiters.Unlock()
	ch, ok := gcpOperationWaiters.done[name]
	if !ok {
		ch = make(chan struct{})
		gcpOperationWaiters.done[name] = ch
	}
	return ch
}

func gcpOperationSignalDone(name string) {
	gcpOperationWaiters.Lock()
	defer gcpOperationWaiters.Unlock()
	if ch, ok := gcpOperationWaiters.done[name]; ok {
		close(ch)
		delete(gcpOperationWaiters.done, name)
	}
}

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
	gcpOperationSignalDone(name)
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
	// Subscribe before reading, so a finish between the read and the wait
	// still wakes it.
	done := gcpOperationDoneSignal(name)
	op, ok := gcpLookupOperation(name)
	if !ok || op.Done {
		gcpOperationSignalDone(name)
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
