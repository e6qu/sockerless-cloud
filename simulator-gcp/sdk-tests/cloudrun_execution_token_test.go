package gcp_sdk_test

import (
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func tokenJobTemplate(script string) *runpb.ExecutionTemplate {
	return &runpb.ExecutionTemplate{
		Template: &runpb.TaskTemplate{
			Containers: []*runpb.Container{{Image: "public.ecr.aws/docker/library/alpine:latest", Command: []string{"sh", "-c", script}}},
			Retries:    &runpb.TaskTemplate_MaxRetries{MaxRetries: 0},
			Timeout:    durationpb.New(120 * time.Second),
		},
	}
}

// A job that names runExecutionToken starts the execution <job>-<token>, and
// its create operation completes, with the job ready, only once that execution
// has completed. startExecutionToken holds the update only until the execution
// has started. An execution that fails fails the job and its operation, and a
// job name and token of 63 characters or more are refused.
func TestSDK_CloudRun_JobExecutionTokens(t *testing.T) {
	jobs := newJobsClient(t)
	executions := newExecutionsClient(t)
	parent := "projects/test-project/locations/us-central1"
	id := uniqueName("sdk-token-job")

	createOp, err := jobs.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: parent,
		JobId:  id,
		Job: &runpb.Job{
			Template:        tokenJobTemplate("echo ran"),
			CreateExecution: &runpb.Job_RunExecutionToken{RunExecutionToken: "first"},
		},
	})
	require.NoError(t, err)
	job, err := createOp.Wait(ctx)
	require.NoError(t, err)
	cleanupJob(t, jobs, job.Name)
	assert.Equal(t, "first", job.GetRunExecutionToken())
	assert.False(t, job.Reconciling)
	assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, job.GetTerminalCondition().GetState())

	first, err := executions.GetExecution(ctx, &runpb.GetExecutionRequest{Name: job.Name + "/executions/" + id + "-first"})
	require.NoError(t, err, "runExecutionToken names the execution <job>-<token>")
	assert.Equal(t, int32(1), first.SucceededCount, "the job was ready only once its execution completed")
	assert.NotNil(t, first.CompletionTime)

	job.CreateExecution = &runpb.Job_StartExecutionToken{StartExecutionToken: "second"}
	job.Template = tokenJobTemplate("trap 'exit 0' TERM; sleep 2147483647 & wait $!")
	updateOp, err := jobs.UpdateJob(ctx, &runpb.UpdateJobRequest{Job: job})
	require.NoError(t, err)
	job, err = updateOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, "second", job.GetStartExecutionToken())
	assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, job.GetTerminalCondition().GetState())
	second, err := executions.GetExecution(ctx, &runpb.GetExecutionRequest{Name: job.Name + "/executions/" + id + "-second"})
	require.NoError(t, err)
	assert.Nil(t, second.CompletionTime, "startExecutionToken readies the job while the execution runs")
	cancelOp, err := executions.CancelExecution(ctx, &runpb.CancelExecutionRequest{Name: second.Name})
	require.NoError(t, err)
	_, err = cancelOp.Wait(ctx)
	require.NoError(t, err)

	job, err = jobs.GetJob(ctx, &runpb.GetJobRequest{Name: job.Name})
	require.NoError(t, err, "the cancel moved the job's latest execution on, and its etag with it")
	job.CreateExecution = &runpb.Job_RunExecutionToken{RunExecutionToken: "fails"}
	job.Template = tokenJobTemplate("exit 3")
	updateOp, err = jobs.UpdateJob(ctx, &runpb.UpdateJobRequest{Job: job})
	require.NoError(t, err)
	_, err = updateOp.Wait(ctx)
	require.Error(t, err, "a run token whose execution fails fails the update")
	failed, err := jobs.GetJob(ctx, &runpb.GetJobRequest{Name: job.Name})
	require.NoError(t, err)
	assert.Equal(t, runpb.Condition_CONDITION_FAILED, failed.GetTerminalCondition().GetState())
	assert.Contains(t, failed.GetTerminalCondition().GetMessage(), id+"-fails")

	_, err = jobs.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: parent,
		JobId:  id,
		Job: &runpb.Job{
			Template:        tokenJobTemplate("echo ran"),
			CreateExecution: &runpb.Job_StartExecutionToken{StartExecutionToken: strings.Repeat("t", 63-len(id))},
		},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a job name and token of 63 characters are refused")
}
