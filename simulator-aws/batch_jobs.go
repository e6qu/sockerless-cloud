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
	batchReasonDependencyFail  = "Dependent Job failed"

	batchMaxDependencies = 20
)

const (
	batchDependencyNToN       = "N_TO_N"
	batchDependencySequential = "SEQUENTIAL"
)

type BatchJobDependency struct {
	JobID string `json:"jobId,omitempty"`
	Type  string `json:"type,omitempty"`
}

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
	jobID    string
	queue    string
	vcpus    float64
	share    string
	priority int
	seq      int
}

func batchRunnableEntryFor(job BatchJob) batchRunnableEntry {
	entry := batchRunnableEntry{jobID: job.JobID, queue: batchNameFromARN(job.JobQueue), vcpus: job.VCPUs, share: job.ShareIdentifier}
	if job.SchedulingPriority != nil {
		entry.priority = *job.SchedulingPriority
	}
	return entry
}

// The scheduler's working set, guarded by batchMu and rebuilt from the job
// store at startup: the RUNNABLE jobs in arrival order, the vCPUs each
// compute environment has placed, the timeout armed on each running attempt,
// the PENDING jobs waiting on each unfinished dependency, and the jobs that
// finished since their dependents were last released. batchJobHandles, under
// the same lock, holds each attempt's container.
var (
	batchRunnable      []batchRunnableEntry
	batchCEInUse       = map[string]float64{}
	batchAttemptTimers = map[string]*bg.Timer{}
	batchDependents    = map[string][]string{}
	batchFinished      []string
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
	if batchTerminal(status) && previous != status {
		batchFinished = append(batchFinished, job.JobID)
	}
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
	batchFinished = append(batchFinished, parent.JobID)
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
	batchRunnable = append(batchRunnable, batchRunnableEntryFor(*job))
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
// waits in PENDING until its dependencies finish and is RUNNABLE otherwise,
// and an array job spawns its children and waits on them in PENDING.
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
		batchAdvanceDependentLocked(&job)
		batchJobs.Put(jobID, job)
		return
	}
	now := batchEpochMs()
	size := job.ArrayProperties.Size
	summary := make(map[string]int, len(batchJobStatuses))
	for _, status := range batchJobStatuses {
		summary[status] = 0
	}
	// A child enters PENDING when its parent spawns it and moves on once the
	// scheduler has evaluated its dependencies; both happen under this one
	// lock hold, so a child with none never shows the PENDING step.
	for index := range size {
		childIndex := index
		childID := batchChildJobID(jobID, index)
		child := BatchJob{
			JobID:              childID,
			JobArn:             batchARN("job/" + childID),
			JobName:            job.JobName,
			JobQueue:           job.JobQueue,
			ShareIdentifier:    job.ShareIdentifier,
			JobDefinition:      job.JobDefinition,
			CreatedAt:          now,
			Container:          maps.Clone(job.Container),
			Attempts:           []BatchAttempt{},
			RetryStrategy:      job.RetryStrategy,
			Timeout:            job.Timeout,
			SchedulingPriority: job.SchedulingPriority,
			DependsOn:          batchChildDependencies(job, index),
			ArrayProperties:    &BatchArrayProperties{Index: &childIndex},
			Tags:               job.Tags,
			ExecutionConfig:    job.ExecutionConfig,
			ArrayJobID:         jobID,
			VCPUs:              job.VCPUs,
			Retry:              job.Retry,
			LogConfiguration:   job.LogConfiguration,
		}
		failed, waitingOn := batchDependencyStateLocked(child)
		switch {
		case failed:
			child.Status = batchStatusFailed
			child.StatusReason = batchReasonDependencyFail
			child.StoppedAt = now
			batchFinished = append(batchFinished, childID)
		case len(waitingOn) > 0:
			child.Status = batchStatusPending
			batchWaitOnLocked(childID, waitingOn)
		default:
			child.Status = batchStatusRunnable
			batchRunnable = append(batchRunnable, batchRunnableEntryFor(child))
		}
		summary[child.Status]++
		batchJobs.Put(childID, child)
	}
	job.Status = batchStatusPending
	job.ArrayProperties.StatusSummary = summary
	job.ArrayProperties.StatusSummaryLastUpdatedAt = now
	batchSettleArrayParent(&job)
	batchJobs.Put(jobID, job)
}

// batchChildDependencies gives array child index the dependencies it waits
// on: every dependency of its parent without a type, the same index of each
// N_TO_N dependency, and, under SEQUENTIAL, the child before it.
func batchChildDependencies(parent BatchJob, index int) []BatchJobDependency {
	var deps []BatchJobDependency
	for _, dep := range parent.DependsOn {
		switch dep.Type {
		case batchDependencyNToN:
			deps = append(deps, BatchJobDependency{JobID: batchChildJobID(dep.JobID, index), Type: batchDependencyNToN})
		case batchDependencySequential:
			if index > 0 {
				deps = append(deps, BatchJobDependency{JobID: batchChildJobID(parent.JobID, index-1), Type: batchDependencySequential})
			}
		default:
			deps = append(deps, dep)
		}
	}
	return deps
}

