package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/jobrun"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"google.golang.org/grpc/codes"
)

// cloudRunDefaultMaxRetries is the retries a task template that names none
// gets, per TaskTemplate.maxRetries.
const cloudRunDefaultMaxRetries = 3

// cloudRunLostTaskMessage is what a task whose containers did not survive a
// simulator restart reports.
const cloudRunLostTaskMessage = "Workload containers not found after control-plane restart"

// crjRuns holds the run of every execution this process drives, by
// execution name, from its start until its RunJob operation completes.
var crjRuns sync.Map // map[string]*jobrun.Execution

// cloudRunJobTemplateDefaults fills what an execution template leaves unset:
// one task, and the retries the API defaults to. Parallelism stays 0, which
// runs as many tasks at once as the execution has.
func cloudRunJobTemplateDefaults(t *ExecutionTemplate) {
	if t == nil {
		return
	}
	if t.TaskCount == 0 {
		t.TaskCount = 1
	}
	if t.Template != nil && t.Template.MaxRetries == nil {
		retries := int32(cloudRunDefaultMaxRetries)
		t.Template.MaxRetries = &retries
	}
}

func cloudRunMaxRetries(t *TaskTemplate) int32 {
	if t == nil || t.MaxRetries == nil {
		return cloudRunDefaultMaxRetries
	}
	return *t.MaxRetries
}

// cloudRunTaskTimeout is the task template's per-attempt timeout, ten minutes
// when it sets none.
func cloudRunTaskTimeout(taskTmpl *TaskTemplate) time.Duration {
	if taskTmpl != nil && taskTmpl.Timeout != "" {
		if d, err := time.ParseDuration(taskTmpl.Timeout); err == nil {
			return d
		}
	}
	return 600 * time.Second
}

// runCloudRunJob starts one execution of a Cloud Run job: it records the
// Execution and its TaskCount Tasks and runs each task as its own container
// group, at most parallelism at once, retried up to maxRetries. It returns the
// Execution once its first tasks are scheduled, as both API versions' run
// methods report.
func runCloudRunJob(project, location, jobID string, job Job, overrides *Overrides, execID string) Execution {
	name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)
	now := nowTimestamp()
	execName := fmt.Sprintf("%s/executions/%s", name, execID)

	var taskCount int32 = 1
	var parallelism int32
	var tmpl *TaskTemplate
	if job.Template != nil {
		if job.Template.TaskCount > 0 {
			taskCount = job.Template.TaskCount
		}
		parallelism = job.Template.Parallelism
		tmpl = job.Template.Template
	}
	if overrides != nil {
		if overrides.TaskCount > 0 {
			taskCount = overrides.TaskCount
		}
		tmpl = applyCloudRunJobOverrides(tmpl, overrides)
	}
	if tmpl != nil {
		resolved := *tmpl
		retries := cloudRunMaxRetries(tmpl)
		resolved.MaxRetries = &retries
		tmpl = &resolved
	}
	// An execution's parallelism is at most its task count, and an unset one
	// runs every task at once.
	if parallelism == 0 || parallelism > taskCount {
		parallelism = taskCount
	}

	exec := Execution{
		Name:        execName,
		UID:         sim.NewUUID(),
		Job:         jobID,
		Generation:  1,
		Labels:      job.Labels,
		Parallelism: parallelism,
		CreateTime:  now,
		StartTime:   now,
		TaskCount:   taskCount,
		Template:    tmpl,
		Conditions: []Condition{
			{Type: "Ready", State: "CONDITION_PENDING", LastTransitionTime: now},
		},
		Etag:        sim.NewUUID(),
		Reconciling: true,
	}
	crjExecutions.Put(execName, exec)

	taskNames := make([]string, taskCount)
	for i := int32(0); i < taskCount; i++ {
		taskName := fmt.Sprintf("%s/tasks/%s", execName, sim.NewUUID())
		taskNames[i] = taskName
		task := Task{
			Name:       taskName,
			UID:        sim.NewUUID(),
			Generation: 1,
			Labels:     job.Labels,
			CreateTime: now,
			Job:        name,
			Execution:  execName,
			Index:      i,
			Conditions: []Condition{
				{Type: "Started", State: "CONDITION_PENDING", LastTransitionTime: now},
			},
			Etag:        sim.NewUUID(),
			Reconciling: true,
		}
		if tmpl != nil {
			task.Containers = tmpl.Containers
			task.Volumes = tmpl.Volumes
			task.MaxRetries = *tmpl.MaxRetries
			task.Timeout = tmpl.Timeout
			task.ServiceAccount = tmpl.ServiceAccount
		}
		crjTasks.Put(taskName, task)
	}

	crjJobs.Update(name, func(j *Job) {
		j.ExecutionCount++
		j.UpdateTime = now
		j.LatestCreatedExecution = &ExecutionReference{
			Name:       execName,
			CreateTime: now,
		}
		j.Etag = sim.NewUUID()
	})

	run := newCloudRunJobRun(exec, taskNames)
	run.register(jobrun.Start(cloudRunJobSpec(exec), run.hooks()))
	if current, ok := crjExecutions.Get(execName); ok {
		return current
	}
	return exec
}

