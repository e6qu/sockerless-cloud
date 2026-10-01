package main

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/codes"
)

// useRunJobOperationStores points the package's operation and execution stores
// at fresh in-memory stores for one test.
func useRunJobOperationStores(t *testing.T) {
	t.Helper()
	savedOps, savedExecs, savedLogOps := crOperations, crjExecutions, logOperations
	crOperations = sim.MakeStore[Operation](nil, "test_run_job_operations")
	crjExecutions = sim.MakeStore[Execution](nil, "test_run_job_executions")
	logOperations = nil
	t.Cleanup(func() { crOperations, crjExecutions, logOperations = savedOps, savedExecs, savedLogOps })
}

const testRunJobExecution = "projects/p/locations/us-central1/jobs/j/executions/j-abc12"

func runningTestExecution() Execution {
	return Execution{Name: testRunJobExecution, Job: "j", TaskCount: 1, RunningCount: 1, Reconciling: true}
}

// awaitInBackground starts a WaitOperation with no timeout and returns the
// channel its answer arrives on.
func awaitInBackground(name string) <-chan Operation {
	answer := make(chan Operation, 1)
	go func() {
		op, _ := gcpAwaitOperation(context.Background(), name, 0)
		answer <- op
	}()
	return answer
}

func TestCloudRunRunJobOperationCompletesWhenTheExecutionFinishes(t *testing.T) {
	cases := []struct {
		name     string
		settle   func(*Execution)
		wantCode codes.Code
		wantMsg  string
	}{
		{
			name: "succeeded",
			settle: func(e *Execution) {
				e.SucceededCount = 1
			},
			wantCode: codes.OK,
		},
		{
			name: "failed",
			settle: func(e *Execution) {
				e.FailedCount = 1
				msg := cloudRunExecutionFailureMessage(*e)
				e.Conditions = []Condition{{Type: "Ready", State: "CONDITION_FAILED", Message: msg},
					{Type: "Completed", State: "CONDITION_FAILED", Message: msg}}
			},
			wantCode: codes.FailedPrecondition,
			wantMsg:  "Execution j-abc12 has failed to complete, 0/1 tasks were a success.",
		},
		{
			name: "cancelled",
			settle: func(e *Execution) {
				e.CancelledCount = 1
			},
			wantCode: codes.Canceled,
			wantMsg:  "Execution j-abc12 was cancelled.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useRunJobOperationStores(t)
			crjExecutions.Put(testRunJobExecution, runningTestExecution())

			op := startCloudRunJobRunOperation("p", "us-central1", runningTestExecution())
			if op.Done || op.Response != nil || op.Error != nil {
				t.Fatalf("RunJob operation for a running execution = %+v, want running with no result", op)
			}
			if op.Metadata["@type"] != cloudRunExecutionType || op.Metadata["name"] != testRunJobExecution {
				t.Fatalf("RunJob operation metadata = %v, want the Execution", op.Metadata)
			}
			waited := awaitInBackground(op.Name)

			crjExecutions.Update(testRunJobExecution, func(e *Execution) {
				e.RunningCount = 0
				e.CompletionTime = nowTimestamp()
				tc.settle(e)
			})
			finishCloudRunJobRunOperations(testRunJobExecution)

			got := <-waited
			if !got.Done {
				t.Fatalf("WaitOperation returned %+v before the operation was done", got)
			}
			if got.Metadata["completionTime"] == nil {
				t.Errorf("metadata = %v, want the finished Execution", got.Metadata)
			}
			if tc.wantCode == codes.OK {
				response, _ := got.Response.(map[string]any)
				if got.Error != nil || response["name"] != testRunJobExecution || response["@type"] != cloudRunExecutionType {
					t.Fatalf("succeeded operation = %+v, want the Execution as its response", got)
				}
				return
			}
			if got.Response != nil || got.Error == nil {
				t.Fatalf("operation = %+v, want an error and no response", got)
			}
			if got.Error.Code != int(tc.wantCode) || got.Error.Message != tc.wantMsg {
				t.Errorf("error = %+v, want code %d message %q", got.Error, tc.wantCode, tc.wantMsg)
			}
		})
	}
}

func TestCloudRunRunJobOperationForAnExecutionAlreadyFinished(t *testing.T) {
	useRunJobOperationStores(t)
	exec := runningTestExecution()
	exec.RunningCount, exec.SucceededCount, exec.CompletionTime = 0, 1, nowTimestamp()
	crjExecutions.Put(exec.Name, exec)

	op := startCloudRunJobRunOperation("p", "us-central1", runningTestExecution())
	if !op.Done || op.Response == nil {
		t.Fatalf("operation = %+v, want it done with the Execution, since the execution finished first", op)
	}
}

