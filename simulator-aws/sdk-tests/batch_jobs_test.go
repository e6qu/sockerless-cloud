package aws_sdk_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	batchtypes "github.com/aws/aws-sdk-go-v2/service/batch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const batchJobImage = "public.ecr.aws/docker/library/alpine:3"

// batchHoldCommand runs until Batch stops its container, and exits on the
// SIGTERM that stop sends: a shell running as PID 1 ignores SIGTERM unless it
// traps it.
var batchHoldCommand = []string{"sh", "-c", `trap "exit 143" TERM; sleep 300 & wait`}

// createBatchQueue creates a compute environment and a job queue that places
// jobs on it, and returns the queue's name.
func createBatchQueue(t *testing.T, c *batch.Client, name string, state batchtypes.CEState, maxVCPUs int32) string {
	t.Helper()
	return createBatchQueueWithPolicy(t, c, name, state, maxVCPUs, "")
}

// createBatchQueueWithPolicy is createBatchQueue for a queue that
// schedulingPolicyArn, when set, schedules by fair share.
func createBatchQueueWithPolicy(t *testing.T, c *batch.Client, name string, state batchtypes.CEState, maxVCPUs int32, schedulingPolicyArn string) string {
	t.Helper()
	input := &batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: aws.String(name + "-ce"),
		Type:                   batchtypes.CETypeManaged,
		State:                  state,
	}
	if maxVCPUs > 0 {
		input.ComputeResources = &batchtypes.ComputeResource{
			Type:     batchtypes.CRTypeFargate,
			MaxvCpus: aws.Int32(maxVCPUs),
			Subnets:  []string{"subnet-00000001"},
		}
	}
	ce, err := c.CreateComputeEnvironment(ctx, input)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteComputeEnvironment(ctx, &batch.DeleteComputeEnvironmentInput{ComputeEnvironment: ce.ComputeEnvironmentArn})
	})
	_, err = c.CreateJobQueue(ctx, &batch.CreateJobQueueInput{
		JobQueueName: aws.String(name),
		State:        batchtypes.JQStateEnabled,
		Priority:     aws.Int32(1),
		SchedulingPolicyArn: func() *string {
			if schedulingPolicyArn == "" {
				return nil
			}
			return aws.String(schedulingPolicyArn)
		}(),
		ComputeEnvironmentOrder: []batchtypes.ComputeEnvironmentOrder{
			{Order: aws.Int32(1), ComputeEnvironment: ce.ComputeEnvironmentArn},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteJobQueue(ctx, &batch.DeleteJobQueueInput{JobQueue: aws.String(name)}) })
	return name
}

func registerBatchJobDefinition(t *testing.T, c *batch.Client, input *batch.RegisterJobDefinitionInput) string {
	t.Helper()
	input.Type = batchtypes.JobDefinitionTypeContainer
	if input.ContainerProperties.Image == nil {
		input.ContainerProperties.Image = aws.String(batchJobImage)
	}
	input.ContainerProperties.ResourceRequirements = []batchtypes.ResourceRequirement{
		{Type: batchtypes.ResourceTypeVcpu, Value: aws.String("1")},
		{Type: batchtypes.ResourceTypeMemory, Value: aws.String("512")},
	}
	reg, err := c.RegisterJobDefinition(ctx, input)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeregisterJobDefinition(ctx, &batch.DeregisterJobDefinitionInput{JobDefinition: reg.JobDefinitionArn})
	})
	return aws.ToString(reg.JobDefinitionArn)
}

// awaitBatchJob polls DescribeJobs, the only view AWS Batch gives of a job's
// progress, until done accepts the job.
func awaitBatchJob(t *testing.T, c *batch.Client, jobID string, done func(batchtypes.JobDetail) bool) batchtypes.JobDetail {
	t.Helper()
	var job batchtypes.JobDetail
	var seen string
	require.Eventually(t, func() bool {
		out, err := c.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: []string{jobID}})
		require.NoError(t, err)
		require.Len(t, out.Jobs, 1)
		job = out.Jobs[0]
		seen = fmt.Sprintf("status=%s reason=%q attempts=%d", job.Status, aws.ToString(job.StatusReason), len(job.Attempts))
		return done(job)
	}, 90*time.Second, 100*time.Millisecond, "job %s never reached the awaited state; last seen %s", jobID, &seen)
	return job
}

