package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestResumeCloudRunJobExecutionsFailsLostTasksWithoutRetries(t *testing.T) {
	savedJobs, savedExecutions, savedTasks := crjJobs, crjExecutions, crjTasks
	t.Cleanup(func() { crjJobs, crjExecutions, crjTasks = savedJobs, savedExecutions, savedTasks })
	crjJobs = sim.MakeStore[Job](nil, "test_crj_jobs")
	crjExecutions = sim.MakeStore[Execution](nil, "test_crj_executions")
	crjTasks = sim.MakeStore[Task](nil, "test_crj_tasks")

	const jobName = "projects/p/locations/us-central1/jobs/orphaned"
	lostExec := jobName + "/executions/lost-exec"
	doneExec := jobName + "/executions/done-exec"
	noRetries := int32(0)

	crjJobs.Put(jobName, Job{
		Name:                   jobName,
		LatestCreatedExecution: &ExecutionReference{Name: lostExec, CreateTime: "2026-01-01T00:00:00Z"},
	})
	crjExecutions.Put(lostExec, Execution{
		Name:         lostExec,
		Job:          "orphaned",
		CreateTime:   "2026-01-01T00:00:00Z",
		StartTime:    "2026-01-01T00:00:00Z",
		RunningCount: 1,
		TaskCount:    3,
		Parallelism:  2,
		Template:     &TaskTemplate{MaxRetries: &noRetries},
		Reconciling:  true,
		Conditions:   []Condition{{Type: "Ready", State: "CONDITION_PENDING"}},
	})
	crjExecutions.Put(doneExec, Execution{
		Name:           doneExec,
		CompletionTime: "2026-01-01T00:01:00Z",
		SucceededCount: 1,
		TaskCount:      1,
	})
	crjTasks.Put(lostExec+"/tasks/t0", Task{
		Name: lostExec + "/tasks/t0", Index: 0, Execution: lostExec, Reconciling: true,
		ScheduledTime: "2026-01-01T00:00:00Z", StartTime: "2026-01-01T00:00:01Z",
	})
	crjTasks.Put(lostExec+"/tasks/t1", Task{
		Name: lostExec + "/tasks/t1", Index: 1, Execution: lostExec,
		CompletionTime: "2026-01-01T00:00:30Z",
		Conditions:     []Condition{{Type: "Completed", State: "CONDITION_SUCCEEDED"}},
	})
	crjTasks.Put(lostExec+"/tasks/t2", Task{
		Name: lostExec + "/tasks/t2", Index: 2, Execution: lostExec, Reconciling: true,
		ScheduledTime: "2026-01-01T00:00:31Z",
	})

	resumeCloudRunJobExecutions()

	lost, ok := crjExecutions.Get(lostExec)
	if !ok {
		t.Fatalf("execution %s missing after recovery", lostExec)
	}
	if lost.RunningCount != 0 || lost.FailedCount != 2 || lost.SucceededCount != 1 {
		t.Errorf("lost execution counts running=%d failed=%d succeeded=%d, want 0/2/1",
			lost.RunningCount, lost.FailedCount, lost.SucceededCount)
	}
	if lost.CompletionTime == "" || lost.Reconciling {
		t.Errorf("lost execution not settled: completionTime=%q reconciling=%v", lost.CompletionTime, lost.Reconciling)
	}
	var completed *Condition
	for i := range lost.Conditions {
		if lost.Conditions[i].Type == "Completed" {
			completed = &lost.Conditions[i]
		}
	}
	if completed == nil || completed.State != "CONDITION_FAILED" {
		t.Fatalf("lost execution Completed condition = %+v, want CONDITION_FAILED", completed)
	}

	for _, name := range []string{lostExec + "/tasks/t0", lostExec + "/tasks/t2"} {
		task, _ := crjTasks.Get(name)
		if task.CompletionTime == "" || task.Reconciling {
			t.Errorf("task %s not settled: %+v", name, task)
		}
		if task.LastAttemptResult == nil || task.LastAttemptResult.Status == nil || task.LastAttemptResult.Status.Message != cloudRunLostTaskMessage {
			t.Errorf("task %s last attempt = %+v, want the lost-containers message", name, task.LastAttemptResult)
		}
	}
	if kept, _ := crjTasks.Get(lostExec + "/tasks/t1"); kept.CompletionTime != "2026-01-01T00:00:30Z" {
		t.Errorf("succeeded task rewritten by recovery: %+v", kept)
	}

	job, _ := crjJobs.Get(jobName)
	if job.LatestCreatedExecution == nil || job.LatestCreatedExecution.CompletionTime == "" {
		t.Error("job latestCreatedExecution CompletionTime not stamped")
	}
	if done, _ := crjExecutions.Get(doneExec); done.FailedCount != 0 || done.SucceededCount != 1 {
		t.Errorf("completed execution mutated by recovery: %+v", done)
	}
	if _, running := crjRuns.Load(lostExec); running {
		t.Error("a settled execution is still registered as running")
	}
}

func TestCloudRunJobTemplateDefaults(t *testing.T) {
	tmpl := &ExecutionTemplate{Template: &TaskTemplate{}}
	cloudRunJobTemplateDefaults(tmpl)
	if tmpl.TaskCount != 1 || tmpl.Parallelism != 0 || tmpl.Template.MaxRetries == nil || *tmpl.Template.MaxRetries != 3 {
		t.Fatalf("defaults = taskCount %d parallelism %d maxRetries %v, want 1, 0 and 3", tmpl.TaskCount, tmpl.Parallelism, tmpl.Template.MaxRetries)
	}
	zero := int32(0)
	explicit := &ExecutionTemplate{TaskCount: 4, Parallelism: 2, Template: &TaskTemplate{MaxRetries: &zero}}
	cloudRunJobTemplateDefaults(explicit)
	if *explicit.Template.MaxRetries != 0 || explicit.TaskCount != 4 || explicit.Parallelism != 2 {
		t.Fatalf("explicit values changed: %+v", explicit)
	}
}
