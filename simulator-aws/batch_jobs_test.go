package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestBatchParseRetryStrategyEnforcesTheDocumentedBounds(t *testing.T) {
	for name, tc := range map[string]struct {
		raw     map[string]any
		wantErr bool
	}{
		"absent":                   {raw: nil},
		"one attempt":              {raw: map[string]any{"attempts": float64(1)}},
		"ten attempts":             {raw: map[string]any{"attempts": float64(10)}},
		"zero attempts":            {raw: map[string]any{"attempts": float64(0)}, wantErr: true},
		"eleven attempts":          {raw: map[string]any{"attempts": float64(11)}, wantErr: true},
		"conditions with attempts": {raw: map[string]any{"attempts": float64(2), "evaluateOnExit": []any{map[string]any{"onExitCode": "1", "action": "exit"}}}},
		"conditions without attempts": {
			raw:     map[string]any{"evaluateOnExit": []any{map[string]any{"onExitCode": "1", "action": "EXIT"}}},
			wantErr: true,
		},
		"unknown action": {
			raw:     map[string]any{"attempts": float64(2), "evaluateOnExit": []any{map[string]any{"action": "IGNORE"}}},
			wantErr: true,
		},
		"six conditions": {
			raw: map[string]any{"attempts": float64(2), "evaluateOnExit": []any{
				map[string]any{"action": "RETRY"}, map[string]any{"action": "RETRY"}, map[string]any{"action": "RETRY"},
				map[string]any{"action": "RETRY"}, map[string]any{"action": "RETRY"}, map[string]any{"action": "RETRY"},
			}},
			wantErr: true,
		},
	} {
		_, err := batchParseRetryStrategy(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: error = %v, want error %v", name, err, tc.wantErr)
		}
	}
}

func TestBatchRetryAfterTakesTheFirstMatchingConditionAndRetriesWhenNoneMatches(t *testing.T) {
	exit := func(code int) *int { return &code }
	strategy := BatchRetryStrategy{
		Attempts: 3,
		EvaluateOnExit: []BatchEvaluateOnExit{
			{OnExitCode: "3*", Action: "exit"},
			{OnStatusReason: "Essential container*", OnExitCode: "3", Action: "RETRY"},
			{OnReason: "CannotPullContainerError:*", Action: "EXIT"},
		},
	}
	exited := func(code int) BatchAttempt {
		return BatchAttempt{StatusReason: batchReasonContainerExited, Container: BatchAttemptContainer{ExitCode: exit(code)}}
	}
	for name, tc := range map[string]struct {
		attemptsMade int
		last         BatchAttempt
		want         bool
	}{
		"exit code 3 hits the first EXIT before the RETRY that also matches": {1, exited(3), false},
		"exit code 31 matches the prefix pattern":                            {1, exited(31), false},
		"no condition matches exit code 1, so Batch retries":                 {1, exited(1), true},
		"attempts exhausted": {3, exited(1), false},
		"container start failure matches onReason": {1, BatchAttempt{
			StatusReason: batchReasonTaskNotStarted,
			Container:    BatchAttemptContainer{Reason: "CannotPullContainerError: pull access denied"},
		}, false},
	} {
		if got := batchRetryAfter(strategy, tc.attemptsMade, tc.last); got != tc.want {
			t.Errorf("%s: retry = %v, want %v", name, got, tc.want)
		}
	}
	if batchRetryAfter(BatchRetryStrategy{}, 1, exited(1)) {
		t.Error("a job with no retry strategy has one attempt")
	}
}

func TestBatchVCPUsReadsVcpusAndResourceRequirementsWithOverridesLast(t *testing.T) {
	definition := map[string]any{"vcpus": float64(2)}
	if got := batchVCPUs(definition, nil); got != 2 {
		t.Fatalf("vcpus = %v, want 2", got)
	}
	override := map[string]any{"resourceRequirements": []any{map[string]any{"type": "VCPU", "value": "0.25"}}}
	if got := batchVCPUs(definition, override); got != 0.25 {
		t.Fatalf("vcpus = %v, want 0.25", got)
	}
	if got := batchVCPUs(nil, nil); got != 1 {
		t.Fatalf("vcpus = %v, want 1", got)
	}
}

