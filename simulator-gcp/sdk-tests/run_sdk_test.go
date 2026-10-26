package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/logging/logadmin"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func newJobsClient(t *testing.T) *run.JobsClient {
	t.Helper()
	client, err := run.NewJobsRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func cleanupJob(t *testing.T, client *run.JobsClient, name string) {
	t.Helper()
	t.Cleanup(func() {
		op, err := client.DeleteJob(ctx, &runpb.DeleteJobRequest{Name: name})
		require.NoError(t, err, "delete job %s", name)
		_, err = op.Wait(ctx)
		require.NoError(t, err, "delete job %s", name)
	})
}

func newExecutionsClient(t *testing.T) *run.ExecutionsClient {
	t.Helper()
	client, err := run.NewExecutionsRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSDK_CloudRun_CreateJob(t *testing.T) {
	t.Parallel()
	client := newJobsClient(t)
	jobID := uniquename.New("sdk-create-job")

	op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "mirror.gcr.io/library/alpine:latest"},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	job, err := op.Wait(ctx)
	require.NoError(t, err)
	cleanupJob(t, client, job.Name)

	assert.Contains(t, job.Name, jobID)
	assert.NotEmpty(t, job.Uid)
	assert.Equal(t, int64(1), job.Generation)
}

// TestSDK_CloudRun_CreateJob_OperationsPersisted: CreateJob's operation is
// addressable by name, and an unknown operation is 404.
func TestSDK_CloudRun_CreateJob_OperationsPersisted(t *testing.T) {
	t.Parallel()
	client := newJobsClient(t)
	jobID := uniquename.New("sdk-ops-persist-job")

	op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "mirror.gcr.io/library/alpine:latest"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	job, err := op.Wait(ctx)
	require.NoError(t, err)
	cleanupJob(t, client, job.Name)
	require.NotNil(t, job.TerminalCondition)
	assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, job.TerminalCondition.State)

	// Cloud Run keeps operation records for GetOperation; an unknown id is 404,
	// not done=true.
	resp, err := http.Get(baseURL + "/v2/projects/test-project/locations/us-central1/operations/does-not-exist-op")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"unknown operation id must 404, not return synthetic done=true")
}

func TestSDK_CloudRun_RunJob(t *testing.T) {
	t.Parallel()
	jobsClient := newJobsClient(t)
	jobID := uniquename.New("sdk-run-job")

	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "mirror.gcr.io/library/alpine:latest"},
					},
					Timeout: durationpb.New(1 * time.Second),
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)

	runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{
		Name: "projects/test-project/locations/us-central1/jobs/" + jobID,
	})
	require.NoError(t, err)

	// The operation carries the execution from the start and completes, with the
	// settled Execution as its response, only when the execution finishes.
	started, err := runOp.Metadata()
	require.NoError(t, err)
	require.NotNil(t, started)
	assert.Contains(t, started.Name, jobID)
	assert.Equal(t, jobID, started.Job)

	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)
	assert.True(t, runOp.Done())
	assert.Equal(t, started.Name, exec.Name)
	assert.Equal(t, int32(1), exec.SucceededCount)
	assert.Equal(t, int32(0), exec.RunningCount)
	assert.NotNil(t, exec.CompletionTime)
}

func TestSDK_CloudRun_RunJob_MultiContainerSharesLocalhost(t *testing.T) {
	t.Parallel()
	jobsClient := newJobsClient(t)
	jobID := uniquename.New("sdk-run-job-sidecar")

	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{
							Name:  "main",
							Image: httpProbeImageName,
							Args:  []string{"probe-once"},
						},
						{
							Name:  "sidecar",
							Image: httpProbeImageName,
							Args:  []string{"server"},
						},
					},
					Timeout: durationpb.New(30 * time.Second),
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)

	runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{
		Name: "projects/test-project/locations/us-central1/jobs/" + jobID,
	})
	require.NoError(t, err)
	settled, err := runOp.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(0), settled.GetRunningCount())
	require.Equal(t, int32(1), settled.GetSucceededCount(),
		"the multi-container task must succeed, not merely stop running")

	client := logadminClient(t)
	it := client.Entries(ctx, logadmin.Filter(`logName:"run.googleapis.com" AND resource.type="cloud_run_job" AND resource.labels.job_name="`+jobID+`"`))
	var logs []string
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if s, ok := entry.Payload.(string); ok {
			logs = append(logs, s)
		}
	}
	assert.True(t, strings.Contains(strings.Join(logs, "\n"), "cloudrun-job-sidecar-ok"), "job main container should reach sidecar over localhost; logs=%v", logs)
}

func TestSDK_CloudRun_GetExecution(t *testing.T) {
	t.Parallel()
	jobsClient := newJobsClient(t)
	execClient := newExecutionsClient(t)
	jobID := uniquename.New("sdk-getexec-job")

	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "mirror.gcr.io/library/alpine:latest"},
					},
					Timeout: durationpb.New(1 * time.Second),
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)

	runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{
		Name: "projects/test-project/locations/us-central1/jobs/" + jobID,
	})
	require.NoError(t, err)

	// The operation completes when the execution finishes.
	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)

	gotExec, err := execClient.GetExecution(ctx, &runpb.GetExecutionRequest{Name: exec.Name})
	require.NoError(t, err)
	assert.Equal(t, exec.Name, gotExec.Name)
	assert.Equal(t, int32(1), gotExec.SucceededCount)
	assert.Equal(t, int32(0), gotExec.RunningCount)
	assert.NotNil(t, gotExec.CompletionTime)
}