func batchJobSettled(job batchtypes.JobDetail) bool {
	return job.Status == batchtypes.JobStatusSucceeded || job.Status == batchtypes.JobStatusFailed
}

func batchAttemptLog(t *testing.T, stream string) string {
	t.Helper()
	out, err := cwLogsClient().GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String("/aws/batch/job"),
		LogStreamName: aws.String(stream),
		StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	var lines []string
	for _, event := range out.Events {
		lines = append(lines, aws.ToString(event.Message))
	}
	return strings.Join(lines, "\n")
}

// A failed attempt with attempts left returns the job to RUNNABLE and runs the
// next attempt in a new container, which sees its attempt number in
// AWS_BATCH_JOB_ATTEMPT; the request's retryStrategy overrides the job
// definition's.
func TestBatch_RetryStrategyRunsEachAttemptInANewContainer_SDK(t *testing.T) {
	c := batchClient()
	queue := createBatchQueue(t, c, "batch-sdk-retry", batchtypes.CEStateEnabled, 0)
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName: aws.String("batch-sdk-retry"),
		RetryStrategy:     &batchtypes.RetryStrategy{Attempts: aws.Int32(1)},
		ContainerProperties: &batchtypes.ContainerProperties{Command: []string{"sh", "-c",
			`echo "attempt=$AWS_BATCH_JOB_ATTEMPT job=$AWS_BATCH_JOB_ID queue=$AWS_BATCH_JQ_NAME ce=$AWS_BATCH_CE_NAME"; [ "$AWS_BATCH_JOB_ATTEMPT" -ge 2 ]`}},
	})

	submit, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName:       aws.String("batch-sdk-retry"),
		JobQueue:      aws.String(queue),
		JobDefinition: aws.String(definition),
		RetryStrategy: &batchtypes.RetryStrategy{Attempts: aws.Int32(3)},
	})
	require.NoError(t, err)
	jobID := aws.ToString(submit.JobId)

	job := awaitBatchJob(t, c, jobID, batchJobSettled)
	require.Equal(t, batchtypes.JobStatusSucceeded, job.Status, "status reason %q", aws.ToString(job.StatusReason))
	require.Len(t, job.Attempts, 2)
	assert.EqualValues(t, 3, aws.ToInt32(job.RetryStrategy.Attempts))
	first, second := job.Attempts[0], job.Attempts[1]
	assert.EqualValues(t, 1, aws.ToInt32(first.Container.ExitCode))
	assert.EqualValues(t, 0, aws.ToInt32(second.Container.ExitCode))
	for _, attempt := range job.Attempts {
		assert.Equal(t, "Essential container in task exited", aws.ToString(attempt.StatusReason))
		assert.Positive(t, aws.ToInt64(attempt.StartedAt))
		assert.GreaterOrEqual(t, aws.ToInt64(attempt.StoppedAt), aws.ToInt64(attempt.StartedAt))
		assert.True(t, strings.HasPrefix(aws.ToString(attempt.Container.LogStreamName), "batch-sdk-retry/default/"))
	}
	assert.NotEqual(t, aws.ToString(first.Container.LogStreamName), aws.ToString(second.Container.LogStreamName),
		"each attempt runs in its own container with its own log stream")
	assert.NotEqual(t, aws.ToString(first.Container.ContainerInstanceArn), aws.ToString(second.Container.ContainerInstanceArn))
	assert.Equal(t, aws.ToString(second.Container.LogStreamName), aws.ToString(job.Container.LogStreamName))
	assert.EqualValues(t, 0, aws.ToInt32(job.Container.ExitCode))

	want := fmt.Sprintf("job=%s queue=%s ce=%s", jobID, queue, queue+"-ce")
	assert.Equal(t, "attempt=1 "+want, batchAttemptLog(t, aws.ToString(first.Container.LogStreamName)))
	assert.Equal(t, "attempt=2 "+want, batchAttemptLog(t, aws.ToString(second.Container.LogStreamName)))
}

