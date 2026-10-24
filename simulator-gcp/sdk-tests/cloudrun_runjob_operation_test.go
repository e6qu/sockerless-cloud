package gcp_sdk_test

import (
	"context"
	"testing"
	"time"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// A RunJob operation that has not completed carries the execution as it
// stands, not as it started: once the execution is cancelled, a poll of the
// operation reports the cancelled task in its metadata while the container,
// which ignores the SIGTERM, is still inside its stop grace; the operation
// completes with CANCELLED once the container is gone.
func TestSDK_CloudRun_RunJobOperationFollowsTheExecution(t *testing.T) {
	t.Parallel()
	jobsClient := newJobsClient(t)
	jobID := uniqueName("sdk-run-job-follow")
	jobName := "projects/test-project/locations/us-central1/jobs/" + jobID

	tailCtx, cancel := context.WithTimeout(ctx, jobLogWaitTimeout)
	defer cancel()
	stream, early := openReadyTail(t, tailCtx, newLoggingV2Client(t), "test-project",
		`resource.type="cloud_run_job" AND resource.labels.job_name="`+jobID+`" AND textPayload="started"`)

	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{Template: &runpb.ExecutionTemplate{Template: &runpb.TaskTemplate{
			Containers: []*runpb.Container{{Image: simWorkloadImage, Command: []string{"sh", "-c"}, Args: []string{"echo started; trap '' TERM; sleep 300"}}},
			Timeout:    durationpb.New(10 * time.Minute),
		}}},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)
	cleanupJob(t, jobsClient, jobName)

	runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{Name: jobName})
	require.NoError(t, err)
	started, err := runOp.Metadata()
	require.NoError(t, err)
	assert.Equal(t, int32(1), started.RunningCount)
	assert.Equal(t, int32(0), started.CancelledCount)

	for len(early) == 0 {
		resp, err := stream.Recv()
		require.NoError(t, err, "the execution's container never started")
		early = resp.GetEntries()
	}

	executions, err := run.NewExecutionsRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { executions.Close() })
	cancelOp, err := executions.CancelExecution(ctx, &runpb.CancelExecutionRequest{Name: started.Name})
	require.NoError(t, err)

	_, err = runOp.Poll(ctx)
	require.NoError(t, err)
	require.False(t, runOp.Done(), "the operation completes only once the container has stopped")
	current, err := runOp.Metadata()
	require.NoError(t, err)
	assert.Equal(t, started.Name, current.Name)
	assert.Equal(t, int32(1), current.CancelledCount, "the metadata follows the execution record")
	assert.Equal(t, int32(0), current.RunningCount)
	assert.NotNil(t, current.CompletionTime)

	_, err = runOp.Wait(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
	assert.True(t, runOp.Done())
	_, err = cancelOp.Wait(ctx)
	require.NoError(t, err)
}
