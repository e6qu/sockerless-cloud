package gcp_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	run "google.golang.org/api/run/v1"
)

// Cloud Run Admin v1 (Knative) jobs lifecycle, run and cancel, driven by the
// canonical google.golang.org/api/run/v1 client. The Knative jobs surface
// writes and reads the same records the v2 collection owns and drives the same
// container execution, so these tests assert on real container outcomes, not
// on record bookkeeping alone.

// waitCloudRunV1ExecutionDone polls the Knative execution until it completes.
func waitCloudRunV1ExecutionDone(t *testing.T, svc *run.APIService, executionID string) *run.Execution {
	t.Helper()
	var execution *run.Execution
	require.Eventually(t, func() bool {
		got, err := svc.Namespaces.Executions.Get(
			"namespaces/" + cloudRunV1Namespace + "/executions/" + executionID).Do()
		if err != nil {
			return false
		}
		execution = got
		return got.Status != nil && got.Status.CompletionTime != ""
	}, 90*time.Second, 200*time.Millisecond)
	return execution
}

func TestCloudRunV1_JobsLifecycle(t *testing.T) {
	svc := newRunV1(t)
	const id = "v1-knative-job"
	parent := "namespaces/" + cloudRunV1Namespace

	created, err := svc.Namespaces.Jobs.Create(parent, &run.Job{
		Metadata: &run.ObjectMeta{Name: id, Labels: map[string]string{"suite": "knative"}},
		Spec: &run.JobSpec{Template: &run.ExecutionTemplateSpec{
			Spec: &run.ExecutionSpec{
				Parallelism: 1,
				TaskCount:   2,
				Template: &run.TaskTemplateSpec{Spec: &run.TaskSpec{
					Containers:     []*run.Container{{Image: "alpine:latest", Args: []string{"true"}}},
					TimeoutSeconds: 45,
					MaxRetries:     3,
				}},
			},
		}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Namespaces.Jobs.Delete(parent + "/jobs/" + id).Do()
	})
	assert.Equal(t, "run.googleapis.com/v1", created.ApiVersion)
	assert.Equal(t, "Job", created.Kind)
	assert.Equal(t, id, created.Metadata.Name)
	assert.Equal(t, cloudRunV1Namespace, created.Metadata.Namespace)
	require.NotNil(t, created.Spec)
	require.NotNil(t, created.Spec.Template)
	require.NotNil(t, created.Spec.Template.Spec)
	assert.Equal(t, int64(2), created.Spec.Template.Spec.TaskCount)
	require.NotNil(t, created.Spec.Template.Spec.Template)
	assert.Equal(t, int64(45), created.Spec.Template.Spec.Template.Spec.TimeoutSeconds)

	// The same job, through the v2 collection: one record, two spellings.
	v2 := getCloudRunV2JSON(t, fmt.Sprintf("/v2/projects/%s/locations/us-central1/jobs/%s", cloudRunV1Namespace, id))
	assert.Equal(t,
		fmt.Sprintf("projects/%s/locations/us-central1/jobs/%s", cloudRunV1Namespace, id), v2["name"])
	template, _ := v2["template"].(map[string]any)
	require.NotNil(t, template)
	assert.Equal(t, float64(2), template["taskCount"])
	taskTemplate, _ := template["template"].(map[string]any)
	require.NotNil(t, taskTemplate)
	assert.Equal(t, "45s", taskTemplate["timeout"],
		"the Knative int64 seconds count is stored as the v2 google-duration")

	got, err := svc.Namespaces.Jobs.Get(parent + "/jobs/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, "knative", got.Metadata.Labels["suite"])

	list, err := svc.Namespaces.Jobs.List(parent).LabelSelector("suite=knative").Do()
	require.NoError(t, err)
	assert.Equal(t, "JobList", list.Kind)
	require.Len(t, list.Items, 1)
	assert.Equal(t, id, list.Items[0].Metadata.Name)

	replaced, err := svc.Namespaces.Jobs.ReplaceJob(parent+"/jobs/"+id, &run.Job{
		Metadata: &run.ObjectMeta{Name: id, Labels: map[string]string{"suite": "knative2"}},
		Spec: &run.JobSpec{Template: &run.ExecutionTemplateSpec{
			Spec: &run.ExecutionSpec{
				TaskCount: 1,
				Template: &run.TaskTemplateSpec{Spec: &run.TaskSpec{
					Containers: []*run.Container{{Image: "alpine:latest", Args: []string{"true"}}},
				}},
			},
		}},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), replaced.Metadata.Generation, "ReplaceJob advances the generation")
	assert.Equal(t, "knative2", replaced.Metadata.Labels["suite"])
	assert.Equal(t, int64(1), replaced.Spec.Template.Spec.TaskCount)

	status, err := svc.Namespaces.Jobs.Delete(parent + "/jobs/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, "Success", status.Status)

	_, err = svc.Namespaces.Jobs.Get(parent + "/jobs/" + id).Do()
	require.Error(t, err)
}

// TestCloudRunV1_JobRunPropagatesNonZeroExit runs a REAL container image
// through the Knative run method and asserts the container's non-zero exit
// reaches the execution's failedCount and the task's lastAttemptResult.
func TestCloudRunV1_JobRunPropagatesNonZeroExit(t *testing.T) {
	svc := newRunV1(t)
	const id = "v1-run-failing-job"
	parent := "namespaces/" + cloudRunV1Namespace

	_, err := svc.Namespaces.Jobs.Create(parent, &run.Job{
		Metadata: &run.ObjectMeta{Name: id},
		Spec: &run.JobSpec{Template: &run.ExecutionTemplateSpec{
			Spec: &run.ExecutionSpec{Template: &run.TaskTemplateSpec{Spec: &run.TaskSpec{
				// The arithmetic evaluator exits non-zero on a malformed
				// expression — a real process failure, not a simulated one.
				Containers:     []*run.Container{{Image: evalImageName, Args: []string{"3 +"}}},
				TimeoutSeconds: 30,
			}}},
		}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Namespaces.Jobs.Delete(parent + "/jobs/" + id).Do()
	})

	started, err := svc.Namespaces.Jobs.Run(parent+"/jobs/"+id, &run.RunJobRequest{}).Do()
	require.NoError(t, err)
	assert.Equal(t, "Execution", started.Kind,
		"the Knative run method answers with an Execution, not a long-running operation")
	require.NotNil(t, started.Metadata)
	executionID := started.Metadata.Name
	require.NotEmpty(t, executionID)

	done := waitCloudRunV1ExecutionDone(t, svc, executionID)
	require.NotNil(t, done.Status)
	assert.Equal(t, int64(1), done.Status.FailedCount,
		"the container's non-zero exit is the execution's failure")
	assert.Equal(t, int64(0), done.Status.SucceededCount)
	assert.Equal(t, int64(0), done.Status.RunningCount)

	tasks, err := svc.Namespaces.Tasks.List(parent).
		LabelSelector("run.googleapis.com/execution=" + executionID).Do()
	require.NoError(t, err)
	require.Len(t, tasks.Items, 1)
	require.NotNil(t, tasks.Items[0].Status)
	require.NotNil(t, tasks.Items[0].Status.LastAttemptResult)
	assert.NotEqual(t, int64(0), tasks.Items[0].Status.LastAttemptResult.ExitCode,
		"the task records the container's real exit code")

	// The job's Knative status reports the outcome of its latest execution.
	job, err := svc.Namespaces.Jobs.Get(parent + "/jobs/" + id).Do()
	require.NoError(t, err)
	require.NotNil(t, job.Status)
	require.NotNil(t, job.Status.LatestCreatedExecution)
	assert.Equal(t, executionID, job.Status.LatestCreatedExecution.Name)
	assert.Equal(t, "EXECUTION_FAILED", job.Status.LatestCreatedExecution.CompletionStatus)
	assert.Equal(t, int64(1), job.Status.ExecutionCount)
}

// TestCloudRunV1_ExecutionCancelStopsTheContainer cancels a running execution
// through the Knative surface and proves the workload container really
// stopped: the container writes a line a second into Cloud Logging, and the
// log stream must stop growing once the cancel returns. A cancel that only
// rewrote the execution record would leave the container ticking.
func TestCloudRunV1_ExecutionCancelStopsTheContainer(t *testing.T) {
	svc := newRunV1(t)
	const id = "v1-cancel-job"
	parent := "namespaces/" + cloudRunV1Namespace

	_, err := svc.Namespaces.Jobs.Create(parent, &run.Job{
		Metadata: &run.ObjectMeta{Name: id},
		Spec: &run.JobSpec{Template: &run.ExecutionTemplateSpec{
			Spec: &run.ExecutionSpec{Template: &run.TaskTemplateSpec{Spec: &run.TaskSpec{
				Containers: []*run.Container{{
					Image: "alpine:latest",
					Args:  []string{"sh", "-c", "i=0; while true; do echo tick-$i; i=$((i+1)); sleep 1; done"},
				}},
				TimeoutSeconds: 300,
			}}},
		}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Namespaces.Jobs.Delete(parent + "/jobs/" + id).Do()
	})

	started, err := svc.Namespaces.Jobs.Run(parent+"/jobs/"+id, &run.RunJobRequest{}).Do()
	require.NoError(t, err)
	executionID := started.Metadata.Name

	// Wait until the container is demonstrably running.
	require.Eventually(t, func() bool {
		return strings.Contains(jobLogs(t, id), "tick-0")
	}, 90*time.Second, 200*time.Millisecond, "the workload container must start ticking")

	cancelled, err := svc.Namespaces.Executions.Cancel(
		parent+"/executions/"+executionID, &run.CancelExecutionRequest{}).Do()
	require.NoError(t, err)
	assert.Equal(t, "Execution", cancelled.Kind)
	require.NotNil(t, cancelled.Status)
	assert.Equal(t, int64(1), cancelled.Status.CancelledCount)
	assert.Equal(t, int64(0), cancelled.Status.RunningCount)
	assert.NotEmpty(t, cancelled.Status.CompletionTime)

	// Let any line already in flight land, then hold still: a stopped
	// container produces no further ticks.
	time.Sleep(2 * time.Second)
	settled := strings.Count(jobLogs(t, id), "tick-")
	time.Sleep(4 * time.Second)
	assert.Equal(t, settled, strings.Count(jobLogs(t, id), "tick-"),
		"the cancelled execution's container must be stopped, not merely recorded as cancelled")
}

// TestCloudRunV1_JobRunOverrides drives the Knative run method's per-run
// overrides against a real container and shows the job resource is untouched.
func TestCloudRunV1_JobRunOverrides(t *testing.T) {
	svc := newRunV1(t)
	const id = "v1-overrides-job"
	parent := "namespaces/" + cloudRunV1Namespace

	_, err := svc.Namespaces.Jobs.Create(parent, &run.Job{
		Metadata: &run.ObjectMeta{Name: id},
		Spec: &run.JobSpec{Template: &run.ExecutionTemplateSpec{
			Spec: &run.ExecutionSpec{Template: &run.TaskTemplateSpec{Spec: &run.TaskSpec{
				Containers:     []*run.Container{{Image: "alpine:latest", Args: []string{"echo", "baseline"}}},
				TimeoutSeconds: 30,
			}}},
		}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Namespaces.Jobs.Delete(parent + "/jobs/" + id).Do()
	})

	started, err := svc.Namespaces.Jobs.Run(parent+"/jobs/"+id, &run.RunJobRequest{
		Overrides: &run.Overrides{
			TaskCount: 2,
			ContainerOverrides: []*run.ContainerOverride{{
				Args: []string{"sh", "-c", "echo overridden-$OVERRIDE_MARKER"},
				Env:  []*run.EnvVar{{Name: "OVERRIDE_MARKER", Value: "yes"}},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, started.Spec)
	assert.Equal(t, int64(2), started.Spec.TaskCount, "the override replaces the execution's task count")
	require.NotNil(t, started.Spec.Template)
	require.NotNil(t, started.Spec.Template.Spec)
	require.Len(t, started.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, []string{"sh", "-c", "echo overridden-$OVERRIDE_MARKER"},
		started.Spec.Template.Spec.Containers[0].Args)

	// The override reached the real container.
	require.Eventually(t, func() bool {
		return strings.Contains(jobLogs(t, id), "overridden-yes")
	}, 90*time.Second, 200*time.Millisecond, "the overridden args and env must reach the container")

	// The job resource itself is unchanged — an override lasts one execution.
	job, err := svc.Namespaces.Jobs.Get(parent + "/jobs/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"echo", "baseline"},
		job.Spec.Template.Spec.Template.Spec.Containers[0].Args)
	assert.Equal(t, int64(1), job.Spec.Template.Spec.TaskCount)
}

// TestCloudRunV1_JobsDryRun pins that a Knative job dry run validates without
// persisting, and that an unsupported dryRun value is rejected.
func TestCloudRunV1_JobsDryRun(t *testing.T) {
	svc := newRunV1(t)
	const id = "v1-dryrun-job"
	parent := "namespaces/" + cloudRunV1Namespace
	body := &run.Job{
		Metadata: &run.ObjectMeta{Name: id},
		Spec: &run.JobSpec{Template: &run.ExecutionTemplateSpec{
			Spec: &run.ExecutionSpec{Template: &run.TaskTemplateSpec{Spec: &run.TaskSpec{
				Containers: []*run.Container{{Image: "alpine:latest"}},
			}}},
		}},
	}

	created, err := svc.Namespaces.Jobs.Create(parent, body).DryRun("all").Do()
	require.NoError(t, err)
	assert.Equal(t, id, created.Metadata.Name)

	_, err = svc.Namespaces.Jobs.Get(parent + "/jobs/" + id).Do()
	require.Error(t, err, "a dry run persists nothing")

	_, err = svc.Namespaces.Jobs.Create(parent, body).DryRun("maybe").Do()
	require.Error(t, err, "an unsupported dryRun value is rejected, never silently ignored")
}

// TestCloudRunV1_ExecutionDeleteRemovesItsTasks pins the Knative execution
// delete: the execution and the tasks it owns both go.
func TestCloudRunV1_ExecutionDeleteRemovesItsTasks(t *testing.T) {
	svc := newRunV1(t)
	const jobID = "v1-exec-delete-job"
	execName := createAndRunJobWithImageAndCommand(t, jobID, "alpine:latest", []string{"true"}, "30s")
	executionID := execName[strings.LastIndex(execName, "/")+1:]
	parent := "namespaces/" + cloudRunV1Namespace

	tasks, err := svc.Namespaces.Tasks.List(parent).
		LabelSelector("run.googleapis.com/execution=" + executionID).Do()
	require.NoError(t, err)
	require.Len(t, tasks.Items, 1)
	taskID := tasks.Items[0].Metadata.Name

	status, err := svc.Namespaces.Executions.Delete(parent + "/executions/" + executionID).Do()
	require.NoError(t, err)
	assert.Equal(t, "Success", status.Status)

	_, err = svc.Namespaces.Executions.Get(parent + "/executions/" + executionID).Do()
	require.Error(t, err)
	_, err = svc.Namespaces.Tasks.Get(parent + "/tasks/" + taskID).Do()
	require.Error(t, err, "deleting an execution removes the tasks it owns")
}

// TestCloudRunV2_JobSetIamPolicyDoesNotRunTheJob pins the v2 jobs colon-verb
// fan-in: setIamPolicy sets a policy. It must never fall through to RunJob and
// start a container.
func TestCloudRunV2_JobSetIamPolicyDoesNotRunTheJob(t *testing.T) {
	const id = "v2-iam-not-run"
	createCloudRunV2JobLRO(t, id)
	base := fmt.Sprintf("%s/v2/projects/%s/locations/us-central1/jobs/%s", baseURL, cloudRunV1Namespace, id)

	req, err := http.NewRequestWithContext(ctx, "POST", base+":setIamPolicy",
		strings.NewReader(`{"policy":{"bindings":[{"role":"roles/run.invoker","members":["user:v2-iam@example.com"]}]}}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var policy map[string]any
	require.NoError(t, json.Unmarshal(data, &policy))
	bindings, _ := policy["bindings"].([]any)
	require.Len(t, bindings, 1, "setIamPolicy answers with the policy it set")

	executions := getCloudRunV2JSON(t, fmt.Sprintf(
		"/v2/projects/%s/locations/us-central1/jobs/%s/executions", cloudRunV1Namespace, id))
	list, _ := executions["executions"].([]any)
	assert.Empty(t, list, "an IAM verb must not start an execution")
}

// TestCloudRunV2_JobRunValidateOnly pins that a validate-only RunJob starts no
// execution and reports no resource it did not create.
func TestCloudRunV2_JobRunValidateOnly(t *testing.T) {
	const id = "v2-run-validate-only"
	createCloudRunV2JobLRO(t, id)
	base := fmt.Sprintf("%s/v2/projects/%s/locations/us-central1/jobs/%s", baseURL, cloudRunV1Namespace, id)

	req, err := http.NewRequestWithContext(ctx, "POST", base+":run", strings.NewReader(`{"validateOnly":true}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var lro map[string]any
	require.NoError(t, json.Unmarshal(data, &lro))
	response, _ := lro["response"].(map[string]any)
	require.NotNil(t, response)
	_, hasName := response["name"]
	assert.False(t, hasName, "a validate-only run creates no execution to report")

	executions := getCloudRunV2JSON(t, fmt.Sprintf(
		"/v2/projects/%s/locations/us-central1/jobs/%s/executions", cloudRunV1Namespace, id))
	list, _ := executions["executions"].([]any)
	assert.Empty(t, list, "a validate-only run starts nothing")
}