// evaluateOnExit conditions apply in order and the first match decides; an
// EXIT match fails the job although attempts remain.
func TestBatch_EvaluateOnExitFailsTheJobOnAMatchingExit_SDK(t *testing.T) {
	c := batchClient()
	queue := createBatchQueue(t, c, "batch-sdk-exit", batchtypes.CEStateEnabled, 0)
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName: aws.String("batch-sdk-exit"),
		RetryStrategy: &batchtypes.RetryStrategy{
			Attempts: aws.Int32(3),
			EvaluateOnExit: []batchtypes.EvaluateOnExit{
				{OnExitCode: aws.String("3*"), Action: batchtypes.RetryActionExit},
				{OnStatusReason: aws.String("*"), Action: batchtypes.RetryActionRetry},
			},
		},
		ContainerProperties: &batchtypes.ContainerProperties{Command: []string{"sh", "-c", "exit 3"}},
	})

	submit, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String("batch-sdk-exit"), JobQueue: aws.String(queue), JobDefinition: aws.String(definition),
	})
	require.NoError(t, err)
	job := awaitBatchJob(t, c, aws.ToString(submit.JobId), batchJobSettled)
	require.Equal(t, batchtypes.JobStatusFailed, job.Status)
	require.Len(t, job.Attempts, 1, "the EXIT condition stops the retries")
	assert.EqualValues(t, 3, aws.ToInt32(job.Attempts[0].Container.ExitCode))
	assert.Equal(t, "Essential container in task exited", aws.ToString(job.StatusReason))
	require.Len(t, job.RetryStrategy.EvaluateOnExit, 2)
	assert.Positive(t, aws.ToInt64(job.StoppedAt))
}

// An array job's parent spawns one child per index, each a job of its own
// with AWS_BATCH_JOB_ARRAY_INDEX set; the parent waits in PENDING and fails
// once a child has failed and the rest have finished.
func TestBatch_ArrayJobRunsAChildPerIndex_SDK(t *testing.T) {
	c := batchClient()
	queue := createBatchQueue(t, c, "batch-sdk-array", batchtypes.CEStateEnabled, 0)
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName: aws.String("batch-sdk-array"),
		ContainerProperties: &batchtypes.ContainerProperties{Command: []string{"sh", "-c",
			`echo "index=$AWS_BATCH_JOB_ARRAY_INDEX job=$AWS_BATCH_JOB_ID"; [ "$AWS_BATCH_JOB_ARRAY_INDEX" != 1 ]`}},
	})

	submit, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName:         aws.String("batch-sdk-array"),
		JobQueue:        aws.String(queue),
		JobDefinition:   aws.String(definition),
		ArrayProperties: &batchtypes.ArrayProperties{Size: aws.Int32(3)},
	})
	require.NoError(t, err)
	parentID := aws.ToString(submit.JobId)

	parent := awaitBatchJob(t, c, parentID, batchJobSettled)
	require.Equal(t, batchtypes.JobStatusFailed, parent.Status)
	require.NotNil(t, parent.ArrayProperties)
	assert.EqualValues(t, 3, aws.ToInt32(parent.ArrayProperties.Size))
	assert.Nil(t, parent.ArrayProperties.Index)
	assert.EqualValues(t, 2, parent.ArrayProperties.StatusSummary["SUCCEEDED"])
	assert.EqualValues(t, 1, parent.ArrayProperties.StatusSummary["FAILED"])
	assert.EqualValues(t, 0, parent.ArrayProperties.StatusSummary["RUNNING"])
	assert.Empty(t, parent.Attempts, "the parent runs no container of its own")

	childIDs := []string{parentID + ":0", parentID + ":1", parentID + ":2"}
	described, err := c.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: childIDs})
	require.NoError(t, err)
	require.Len(t, described.Jobs, 3)
	for index, child := range described.Jobs {
		assert.Equal(t, childIDs[index], aws.ToString(child.JobId))
		require.NotNil(t, child.ArrayProperties)
		assert.EqualValues(t, index, aws.ToInt32(child.ArrayProperties.Index))
		require.Len(t, child.Attempts, 1)
		want := batchtypes.JobStatusSucceeded
		if index == 1 {
			want = batchtypes.JobStatusFailed
		}
		assert.Equal(t, want, child.Status, "child %d", index)
		assert.Equal(t, fmt.Sprintf("index=%d job=%s", index, childIDs[index]),
			batchAttemptLog(t, aws.ToString(child.Attempts[0].Container.LogStreamName)))
	}

	succeeded, err := c.ListJobs(ctx, &batch.ListJobsInput{ArrayJobId: aws.String(parentID), JobStatus: batchtypes.JobStatusSucceeded})
	require.NoError(t, err)
	require.Len(t, succeeded.JobSummaryList, 2)
	assert.Equal(t, childIDs[0], aws.ToString(succeeded.JobSummaryList[0].JobId))
	assert.EqualValues(t, 0, aws.ToInt32(succeeded.JobSummaryList[0].ArrayProperties.Index))
	assert.Equal(t, childIDs[2], aws.ToString(succeeded.JobSummaryList[1].JobId))

	failedInQueue, err := c.ListJobs(ctx, &batch.ListJobsInput{JobQueue: aws.String(queue), JobStatus: batchtypes.JobStatusFailed})
	require.NoError(t, err)
	listedParent := false
	for _, summary := range failedInQueue.JobSummaryList {
		assert.False(t, strings.HasPrefix(aws.ToString(summary.JobId), parentID+":"), "a queue lists the array parent, not its children")
		if aws.ToString(summary.JobId) == parentID {
			listedParent = true
			assert.EqualValues(t, 3, aws.ToInt32(summary.ArrayProperties.Size))
		}
	}
	assert.True(t, listedParent, "the queue lists the failed array parent")
}