func TestBatchListFilterMatchesAsTheListJobsReferenceDescribes(t *testing.T) {
	job := BatchJob{
		JobName:       "Test10",
		JobDefinition: batchARN("job-definition/jd1A:3"),
		CreatedAt:     1000,
	}
	for name, tc := range map[string]struct {
		filter batchListFilter
		want   bool
	}{
		"job name ignores case":                  {batchListFilter{Name: "JOB_NAME", Values: []string{"test10"}}, true},
		"job name prefix":                        {batchListFilter{Name: "JOB_NAME", Values: []string{"test1*"}}, true},
		"job name exact mismatch":                {batchListFilter{Name: "JOB_NAME", Values: []string{"test1"}}, false},
		"definition name is exact":               {batchListFilter{Name: "JOB_DEFINITION", Values: []string{"jd1"}}, false},
		"definition name prefix":                 {batchListFilter{Name: "JOB_DEFINITION", Values: []string{"jd1*"}}, true},
		"definition ARN names the revision":      {batchListFilter{Name: "JOB_DEFINITION", Values: []string{batchARN("job-definition/jd1A:3")}}, true},
		"definition ARN of another revision":     {batchListFilter{Name: "JOB_DEFINITION", Values: []string{batchARN("job-definition/jd1A:2")}}, false},
		"created before":                         {batchListFilter{Name: "BEFORE_CREATED_AT", Values: []string{"1001"}}, true},
		"created after":                          {batchListFilter{Name: "AFTER_CREATED_AT", Values: []string{"1001"}}, false},
		"share identifier the job was not given": {batchListFilter{Name: "SHARE_IDENTIFIER", Values: []string{"teamA"}}, false},
	} {
		if got := tc.filter.matches(job); got != tc.want {
			t.Errorf("%s: match = %v, want %v", name, got, tc.want)
		}
	}
}

func batchUseMemoryJobStore(t *testing.T) {
	t.Helper()
	previousJobs, previousRunnable, previousInUse := batchJobs, batchRunnable, batchCEInUse
	previousDependents, previousFinished := batchDependents, batchFinished
	batchJobs = sim.NewStateStore[BatchJob]()
	batchRunnable = nil
	batchCEInUse = map[string]float64{}
	batchDependents = map[string][]string{}
	batchFinished = nil
	t.Cleanup(func() {
		batchJobs, batchRunnable, batchCEInUse = previousJobs, previousRunnable, previousInUse
		batchDependents, batchFinished = previousDependents, previousFinished
	})
}

func batchSubmittedArray(t *testing.T, size int) BatchJob {
	t.Helper()
	return batchSubmit(t, "parent", size)
}

func batchSubmit(t *testing.T, jobID string, size int, deps ...BatchJobDependency) BatchJob {
	t.Helper()
	if err := batchCheckDependencies(deps, size); err != nil {
		t.Fatalf("dependsOn %v: %v", deps, err)
	}
	job := BatchJob{
		JobID:           jobID,
		JobArn:          batchARN("job/" + jobID),
		JobName:         jobID,
		JobQueue:        batchARN("job-queue/queue"),
		Status:          batchStatusSubmitted,
		Container:       map[string]any{"image": "busybox"},
		ExecutionConfig: &sim.ContainerConfig{Image: "busybox"},
		VCPUs:           1,
		DependsOn:       deps,
	}
	if size > 0 {
		job.ArrayProperties = &BatchArrayProperties{Size: size}
	}
	batchJobs.Put(jobID, job)
	batchScheduleJobLocked(jobID)
	job, _ = batchJobs.Get(jobID)
	return job
}

// batchFinish ends a RUNNABLE job the way an attempt ending does and releases
// the jobs that wait on it.
func batchFinish(t *testing.T, jobID, status string) {
	t.Helper()
	job, ok := batchJobs.Get(jobID)
	if !ok || job.Status != batchStatusRunnable {
		t.Fatalf("job %s = %s, want RUNNABLE", jobID, job.Status)
	}
	batchDequeueLocked(jobID)
	batchSetStatus(&job, status)
	batchJobs.Put(jobID, job)
	batchReleaseDependentsLocked()
}

func batchStatusOf(t *testing.T, jobID string) BatchJob {
	t.Helper()
	job, ok := batchJobs.Get(jobID)
	if !ok {
		t.Fatalf("job %s missing", jobID)
	}
	return job
}

