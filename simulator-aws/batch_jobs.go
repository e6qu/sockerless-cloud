package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/google/uuid"
)

const (
	batchStatusSubmitted = "SUBMITTED"
	batchStatusPending   = "PENDING"
	batchStatusRunnable  = "RUNNABLE"
	batchStatusStarting  = "STARTING"
	batchStatusRunning   = "RUNNING"
	batchStatusSucceeded = "SUCCEEDED"
	batchStatusFailed    = "FAILED"
)

var batchJobStatuses = []string{
	batchStatusSubmitted, batchStatusPending, batchStatusRunnable, batchStatusStarting,
	batchStatusRunning, batchStatusSucceeded, batchStatusFailed,
}

const (
	// batchContainerStopTimeout is how long Batch waits after the SIGTERM that
	// stops a job's container before it sends SIGKILL.
	batchContainerStopTimeout = 30 * time.Second
	batchDefaultLogGroup      = "/aws/batch/job"

	batchReasonContainerExited = "Essential container in task exited"
	batchReasonTaskNotStarted  = "Task failed to start"
	batchReasonTimedOut        = "Job attempt duration exceeded timeout"
)

// BatchRetryStrategy is the parsed form of a job's retryStrategy.
type BatchRetryStrategy struct {
	Attempts       int                   `json:"attempts,omitempty"`
	EvaluateOnExit []BatchEvaluateOnExit `json:"evaluateOnExit,omitempty"`
}

type BatchEvaluateOnExit struct {
	OnStatusReason string `json:"onStatusReason,omitempty"`
	OnReason       string `json:"onReason,omitempty"`
	OnExitCode     string `json:"onExitCode,omitempty"`
	Action         string `json:"action"`
}

type BatchArrayProperties struct {
	StatusSummary              map[string]int `json:"statusSummary,omitempty"`
	StatusSummaryLastUpdatedAt int64          `json:"statusSummaryLastUpdatedAt,omitempty"`
	Size                       int            `json:"size,omitempty"`
	Index                      *int           `json:"index,omitempty"`
}

type BatchAttempt struct {
	Container    BatchAttemptContainer `json:"container"`
	StartedAt    int64                 `json:"startedAt,omitempty"`
	StoppedAt    int64                 `json:"stoppedAt,omitempty"`
	StatusReason string                `json:"statusReason,omitempty"`
}

type BatchAttemptContainer struct {
	ContainerInstanceArn string `json:"containerInstanceArn,omitempty"`
	ExitCode             *int   `json:"exitCode,omitempty"`
	Reason               string `json:"reason,omitempty"`
	LogStreamName        string `json:"logStreamName,omitempty"`
}

// batchRunningAttempt is the attempt a job has in flight between the scheduler
// placing it on a compute environment and its container exiting.
type batchRunningAttempt struct {
	Number             int    `json:"number"`
	TaskID             string `json:"taskId"`
	ComputeEnvironment string `json:"computeEnvironment"`
	LogGroup           string `json:"logGroup,omitempty"`
	LogStream          string `json:"logStream,omitempty"`
	ContainerID        string `json:"containerId,omitempty"`
	StartedAt          int64  `json:"startedAt,omitempty"`
	TimedOut           bool   `json:"timedOut,omitempty"`
}

type batchRunnableEntry struct {
	jobID string
	queue string
	vcpus float64
}

// The scheduler's working set, guarded by batchMu and rebuilt from the job
// store at startup: the RUNNABLE jobs in arrival order, the vCPUs each
// compute environment has placed, and the timeout armed on each running
// attempt. batchJobHandles, under the same lock, holds each attempt's container.
var (
	batchRunnable      []batchRunnableEntry
	batchCEInUse       = map[string]float64{}
	batchAttemptTimers = map[string]*bg.Timer{}
)

func batchTerminal(status string) bool {
	return status == batchStatusSucceeded || status == batchStatusFailed
}

func (job BatchJob) isArrayParent() bool {
	return job.ArrayProperties != nil && job.ArrayProperties.Size > 0
}

