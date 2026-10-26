package gcp_sdk_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func createRunJobTasks(t *testing.T, client *run.JobsClient, jobID string, taskCount, parallelism int32, template *runpb.TaskTemplate) string {
	t.Helper()
	op, err := client.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: crJobParent,
		JobId:  jobID,
		Job: &runpb.Job{Template: &runpb.ExecutionTemplate{
			TaskCount:   taskCount,
			Parallelism: parallelism,
			Template:    template,
		}},
	})
	require.NoError(t, err)
	job, err := op.Wait(ctx)
	require.NoError(t, err)
	cleanupJob(t, client, job.Name)
	return job.Name
}

func listRunTasks(t *testing.T, execution string) []*runpb.Task {
	t.Helper()
	it := newTasksClient(t).ListTasks(ctx, &runpb.ListTasksRequest{Parent: execution})
	var tasks []*runpb.Task
	for {
		task, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].GetIndex() < tasks[j].GetIndex() })
	return tasks
}

func runTaskCompleted(task *runpb.Task) *runpb.Condition {
	for _, c := range task.GetConditions() {
		if c.GetType() == "Completed" {
			return c
		}
	}
	return nil
}

// An execution runs each task as its own container group, at most its
// parallelism at once, with the task's index, attempt and count, the job and
// the execution in its environment; a task that fails runs again under its
// retries, and each task reports its own outcome.
func TestSDK_CloudRun_ExecutionRunsEachTaskOnItsOwn(t *testing.T) {
	t.Parallel()
	jobs := newJobsClient(t)
	jobID := uniquename.New("sdk-job-tasks")
	name := createRunJobTasks(t, jobs, jobID, 3, 2, &runpb.TaskTemplate{
		Containers: []*runpb.Container{{
			Image: simWorkloadImage,
			Command: []string{"sh", "-c", `echo "start $CLOUD_RUN_TASK_INDEX $CLOUD_RUN_TASK_ATTEMPT $CLOUD_RUN_TASK_COUNT $CLOUD_RUN_JOB $CLOUD_RUN_EXECUTION"
sleep 2
if [ "$CLOUD_RUN_TASK_INDEX" = 1 ] && [ "$CLOUD_RUN_TASK_ATTEMPT" = 0 ]; then echo "end $CLOUD_RUN_TASK_INDEX $CLOUD_RUN_TASK_ATTEMPT"; exit 4; fi
echo "end $CLOUD_RUN_TASK_INDEX $CLOUD_RUN_TASK_ATTEMPT"`},
		}},
		Retries: &runpb.TaskTemplate_MaxRetries{MaxRetries: 1},
		Timeout: durationpb.New(60 * time.Second),
	})

	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(3), exec.GetTaskCount())
	assert.Equal(t, int32(2), exec.GetParallelism())
	assert.Equal(t, int32(3), exec.GetSucceededCount())
	assert.Equal(t, int32(0), exec.GetFailedCount())
	assert.Equal(t, int32(1), exec.GetRetriedCount(), "one task retried")
	execID := exec.GetName()[strings.LastIndex(exec.GetName(), "/")+1:]

	tasks := listRunTasks(t, exec.GetName())
	require.Len(t, tasks, 3)
	for i, task := range tasks {
		assert.Equal(t, int32(i), task.GetIndex())
		require.NotNil(t, runTaskCompleted(task), "task %d", i)
		assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, runTaskCompleted(task).GetState(), "task %d", i)
		assert.Equal(t, int32(codes.OK), task.GetLastAttemptResult().GetStatus().GetCode(), "task %d", i)
		wantRetried := int32(0)
		if i == 1 {
			wantRetried = 1
		}
		assert.Equal(t, wantRetried, task.GetRetried(), "task %d", i)
	}

	type attempt struct{ index, attempt string }
	want := []attempt{{"0", "0"}, {"1", "0"}, {"1", "1"}, {"2", "0"}}
	entries := waitForJobLogEntries(t, jobID, func(entries []jobLogEntry) bool {
		ends := 0
		for _, e := range entries {
			if strings.HasPrefix(e.message, "end ") {
				ends++
			}
		}
		return ends == len(want)
	})
	starts := map[attempt]time.Time{}
	ends := map[attempt]time.Time{}
	for _, e := range entries {
		fields := strings.Fields(e.message)
		if len(fields) < 3 {
			continue
		}
		key := attempt{fields[1], fields[2]}
		assert.Equal(t, key.index, e.labels["task_index"], "entry %q", e.message)
		assert.Equal(t, key.attempt, e.labels["task_attempt"], "entry %q", e.message)
		assert.Equal(t, execID, e.labels["execution_name"], "entry %q", e.message)
		switch fields[0] {
		case "start":
			assert.Equal(t, fmt.Sprintf("start %s %s 3 %s %s", key.index, key.attempt, jobID, execID), e.message)
			starts[key] = e.timestamp
		case "end":
			ends[key] = e.timestamp
		}
	}
	for _, key := range want {
		require.Contains(t, starts, key)
		require.Contains(t, ends, key)
	}
	// At no task's start were more than two tasks running.
	most := 0
	for _, at := range starts {
		running := 0
		for key, start := range starts {
			if !start.After(at) && ends[key].After(at) {
				running++
			}
		}
		most = max(most, running)
	}
	assert.Equal(t, 2, most, "the execution ran two tasks at once and never more")
}