func TestBatchJobWaitsInPendingUntilItsDependencySucceeds(t *testing.T) {
	batchUseMemoryJobStore(t)
	batchSubmit(t, "first", 0)
	second := batchSubmit(t, "second", 0, BatchJobDependency{JobID: "first"})
	if second.Status != batchStatusPending {
		t.Fatalf("dependent job = %s, want PENDING", second.Status)
	}
	if len(batchRunnable) != 1 {
		t.Fatalf("scheduler queue = %v, want only the job with no dependency", batchRunnable)
	}
	batchFinish(t, "first", batchStatusSucceeded)
	if got := batchStatusOf(t, "second").Status; got != batchStatusRunnable {
		t.Fatalf("dependent job = %s once its dependency succeeded, want RUNNABLE", got)
	}
	third := batchSubmit(t, "third", 0, BatchJobDependency{JobID: "first"})
	if third.Status != batchStatusRunnable {
		t.Fatalf("job depending on a SUCCEEDED job = %s, want RUNNABLE", third.Status)
	}
}

func TestBatchJobFailsWhenADependencyFailsAndTheFailureCascades(t *testing.T) {
	batchUseMemoryJobStore(t)
	batchSubmit(t, "first", 0)
	batchSubmit(t, "other", 0)
	batchSubmit(t, "second", 0, BatchJobDependency{JobID: "other"}, BatchJobDependency{JobID: "first"})
	batchSubmit(t, "third", 0, BatchJobDependency{JobID: "second"})
	batchFinish(t, "first", batchStatusFailed)
	for _, jobID := range []string{"second", "third"} {
		job := batchStatusOf(t, jobID)
		if job.Status != batchStatusFailed || job.StatusReason != batchReasonDependencyFail || job.StoppedAt == 0 {
			t.Fatalf("%s = %s reason %q stoppedAt %d, want FAILED on its failed dependency", jobID, job.Status, job.StatusReason, job.StoppedAt)
		}
	}
	if got := batchStatusOf(t, "other").Status; got != batchStatusRunnable {
		t.Fatalf("the other dependency = %s, want it left RUNNABLE", got)
	}
}

func TestBatchSequentialArrayRunsItsChildrenInIndexOrder(t *testing.T) {
	batchUseMemoryJobStore(t)
	parent := batchSubmit(t, "parent", 3, BatchJobDependency{Type: batchDependencySequential})
	summary := parent.ArrayProperties.StatusSummary
	if summary[batchStatusRunnable] != 1 || summary[batchStatusPending] != 2 {
		t.Fatalf("status summary = %v, want child 0 RUNNABLE and the rest PENDING", summary)
	}
	child1 := batchStatusOf(t, "parent:1")
	if len(child1.DependsOn) != 1 || child1.DependsOn[0] != (BatchJobDependency{JobID: "parent:0", Type: batchDependencySequential}) {
		t.Fatalf("child 1 dependsOn = %v", child1.DependsOn)
	}
	batchFinish(t, "parent:0", batchStatusSucceeded)
	if got := batchStatusOf(t, "parent:1").Status; got != batchStatusRunnable {
		t.Fatalf("child 1 = %s after child 0 succeeded, want RUNNABLE", got)
	}
	if got := batchStatusOf(t, "parent:2").Status; got != batchStatusPending {
		t.Fatalf("child 2 = %s before child 1 finished, want PENDING", got)
	}
	batchFinish(t, "parent:1", batchStatusFailed)
	if got := batchStatusOf(t, "parent:2"); got.Status != batchStatusFailed || got.StatusReason != batchReasonDependencyFail {
		t.Fatalf("child 2 = %s reason %q after child 1 failed", got.Status, got.StatusReason)
	}
	if got := batchStatusOf(t, "parent").Status; got != batchStatusFailed {
		t.Fatalf("parent = %s once every child finished and one failed, want FAILED", got)
	}
}

func TestBatchNToNChildWaitsOnTheSameIndexOfItsDependency(t *testing.T) {
	batchUseMemoryJobStore(t)
	batchSubmit(t, "first", 2)
	batchSubmit(t, "second", 2, BatchJobDependency{JobID: "first", Type: batchDependencyNToN})
	batchFinish(t, "first:1", batchStatusSucceeded)
	if got := batchStatusOf(t, "second:1").Status; got != batchStatusRunnable {
		t.Fatalf("second:1 = %s after first:1 succeeded, want RUNNABLE", got)
	}
	if got := batchStatusOf(t, "second:0").Status; got != batchStatusPending {
		t.Fatalf("second:0 = %s while first:0 is RUNNABLE, want PENDING", got)
	}
}