func batchChildJobID(parentID string, index int) string {
	return parentID + ":" + strconv.Itoa(index)
}

func batchParseRetryStrategy(raw map[string]any) (BatchRetryStrategy, error) {
	var strategy BatchRetryStrategy
	if raw == nil {
		return strategy, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return strategy, fmt.Errorf("retryStrategy is malformed: %w", err)
	}
	if err := json.Unmarshal(encoded, &strategy); err != nil {
		return strategy, fmt.Errorf("retryStrategy is malformed: %w", err)
	}
	_, attemptsSet := raw["attempts"]
	if attemptsSet && (strategy.Attempts < 1 || strategy.Attempts > 10) {
		return strategy, fmt.Errorf("retryStrategy.attempts must be between 1 and 10, got %d", strategy.Attempts)
	}
	if len(strategy.EvaluateOnExit) > 5 {
		return strategy, fmt.Errorf("retryStrategy.evaluateOnExit takes at most 5 conditions, got %d", len(strategy.EvaluateOnExit))
	}
	if len(strategy.EvaluateOnExit) > 0 && !attemptsSet {
		return strategy, errors.New("retryStrategy.attempts is required when retryStrategy.evaluateOnExit is specified")
	}
	for _, rule := range strategy.EvaluateOnExit {
		if !strings.EqualFold(rule.Action, "RETRY") && !strings.EqualFold(rule.Action, "EXIT") {
			return strategy, fmt.Errorf("retryStrategy.evaluateOnExit action must be RETRY or EXIT, got %q", rule.Action)
		}
	}
	return strategy, nil
}

// batchGlobMatch applies an evaluateOnExit pattern, which matches exactly or,
// ending in an asterisk, by prefix.
func batchGlobMatch(pattern, value string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(value, prefix)
	}
	return pattern == value
}

func (rule BatchEvaluateOnExit) matches(attempt BatchAttempt) bool {
	if rule.OnStatusReason != "" && !batchGlobMatch(rule.OnStatusReason, attempt.StatusReason) {
		return false
	}
	if rule.OnReason != "" && !batchGlobMatch(rule.OnReason, attempt.Container.Reason) {
		return false
	}
	if rule.OnExitCode != "" {
		exitCode := ""
		if attempt.Container.ExitCode != nil {
			exitCode = strconv.Itoa(*attempt.Container.ExitCode)
		}
		if !batchGlobMatch(rule.OnExitCode, exitCode) {
			return false
		}
	}
	return true
}

// batchRetryAfter decides whether a failed attempt goes back to RUNNABLE: the
// first evaluateOnExit condition that matches decides, and with none matching
// Batch retries while attempts remain.
func batchRetryAfter(strategy BatchRetryStrategy, attemptsMade int, last BatchAttempt) bool {
	limit := max(strategy.Attempts, 1)
	if attemptsMade >= limit {
		return false
	}
	for _, rule := range strategy.EvaluateOnExit {
		if rule.matches(last) {
			return strings.EqualFold(rule.Action, "RETRY")
		}
	}
	return true
}

func batchNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		parsed, err := strconv.ParseFloat(n, 64)
		return parsed, err == nil
	}
	return 0, false
}

// batchVCPUs reads the vCPUs a job reserves from its container properties and
// overrides, through either vcpus or a VCPU resource requirement.
func batchVCPUs(properties ...map[string]any) float64 {
	vcpus := 1.0
	for _, props := range properties {
		if props == nil {
			continue
		}
		if n, ok := batchNumber(props["vcpus"]); ok && n > 0 {
			vcpus = n
		}
		requirements, _ := props["resourceRequirements"].([]any)
		for _, item := range requirements {
			requirement, _ := item.(map[string]any)
			if batchString(requirement["type"]) != "VCPU" {
				continue
			}
			if n, ok := batchNumber(requirement["value"]); ok && n > 0 {
				vcpus = n
			}
		}
	}
	return vcpus
}

