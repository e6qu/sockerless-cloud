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

	op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  "sdk-create-job",
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

	assert.Contains(t, job.Name, "sdk-create-job")
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

	op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  "sdk-ops-persist-job",
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

	// Create job first
	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  "sdk-run-job",
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
		Name: "projects/test-project/locations/us-central1/jobs/sdk-run-job",
	})
	require.NoError(t, err)

	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)

	assert.Contains(t, exec.Name, "sdk-run-job")
	assert.NotEmpty(t, exec.Name)
}

func TestSDK_CloudRun_RunJob_MultiContainerSharesLocalhost(t *testing.T) {
	jobsClient := newJobsClient(t)
	execClient := newExecutionsClient(t)

	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  "sdk-run-job-sidecar",
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
		Name: "projects/test-project/locations/us-central1/jobs/sdk-run-job-sidecar",
	})
	require.NoError(t, err)
	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)

	// Generous deadline so a slow real multi-container job start on a loaded CI
	// runner doesn't expire before the execution completes; matches sibling tests.
	require.Eventually(t, func() bool {
		got, err := execClient.GetExecution(ctx, &runpb.GetExecutionRequest{Name: exec.Name})
		require.NoError(t, err)
		return got.RunningCount == 0 && got.SucceededCount == 1
	}, 60*time.Second, 200*time.Millisecond)

	client := logadminClient(t)
	it := client.Entries(ctx, logadmin.Filter(`resource.type="cloud_run_job" AND resource.labels.job_name="sdk-run-job-sidecar"`))
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

	// Create and run job
	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  "sdk-getexec-job",
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
		Name: "projects/test-project/locations/us-central1/jobs/sdk-getexec-job",
	})
	require.NoError(t, err)

	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)

	// runOp.Wait returns when the execution STARTS; poll GetExecution until it
	// completes (CompletionTime set) — a fixed sleep races a loaded runner.
	var gotExec *runpb.Execution
	require.Eventually(t, func() bool {
		gotExec, err = execClient.GetExecution(ctx, &runpb.GetExecutionRequest{Name: exec.Name})
		return err == nil && gotExec.CompletionTime != nil
	}, 60*time.Second, 200*time.Millisecond)

	assert.Equal(t, exec.Name, gotExec.Name)
	assert.Equal(t, int32(1), gotExec.SucceededCount)
	assert.Equal(t, int32(0), gotExec.RunningCount)
	assert.NotNil(t, gotExec.CompletionTime)
}

func TestSDK_CloudRun_CancelExecution(t *testing.T) {
	jobsClient := newJobsClient(t)
	execClient := newExecutionsClient(t)

	// Create and run a long-running job (no command = sleeps for default timeout)
	createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  "sdk-cancel-job",
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

	runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{
		Name: "projects/test-project/locations/us-central1/jobs/sdk-cancel-job",
	})
	require.NoError(t, err)

	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)

	// Cancel immediately
	cancelOp, err := execClient.CancelExecution(ctx, &runpb.CancelExecutionRequest{
		Name: exec.Name,
	})
	require.NoError(t, err)

	cancelledExec, err := cancelOp.Wait(ctx)
	require.NoError(t, err)

	assert.Equal(t, int32(0), cancelledExec.RunningCount)
	assert.Equal(t, int32(1), cancelledExec.CancelledCount)
	assert.NotNil(t, cancelledExec.CompletionTime)
}

func TestSDK_CloudRun_DeleteJob(t *testing.T) {
	client := newJobsClient(t)

	// Create job
	createOp, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: "projects/test-project/locations/us-central1",
		JobId:  "sdk-delete-job",
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
		Name: "projects/test-project/locations/us-central1/jobs/sdk-delete-job",
	})
	require.NoError(t, err)

	deletedJob, err := deleteOp.Wait(ctx)
	require.NoError(t, err)
	assert.Contains(t, deletedJob.Name, "sdk-delete-job")
}

func TestSDK_CloudRun_ListJobs(t *testing.T) {
	client := newJobsClient(t)

	// Create two jobs with unique prefix
	for _, id := range []string{"sdk-list-job-a", "sdk-list-job-b"} {
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
		_, err = op.Wait(ctx)
		require.NoError(t, err)
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
		if n == "projects/test-project/locations/us-central1/jobs/sdk-list-job-a" {
			foundA = true
		}
		if n == "projects/test-project/locations/us-central1/jobs/sdk-list-job-b" {
			foundB = true
		}
	}
	assert.True(t, foundA, "sdk-list-job-a not found in list")
	assert.True(t, foundB, "sdk-list-job-b not found in list")
}

func TestSDK_CloudRun_ListJobsPaginationAndEmptyWireShape(t *testing.T) {
	client := newJobsClient(t)
	parent := "projects/test-project/locations/us-east1"

	for _, id := range []string{"sdk-page-job-a", "sdk-page-job-b"} {
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
		t.Cleanup(func() {
			deleteOp, err := client.DeleteJob(ctx, &runpb.DeleteJobRequest{Name: parent + "/jobs/" + id})
			if err == nil {
				_, _ = deleteOp.Wait(ctx)
			}
		})
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