func cloudRunJobSpec(exec Execution) jobrun.Spec {
	return jobrun.Spec{
		Tasks:          int(exec.TaskCount),
		Parallelism:    int(exec.Parallelism),
		MaxRetries:     int(cloudRunMaxRetries(exec.Template)),
		AttemptTimeout: cloudRunTaskTimeout(exec.Template),
	}
}

// cloudRunJobRun is what one execution's run writes to and starts from.
type cloudRunJobRun struct {
	execName  string
	project   string
	jobID     string
	execID    string
	tmpl      *TaskTemplate
	taskNames []string
	// adoptable is the containers an earlier process left, by task index and
	// attempt, while the run resumes.
	adoptable map[[2]int][]placement.Existing
	adopted   map[[2]int]bool

	mu      sync.Mutex
	started bool
	done    bool
}

func newCloudRunJobRun(exec Execution, taskNames []string) *cloudRunJobRun {
	return &cloudRunJobRun{
		execName:  exec.Name,
		project:   resourceProject(exec.Name),
		jobID:     exec.Job,
		execID:    exec.Name[strings.LastIndex(exec.Name, "/")+1:],
		tmpl:      exec.Template,
		taskNames: taskNames,
	}
}

func (r *cloudRunJobRun) hooks() jobrun.Hooks {
	return jobrun.Hooks{Start: r.start, Adopt: r.adopt, Changed: r.changed, Finished: r.finished}
}