type batchPlacement struct {
	name     string
	maxVCPUs float64
}

// batchQueuePlacements lists the compute environments a queue can place jobs
// on, in the queue's order: those that exist, are ENABLED and are VALID.
func batchQueuePlacements(queueName string) []batchPlacement {
	queue, ok := batchJobQueues.Get(queueName)
	if !ok {
		return nil
	}
	type ordered struct {
		order float64
		name  string
	}
	var entries []ordered
	for _, entry := range queue.ComputeEnvironmentOrder {
		order, _ := batchNumber(entry["order"])
		entries = append(entries, ordered{order: order, name: batchNameFromARN(batchString(entry["computeEnvironment"]))})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].order < entries[j].order })
	var placements []batchPlacement
	for _, entry := range entries {
		ce, ok := batchComputeEnvs.Get(entry.name)
		if !ok || ce.State != "ENABLED" || ce.Status != "VALID" {
			continue
		}
		maxVCPUs, _ := batchNumber(ce.ComputeResources["maxvCpus"])
		placements = append(placements, batchPlacement{name: ce.ComputeEnvironmentName, maxVCPUs: maxVCPUs})
	}
	return placements
}

// batchSetStatus moves a job to status and, for an array child, keeps its
// parent's statusSummary in step and settles the parent once every child has.
// The caller holds batchMu and stores job.
func batchSetStatus(job *BatchJob, status string) {
	previous := job.Status
	job.Status = status
	if job.ArrayJobID == "" || previous == status {
		return
	}
	parent, ok := batchJobs.Get(job.ArrayJobID)
	if !ok || parent.ArrayProperties == nil {
		return
	}
	summary := parent.ArrayProperties.StatusSummary
	if previous != "" {
		summary[previous]--
	}
	summary[status]++
	parent.ArrayProperties.StatusSummaryLastUpdatedAt = batchEpochMs()
	batchSettleArrayParent(&parent)
	batchJobs.Put(parent.JobID, parent)
}

func batchSettleArrayParent(parent *BatchJob) {
	if batchTerminal(parent.Status) {
		return
	}
	summary := parent.ArrayProperties.StatusSummary
	if summary[batchStatusSucceeded]+summary[batchStatusFailed] < parent.ArrayProperties.Size {
		return
	}
	parent.StoppedAt = batchEpochMs()
	if summary[batchStatusFailed] == 0 && !parent.IsCancelled && !parent.IsTerminated {
		parent.Status = batchStatusSucceeded
		return
	}
	parent.Status = batchStatusFailed
	if parent.StopReason != "" {
		parent.StatusReason = parent.StopReason
	}
}

func batchEnqueueLocked(job *BatchJob) {
	batchSetStatus(job, batchStatusRunnable)
	batchRunnable = append(batchRunnable, batchRunnableEntry{
		jobID: job.JobID,
		queue: batchNameFromARN(job.JobQueue),
		vcpus: job.VCPUs,
	})
}

func batchDequeueLocked(jobID string) {
	kept := batchRunnable[:0]
	for _, entry := range batchRunnable {
		if entry.jobID != jobID {
			kept = append(kept, entry)
		}
	}
	batchRunnable = kept
}

// batchScheduleJob is the scheduler's evaluation of a SUBMITTED job: a job
// with no dependencies becomes RUNNABLE, and an array job spawns its children
// and waits on them in PENDING.
func batchScheduleJob(jobID string) {
	batchMu.Lock()
	defer batchMu.Unlock()
	batchScheduleJobLocked(jobID)
	batchDispatchLocked()
}