func TestBatchCancelledPendingJobFailsOnceItsDependenciesFinish(t *testing.T) {
	batchUseMemoryJobStore(t)
	batchSubmit(t, "first", 0)
	batchSubmit(t, "second", 0, BatchJobDependency{JobID: "first"})
	batchCancelJobLocked("second", "no longer needed")
	if job := batchStatusOf(t, "second"); job.Status != batchStatusPending || !job.IsCancelled {
		t.Fatalf("cancelled PENDING job = %s cancelled=%v, want it PENDING until its dependency finishes", job.Status, job.IsCancelled)
	}
	batchFinish(t, "first", batchStatusSucceeded)
	job := batchStatusOf(t, "second")
	if job.Status != batchStatusFailed || job.StatusReason != "no longer needed" {
		t.Fatalf("cancelled job = %s reason %q, want FAILED with the cancel reason", job.Status, job.StatusReason)
	}
	if len(batchRunnable) != 0 {
		t.Fatalf("scheduler queue = %v, want the cancelled job left out", batchRunnable)
	}
}

func TestBatchRecoveryReleasesAPendingJobWhoseDependencyFinished(t *testing.T) {
	batchUseMemoryJobStore(t)
	batchJobs.Put("first", BatchJob{JobID: "first", Status: batchStatusSucceeded})
	pending := BatchJob{JobID: "second", JobQueue: batchARN("job-queue/queue"), Status: batchStatusPending, DependsOn: []BatchJobDependency{{JobID: "first"}}}
	batchRecoverDependentLocked(pending)
	if got := batchStatusOf(t, "second").Status; got != batchStatusRunnable {
		t.Fatalf("recovered job = %s, want RUNNABLE", got)
	}
	batchJobs.Put("third", BatchJob{JobID: "third", Status: batchStatusRunning})
	waiting := BatchJob{JobID: "fourth", Status: batchStatusPending, DependsOn: []BatchJobDependency{{JobID: "third"}}}
	batchJobs.Put("fourth", waiting)
	batchRecoverDependentLocked(waiting)
	if got := batchDependents["third"]; len(got) != 1 || got[0] != "fourth" {
		t.Fatalf("dependents of the running job = %v, want [fourth]", got)
	}
}

func TestBatchCheckDependenciesEnforcesTheSubmitJobContract(t *testing.T) {
	batchUseMemoryJobStore(t)
	batchSubmit(t, "single", 0)
	batchSubmit(t, "array", 2)
	many := make([]BatchJobDependency, batchMaxDependencies+1)
	for i := range many {
		many[i] = BatchJobDependency{JobID: "single"}
	}
	for name, tc := range map[string]struct {
		deps      []BatchJobDependency
		arraySize int
		wantErr   bool
	}{
		"plain dependency":                  {deps: []BatchJobDependency{{JobID: "single"}}},
		"unknown job":                       {deps: []BatchJobDependency{{JobID: "missing"}}, wantErr: true},
		"missing job ID":                    {deps: []BatchJobDependency{{}}, wantErr: true},
		"more than twenty":                  {deps: many, wantErr: true},
		"unknown type":                      {deps: []BatchJobDependency{{JobID: "single", Type: "ALL"}}, wantErr: true},
		"sequential array":                  {deps: []BatchJobDependency{{Type: batchDependencySequential}}, arraySize: 2},
		"sequential single job":             {deps: []BatchJobDependency{{Type: batchDependencySequential}}, wantErr: true},
		"N_TO_N between equal arrays":       {deps: []BatchJobDependency{{JobID: "array", Type: batchDependencyNToN}}, arraySize: 2},
		"N_TO_N between unequal arrays":     {deps: []BatchJobDependency{{JobID: "array", Type: batchDependencyNToN}}, arraySize: 3, wantErr: true},
		"N_TO_N on a single job":            {deps: []BatchJobDependency{{JobID: "single", Type: batchDependencyNToN}}, arraySize: 2, wantErr: true},
		"N_TO_N from a single job":          {deps: []BatchJobDependency{{JobID: "array", Type: batchDependencyNToN}}, wantErr: true},
		"array child as a plain dependency": {deps: []BatchJobDependency{{JobID: "array:1"}}},
	} {
		err := batchCheckDependencies(tc.deps, tc.arraySize)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: error = %v, want error %v", name, err, tc.wantErr)
		}
	}
}