func TestSDK_CloudRun_CancelExecution(t *testing.T) {
	t.Parallel()
	jobsClient := newJobsClient(t)
	execClient := newExecutionsClient(t)
	jobID := uniquename.New("sdk-cancel-job")
	const marker = "sdk-cancel-marker"

	// The container announces itself and holds until cancelled: one that had
	// already exited would settle the execution from its exit status, leaving the
	// cancelled count at zero.
	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: commandImageName, Args: []string{"log", marker, "60"}},
					},
					Timeout: durationpb.New(60 * time.Second),
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)

	runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{
		Name: "projects/test-project/locations/us-central1/jobs/" + jobID,
	})
	require.NoError(t, err)

	// The execution is running, so the RunJob operation is too; its Execution
	// metadata names the execution to cancel.
	exec, err := runOp.Metadata()
	require.NoError(t, err)
	require.NotNil(t, exec)

	waitForJobLogMessage(t, jobID, marker)
	running, err := execClient.GetExecution(ctx, &runpb.GetExecutionRequest{Name: exec.Name})
	require.NoError(t, err)
	require.Equal(t, int32(1), running.GetRunningCount(), "the execution is running when the cancel arrives")
	require.Nil(t, running.GetCompletionTime())

	cancelOp, err := execClient.CancelExecution(ctx, &runpb.CancelExecutionRequest{
		Name: exec.Name,
	})
	require.NoError(t, err)

	cancelledExec, err := cancelOp.Wait(ctx)
	require.NoError(t, err)

	assert.Equal(t, int32(0), cancelledExec.RunningCount)
	assert.Equal(t, int32(1), cancelledExec.CancelledCount)
	assert.Equal(t, int32(0), cancelledExec.SucceededCount)
	assert.Equal(t, int32(0), cancelledExec.FailedCount)
	assert.NotNil(t, cancelledExec.CompletionTime)

	// The RunJob operation ends CANCELLED once the workload has stopped.
	_, err = runOp.Wait(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err), "RunJob operation error: %v", err)
}

func TestSDK_CloudRun_DeleteJob(t *testing.T) {
	t.Parallel()
	client := newJobsClient(t)
	jobID := uniquename.New("sdk-delete-job")

	createOp, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "mirror.gcr.io/library/alpine:latest"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)

	deleteOp, err := client.DeleteJob(ctx, &runpb.DeleteJobRequest{
		Name: "projects/test-project/locations/us-central1/jobs/" + jobID,
	})
	require.NoError(t, err)

	deletedJob, err := deleteOp.Wait(ctx)
	require.NoError(t, err)
	assert.Contains(t, deletedJob.Name, jobID)
}

func TestSDK_CloudRun_ListJobs(t *testing.T) {
	t.Parallel()
	client := newJobsClient(t)

	nameA, nameB := uniquename.New("sdk-list-job-a"), uniquename.New("sdk-list-job-b")
	for _, id := range []string{nameA, nameB} {
		op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: "projects/test-project/locations/us-central1",
			JobId:  id,
			Job: &runpb.Job{
				Template: &runpb.ExecutionTemplate{
					Template: &runpb.TaskTemplate{
						Containers: []*runpb.Container{
							{Image: "mirror.gcr.io/library/alpine:latest"},
						},
					},
				},
			},
		})
		require.NoError(t, err)
		job, err := op.Wait(ctx)
		require.NoError(t, err)
		cleanupJob(t, client, job.Name)
	}

	it := client.ListJobs(ctx, &runpb.ListJobsRequest{
		Parent: "projects/test-project/locations/us-central1",
	})

	var names []string
	for {
		job, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		names = append(names, job.Name)
	}

	foundA, foundB := false, false
	for _, n := range names {
		if n == "projects/test-project/locations/us-central1/jobs/"+nameA {
			foundA = true
		}
		if n == "projects/test-project/locations/us-central1/jobs/"+nameB {
			foundB = true
		}
	}
	assert.True(t, foundA, "%s not found in list", nameA)
	assert.True(t, foundB, "%s not found in list", nameB)
}

func TestSDK_CloudRun_ListJobsPaginationAndEmptyWireShape(t *testing.T) {
	t.Parallel()
	client := newJobsClient(t)
	parent := "projects/test-project/locations/us-east1"

	for _, id := range []string{uniquename.New("sdk-page-job-a"), uniquename.New("sdk-page-job-b")} {
		op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: parent,
			JobId:  id,
			Job: &runpb.Job{
				Template: &runpb.ExecutionTemplate{
					Template: &runpb.TaskTemplate{
						Containers: []*runpb.Container{{Image: "mirror.gcr.io/library/alpine:latest"}},
					},
				},
			},
		})
		require.NoError(t, err)
		_, err = op.Wait(ctx)
		require.NoError(t, err)
		cleanupJob(t, client, parent+"/jobs/"+id)
	}

	resp, err := http.Get(baseURL + "/v2/" + parent + "/jobs?pageSize=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var first struct {
		Jobs          []map[string]any `json:"jobs"`
		NextPageToken string           `json:"nextPageToken"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&first))
	require.Len(t, first.Jobs, 1)
	require.NotEmpty(t, first.NextPageToken)

	emptyResp, err := http.Get(baseURL + "/v2/projects/test-project/locations/asia-south1/jobs")
	require.NoError(t, err)
	defer emptyResp.Body.Close()
	require.Equal(t, http.StatusOK, emptyResp.StatusCode)
	var empty map[string]any
	require.NoError(t, json.NewDecoder(emptyResp.Body).Decode(&empty))
	require.IsType(t, []any{}, empty["jobs"], "empty job list must serialize as [] not null")
}