func batchScheduleJobLocked(jobID string) {
	job, ok := batchJobs.Get(jobID)
	if !ok || job.Status != batchStatusSubmitted {
		return
	}
	if !job.isArrayParent() {
		batchEnqueueLocked(&job)
		batchJobs.Put(jobID, job)
		return
	}
	now := batchEpochMs()
	size := job.ArrayProperties.Size
	summary := make(map[string]int, len(batchJobStatuses))
	for _, status := range batchJobStatuses {
		summary[status] = 0
	}
	// A child enters PENDING when its parent spawns it and RUNNABLE when the
	// scheduler finds it has no dependencies; both happen under this one lock
	// hold, so no reader can observe the PENDING step.
	summary[batchStatusRunnable] = size
	for index := range size {
		childIndex := index
		childID := batchChildJobID(jobID, index)
		child := BatchJob{
			JobID:            childID,
			JobArn:           batchARN("job/" + childID),
			JobName:          job.JobName,
			JobQueue:         job.JobQueue,
			Status:           batchStatusRunnable,
			JobDefinition:    job.JobDefinition,
			CreatedAt:        now,
			Container:        maps.Clone(job.Container),
			Attempts:         []BatchAttempt{},
			RetryStrategy:    job.RetryStrategy,
			Timeout:          job.Timeout,
			ArrayProperties:  &BatchArrayProperties{Index: &childIndex},
			Tags:             job.Tags,
			ExecutionConfig:  job.ExecutionConfig,
			ArrayJobID:       jobID,
			VCPUs:            job.VCPUs,
			Retry:            job.Retry,
			LogConfiguration: job.LogConfiguration,
		}
		batchJobs.Put(childID, child)
		batchRunnable = append(batchRunnable, batchRunnableEntry{jobID: childID, queue: batchNameFromARN(job.JobQueue), vcpus: job.VCPUs})
	}
	job.Status = batchStatusPending
	job.ArrayProperties.StatusSummary = summary
	job.ArrayProperties.StatusSummaryLastUpdatedAt = now
	batchJobs.Put(jobID, job)
}

// batchDispatchLocked places RUNNABLE jobs, oldest first, on the first
// compute environment of their queue with the vCPUs to spare, and starts
// their attempts. Jobs no environment can take stay RUNNABLE until an attempt
// frees capacity or an environment or queue changes.
func batchDispatchLocked() {
	placements := map[string][]batchPlacement{}
	kept := batchRunnable[:0]
	for _, entry := range batchRunnable {
		candidates, ok := placements[entry.queue]
		if !ok {
			candidates = batchQueuePlacements(entry.queue)
			placements[entry.queue] = candidates
		}
		placed := ""
		for _, candidate := range candidates {
			if candidate.maxVCPUs == 0 || batchCEInUse[candidate.name]+entry.vcpus <= candidate.maxVCPUs {
				placed = candidate.name
				break
			}
		}
		if placed == "" {
			kept = append(kept, entry)
			continue
		}
		job, ok := batchJobs.Get(entry.jobID)
		if !ok || job.Status != batchStatusRunnable {
			continue
		}
		batchCEInUse[placed] += job.VCPUs
		number := len(job.Attempts) + 1
		job.Running = &batchRunningAttempt{
			Number:             number,
			TaskID:             strings.ReplaceAll(uuid.NewString(), "-", ""),
			ComputeEnvironment: placed,
		}
		batchSetStatus(&job, batchStatusStarting)
		batchJobs.Put(job.JobID, job)
		jobID := job.JobID
		bg.Go(func() { batchLaunchAttempt(jobID, number) })
	}
	batchRunnable = kept
}

func batchJobDefinitionName(arn string) string {
	name, _, _ := strings.Cut(batchJobDefKey(arn), ":")
	return name
}

func batchAttemptConfig(job BatchJob) sim.ContainerConfig {
	cfg := *job.ExecutionConfig
	env := maps.Clone(cfg.Env)
	if env == nil {
		env = map[string]string{}
	}
	env["AWS_BATCH_JOB_ID"] = job.JobID
	env["AWS_BATCH_JOB_ATTEMPT"] = strconv.Itoa(job.Running.Number)
	env["AWS_BATCH_JQ_NAME"] = batchNameFromARN(job.JobQueue)
	env["AWS_BATCH_CE_NAME"] = job.Running.ComputeEnvironment
	if job.ArrayProperties != nil && job.ArrayProperties.Index != nil {
		env["AWS_BATCH_JOB_ARRAY_INDEX"] = strconv.Itoa(*job.ArrayProperties.Index)
	}
	cfg.Env = env
	// Docker container names admit no colon, which array child job IDs carry.
	cfg.Name = "sockerless-batch-" + strings.ReplaceAll(job.JobID, ":", "-") + "-" + strconv.Itoa(job.Running.Number)
	cfg.Labels = batchAttemptLabels(job.JobID, job.Running.Number)
	return cfg
}

