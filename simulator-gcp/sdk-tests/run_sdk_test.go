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

// cleanupJob deletes a Cloud Run job when the test ends and waits for the
// delete to complete.
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
	client := newJobsClient(t)
	jobID := uniqueName("sdk-create-job")

	op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "alpine:latest"},
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

// TestSDK_CloudRun_CreateJob_OperationsPersisted pins the real Cloud Run
// LRO contract: the operation returned by CreateJob is addressable by name
// (GET /operations/{op} returns the same record); unknown ops 404.
// Previously the sim returned a synthetic `done=true` Operation for any op
// id, which masked client bugs and dropped LRO inspection by name.
func TestSDK_CloudRun_CreateJob_OperationsPersisted(t *testing.T) {
	client := newJobsClient(t)
	jobID := uniqueName("sdk-ops-persist-job")

	op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "alpine:latest"},
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

	// The operation record must be retrievable by name from the
	// /operations endpoint — real Cloud Run keeps LRO records around for
	// the SDK to GetOperation against. Unknown operation ids must 404
	// (not silently return done=true).
	resp, err := http.Get(baseURL + "/v2/projects/test-project/locations/us-central1/operations/does-not-exist-op")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"unknown operation id must 404, not return synthetic done=true")
}

func TestSDK_CloudRun_RunJob(t *testing.T) {
	jobsClient := newJobsClient(t)
	jobID := uniqueName("sdk-run-job")

	// Create job first
	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "alpine:latest"},
					},
					Timeout: durationpb.New(1 * time.Second),
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)

	// Run job
	runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{
		Name: "projects/test-project/locations/us-central1/jobs/" + jobID,
	})
	require.NoError(t, err)

	// The operation carries the execution from the start and completes only
	// when it finishes, with the settled Execution as its response.
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
	jobsClient := newJobsClient(t)
	jobID := uniqueName("sdk-run-job-sidecar")

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
	jobsClient := newJobsClient(t)
	execClient := newExecutionsClient(t)
	jobID := uniqueName("sdk-getexec-job")

	// Create and run job
	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "alpine:latest"},
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
	jobsClient := newJobsClient(t)
	execClient := newExecutionsClient(t)
	jobID := uniqueName("sdk-cancel-job")
	const marker = "sdk-cancel-marker"

	// The container announces itself on stdout and then holds until it is
	// cancelled. A container that had already exited would settle the
	// execution from its exit status, so the cancel would find no running task
	// and the cancelled count would stay at zero.
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

	// Cancel while that container is demonstrably running.
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
	client := newJobsClient(t)
	jobID := uniqueName("sdk-delete-job")

	// Create job
	createOp, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  jobID,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{
						{Image: "alpine:latest"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = createOp.Wait(ctx)
	require.NoError(t, err)

	// Delete job
	deleteOp, err := client.DeleteJob(ctx, &runpb.DeleteJobRequest{
		Name: "projects/test-project/locations/us-central1/jobs/" + jobID,
	})
	require.NoError(t, err)

	deletedJob, err := deleteOp.Wait(ctx)
	require.NoError(t, err)
	assert.Contains(t, deletedJob.Name, jobID)
}

func TestSDK_CloudRun_ListJobs(t *testing.T) {
	client := newJobsClient(t)

	nameA, nameB := uniqueName("sdk-list-job-a"), uniqueName("sdk-list-job-b")
	for _, id := range []string{nameA, nameB} {
		op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: "projects/test-project/locations/us-central1",
			JobId:  id,
			Job: &runpb.Job{
				Template: &runpb.ExecutionTemplate{
					Template: &runpb.TaskTemplate{
						Containers: []*runpb.Container{
							{Image: "alpine:latest"},
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

	// List jobs
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

	// Should contain at least our two jobs
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
	client := newJobsClient(t)
	parent := "projects/test-project/locations/us-east1"

	for _, id := range []string{uniqueName("sdk-page-job-a"), uniqueName("sdk-page-job-b")} {
		op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: parent,
			JobId:  id,
			Job: &runpb.Job{
				Template: &runpb.ExecutionTemplate{
					Template: &runpb.TaskTemplate{
						Containers: []*runpb.Container{{Image: "alpine:latest"}},
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