// A failed task fails on its own: the execution counts it apart from the task
// that succeeded, and its last attempt carries its exit code.
func TestSDK_CloudRun_ExecutionReportsEachTasksOutcome(t *testing.T) {
	t.Parallel()
	jobs := newJobsClient(t)
	name := createRunJobTasks(t, jobs, uniquename.New("sdk-job-outcomes"), 2, 0, &runpb.TaskTemplate{
		Containers: []*runpb.Container{{
			Image:   simWorkloadImage,
			Command: []string{"sh", "-c", `[ "$CLOUD_RUN_TASK_INDEX" = 1 ] && exit 3; exit 0`},
		}},
		Retries: &runpb.TaskTemplate_MaxRetries{MaxRetries: 0},
		Timeout: durationpb.New(60 * time.Second),
	})

	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	started, err := runOp.Metadata()
	require.NoError(t, err)
	assert.Equal(t, int32(2), started.GetParallelism(), "an unset parallelism runs every task at once")
	_, err = runOp.Wait(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	exec, err := newExecutionsClient(t).GetExecution(ctx, &runpb.GetExecutionRequest{Name: started.GetName()})
	require.NoError(t, err)
	assert.Equal(t, int32(1), exec.GetSucceededCount())
	assert.Equal(t, int32(1), exec.GetFailedCount())
	assert.Equal(t, int32(0), exec.GetRetriedCount())

	tasks := listRunTasks(t, exec.GetName())
	require.Len(t, tasks, 2)
	assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, runTaskCompleted(tasks[0]).GetState())
	assert.Equal(t, int32(codes.OK), tasks[0].GetLastAttemptResult().GetStatus().GetCode())
	assert.Equal(t, runpb.Condition_CONDITION_FAILED, runTaskCompleted(tasks[1]).GetState())
	assert.Equal(t, int32(3), tasks[1].GetLastAttemptResult().GetExitCode())
	assert.Equal(t, int32(0), tasks[1].GetRetried())
}

// A task template that names no maxRetries gets the API's default of three.
func TestSDK_CloudRun_JobDefaultsMaxRetries(t *testing.T) {
	t.Parallel()
	jobs := newJobsClient(t)
	name := createRunJob(t, jobs, uniquename.New("sdk-job-default-retries"), &runpb.TaskTemplate{
		Containers: []*runpb.Container{{Image: simWorkloadImage}},
	})
	job, err := jobs.GetJob(ctx, &runpb.GetJobRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, int32(3), job.GetTemplate().GetTemplate().GetMaxRetries())
	assert.Equal(t, int32(1), job.GetTemplate().GetTaskCount())
}