func batchAttemptLabels(jobID string, number int) map[string]string {
	return map[string]string{"aws-batch-job-id": jobID, "aws-batch-job-attempt": strconv.Itoa(number)}
}

// batchAttemptLogTarget names the CloudWatch Logs stream an attempt's awslogs
// driver writes, `<job definition name>/default/<task id>` in /aws/batch/job
// unless the job definition configures its own; ok is false when the job
// definition names another log driver.
func batchAttemptLogTarget(job BatchJob) (group, stream string, createGroup, ok bool) {
	driver := "awslogs"
	options := map[string]string{}
	if job.LogConfiguration != nil {
		driver = batchString(job.LogConfiguration["logDriver"])
		raw, _ := job.LogConfiguration["options"].(map[string]any)
		for key, value := range raw {
			options[key] = batchString(value)
		}
	}
	if driver != "awslogs" {
		return "", "", false, false
	}
	group = options["awslogs-group"]
	createGroup = options["awslogs-create-group"] == "true"
	if group == "" {
		group, createGroup = batchDefaultLogGroup, true
	}
	prefix := options["awslogs-stream-prefix"]
	if prefix == "" {
		prefix = batchJobDefinitionName(job.JobDefinition)
		if len(prefix) > 200 {
			prefix = prefix[:200]
		}
	}
	return group, prefix + "/default/" + job.Running.TaskID, createGroup, true
}

func batchOpenLogStream(group, stream string, createGroup bool) error {
	nowMs := time.Now().UnixMilli()
	if _, exists := cwLogGroups.Get(group); !exists {
		if !createGroup {
			return fmt.Errorf("ResourceInitializationError: failed to validate logger args: The specified log group does not exist: %s", group)
		}
		cwLogGroups.Put(group, CWLogGroup{LogGroupName: group, Arn: cwLogGroupArn(group), CreationTime: nowMs})
	}
	key := cwEventsKey(group, stream)
	if _, exists := cwLogStreams.Get(key); !exists {
		cwLogStreams.Put(key, CWLogStream{
			LogStreamName:       stream,
			LogGroupName:        group,
			CreationTime:        nowMs,
			Arn:                 cwLogStreamArn(group, stream),
			UploadSequenceToken: "1",
		})
		cwLogEvents.Put(key, []CWLogEvent{})
	}
	return nil
}

func batchAttemptSink(running batchRunningAttempt) sim.LogSink {
	if running.LogStream == "" {
		return sim.NoopSink{}
	}
	return &cwLogSink{logGroup: running.LogGroup, logStream: running.LogStream}
}

