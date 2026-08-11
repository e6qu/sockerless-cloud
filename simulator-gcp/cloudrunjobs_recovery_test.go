package main

import (
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

func TestRecoverCloudRunJobExecutionsFailsRunningExecutionsWithoutProcesses(t *testing.T) {
	jobs := sim.MakeStore[Job](nil, "test_crj_jobs")
	executions := sim.MakeStore[Execution](nil, "test_crj_executions")
	tasks := sim.MakeStore[Task](nil, "test_crj_tasks")

	const jobName = "projects/p/locations/us-central1/jobs/orphaned"
	lostExec := jobName + "/executions/lost-exec"
	doneExec := jobName + "/executions/done-exec"
	liveExec := jobName + "/executions/live-exec"

	jobs.Put(jobName, Job{
		Name: jobName,
		LatestCreatedExecution: &ExecutionReference{
			Name:       lostExec,
			CreateTime: "2026-01-01T00:00:00Z",
		},
	})
	executions.Put(lostExec, Execution{
		Name:         lostExec,
		CreateTime:   "2026-01-01T00:00:00Z",
		StartTime:    "2026-01-01T00:00:00Z",
		RunningCount: 2,
		TaskCount:    2,
		Reconciling:  true,
		Conditions: []Condition{
			{Type: "Ready", State: "CONDITION_PENDING"},
		},
	})
	executions.Put(doneExec, Execution{
		Name:           doneExec,
		CompletionTime: "2026-01-01T00:01:00Z",
		SucceededCount: 1,
		TaskCount:      1,
	})
	executions.Put(liveExec, Execution{
		Name:         liveExec,
		RunningCount: 1,
		TaskCount:    1,
		Reconciling:  true,
	})
	for _, taskName := range []string{lostExec + "/tasks/t0", lostExec + "/tasks/t1"} {
		tasks.Put(taskName, Task{
			Name:        taskName,
			Job:         jobName,
			Execution:   lostExec,
			Reconciling: true,
		})
	}

	// An execution whose workload processes are still tracked must not be
	// touched by recovery.
	crjProcessHandles.Store(liveExec, &cloudRunJobProcesses{})
	defer crjProcessHandles.Delete(liveExec)

	recoverCloudRunJobExecutions(jobs, executions, tasks)

	lost, ok := executions.Get(lostExec)
	if !ok {
		t.Fatalf("execution %s missing after recovery", lostExec)
	}
	if lost.RunningCount != 0 {
		t.Errorf("lost execution RunningCount = %d, want 0", lost.RunningCount)
	}
	if lost.FailedCount != 2 {
		t.Errorf("lost execution FailedCount = %d, want 2", lost.FailedCount)
	}
	if lost.CompletionTime == "" {
		t.Error("lost execution CompletionTime not stamped")
	}
	if lost.Reconciling {
		t.Error("lost execution still reconciling")
	}
	var completed *Condition
	for i := range lost.Conditions {
		if lost.Conditions[i].Type == "Completed" {
			completed = &lost.Conditions[i]
		}
	}
	if completed == nil {
		t.Fatal("lost execution has no Completed condition")
	}
	if completed.State != "CONDITION_FAILED" {
		t.Errorf("Completed condition state = %s, want CONDITION_FAILED", completed.State)
	}
	if !strings.Contains(completed.Message, "control-plane restart") {
		t.Errorf("Completed condition message = %q, want a control-plane-restart explanation", completed.Message)
	}

	for _, taskName := range []string{lostExec + "/tasks/t0", lostExec + "/tasks/t1"} {
		task, ok := tasks.Get(taskName)
		if !ok {
			t.Fatalf("task %s missing after recovery", taskName)
		}
		if task.CompletionTime == "" {
			t.Errorf("task %s CompletionTime not stamped", taskName)
		}
		if task.Reconciling {
			t.Errorf("task %s still reconciling", taskName)
		}
	}

	job, ok := jobs.Get(jobName)
	if !ok {
		t.Fatalf("job %s missing after recovery", jobName)
	}
	if job.LatestCreatedExecution == nil || job.LatestCreatedExecution.CompletionTime == "" {
		t.Error("job latestCreatedExecution CompletionTime not stamped")
	}

	done, _ := executions.Get(doneExec)
	if done.FailedCount != 0 || done.SucceededCount != 1 {
		t.Errorf("completed execution mutated by recovery: %+v", done)
	}

	live, _ := executions.Get(liveExec)
	if live.RunningCount != 1 {
		t.Errorf("execution with a live process handle RunningCount = %d, want 1", live.RunningCount)
	}
}
