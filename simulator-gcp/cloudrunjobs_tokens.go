package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/codes"
)

const cloudRunJobType = "type.googleapis.com/google.cloud.run.v2.Job"

// executionToken is the token a job names to create an execution with, and
// whether the job becomes ready only once that execution completes
// (runExecutionToken) rather than once it starts (startExecutionToken).
func (j Job) executionToken() (token string, toCompletion bool) {
	if j.RunExecutionToken != "" {
		return j.RunExecutionToken, true
	}
	return j.StartExecutionToken, false
}

// cloudRunJobTokenExecutionID is the id of the execution a token creates.
func cloudRunJobTokenExecutionID(jobID, token string) string {
	return jobID + "-" + token
}

// cloudRunJobExecutionTokenValid answers INVALID_ARGUMENT for a job naming
// both tokens, which the API declares as one of a oneof, and for a token whose
// length with the job's name's is 63 characters or more.
func cloudRunJobExecutionTokenValid(w http.ResponseWriter, jobID string, job Job) bool {
	if job.StartExecutionToken != "" && job.RunExecutionToken != "" {
		GCPError(w, http.StatusBadRequest, "A job may set only one of startExecutionToken and runExecutionToken.", "INVALID_ARGUMENT")
		return false
	}
	if token, _ := job.executionToken(); token != "" && len(jobID)+len(token) >= 63 {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"The sum of job name and token length must be fewer than 63 characters: %q and %q have %d.",
			jobID, token, len(jobID)+len(token))
		return false
	}
	return true
}

// holdCloudRunJobForExecutionToken keeps a job that names an execution token
// reconciling until the execution the token creates has started or completed.
func holdCloudRunJobForExecutionToken(job *Job, now string) {
	if token, _ := job.executionToken(); token == "" {
		return
	}
	job.Reconciling = true
	job.TerminalCondition = &Condition{Type: "Ready", State: "CONDITION_RECONCILING", LastTransitionTime: now}
	job.Conditions = []Condition{
		{Type: "ConfigurationsReady", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
		{Type: "Ready", State: "CONDITION_RECONCILING", LastTransitionTime: now},
	}
}

// cloudRunJobWriteOperation is the operation CreateJob and UpdateJob answer
// with. A job that names an execution token gets an operation that runs until
// the job is ready, and the execution starts; any other completes at once.
func cloudRunJobWriteOperation(project, location, jobID string, job Job) Operation {
	if token, _ := job.executionToken(); token == "" {
		return cloudRunLRO(project, location, job, cloudRunJobType)
	}
	op := Operation{
		Name:     gcpLocationOperationName(project, location, sim.NewUUID()),
		Metadata: gcpResourceOperationMetadata(gcpOperationAny(job, cloudRunJobType)),
	}
	crOperations.Put(op.Name, op)
	startCloudRunJobTokenExecution(project, location, jobID, job)
	if current, ok := crOperations.Get(op.Name); ok {
		return current
	}
	return op
}

// startCloudRunJobTokenExecution starts the execution a job's token names,
// unless it exists already, and returns the job as it stands after. A job
// whose token names an execution that has already started or completed
// becomes ready on the spot.
func startCloudRunJobTokenExecution(project, location, jobID string, job Job) Job {
	token, _ := job.executionToken()
	if token == "" {
		return job
	}
	execID := cloudRunJobTokenExecutionID(jobID, token)
	execName := job.Name + "/executions/" + execID
	if _, exists := crjExecutions.Get(execName); !exists {
		runCloudRunJob(project, location, jobID, job, nil, execID)
	}
	settleCloudRunJobExecutionToken(execName)
	if current, ok := crjJobs.Get(job.Name); ok {
		return current
	}
	return job
}

// cloudRunTokenExecutionOutcome reports whether the execution a token created
// has reached the point the job waits for, and the error the job fails with
// when it did not get there.
func cloudRunTokenExecutionOutcome(execName string, toCompletion bool) (*OperationError, bool) {
	exec, ok := crjExecutions.Get(execName)
	if !ok {
		return &OperationError{Code: int(codes.NotFound),
			Message: fmt.Sprintf("execution %q was deleted before it finished", execName)}, true
	}
	if exec.CompletionTime != "" {
		return cloudRunExecutionOperationError(exec), true
	}
	if _, running := crjProcessHandles.Load(execName); running && !toCompletion {
		return nil, true
	}
	return nil, false
}

// settleCloudRunJobExecutionToken makes the job whose token created the
// execution named execName ready once the execution has started
// (startExecutionToken) or completed (runExecutionToken), or failed when it
// did not get there, and completes the job's create or update operation with
// it. A job that no longer waits on this execution is left alone.
func settleCloudRunJobExecutionToken(execName string) {
	jobName, execID, ok := strings.Cut(execName, "/executions/")
	if !ok {
		return
	}
	jobID := jobName[strings.LastIndex(jobName, "/")+1:]
	waiting := func(j Job) bool {
		token, _ := j.executionToken()
		return j.Reconciling && token != "" && cloudRunJobTokenExecutionID(jobID, token) == execID
	}
	job, found := crjJobs.Get(jobName)
	if !found {
		finishCloudRunJobWriteOperations(jobName, nil, &OperationError{Code: int(codes.NotFound),
			Message: fmt.Sprintf("job %q was deleted before it became ready", jobName)})
		return
	}
	if !waiting(job) {
		return
	}
	_, toCompletion := job.executionToken()
	failure, decided := cloudRunTokenExecutionOutcome(execName, toCompletion)
	if !decided {
		return
	}
	settled := false
	crjJobs.Update(jobName, func(j *Job) {
		if !waiting(*j) {
			return
		}
		settled = true
		now := nowTimestamp()
		ready := Condition{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now}
		if failure != nil {
			ready.State, ready.Message = "CONDITION_FAILED", failure.Message
		}
		j.TerminalCondition = &ready
		j.Conditions = []Condition{
			{Type: "ConfigurationsReady", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
			ready,
		}
		j.Reconciling = false
		j.Etag = sim.NewUUID()
	})
	if !settled {
		return
	}
	if job, found = crjJobs.Get(jobName); found {
		finishCloudRunJobWriteOperations(jobName, &job, failure)
	}
}

// finishCloudRunJobWriteOperations completes the job's running create and
// update operations: with the job as the response, or with failure.
func finishCloudRunJobWriteOperations(jobName string, job *Job, failure *OperationError) {
	if crOperations == nil {
		return
	}
	for _, op := range crOperations.Filter(func(op Operation) bool {
		return !op.Done && op.Metadata["@type"] == cloudRunJobType && op.Metadata["name"] == jobName
	}) {
		gcpFinishOperation(op.Name, func(o *Operation) {
			if job != nil {
				body := gcpOperationAny(*job, cloudRunJobType)
				o.Metadata = gcpResourceOperationMetadata(body)
				o.Response = body
			}
			if failure != nil {
				o.Response = nil
				o.Error = failure
			}
		})
	}
}

// recoverCloudRunJobExecutionTokens settles every job still waiting on its
// token's execution after a restart, once recoverCloudRunJobExecutions has
// settled the executions the restart cut short.
func recoverCloudRunJobExecutionTokens() {
	for _, job := range crjJobs.Filter(func(j Job) bool { return j.Reconciling }) {
		token, _ := job.executionToken()
		if token == "" {
			continue
		}
		jobID := job.Name[strings.LastIndex(job.Name, "/")+1:]
		settleCloudRunJobExecutionToken(job.Name + "/executions/" + cloudRunJobTokenExecutionID(jobID, token))
	}
}