// batchLaunchAttempt starts the container of an attempt the scheduler placed:
// STARTING covers the image pull and container creation, RUNNING begins once
// the container has started.
func batchLaunchAttempt(jobID string, number int) {
	batchMu.Lock()
	job, ok := batchJobs.Get(jobID)
	if !ok || job.Status != batchStatusStarting || job.Running == nil || job.Running.Number != number {
		batchMu.Unlock()
		return
	}
	cfg := batchAttemptConfig(job)
	var openErr error
	if group, stream, createGroup, ok := batchAttemptLogTarget(job); ok {
		if openErr = batchOpenLogStream(group, stream, createGroup); openErr == nil {
			job.Running.LogGroup, job.Running.LogStream = group, stream
			batchJobs.Put(jobID, job)
		}
	}
	sink := batchAttemptSink(*job.Running)
	batchMu.Unlock()
	if openErr != nil {
		batchAttemptEnded(jobID, number, sim.ProcessResult{}, openErr)
		return
	}

	handle, err := sim.StartContainerSync(cfg, sink)
	if err != nil {
		batchAttemptEnded(jobID, number, sim.ProcessResult{}, err)
		return
	}

	batchMu.Lock()
	job, ok = batchJobs.Get(jobID)
	if !ok || job.Running == nil || job.Running.Number != number {
		batchMu.Unlock()
		handle.Cancel()
		return
	}
	job.Running.ContainerID = handle.ContainerID
	job.Running.StartedAt = batchEpochMs()
	batchJobHandles[jobID] = handle
	if job.IsTerminated {
		handle.Cancel()
	} else {
		batchMarkRunningLocked(&job)
		batchArmAttemptTimeoutLocked(job, batchTimeout(job.Timeout))
	}
	batchJobs.Put(jobID, job)
	batchMu.Unlock()
	batchWatchAttempt(jobID, number, handle)
}

func batchMarkRunningLocked(job *BatchJob) {
	batchSetStatus(job, batchStatusRunning)
	job.StartedAt = job.Running.StartedAt
	if job.Container == nil {
		job.Container = map[string]any{}
	}
	job.Container["containerInstanceArn"] = batchARN("container/" + job.Running.ContainerID)
	if job.Running.LogStream != "" {
		job.Container["logStreamName"] = job.Running.LogStream
	}
}

func batchWatchAttempt(jobID string, number int, handle *sim.ContainerHandle) {
	var result sim.ProcessResult
	bg.WatchThen(func() { result = handle.Wait() }, func() {
		batchAttemptEnded(jobID, number, result, nil)
	})
}

// batchArmAttemptTimeoutLocked enforces the job's attemptDurationSeconds on the
// running attempt: Batch terminates an attempt that outlives it.
func batchArmAttemptTimeoutLocked(job BatchJob, remaining time.Duration) {
	if batchTimeout(job.Timeout) == 0 {
		return
	}
	jobID, number := job.JobID, job.Running.Number
	timer := bg.AfterFunc(max(remaining, 0), func() { batchAttemptTimedOut(jobID, number) })
	batchAttemptTimers[jobID] = timer
}

func batchAttemptTimedOut(jobID string, number int) {
	batchMu.Lock()
	defer batchMu.Unlock()
	job, ok := batchJobs.Get(jobID)
	if !ok || job.Running == nil || job.Running.Number != number || job.Status != batchStatusRunning {
		return
	}
	job.Running.TimedOut = true
	batchJobs.Put(jobID, job)
	if handle, ok := batchJobHandles[jobID]; ok {
		handle.Cancel()
	}
}

func batchTruncate(s string, limit int) string {
	if len(s) > limit {
		return s[:limit]
	}
	return s
}