func TestCloudRunRunJobOperationForADeletedExecution(t *testing.T) {
	useRunJobOperationStores(t)
	crjExecutions.Put(testRunJobExecution, runningTestExecution())
	op := startCloudRunJobRunOperation("p", "us-central1", runningTestExecution())
	crjExecutions.Delete(testRunJobExecution)
	finishCloudRunJobRunOperations(testRunJobExecution)

	got, _ := crOperations.Get(op.Name)
	if !got.Done || got.Error == nil || got.Error.Code != int(codes.NotFound) {
		t.Fatalf("operation = %+v, want NOT_FOUND once its execution is deleted", got)
	}
}

func TestRecoverCloudRunJobRunOperations(t *testing.T) {
	useRunJobOperationStores(t)
	live := runningTestExecution()
	live.Name = "projects/p/locations/us-central1/jobs/j/executions/j-live1"
	crjExecutions.Put(live.Name, live)
	liveOp := startCloudRunJobRunOperation("p", "us-central1", live)

	settled := runningTestExecution()
	crjExecutions.Put(settled.Name, settled)
	settledOp := startCloudRunJobRunOperation("p", "us-central1", settled)
	crjExecutions.Update(settled.Name, func(e *Execution) {
		e.RunningCount, e.FailedCount, e.CompletionTime = 0, 1, nowTimestamp()
	})

	recoverCloudRunJobRunOperations()

	if got, _ := crOperations.Get(settledOp.Name); !got.Done || got.Error == nil {
		t.Errorf("operation of an execution that settled before the restart = %+v, want done with an error", got)
	}
	if got, _ := crOperations.Get(liveOp.Name); got.Done {
		t.Errorf("operation of a running execution = %+v, want it still running", got)
	}
}

func TestGCPAwaitOperationHonoursTheTimeout(t *testing.T) {
	useRunJobOperationStores(t)
	crjExecutions.Put(testRunJobExecution, runningTestExecution())
	op := startCloudRunJobRunOperation("p", "us-central1", runningTestExecution())

	got, ok := gcpAwaitOperation(context.Background(), op.Name, time.Millisecond)
	if !ok || got.Done {
		t.Fatalf("WaitOperation past its timeout = %+v (found %v), want the running operation", got, ok)
	}
	if _, ok := gcpAwaitOperation(context.Background(), "projects/p/locations/l/operations/never", 0); ok {
		t.Fatal("WaitOperation on an unknown operation must report it missing")
	}
}

func TestCloudBuildOperationProjectsTheBuild(t *testing.T) {
	cases := []struct {
		status   string
		done     bool
		wantCode int
	}{
		{status: "QUEUED"},
		{status: "WORKING"},
		{status: "SUCCESS", done: true},
		{status: "FAILURE", done: true, wantCode: int(codes.Internal)},
		{status: "TIMEOUT", done: true, wantCode: int(codes.DeadlineExceeded)},
		{status: "CANCELLED", done: true, wantCode: int(codes.Canceled)},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			build := Build{ID: "b1", ProjectID: "p", Status: tc.status, Name: "projects/p/locations/global/builds/b1"}
			op := cbBuildOperation("p", build)
			if op.Name != "operations/build/p/b1" || op.Done != tc.done {
				t.Fatalf("operation = %+v, want operations/build/p/b1 with done=%v", op, tc.done)
			}
			if op.Metadata["@type"] != "type.googleapis.com/google.devtools.cloudbuild.v1.BuildOperationMetadata" {
				t.Errorf("metadata = %v, want BuildOperationMetadata", op.Metadata)
			}
			if got, _ := op.Metadata["build"].(Build); got.ID != "b1" || got.Status != tc.status {
				t.Errorf("metadata build = %+v, want the build as it stands", op.Metadata["build"])
			}
			switch {
			case !tc.done:
				if op.Response != nil || op.Error != nil {
					t.Errorf("running build operation carries a result: %+v", op)
				}
			case tc.wantCode == 0:
				if op.Error != nil || op.Response["id"] != "b1" ||
					op.Response["@type"] != "type.googleapis.com/google.devtools.cloudbuild.v1.Build" {
					t.Errorf("succeeded build operation = %+v, want the Build as its response", op)
				}
			default:
				if op.Response != nil || op.Error == nil || op.Error.Code != tc.wantCode {
					t.Errorf("unsuccessful build operation = %+v, want error code %d", op, tc.wantCode)
				}
			}
		})
	}
}