// A job waits in RUNNABLE while its queue's compute environments cannot place
// it — here the only one is DISABLED, later full — and runs once one can.
func TestBatch_JobWaitsRunnableUntilAComputeEnvironmentPlacesIt_SDK(t *testing.T) {
	c := batchClient()
	queue := createBatchQueue(t, c, "batch-sdk-capacity", batchtypes.CEStateDisabled, 1)
	holding := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName:   aws.String("batch-sdk-capacity-hold"),
		ContainerProperties: &batchtypes.ContainerProperties{Command: batchHoldCommand},
	})
	quick := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName:   aws.String("batch-sdk-capacity-quick"),
		ContainerProperties: &batchtypes.ContainerProperties{Command: []string{"true"}},
	})

	first, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String("batch-sdk-capacity-hold"), JobQueue: aws.String(queue), JobDefinition: aws.String(holding),
		RetryStrategy: &batchtypes.RetryStrategy{Attempts: aws.Int32(3)},
	})
	require.NoError(t, err)
	firstID := aws.ToString(first.JobId)
	awaitBatchJob(t, c, firstID, func(job batchtypes.JobDetail) bool { return job.Status == batchtypes.JobStatusRunnable })

	snapshot, err := c.GetJobQueueSnapshot(ctx, &batch.GetJobQueueSnapshotInput{JobQueue: aws.String(queue)})
	require.NoError(t, err)
	require.Len(t, snapshot.FrontOfQueue.Jobs, 1)
	assert.Equal(t, aws.ToString(first.JobArn), aws.ToString(snapshot.FrontOfQueue.Jobs[0].JobArn))

	_, err = c.UpdateComputeEnvironment(ctx, &batch.UpdateComputeEnvironmentInput{
		ComputeEnvironment: aws.String(queue + "-ce"),
		State:              batchtypes.CEStateEnabled,
	})
	require.NoError(t, err)
	running := awaitBatchJob(t, c, firstID, func(job batchtypes.JobDetail) bool { return job.Status == batchtypes.JobStatusRunning })
	assert.Positive(t, aws.ToInt64(running.StartedAt))
	assert.NotEmpty(t, aws.ToString(running.Container.LogStreamName))

	second, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String("batch-sdk-capacity-quick"), JobQueue: aws.String(queue), JobDefinition: aws.String(quick),
	})
	require.NoError(t, err)
	secondID := aws.ToString(second.JobId)
	awaitBatchJob(t, c, secondID, func(job batchtypes.JobDetail) bool { return job.Status == batchtypes.JobStatusRunnable })
	listed, err := c.ListJobs(ctx, &batch.ListJobsInput{JobQueue: aws.String(queue)})
	require.NoError(t, err)
	require.Len(t, listed.JobSummaryList, 1, "ListJobs with no status lists RUNNING jobs")
	assert.Equal(t, firstID, aws.ToString(listed.JobSummaryList[0].JobId))

	_, err = c.TerminateJob(ctx, &batch.TerminateJobInput{JobId: aws.String(firstID), Reason: aws.String("make room")})
	require.NoError(t, err)
	terminated := awaitBatchJob(t, c, firstID, batchJobSettled)
	assert.Equal(t, batchtypes.JobStatusFailed, terminated.Status)
	assert.True(t, aws.ToBool(terminated.IsTerminated))
	assert.Equal(t, "make room", aws.ToString(terminated.StatusReason))
	assert.Len(t, terminated.Attempts, 1, "a terminated job is not retried")

	done := awaitBatchJob(t, c, secondID, batchJobSettled)
	assert.Equal(t, batchtypes.JobStatusSucceeded, done.Status)
}