// batchAttemptEnded records a finished attempt and moves the job on: to
// SUCCEEDED on exit code 0, back to RUNNABLE when its retry strategy retries,
// and to FAILED otherwise. A terminated or timed-out attempt is never retried.
func batchAttemptEnded(jobID string, number int, result sim.ProcessResult, startErr error) {
	batchMu.Lock()
	defer batchMu.Unlock()
	job, ok := batchJobs.Get(jobID)
	if !ok || job.Running == nil || job.Running.Number != number {
		return
	}
	if timer, ok := batchAttemptTimers[jobID]; ok {
		timer.Stop()
		delete(batchAttemptTimers, jobID)
	}
	delete(batchJobHandles, jobID)
	running := *job.Running
	batchCEInUse[running.ComputeEnvironment] -= job.VCPUs

	now := batchEpochMs()
	attempt := BatchAttempt{
		StartedAt: running.StartedAt,
		StoppedAt: now,
		Container: BatchAttemptContainer{LogStreamName: running.LogStream},
	}
	if running.ContainerID != "" {
		attempt.Container.ContainerInstanceArn = batchARN("container/" + running.ContainerID)
	}
	if startErr != nil {
		attempt.StatusReason = batchReasonTaskNotStarted
		attempt.Container.Reason = batchTruncate(startErr.Error(), 255)
	} else {
		if !result.StoppedAt.IsZero() {
			attempt.StoppedAt = result.StoppedAt.UnixMilli()
		}
		if result.ExitCode >= 0 {
			exitCode := result.ExitCode
			attempt.Container.ExitCode = &exitCode
		}
		if result.Error != nil && !job.IsTerminated && !running.TimedOut {
			attempt.Container.Reason = batchTruncate(result.Error.Error(), 255)
		}
		attempt.StatusReason = batchReasonContainerExited
		if running.TimedOut {
			attempt.StatusReason = batchReasonTimedOut
		}
	}
	job.Attempts = append(job.Attempts, attempt)
	job.Running = nil
	if job.Container == nil {
		job.Container = map[string]any{}
	}
	delete(job.Container, "exitCode")
	delete(job.Container, "reason")
	if attempt.Container.ExitCode != nil {
		job.Container["exitCode"] = *attempt.Container.ExitCode
	}
	if attempt.Container.Reason != "" {
		job.Container["reason"] = attempt.Container.Reason
	}

	succeeded := startErr == nil && result.Error == nil && result.ExitCode == 0 && !job.IsTerminated && !running.TimedOut
	switch {
	case succeeded:
		job.StatusReason = attempt.StatusReason
		job.StoppedAt = attempt.StoppedAt
		batchSetStatus(&job, batchStatusSucceeded)
	case !job.IsTerminated && !running.TimedOut && batchRetryAfter(job.Retry, len(job.Attempts), attempt):
		job.StatusReason = ""
		batchEnqueueLocked(&job)
	default:
		job.StatusReason = attempt.StatusReason
		if job.IsTerminated {
			job.StatusReason = job.StopReason
		}
		job.StoppedAt = attempt.StoppedAt
		batchSetStatus(&job, batchStatusFailed)
	}
	batchJobs.Put(jobID, job)
	batchDispatchLocked()
}

// batchCancelJobLocked applies CancelJob: a job not yet STARTING fails, a
// STARTING or RUNNING one is left alone, and an array parent cancels its
// children and fails once they have all finished.
func batchCancelJobLocked(jobID, reason string) {
	job, ok := batchJobs.Get(jobID)
	if !ok || batchTerminal(job.Status) {
		return
	}
	if job.isArrayParent() {
		job.IsCancelled = true
		job.StopReason = reason
		if job.Status == batchStatusSubmitted {
			batchStopBeforeStart(&job, reason)
			batchJobs.Put(jobID, job)
			return
		}
		batchJobs.Put(jobID, job)
		for index := range job.ArrayProperties.Size {
			batchCancelJobLocked(batchChildJobID(jobID, index), reason)
		}
		return
	}
	switch job.Status {
	case batchStatusSubmitted, batchStatusPending, batchStatusRunnable:
		job.IsCancelled = true
		batchStopBeforeStart(&job, reason)
		batchJobs.Put(jobID, job)
	}
}

// batchTerminateJobLocked applies TerminateJob: a job not yet STARTING fails
// at once, a STARTING or RUNNING one has its container stopped and fails when
// it exits, and an array parent terminates every child.
func batchTerminateJobLocked(jobID, reason string) {
	job, ok := batchJobs.Get(jobID)
	if !ok || batchTerminal(job.Status) {
		return
	}
	job.IsTerminated = true
	job.StopReason = reason
	if job.isArrayParent() {
		if job.Status == batchStatusSubmitted {
			batchStopBeforeStart(&job, reason)
			batchJobs.Put(jobID, job)
			return
		}
		batchJobs.Put(jobID, job)
		for index := range job.ArrayProperties.Size {
			batchTerminateJobLocked(batchChildJobID(jobID, index), reason)
		}
		return
	}
	switch job.Status {
	case batchStatusSubmitted, batchStatusPending, batchStatusRunnable:
		batchStopBeforeStart(&job, reason)
	case batchStatusStarting, batchStatusRunning:
		if handle, ok := batchJobHandles[jobID]; ok {
			handle.Cancel()
		}
	}
	batchJobs.Put(jobID, job)
}