// batchDependencyStateLocked reports whether one of a job's dependencies
// failed, and otherwise which have yet to finish.
func batchDependencyStateLocked(job BatchJob) (failed bool, waitingOn []string) {
	for _, dep := range job.DependsOn {
		dependency, ok := batchJobs.Get(dep.JobID)
		if !ok {
			continue
		}
		switch dependency.Status {
		case batchStatusSucceeded:
		case batchStatusFailed:
			return true, nil
		default:
			waitingOn = append(waitingOn, dep.JobID)
		}
	}
	return false, waitingOn
}

func batchWaitOnLocked(jobID string, dependencies []string) {
	for _, dependency := range dependencies {
		batchDependents[dependency] = append(batchDependents[dependency], jobID)
	}
}

// batchAdvanceDependentLocked moves a SUBMITTED or PENDING job on once its
// dependencies allow: to FAILED when one failed, to PENDING while one has yet
// to finish, and otherwise to RUNNABLE, or to FAILED when CancelJob or
// TerminateJob stopped it while it waited. The caller stores job.
func batchAdvanceDependentLocked(job *BatchJob) {
	failed, waitingOn := batchDependencyStateLocked(*job)
	stopped := job.IsCancelled || job.IsTerminated
	switch {
	case failed && stopped:
		batchStopBeforeStart(job, job.StopReason)
	case failed:
		batchStopBeforeStart(job, batchReasonDependencyFail)
	case len(waitingOn) > 0:
		if job.Status != batchStatusPending {
			batchSetStatus(job, batchStatusPending)
			batchWaitOnLocked(job.JobID, waitingOn)
		}
	case stopped:
		batchStopBeforeStart(job, job.StopReason)
	default:
		batchEnqueueLocked(job)
	}
}

// batchReleaseDependentsLocked re-evaluates the PENDING jobs that wait on a
// job that has finished, until no release finishes another job.
func batchReleaseDependentsLocked() {
	for len(batchFinished) > 0 {
		finished := batchFinished[0]
		batchFinished = batchFinished[1:]
		dependents := batchDependents[finished]
		delete(batchDependents, finished)
		for _, dependentID := range dependents {
			dependent, ok := batchJobs.Get(dependentID)
			if !ok || dependent.Status != batchStatusPending {
				continue
			}
			batchAdvanceDependentLocked(&dependent)
			batchJobs.Put(dependentID, dependent)
		}
	}
}

// batchDispatchLocked places RUNNABLE jobs on the first compute environment
// of their queue with the vCPUs to spare, and starts their attempts. Queues go
// by priority, highest first; a FIFO queue offers its jobs oldest first and a
// fair-share queue in the order its scheduling policy gives. Jobs no
// environment can take stay RUNNABLE until an attempt frees capacity or an
// environment or queue changes.
func batchDispatchLocked() {
	batchReleaseDependentsLocked()
	byQueue := map[string][]batchRunnableEntry{}
	var queueNames []string
	for seq, entry := range batchRunnable {
		entry.seq = seq
		if _, seen := byQueue[entry.queue]; !seen {
			queueNames = append(queueNames, entry.queue)
		}
		byQueue[entry.queue] = append(byQueue[entry.queue], entry)
	}
	queues := map[string]BatchJobQueue{}
	for _, name := range queueNames {
		queues[name], _ = batchJobQueues.Get(name)
	}
	sort.SliceStable(queueNames, func(i, j int) bool { return queues[queueNames[i]].Priority > queues[queueNames[j]].Priority })

	settled := map[string]bool{}
	for _, name := range queueNames {
		candidates := batchQueuePlacements(name)
		entries := byQueue[name]
		policy, fairshare := batchQueueFairsharePolicy(queues[name])
		if !fairshare {
			for _, entry := range entries {
				settled[entry.jobID] = batchPlaceLocked(entry, candidates, nil)
			}
			continue
		}
		scheduler := newBatchFairshareScheduler(name, policy, entries)
		for entry, ok := scheduler.pop(); ok; entry, ok = scheduler.pop() {
			share := entry.share
			settled[entry.jobID] = batchPlaceLocked(entry, candidates, func(maxVCPUs float64) float64 {
				return scheduler.capacity(share, maxVCPUs)
			})
			if settled[entry.jobID] {
				scheduler.placed(entry)
			}
		}
	}
	kept := batchRunnable[:0]
	for _, entry := range batchRunnable {
		if !settled[entry.jobID] {
			kept = append(kept, entry)
		}
	}
	batchRunnable = kept
}