// Terminating an array parent terminates every child, and the parent fails
// once the stopped children have exited.
func TestBatch_TerminatingAnArrayParentTerminatesItsChildren_SDK(t *testing.T) {
	c := batchClient()
	queue := createBatchQueue(t, c, "batch-sdk-array-stop", batchtypes.CEStateEnabled, 0)
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName:   aws.String("batch-sdk-array-stop"),
		ContainerProperties: &batchtypes.ContainerProperties{Command: batchHoldCommand},
	})
	submit, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String("batch-sdk-array-stop"), JobQueue: aws.String(queue), JobDefinition: aws.String(definition),
		ArrayProperties: &batchtypes.ArrayProperties{Size: aws.Int32(2)},
		RetryStrategy:   &batchtypes.RetryStrategy{Attempts: aws.Int32(2)},
	})
	require.NoError(t, err)
	parentID := aws.ToString(submit.JobId)
	awaitBatchJob(t, c, parentID, func(job batchtypes.JobDetail) bool {
		return job.ArrayProperties != nil && job.ArrayProperties.StatusSummary["RUNNING"] == 2
	})
	pending, err := c.ListJobs(ctx, &batch.ListJobsInput{JobQueue: aws.String(queue), JobStatus: batchtypes.JobStatusPending})
	require.NoError(t, err)
	require.Len(t, pending.JobSummaryList, 1, "the parent stays PENDING while its children run")

	_, err = c.TerminateJob(ctx, &batch.TerminateJobInput{JobId: aws.String(parentID), Reason: aws.String("stop the array")})
	require.NoError(t, err)
	parent := awaitBatchJob(t, c, parentID, batchJobSettled)
	assert.Equal(t, batchtypes.JobStatusFailed, parent.Status)
	assert.Equal(t, "stop the array", aws.ToString(parent.StatusReason))
	assert.EqualValues(t, 2, parent.ArrayProperties.StatusSummary["FAILED"])

	children, err := c.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: []string{parentID + ":0", parentID + ":1"}})
	require.NoError(t, err)
	require.Len(t, children.Jobs, 2)
	for _, child := range children.Jobs {
		assert.Equal(t, batchtypes.JobStatusFailed, child.Status)
		assert.True(t, aws.ToBool(child.IsTerminated))
		assert.Equal(t, "stop the array", aws.ToString(child.StatusReason))
		assert.Len(t, child.Attempts, 1)
	}
}