func batchStopBeforeStart(job *BatchJob, reason string) {
	batchDequeueLocked(job.JobID)
	job.StatusReason = reason
	job.StoppedAt = batchEpochMs()
	batchSetStatus(job, batchStatusFailed)
}

// recoverBatchJobs rebuilds the scheduler from the job store after a restart:
// RUNNABLE jobs queue again, a SUBMITTED job is scheduled, and an attempt in
// flight adopts its container, or returns to RUNNABLE when its container never
// started.
func recoverBatchJobs() error {
	batchMu.Lock()
	defer batchMu.Unlock()
	batchRunnable = nil
	batchCEInUse = map[string]float64{}

	jobs := batchJobs.List()
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt != jobs[j].CreatedAt {
			return jobs[i].CreatedAt < jobs[j].CreatedAt
		}
		if jobs[i].ArrayJobID == jobs[j].ArrayJobID && jobs[i].ArrayProperties != nil && jobs[j].ArrayProperties != nil &&
			jobs[i].ArrayProperties.Index != nil && jobs[j].ArrayProperties.Index != nil {
			return *jobs[i].ArrayProperties.Index < *jobs[j].ArrayProperties.Index
		}
		return jobs[i].JobID < jobs[j].JobID
	})
	var submitted []string
	for _, job := range jobs {
		switch job.Status {
		case batchStatusSubmitted:
			submitted = append(submitted, job.JobID)
		case batchStatusRunnable:
			batchRunnable = append(batchRunnable, batchRunnableEntry{jobID: job.JobID, queue: batchNameFromARN(job.JobQueue), vcpus: job.VCPUs})
		case batchStatusStarting, batchStatusRunning:
			if err := batchRecoverAttemptLocked(job); err != nil {
				return err
			}
		}
	}
	for _, jobID := range submitted {
		batchScheduleJobLocked(jobID)
	}
	batchDispatchLocked()
	return nil
}

func batchRecoverAttemptLocked(job BatchJob) error {
	if job.Running == nil || job.ExecutionConfig == nil {
		return fmt.Errorf("job %s is %s with no attempt in flight", job.JobID, job.Status)
	}
	number := job.Running.Number
	existing, err := sim.FindExistingContainers(batchAttemptLabels(job.JobID, number))
	if err != nil {
		return fmt.Errorf("find job %s attempt %d container: %w", job.JobID, number, err)
	}
	if len(existing) > 1 {
		return fmt.Errorf("job %s attempt %d has %d workload containers", job.JobID, number, len(existing))
	}
	if len(existing) == 0 {
		job.Running = nil
		batchEnqueueLocked(&job)
		batchJobs.Put(job.JobID, job)
		return nil
	}
	batchCEInUse[job.Running.ComputeEnvironment] += job.VCPUs
	cfg := *job.ExecutionConfig
	handle, err := sim.AdoptContainer(existing[0].ID, cfg, batchAttemptSink(*job.Running))
	if err != nil {
		return fmt.Errorf("adopt job %s container: %w", job.JobID, err)
	}
	job.Running.ContainerID = handle.ContainerID
	if job.Running.StartedAt == 0 {
		job.Running.StartedAt = batchEpochMs()
	}
	batchJobHandles[job.JobID] = handle
	if job.IsTerminated {
		handle.Cancel()
	} else {
		if job.Status == batchStatusStarting {
			batchMarkRunningLocked(&job)
		}
		elapsed := time.Since(time.UnixMilli(job.Running.StartedAt))
		batchArmAttemptTimeoutLocked(job, batchTimeout(job.Timeout)-elapsed)
	}
	batchJobs.Put(job.JobID, job)
	batchWatchAttempt(job.JobID, number, handle)
	return nil
}