func TestBatchArrayParentWaitsInPendingAndSettlesOnceEveryChildHas(t *testing.T) {
	batchUseMemoryJobStore(t)
	parent := batchSubmittedArray(t, 3)
	if parent.Status != batchStatusPending {
		t.Fatalf("parent status = %s, want PENDING", parent.Status)
	}
	if got := parent.ArrayProperties.StatusSummary[batchStatusRunnable]; got != 3 {
		t.Fatalf("RUNNABLE children = %d, want 3", got)
	}
	if len(batchRunnable) != 3 {
		t.Fatalf("scheduler queue holds %d children, want 3", len(batchRunnable))
	}
	for index, status := range []string{batchStatusSucceeded, batchStatusFailed} {
		child, ok := batchJobs.Get(batchChildJobID("parent", index))
		if !ok || child.ArrayProperties.Index == nil || *child.ArrayProperties.Index != index {
			t.Fatalf("child %d missing or unindexed: %+v", index, child)
		}
		batchDequeueLocked(child.JobID)
		batchSetStatus(&child, status)
		batchJobs.Put(child.JobID, child)
	}
	parent, _ = batchJobs.Get("parent")
	if parent.Status != batchStatusPending {
		t.Fatalf("parent settled to %s with a child still RUNNABLE", parent.Status)
	}
	summary := parent.ArrayProperties.StatusSummary
	if summary[batchStatusSucceeded] != 1 || summary[batchStatusFailed] != 1 || summary[batchStatusRunnable] != 1 {
		t.Fatalf("status summary = %v", summary)
	}
	last, _ := batchJobs.Get(batchChildJobID("parent", 2))
	batchDequeueLocked(last.JobID)
	batchSetStatus(&last, batchStatusSucceeded)
	batchJobs.Put(last.JobID, last)
	parent, _ = batchJobs.Get("parent")
	if parent.Status != batchStatusFailed || parent.StoppedAt == 0 {
		t.Fatalf("parent = %s stoppedAt %d, want FAILED once a child failed", parent.Status, parent.StoppedAt)
	}
}

func TestBatchCancellingAnArrayParentCancelsItsWaitingChildren(t *testing.T) {
	batchUseMemoryJobStore(t)
	batchSubmittedArray(t, 2)
	batchCancelJobLocked("parent", "no longer needed")
	for index := range 2 {
		child, _ := batchJobs.Get(batchChildJobID("parent", index))
		if child.Status != batchStatusFailed || !child.IsCancelled || child.StatusReason != "no longer needed" {
			t.Fatalf("child %d = %s cancelled=%v reason=%q", index, child.Status, child.IsCancelled, child.StatusReason)
		}
	}
	parent, _ := batchJobs.Get("parent")
	if parent.Status != batchStatusFailed || !parent.IsCancelled || parent.StatusReason != "no longer needed" {
		t.Fatalf("parent = %s cancelled=%v reason=%q", parent.Status, parent.IsCancelled, parent.StatusReason)
	}
	if len(batchRunnable) != 0 {
		t.Fatalf("cancelled children remain queued: %v", batchRunnable)
	}
}

func TestBatchCancelJobLeavesAStartedJobToTerminateJob(t *testing.T) {
	batchUseMemoryJobStore(t)
	batchJobs.Put("running", BatchJob{JobID: "running", Status: batchStatusRunning, Running: &batchRunningAttempt{Number: 1}})
	batchCancelJobLocked("running", "stop")
	job, _ := batchJobs.Get("running")
	if job.Status != batchStatusRunning || job.IsCancelled {
		t.Fatalf("CancelJob moved a RUNNING job to %s (cancelled=%v)", job.Status, job.IsCancelled)
	}
	batchTerminateJobLocked("running", "stop")
	job, _ = batchJobs.Get("running")
	if !job.IsTerminated || job.StopReason != "stop" || job.Status != batchStatusRunning {
		t.Fatalf("TerminateJob on a RUNNING job = %s terminated=%v reason=%q; it fails once the container exits", job.Status, job.IsTerminated, job.StopReason)
	}
}