func TestBatch_SubmitJobRejectsOutOfRangeArrayAndRetryProperties_SDK(t *testing.T) {
	c := batchClient()
	queue := createBatchQueue(t, c, "batch-sdk-invalid", batchtypes.CEStateDisabled, 0)
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName:   aws.String("batch-sdk-invalid"),
		ContainerProperties: &batchtypes.ContainerProperties{Command: []string{"true"}},
	})
	for name, input := range map[string]*batch.SubmitJobInput{
		"array of one":               {ArrayProperties: &batchtypes.ArrayProperties{Size: aws.Int32(1)}},
		"array too large":            {ArrayProperties: &batchtypes.ArrayProperties{Size: aws.Int32(10001)}},
		"eleven attempts":            {RetryStrategy: &batchtypes.RetryStrategy{Attempts: aws.Int32(11)}},
		"reserved env var":           {ContainerOverrides: &batchtypes.ContainerOverrides{Environment: []batchtypes.KeyValuePair{{Name: aws.String("AWS_BATCH_JOB_ID"), Value: aws.String("x")}}}},
		"unknown dependency":         {DependsOn: []batchtypes.JobDependency{{JobId: aws.String("00000000-0000-0000-0000-000000000000")}}},
		"SEQUENTIAL on a single job": {DependsOn: []batchtypes.JobDependency{{Type: batchtypes.ArrayJobDependencySequential}}},
	} {
		input.JobName, input.JobQueue, input.JobDefinition = aws.String("batch-sdk-invalid"), aws.String(queue), aws.String(definition)
		_, err := c.SubmitJob(ctx, input)
		var apiErr smithy.APIError
		require.True(t, errors.As(err, &apiErr), "%s: SubmitJob returned %v", name, err)
		assert.Equal(t, "ClientException", apiErr.ErrorCode(), name)
	}
}

// A job with dependsOn waits in PENDING while a dependency runs, and fails
// without running once that dependency fails.
func TestBatch_DependentJobWaitsPendingAndFailsWithItsDependency_SDK(t *testing.T) {
	c := batchClient()
	queue := createBatchQueue(t, c, uniqueName("batch-sdk-deps"), batchtypes.CEStateEnabled, 0)
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName:   aws.String(uniqueName("batch-sdk-deps")),
		ContainerProperties: &batchtypes.ContainerProperties{Command: batchHoldCommand},
	})

	first, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String("first"), JobQueue: aws.String(queue), JobDefinition: aws.String(definition),
	})
	require.NoError(t, err)
	second, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String("second"), JobQueue: aws.String(queue), JobDefinition: aws.String(definition),
		DependsOn: []batchtypes.JobDependency{{JobId: first.JobId}},
	})
	require.NoError(t, err)

	awaitBatchJob(t, c, aws.ToString(first.JobId), func(job batchtypes.JobDetail) bool {
		return job.Status == batchtypes.JobStatusRunning
	})
	described, err := c.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: []string{aws.ToString(second.JobId)}})
	require.NoError(t, err)
	require.Len(t, described.Jobs, 1)
	assert.Equal(t, batchtypes.JobStatusPending, described.Jobs[0].Status)
	require.Len(t, described.Jobs[0].DependsOn, 1)
	assert.Equal(t, aws.ToString(first.JobId), aws.ToString(described.Jobs[0].DependsOn[0].JobId))

	pending, err := c.ListJobs(ctx, &batch.ListJobsInput{JobQueue: aws.String(queue), JobStatus: batchtypes.JobStatusPending})
	require.NoError(t, err)
	require.Len(t, pending.JobSummaryList, 1)
	assert.Equal(t, aws.ToString(second.JobId), aws.ToString(pending.JobSummaryList[0].JobId))

	_, err = c.TerminateJob(ctx, &batch.TerminateJobInput{JobId: first.JobId, Reason: aws.String("stop the dependency")})
	require.NoError(t, err)
	dependent := awaitBatchJob(t, c, aws.ToString(second.JobId), batchJobSettled)
	assert.Equal(t, batchtypes.JobStatusFailed, dependent.Status)
	assert.Equal(t, "Dependent Job failed", aws.ToString(dependent.StatusReason))
	assert.Empty(t, dependent.Attempts, "a job whose dependency failed never runs")
}

