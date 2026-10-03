package gcp_sdk_test

import (
	"testing"
	"time"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const crJobParent = "projects/test-project/locations/us-central1"

// createRunJob creates a Cloud Run job from template and deletes it when the
// test ends.
func createRunJob(t *testing.T, client *run.JobsClient, jobID string, template *runpb.TaskTemplate) string {
	t.Helper()
	op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: crJobParent,
		JobId:  jobID,
		Job:    &runpb.Job{Template: &runpb.ExecutionTemplate{Template: template}},
	})
	require.NoError(t, err)
	job, err := op.Wait(ctx)
	require.NoError(t, err)
	cleanupJob(t, client, job.Name)
	return job.Name
}

// A container that dependsOn another starts only once that container has
// passed its startup probe: the main container makes one connection attempt to
// a server that opens its port three seconds after it starts, and succeeds.
func TestSDK_CloudRun_RunJob_DependsOnWaitsForStartupProbe(t *testing.T) {
	jobs := newJobsClient(t)
	name := createRunJob(t, jobs, uniqueName("sdk-job-depends"), &runpb.TaskTemplate{
		Containers: []*runpb.Container{
			{
				Name:      "main",
				Image:     "alpine:latest",
				Command:   []string{"nc", "-z", "127.0.0.1", "9090"},
				DependsOn: []string{"server"},
			},
			{
				Name:  "server",
				Image: commandImageName,
				Args:  []string{"http", "9090", "ready", "3"},
				StartupProbe: &runpb.Probe{
					PeriodSeconds:    1,
					TimeoutSeconds:   1,
					FailureThreshold: 30,
					ProbeType: &runpb.Probe_HttpGet{HttpGet: &runpb.HTTPGetAction{
						Path: "/",
						Port: 9090,
					}},
				},
			},
		},
		Timeout: durationpb.New(60 * time.Second),
	})

	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), exec.GetSucceededCount())
}

// A task whose container fails its startup probe fails, and the attempt's
// status says so.
func TestSDK_CloudRun_RunJob_StartupProbeFailureFailsTask(t *testing.T) {
	jobs := newJobsClient(t)
	name := createRunJob(t, jobs, uniqueName("sdk-job-probe-fail"), &runpb.TaskTemplate{
		Containers: []*runpb.Container{{
			Name:  "main",
			Image: commandImageName,
			Args:  []string{"hold"},
			StartupProbe: &runpb.Probe{
				PeriodSeconds:    1,
				TimeoutSeconds:   1,
				FailureThreshold: 1,
				ProbeType:        &runpb.Probe_TcpSocket{TcpSocket: &runpb.TCPSocketAction{Port: 9191}},
			},
		}},
		Timeout: durationpb.New(60 * time.Second),
	})

	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	started, err := runOp.Metadata()
	require.NoError(t, err)
	_, err = runOp.Wait(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "RunJob operation error: %v", err)

	executions := newExecutionsClient(t)
	exec, err := executions.GetExecution(ctx, &runpb.GetExecutionRequest{Name: started.GetName()})
	require.NoError(t, err)
	assert.Equal(t, int32(1), exec.GetFailedCount())

	tasks := newTasksClient(t)
	task, err := tasks.ListTasks(ctx, &runpb.ListTasksRequest{Parent: exec.GetName()}).Next()
	require.NoError(t, err)
	require.NotNil(t, task.GetLastAttemptResult())
	assert.NotEqual(t, int32(codes.OK), task.GetLastAttemptResult().GetStatus().GetCode())
	assert.Contains(t, task.GetLastAttemptResult().GetStatus().GetMessage(), "startup probe")
}

// A dependsOn that names no container of the template, or that forms a cycle,
// is refused when the job is created.
func TestSDK_CloudRun_CreateJob_RefusesUnresolvableDependsOn(t *testing.T) {
	jobs := newJobsClient(t)
	for _, containers := range [][]*runpb.Container{
		{{Name: "main", Image: "alpine:latest", DependsOn: []string{"absent"}}},
		{
			{Name: "a", Image: "alpine:latest", DependsOn: []string{"b"}},
			{Name: "b", Image: "alpine:latest", DependsOn: []string{"a"}},
		},
	} {
		_, err := jobs.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: crJobParent,
			JobId:  uniqueName("sdk-job-bad-deps"),
			Job: &runpb.Job{Template: &runpb.ExecutionTemplate{Template: &runpb.TaskTemplate{
				Containers: containers,
			}}},
		})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	}
}

// Deleting a job stops the executions it still runs: the RunJob operation of
// a running execution ends once its container has stopped, reporting the
// execution gone.
func TestSDK_CloudRun_DeleteJobStopsItsRunningExecution(t *testing.T) {
	jobs := newJobsClient(t)
	jobID := uniqueName("sdk-job-delete-running")
	const marker = "sdk-delete-running-marker"
	createOp, err := jobs.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: crJobParent,
		JobId:  jobID,
		Job: &runpb.Job{Template: &runpb.ExecutionTemplate{Template: &runpb.TaskTemplate{
			Containers: []*runpb.Container{{Image: commandImageName, Args: []string{"log", marker, "600"}}},
			Timeout:    durationpb.New(600 * time.Second),
		}}},
	})
	require.NoError(t, err)
	job, err := createOp.Wait(ctx)
	require.NoError(t, err)

	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: job.Name})
	require.NoError(t, err)
	waitForJobLogMessage(t, jobID, marker)

	deleteOp, err := jobs.DeleteJob(ctx, &runpb.DeleteJobRequest{Name: job.Name})
	require.NoError(t, err)
	_, err = deleteOp.Wait(ctx)
	require.NoError(t, err)

	_, err = runOp.Wait(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err), "RunJob operation error: %v", err)
}

// Cancelling an execution that has already completed leaves it as it is.
func TestSDK_CloudRun_CancelCompletedExecutionLeavesIt(t *testing.T) {
	jobs := newJobsClient(t)
	name := createRunJob(t, jobs, uniqueName("sdk-job-cancel-done"), &runpb.TaskTemplate{
		Containers: []*runpb.Container{{Image: "alpine:latest"}},
	})
	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	done, err := runOp.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(1), done.GetSucceededCount())

	executions := newExecutionsClient(t)
	cancelOp, err := executions.CancelExecution(ctx, &runpb.CancelExecutionRequest{Name: done.GetName()})
	require.NoError(t, err)
	after, err := cancelOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), after.GetSucceededCount())
	assert.Equal(t, int32(0), after.GetCancelledCount())
	assert.Equal(t, done.GetEtag(), after.GetEtag(), "the completed execution is unchanged")
	for _, c := range after.GetConditions() {
		assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, c.GetState(), "condition %s", c.GetType())
	}
}