// register makes the run reachable by cancel and delete, unless it finished
// before Start or Resume returned it.
func (r *cloudRunJobRun) register(e *jobrun.Execution, err error) {
	if err != nil {
		log.Printf("Cloud Run execution %s: %v", r.execName, err)
		r.failUnrunnable(err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.done {
		crjRuns.Store(r.execName, e)
	}
}

// failUnrunnable settles an execution whose records the run cannot start
// from.
func (r *cloudRunJobRun) failUnrunnable(err error) {
	now := nowTimestamp()
	crjExecutions.Update(r.execName, func(e *Execution) {
		e.CompletionTime = now
		e.RunningCount = 0
		e.Conditions = []Condition{
			{Type: "Ready", State: "CONDITION_FAILED", LastTransitionTime: now, Message: err.Error()},
			{Type: "Completed", State: "CONDITION_FAILED", LastTransitionTime: now, Message: err.Error()},
		}
		e.Reconciling = false
		e.Etag = sim.NewUUID()
	})
	r.concluded()
}

func (r *cloudRunJobRun) taskEnv(t jobrun.Task) map[string]string {
	return map[string]string{
		"CLOUD_RUN_JOB":          r.jobID,
		"CLOUD_RUN_EXECUTION":    r.execID,
		"CLOUD_RUN_TASK_INDEX":   strconv.Itoa(t.Index),
		"CLOUD_RUN_TASK_ATTEMPT": strconv.Itoa(t.Attempt),
		"CLOUD_RUN_TASK_COUNT":   strconv.Itoa(len(r.taskNames)),
	}
}

func (r *cloudRunJobRun) sink(t jobrun.Task) *crjLogSink {
	return &crjLogSink{project: r.project, jobName: r.jobID, labels: map[string]string{
		"execution_name": r.execID,
		"task_index":     strconv.Itoa(t.Index),
		"task_attempt":   strconv.Itoa(t.Attempt),
	}}
}

// start runs one attempt of task t as its own container group.
func (r *cloudRunJobRun) start(ctx context.Context, t jobrun.Task) (jobrun.Workload, error) {
	if r.tmpl == nil || len(r.tmpl.Containers) == 0 {
		return nil, errors.New("the execution's task template has no containers")
	}
	group, release, err := startCloudRunJobContainers(ctx, r.execName, r.execID, t.Index, t.Attempt, r.tmpl, r.taskEnv(t), r.sink(t))
	if err != nil {
		return nil, err
	}
	return &cloudRunTaskWorkload{group: group, release: release}, nil
}

// cloudRunTaskWorkload is one task attempt's container group.
type cloudRunTaskWorkload struct {
	group   *workload.Group
	release func()
}

func (w *cloudRunTaskWorkload) Wait() jobrun.Exit {
	result := w.group.Main.Wait()
	for _, h := range w.group.Sidecars {
		h.Cancel()
		h.Wait()
	}
	w.release()
	return jobrun.Exit{Code: result.ExitCode, Err: result.Error, At: result.StoppedAt}
}

// Stop sends the main container the SIGTERM Cloud Run sends a cancelled or
// timed-out task, with the documented grace before the SIGKILL, so Wait reads
// the exit code the container really stopped with.
func (w *cloudRunTaskWorkload) Stop() { w.group.Main.Stop() }

// changed writes task t's state onto its Task record and the execution's
// counts onto the Execution.
func (r *cloudRunJobRun) changed(t jobrun.Task, c jobrun.Counts) {
	now := nowTimestamp()
	crjTasks.Update(r.taskNames[t.Index], func(task *Task) {
		task.Retried = int32(t.Attempt)
		switch {
		case t.Stopping:
			// The task settles from its containers' exit, so it reports the
			// exit code they stopped with.
		case t.Status == jobrun.Running && t.Started.IsZero():
			task.ScheduledTime = now
			task.StartTime = ""
			task.Conditions = []Condition{{Type: "Started", State: "CONDITION_PENDING", LastTransitionTime: now}}
		case t.Status == jobrun.Running:
			task.StartTime = cloudRunTimestamp(t.Started)
			task.Conditions = []Condition{{Type: "Started", State: "CONDITION_SUCCEEDED", LastTransitionTime: task.StartTime}}
		case t.Status.Terminal():
			task.CompletionTime = cloudRunTimestamp(t.Ended)
			task.Conditions = cloudRunTaskTerminalConditions(t, task.CompletionTime)
		}
		if t.Last != nil {
			task.LastAttemptResult = cloudRunAttemptResult(*t.Last)
		}
		task.Reconciling = !t.Status.Terminal() || t.Stopping
		task.Etag = sim.NewUUID()
	})
	crjExecutions.Update(r.execName, func(e *Execution) {
		e.RunningCount = int32(c.Running)
		e.SucceededCount = int32(c.Succeeded)
		e.FailedCount = int32(c.Failed)
		e.CancelledCount = int32(c.Cancelled)
		e.RetriedCount = int32(c.Retried)
		e.Etag = sim.NewUUID()
	})
	if t.Status == jobrun.Running && !t.Started.IsZero() {
		r.mu.Lock()
		first := !r.started
		r.started = true
		r.mu.Unlock()
		if first {
			settleCloudRunJobExecutionToken(r.execName)
		}
	}
}

func cloudRunTimestamp(t time.Time) string {
	if t.IsZero() {
		return nowTimestamp()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func cloudRunTaskTerminalConditions(t jobrun.Task, at string) []Condition {
	completed := Condition{Type: "Completed", State: "CONDITION_SUCCEEDED", LastTransitionTime: at}
	switch t.Status {
	case jobrun.Cancelled:
		completed.State, completed.Reason = "CONDITION_FAILED", "Cancelled"
	case jobrun.Failed:
		completed.State = "CONDITION_FAILED"
		switch {
		case t.Last != nil && t.Last.Lost:
			completed.Message = cloudRunLostTaskMessage
		case t.Last != nil && t.Last.StartErr != nil:
			completed.Reason, completed.Message = "NonZeroExitCode", t.Last.StartErr.Error()
		default:
			completed.Reason = "NonZeroExitCode"
		}
	}
	started := Condition{Type: "Started", State: "CONDITION_SUCCEEDED", LastTransitionTime: at}
	if t.Started.IsZero() && t.Last == nil {
		started.State = "CONDITION_PENDING"
	}
	return []Condition{started, completed}
}

// cloudRunAttemptResult renders how an attempt ended as a TaskAttemptResult:
// OK when it succeeded, CANCELLED when a cancel stopped it, and UNKNOWN
// otherwise, with the exit code the main container stopped with.
func cloudRunAttemptResult(r jobrun.Result) *TaskAttemptResult {
	result := &TaskAttemptResult{}
	if r.Code > 0 {
		result.ExitCode = int32(r.Code)
	}
	switch {
	case r.Succeeded():
		result.Status = &RPCStatus{Code: int32(codes.OK)}
	case r.Stopped:
		result.Status = &RPCStatus{Code: int32(codes.Canceled)}
	case r.Lost:
		result.Status = &RPCStatus{Code: int32(codes.Unknown), Message: cloudRunLostTaskMessage}
	case r.StartErr != nil:
		result.Status = &RPCStatus{Code: int32(codes.Unknown), Message: r.StartErr.Error()}
	default:
		result.Status = &RPCStatus{Code: int32(codes.Unknown)}
	}
	return result
}

// finished settles the execution from its verdict: its conditions, the job's
// reference to it, the job its token created, and its RunJob operation.
func (r *cloudRunJobRun) finished(v jobrun.Verdict, c jobrun.Counts) {
	now := nowTimestamp()
	crjExecutions.Update(r.execName, func(e *Execution) {
		e.RunningCount = int32(c.Running)
		e.SucceededCount = int32(c.Succeeded)
		e.FailedCount = int32(c.Failed)
		e.CancelledCount = int32(c.Cancelled)
		e.RetriedCount = int32(c.Retried)
		e.Reconciling = false
		e.Etag = sim.NewUUID()
		if e.CompletionTime != "" {
			// A cancel already recorded the execution's end.
			return
		}
		e.CompletionTime = now
		state, reason, message := "CONDITION_SUCCEEDED", "", ""
		switch v {
		case jobrun.Cancellation:
			state, reason = "CONDITION_FAILED", "Cancelled"
		case jobrun.Failure:
			state, reason, message = "CONDITION_FAILED", "NonZeroExitCode", cloudRunExecutionFailureMessage(*e)
		}
		e.Conditions = []Condition{
			{Type: "Ready", State: protoEnum[runConditionState](state), LastTransitionTime: now, Reason: reason, Message: message},
			{Type: "Completed", State: protoEnum[runConditionState](state), LastTransitionTime: now, Reason: reason, Message: message},
		}
	})
	r.concluded()
}

// concluded stamps the job's reference to the finished execution, settles the
// job its token created and completes its RunJob operation, once everything
// the execution's end writes is in place, so a client the operation wakes
// reads the settled state.
func (r *cloudRunJobRun) concluded() {
	r.mu.Lock()
	r.done = true
	crjRuns.Delete(r.execName)
	r.mu.Unlock()
	if jobName, _, ok := strings.Cut(r.execName, "/executions/"); ok {
		crjJobs.Update(jobName, func(j *Job) {
			if j.LatestCreatedExecution != nil && j.LatestCreatedExecution.Name == r.execName && j.LatestCreatedExecution.CompletionTime == "" {
				j.LatestCreatedExecution.CompletionTime = nowTimestamp()
				j.Etag = sim.NewUUID()
			}
		})
	}
	settleCloudRunJobExecutionToken(r.execName)
	finishCloudRunJobRunOperations(r.execName)
}

// cloudRunExecutionStarted reports whether any task of the execution has had
// its containers start.
func cloudRunExecutionStarted(execName string) bool {
	prefix := execName + "/tasks/"
	return len(crjTasks.Filter(func(t Task) bool {
		return strings.HasPrefix(t.Name, prefix) && t.StartTime != ""
	})) > 0
}

// cancelCloudRunExecution cancels a running execution: it records the
// execution as cancelled and stops its tasks — those not yet started never
// start, and the containers of those running get the stop signal. Each task
// settles once its containers exit, from the exit code they stopped with.
func cancelCloudRunExecution(project, location, jobID, execID string) (Execution, bool) {
	name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s/executions/%s", project, location, jobID, execID)

	running := false
	ok := crjExecutions.Update(name, func(e *Execution) {
		if e.CompletionTime != "" {
			return
		}
		running = true
		now := nowTimestamp()
		e.CompletionTime = now
		// A cancelled execution's terminal condition is the failed one with its
		// reason: `gcloud run jobs executions cancel` reads a succeeded condition as
		// "finished before the cancel landed".
		e.Conditions = []Condition{
			{Type: "Ready", State: "CONDITION_FAILED", LastTransitionTime: now, Reason: "Cancelled"},
			{Type: "Completed", State: "CONDITION_FAILED", LastTransitionTime: now, Reason: "Cancelled"},
		}
		e.Reconciling = false
		e.Etag = sim.NewUUID()
	})
	if !ok {
		return Execution{}, false
	}
	if running {
		stopCloudRunExecutionRun(name)
		jobName := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)
		crjJobs.Update(jobName, func(j *Job) {
			if j.LatestCreatedExecution != nil && j.LatestCreatedExecution.Name == name && j.LatestCreatedExecution.CompletionTime == "" {
				j.LatestCreatedExecution.CompletionTime = nowTimestamp()
				j.Etag = sim.NewUUID()
			}
		})
	}
	exec, _ := crjExecutions.Get(name)
	return exec, true
}

// stopCloudRunExecutionRun cancels whatever the execution still runs.
func stopCloudRunExecutionRun(execName string) {
	if v, ok := crjRuns.Load(execName); ok {
		if run, ok := v.(*jobrun.Execution); ok {
			run.Cancel()
		}
	}
}

// cloudRunJobTaskLabels ties a task attempt's containers to the execution,
// task and attempt they run, which is what adoption reads back.
func cloudRunJobTaskLabels(execName string, index, attempt int, container string) map[string]string {
	return map[string]string{
		cloudRunTaskExecutionLabel: execName,
		cloudRunTaskContainerLabel: container,
		cloudRunTaskIndexLabel:     strconv.Itoa(index),
		cloudRunTaskAttemptLabel:   strconv.Itoa(attempt),
	}
}

// resumeCloudRunJobExecutions takes over every execution an earlier process
// left running: each running task adopts the containers that process left
// for its attempt, a task whose containers are gone ends its attempt as
// lost and is retried while it has retries left, and pending tasks start.
// Every container no running task claims is removed.
func resumeCloudRunJobExecutions() {
	found := cloudRunJobContainersLeft()
	claimed := map[string]bool{}
	for _, exec := range crjExecutions.List() {
		if exec.CompletionTime != "" {
			continue
		}
		prefix := exec.Name + "/tasks/"
		records := crjTasks.Filter(func(t Task) bool { return strings.HasPrefix(t.Name, prefix) })
		sort.Slice(records, func(i, j int) bool { return records[i].Index < records[j].Index })
		taskNames := make([]string, len(records))
		tasks := make([]jobrun.Task, len(records))
		for i, record := range records {
			taskNames[i] = record.Name
			tasks[i] = cloudRunJobRunTask(record)
		}
		run := newCloudRunJobRun(exec, taskNames)
		run.adoptable = found[exec.Name]
		run.adopted = map[[2]int]bool{}
		run.register(jobrun.Resume(cloudRunJobSpec(exec), run.hooks(), tasks))
		for key := range run.adopted {
			for _, c := range run.adoptable[key] {
				claimed[c.ID] = true
			}
		}
	}
	for execName, byTask := range found {
		for _, group := range byTask {
			var left []placement.Existing
			for _, c := range group {
				if !claimed[c.ID] {
					left = append(left, c)
				}
			}
			if len(left) > 0 {
				removeCloudRunContainers(execName, left)
			}
		}
	}
}

// cloudRunJobContainersLeft is the job task containers an earlier process
// left, by execution, then task index and attempt.
func cloudRunJobContainersLeft() map[string]map[[2]int][]placement.Existing {
	found := map[string]map[[2]int][]placement.Existing{}
	if !sim.HasPersistentWorkloadIdentity() || sim.RequireContainerRuntime("adopting Cloud Run job executions") != nil {
		return found
	}
	backend, err := gcpPlacement.For(placementRun)
	if err != nil {
		log.Printf("Cloud Run jobs: list the containers an earlier process left: %v", err)
		return found
	}
	containers, err := backend.Find(context.Background(), map[string]string{cloudRunTaskExecutionLabel: ""})
	if err != nil {
		log.Printf("Cloud Run jobs: list the containers an earlier process left: %v", err)
		return found
	}
	for _, c := range containers {
		execName := c.Labels[cloudRunTaskExecutionLabel]
		index, indexErr := strconv.Atoi(c.Labels[cloudRunTaskIndexLabel])
		attempt, attemptErr := strconv.Atoi(c.Labels[cloudRunTaskAttemptLabel])
		if indexErr != nil || attemptErr != nil {
			index, attempt = -1, -1
		}
		if found[execName] == nil {
			found[execName] = map[[2]int][]placement.Existing{}
		}
		key := [2]int{index, attempt}
		found[execName][key] = append(found[execName][key], c)
	}
	return found
}

// cloudRunJobRunTask reads a Task record back into the run's view of it.
func cloudRunJobRunTask(record Task) jobrun.Task {
	t := jobrun.Task{Index: int(record.Index), Attempt: int(record.Retried)}
	parse := func(ts string) time.Time {
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return time.Time{}
		}
		return parsed
	}
	switch {
	case record.CompletionTime != "":
		t.Ended = parse(record.CompletionTime)
		t.Status = jobrun.Failed
		for _, c := range record.Conditions {
			if c.Type != "Completed" {
				continue
			}
			switch {
			case c.State == "CONDITION_SUCCEEDED":
				t.Status = jobrun.Succeeded
			case c.Reason == "Cancelled":
				t.Status = jobrun.Cancelled
			}
		}
	case record.ScheduledTime != "":
		t.Status = jobrun.Running
		t.Started = parse(record.StartTime)
		t.FirstStarted = t.Started
	}
	return t
}

// adopt takes over the containers an earlier process left running task t's
// attempt, under the timeout the run counts from the attempt's start.
func (r *cloudRunJobRun) adopt(t jobrun.Task) (jobrun.Workload, error) {
	key := [2]int{t.Index, t.Attempt}
	group := r.adoptable[key]
	if len(group) == 0 {
		return nil, nil
	}
	w, err := adoptCloudRunJobTask(r, t, group)
	if err != nil {
		log.Printf("Cloud Run execution %s task %d: %v", r.execName, t.Index, err)
		return nil, err
	}
	r.adopted[key] = true
	return w, nil
}

// adoptCloudRunJobTask resumes task t's attempt on the containers group
// holds.
func adoptCloudRunJobTask(r *cloudRunJobRun, t jobrun.Task, group []placement.Existing) (jobrun.Workload, error) {
	if r.tmpl == nil || len(r.tmpl.Containers) == 0 || len(group) != len(r.tmpl.Containers) {
		return nil, fmt.Errorf("the containers do not run the execution's task template")
	}
	byName := make(map[string]placement.Existing, len(group))
	for _, c := range group {
		byName[c.Labels[cloudRunTaskContainerLabel]] = c
	}
	containers := r.tmpl.Containers
	for _, c := range containers {
		if _, ok := byName[c.Name]; !ok {
			return nil, fmt.Errorf("no container runs %q", c.Name)
		}
	}
	writable, err := cloudRunWritableBuckets(containers, r.tmpl.Volumes)
	if err != nil {
		return nil, err
	}
	releaseMounts, err := gcsAcquireMounts(writable)
	if err != nil {
		return nil, err
	}
	taskSink := r.sink(t)
	sink := newCloudRunAdoptedLogSink(taskSink, r.project, cloudRunJobLogResource(r.jobID), taskSink.labels)
	handles := make([]placement.Handle, 0, len(containers))
	for _, c := range containers {
		handle, err := adoptCloudRunContainer(byName[c.Name].ID, sim.ContainerConfig{CancelGracePeriod: cloudRunStopGrace}, sink)
		if err != nil {
			for _, h := range handles {
				h.Cancel()
			}
			releaseMounts()
			return nil, fmt.Errorf("adopt container %s: %w", byName[c.Name].ID, err)
		}
		handles = append(handles, handle)
	}
	releaseAddress, err := gcpReserveAdoptedAddress(context.Background(), handles[0], r.execName, r.tmpl.VpcAccess)
	if err != nil {
		for _, h := range handles {
			h.Cancel()
		}
		releaseMounts()
		return nil, err
	}
	cpu := cloudRunAdoptCPU(r.project, resourceLocation(r.execName), containers)
	return &cloudRunTaskWorkload{
		group: &workload.Group{Main: handles[0], Sidecars: handles[1:]},
		release: func() {
			releaseMounts()
			releaseAddress()
			cpu.release()
		},
	}, nil
}