// batchPlaceLocked starts an attempt of entry's job on the first candidate
// with room for it under limit, which caps a compute environment's maxVCPUs;
// it reports false when the job must stay queued.
func batchPlaceLocked(entry batchRunnableEntry, candidates []batchPlacement, limit func(float64) float64) bool {
	placed := ""
	for _, candidate := range candidates {
		capacity := candidate.maxVCPUs
		if capacity > 0 && limit != nil {
			capacity = limit(capacity)
		}
		if candidate.maxVCPUs == 0 || batchCEInUse[candidate.name]+entry.vcpus <= capacity {
			placed = candidate.name
			break
		}
	}
	if placed == "" {
		return false
	}
	job, ok := batchJobs.Get(entry.jobID)
	if !ok || job.Status != batchStatusRunnable {
		return true
	}
	batchCEInUse[placed] += job.VCPUs
	batchChargeShareLocked(entry.queue, job.ShareIdentifier, job.VCPUs)
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
	return true
}

// batchCheckDependencies validates SubmitJob's dependsOn for a job of
// arraySize children (0 for a single job).
func batchCheckDependencies(deps []BatchJobDependency, arraySize int) error {
	if len(deps) > batchMaxDependencies {
		return fmt.Errorf("dependsOn takes at most %d jobs, got %d", batchMaxDependencies, len(deps))
	}
	for _, dep := range deps {
		switch dep.Type {
		case "", batchDependencyNToN, batchDependencySequential:
		default:
			return fmt.Errorf("dependsOn type must be N_TO_N or SEQUENTIAL, got %q", dep.Type)
		}
		if dep.Type == batchDependencySequential {
			if arraySize == 0 {
				return errors.New("a SEQUENTIAL dependency applies only to an array job")
			}
			if dep.JobID != "" {
				return errors.New("a SEQUENTIAL dependency takes no jobId")
			}
			continue
		}
		if dep.JobID == "" {
			return errors.New("dependsOn jobId is required")
		}
		dependency, ok := batchJobs.Get(dep.JobID)
		if !ok {
			return errors.New("Job not found: " + dep.JobID)
		}
		if dep.Type != batchDependencyNToN {
			continue
		}
		if arraySize == 0 || !dependency.isArrayParent() {
			return errors.New("an N_TO_N dependency applies only between array jobs")
		}
		if dependency.ArrayProperties.Size != arraySize {
			return fmt.Errorf("an N_TO_N dependency needs array jobs of the same size, got %d and %d", arraySize, dependency.ArrayProperties.Size)
		}
	}
	return nil
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
	batchChargeShareLocked(batchNameFromARN(job.JobQueue), job.ShareIdentifier, -job.VCPUs)

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

// batchCancelJobLocked applies CancelJob: a job not yet STARTING fails, once
// its dependencies have finished when it waits on them in PENDING; a STARTING
// or RUNNING one is left alone; and an array parent cancels its children and
// fails once they have all finished.
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
	case batchStatusPending:
		job.IsCancelled = true
		job.StopReason = reason
		batchJobs.Put(jobID, job)
	case batchStatusSubmitted, batchStatusRunnable:
		job.IsCancelled = true
		batchStopBeforeStart(&job, reason)
		batchJobs.Put(jobID, job)
	}
}

// batchTerminateJobLocked applies TerminateJob: a job not yet STARTING is
// cancelled as CancelJob cancels it, a STARTING or RUNNING one has its
// container stopped and fails when it exits, and an array parent terminates
// every child.
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
	case batchStatusSubmitted, batchStatusRunnable:
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
// RUNNABLE jobs queue again, a SUBMITTED job is scheduled, a PENDING job
// waits on its dependencies again, and an attempt in
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
	batchDependents = map[string][]string{}
	batchFinished = nil
	var submitted []string
	for _, job := range jobs {
		switch job.Status {
		case batchStatusSubmitted:
			submitted = append(submitted, job.JobID)
		case batchStatusPending:
			if !job.isArrayParent() {
				batchRecoverDependentLocked(job)
			}
		case batchStatusRunnable:
			batchRunnable = append(batchRunnable, batchRunnableEntryFor(job))
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

// batchRecoverDependentLocked rebuilds a PENDING job's place in the
// dependency index, releasing it when its dependencies finished while the
// simulator was down.
func batchRecoverDependentLocked(job BatchJob) {
	failed, waitingOn := batchDependencyStateLocked(job)
	if !failed && len(waitingOn) > 0 {
		batchWaitOnLocked(job.JobID, waitingOn)
		return
	}
	batchAdvanceDependentLocked(&job)
	batchJobs.Put(job.JobID, job)
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
		batchChargeShareLocked(batchNameFromARN(job.JobQueue), job.ShareIdentifier, -job.VCPUs)
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