// The task timeout bounds each attempt: the timed-out attempt gets SIGTERM,
// and the task's retry runs for a timeout of its own.
func TestSDK_CloudRun_TaskTimeoutStopsTheAttemptAndRetries(t *testing.T) {
	t.Parallel()
	jobs := newJobsClient(t)
	jobID := uniquename.New("sdk-job-timeout")
	name := createRunJob(t, jobs, jobID, &runpb.TaskTemplate{
		Containers: []*runpb.Container{{
			Image: simWorkloadImage,
			Command: []string{"sh", "-c", `if [ "$CLOUD_RUN_TASK_ATTEMPT" = 0 ]; then
trap 'echo "attempt 0 stopped"; exit 143' TERM
echo "attempt 0 running"
sleep 2147483647 & wait $!
fi
echo "attempt $CLOUD_RUN_TASK_ATTEMPT done"`},
		}},
		Retries: &runpb.TaskTemplate_MaxRetries{MaxRetries: 1},
		Timeout: durationpb.New(2 * time.Second),
	})

	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), exec.GetSucceededCount())
	assert.Equal(t, int32(1), exec.GetRetriedCount())
	tasks := listRunTasks(t, exec.GetName())
	require.Len(t, tasks, 1)
	assert.Equal(t, int32(1), tasks[0].GetRetried())
	logs := waitForJobLogs(t, jobID, func(logs string) bool { return strings.Contains(logs, "attempt 1 done") })
	assert.Contains(t, logs, "attempt 0 stopped", "the timeout sent the first attempt SIGTERM")
}

// A cancel stops the running task and keeps the tasks waiting for a slot from
// ever starting: every task of the execution counts as cancelled.
func TestSDK_CloudRun_CancelStopsTasksNotYetStarted(t *testing.T) {
	t.Parallel()
	jobs := newJobsClient(t)
	jobID := uniquename.New("sdk-job-cancel-tasks")
	name := createRunJobTasks(t, jobs, jobID, 3, 1, &runpb.TaskTemplate{
		Containers: []*runpb.Container{{
			Image: simWorkloadImage,
			Command: []string{"sh", "-c",
				`trap 'exit 143' TERM; echo "running $CLOUD_RUN_TASK_INDEX"; sleep 2147483647 & wait $!`},
		}},
		Retries: &runpb.TaskTemplate_MaxRetries{MaxRetries: 0},
		Timeout: durationpb.New(600 * time.Second),
	})

	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	started, err := runOp.Metadata()
	require.NoError(t, err)
	waitForJobLogMessage(t, jobID, "running 0")

	executions := newExecutionsClient(t)
	cancelOp, err := executions.CancelExecution(ctx, &runpb.CancelExecutionRequest{Name: started.GetName()})
	require.NoError(t, err)
	cancelled, err := cancelOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(3), cancelled.GetCancelledCount())
	assert.Equal(t, int32(0), cancelled.GetRunningCount())

	_, err = runOp.Wait(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))

	tasks := listRunTasks(t, started.GetName())
	require.Len(t, tasks, 3)
	assert.Equal(t, int32(143), tasks[0].GetLastAttemptResult().GetExitCode(), "the running task stopped through its SIGTERM handler")
	assert.Equal(t, int32(codes.Canceled), tasks[0].GetLastAttemptResult().GetStatus().GetCode())
	for _, task := range tasks[1:] {
		assert.Nil(t, task.GetStartTime(), "task %d never started", task.GetIndex())
		assert.Nil(t, task.GetLastAttemptResult(), "task %d never ran an attempt", task.GetIndex())
		assert.Equal(t, runpb.Condition_CONDITION_FAILED, runTaskCompleted(task).GetState(), "task %d", task.GetIndex())
	}
	assert.NotContains(t, jobLogs(t, jobID), "running 1")
}