// A SEQUENTIAL array job runs child i only after child i-1 has finished.
func TestBatch_SequentialArrayRunsOneChildAfterAnother_SDK(t *testing.T) {
	c := batchClient()
	queue := createBatchQueue(t, c, uniqueName("batch-sdk-seq"), batchtypes.CEStateEnabled, 0)
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName:   aws.String(uniqueName("batch-sdk-seq")),
		ContainerProperties: &batchtypes.ContainerProperties{Command: []string{"true"}},
	})
	submit, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String("sequential"), JobQueue: aws.String(queue), JobDefinition: aws.String(definition),
		ArrayProperties: &batchtypes.ArrayProperties{Size: aws.Int32(3)},
		DependsOn:       []batchtypes.JobDependency{{Type: batchtypes.ArrayJobDependencySequential}},
	})
	require.NoError(t, err)
	parentID := aws.ToString(submit.JobId)
	parent := awaitBatchJob(t, c, parentID, batchJobSettled)
	require.Equal(t, batchtypes.JobStatusSucceeded, parent.Status)

	childIDs := []string{parentID + ":0", parentID + ":1", parentID + ":2"}
	described, err := c.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: childIDs})
	require.NoError(t, err)
	require.Len(t, described.Jobs, 3)
	assert.Empty(t, described.Jobs[0].DependsOn)
	for index := 1; index < 3; index++ {
		child, previous := described.Jobs[index], described.Jobs[index-1]
		require.Len(t, child.DependsOn, 1)
		assert.Equal(t, childIDs[index-1], aws.ToString(child.DependsOn[0].JobId))
		assert.Equal(t, batchtypes.ArrayJobDependencySequential, child.DependsOn[0].Type)
		assert.GreaterOrEqual(t, aws.ToInt64(child.StartedAt), aws.ToInt64(previous.StoppedAt),
			"child %d started before child %d stopped", index, index-1)
	}
}

// On a fair-share queue with room for one job, the share that has just held
// the compute environment waits while a share that has held none runs, even
// though its job arrived later.
func TestBatch_FairshareQueueRunsTheLeastUsedShareFirst_SDK(t *testing.T) {
	c := batchClient()
	policy, err := c.CreateSchedulingPolicy(ctx, &batch.CreateSchedulingPolicyInput{
		Name:            aws.String(uniqueName("batch-sdk-fairshare")),
		FairsharePolicy: &batchtypes.FairsharePolicy{ShareDecaySeconds: aws.Int32(3600)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteSchedulingPolicy(ctx, &batch.DeleteSchedulingPolicyInput{Arn: policy.Arn}) })
	queue := createBatchQueueWithPolicy(t, c, uniqueName("batch-sdk-fairshare"), batchtypes.CEStateEnabled, 1, aws.ToString(policy.Arn))
	definition := registerBatchJobDefinition(t, c, &batch.RegisterJobDefinitionInput{
		JobDefinitionName:   aws.String(uniqueName("batch-sdk-fairshare")),
		ContainerProperties: &batchtypes.ContainerProperties{Command: batchHoldCommand},
	})
	submit := func(name, share string) string {
		out, err := c.SubmitJob(ctx, &batch.SubmitJobInput{
			JobName: aws.String(name), JobQueue: aws.String(queue), JobDefinition: aws.String(definition),
			ShareIdentifier: aws.String(share),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.TerminateJob(ctx, &batch.TerminateJobInput{JobId: out.JobId, Reason: aws.String("cleanup")})
		})
		return aws.ToString(out.JobId)
	}

	_, err = c.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName: aws.String("no-share"), JobQueue: aws.String(queue), JobDefinition: aws.String(definition),
	})
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "SubmitJob without a share identifier returned %v", err)
	assert.Equal(t, "ClientException", apiErr.ErrorCode())

	holder := submit("holder", "teamA")
	awaitBatchJob(t, c, holder, func(job batchtypes.JobDetail) bool { return job.Status == batchtypes.JobStatusRunning })
	waitingA := submit("teamA-next", "teamA")
	waitingB := submit("teamB-first", "teamB")

	_, err = c.TerminateJob(ctx, &batch.TerminateJobInput{JobId: aws.String(holder), Reason: aws.String("free the compute environment")})
	require.NoError(t, err)
	awaitBatchJob(t, c, waitingB, func(job batchtypes.JobDetail) bool { return job.Status == batchtypes.JobStatusRunning })
	described, err := c.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: []string{waitingA}})
	require.NoError(t, err)
	require.Len(t, described.Jobs, 1)
	assert.Equal(t, batchtypes.JobStatusRunnable, described.Jobs[0].Status, "teamA's job waits while teamB takes its turn")
	assert.Equal(t, "teamA", aws.ToString(described.Jobs[0].ShareIdentifier))
}
