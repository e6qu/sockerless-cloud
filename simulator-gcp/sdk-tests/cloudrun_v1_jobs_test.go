package gcp_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	run "google.golang.org/api/run/v1"
)

// Cloud Run Admin v1 (Knative) Jobs-family SDK tests, driven by the canonical
// google.golang.org/api/run/v1 client.
//
// A Cloud Run job, its executions and its tasks are one resource each,
// addressable through two API versions. These tests create the resource
// through one version and read it back through the other, which is the whole
// point of the projection: there is no separate v1 store to diverge.

// cloudRunV1Namespace is the Knative namespace the jobs surface addresses —
// the project, at the simulator's single origin.
const cloudRunV1Namespace = "test-project"

// createCloudRunV2JobLRO creates a Cloud Run job over the v2 collection and
// returns the long-running operation name the create returned.
func createCloudRunV2JobLRO(t *testing.T, jobID string) string {
	t.Helper()
	body := `{"template":{"template":{"containers":[{"image":"alpine:latest"}]}}}`
	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/v2/projects/%s/locations/us-central1/jobs?jobId=%s", baseURL, cloudRunV1Namespace, jobID),
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var lro map[string]any
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &lro))
	t.Cleanup(func() {
		delReq, _ := http.NewRequestWithContext(ctx, "DELETE",
			fmt.Sprintf("%s/v2/projects/%s/locations/us-central1/jobs/%s", baseURL, cloudRunV1Namespace, jobID), nil)
		if delResp, err := http.DefaultClient.Do(delReq); err == nil {
			delResp.Body.Close()
		}
	})
	name, _ := lro["name"].(string)
	require.NotEmpty(t, name, "CreateJob returns a long-running operation")
	return name
}

// TestCloudRunV1_ExecutionAndTaskProjection runs a job over the v2 API and
// reads the execution and its task back through the Knative v1 surface.
func TestCloudRunV1_ExecutionAndTaskProjection(t *testing.T) {
	svc := newRunV1(t)
	const jobID = "v1-proj-job"
	execName := createAndRunJobWithImageAndCommand(t, jobID, "alpine:latest", []string{"true"}, "30s")
	execID := execName[strings.LastIndex(execName, "/")+1:]

	execution, err := svc.Namespaces.Executions.Get(
		"namespaces/" + cloudRunV1Namespace + "/executions/" + execID).Do()
	require.NoError(t, err)
	assert.Equal(t, "run.googleapis.com/v1", execution.ApiVersion)
	assert.Equal(t, "Execution", execution.Kind)
	require.NotNil(t, execution.Metadata)
	assert.Equal(t, execID, execution.Metadata.Name)
	assert.Equal(t, cloudRunV1Namespace, execution.Metadata.Namespace)
	assert.Equal(t, jobID, execution.Metadata.Labels["run.googleapis.com/job"],
		"the Knative execution names its owning job through the run.googleapis.com/job label")
	require.NotNil(t, execution.Spec)
	assert.Equal(t, int64(1), execution.Spec.TaskCount)
	assert.Equal(t, int64(1), execution.Spec.Parallelism)
	require.NotNil(t, execution.Spec.Template)
	require.NotNil(t, execution.Spec.Template.Spec)
	require.Len(t, execution.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, "alpine:latest", execution.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, int64(30), execution.Spec.Template.Spec.TimeoutSeconds,
		"the v2 google-duration timeout renders as the Knative int64 seconds count")
	require.NotNil(t, execution.Status)
	assert.NotEmpty(t, execution.Status.StartTime)

	// Executions list, narrowed to this job by the Knative label selector.
	list, err := svc.Namespaces.Executions.List("namespaces/" + cloudRunV1Namespace).
		LabelSelector("run.googleapis.com/job=" + jobID).Do()
	require.NoError(t, err)
	assert.Equal(t, "run.googleapis.com/v1", list.ApiVersion)
	assert.Equal(t, "ExecutionList", list.Kind)
	require.Len(t, list.Items, 1)
	assert.Equal(t, execID, list.Items[0].Metadata.Name)

	// A selector naming a different job matches nothing — the filter is real.
	empty, err := svc.Namespaces.Executions.List("namespaces/" + cloudRunV1Namespace).
		LabelSelector("run.googleapis.com/job=some-other-job").Do()
	require.NoError(t, err)
	assert.Empty(t, empty.Items)

	// Tasks: one per index, listed per namespace and narrowed by execution.
	tasks, err := svc.Namespaces.Tasks.List("namespaces/" + cloudRunV1Namespace).
		LabelSelector("run.googleapis.com/execution=" + execID).Do()
	require.NoError(t, err)
	assert.Equal(t, "TaskList", tasks.Kind)
	require.Len(t, tasks.Items, 1)
	task := tasks.Items[0]
	assert.Equal(t, jobID, task.Metadata.Labels["run.googleapis.com/job"])
	require.NotNil(t, task.Status)
	assert.Equal(t, int64(0), task.Status.Index)

	got, err := svc.Namespaces.Tasks.Get(
		"namespaces/" + cloudRunV1Namespace + "/tasks/" + task.Metadata.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "run.googleapis.com/v1", got.ApiVersion)
	assert.Equal(t, "Task", got.Kind)
	assert.Equal(t, task.Metadata.Name, got.Metadata.Name)
	require.NotNil(t, got.Spec)
	require.Len(t, got.Spec.Containers, 1)
	assert.Equal(t, "alpine:latest", got.Spec.Containers[0].Image)
}

// TestCloudRunV1_ExecutionAndTaskNotFound pins the answer for names that were
// never created: NOT_FOUND from a handler that looked, not a mux miss.
func TestCloudRunV1_ExecutionAndTaskNotFound(t *testing.T) {
	svc := newRunV1(t)

	_, err := svc.Namespaces.Executions.Get(
		"namespaces/" + cloudRunV1Namespace + "/executions/never-created").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never-created")

	_, err = svc.Namespaces.Tasks.Get(
		"namespaces/" + cloudRunV1Namespace + "/tasks/never-created").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never-created")
}

// TestCloudRunV1_ExecutionsListPaging drives the Knative limit/continue cursor.
func TestCloudRunV1_ExecutionsListPaging(t *testing.T) {
	svc := newRunV1(t)
	const jobID = "v1-paging-job"
	first := createAndRunJobWithImageAndCommand(t, jobID, "alpine:latest", []string{"true"}, "30s")
	require.NotEmpty(t, first)
	runURL := fmt.Sprintf("%s/v2/projects/%s/locations/us-central1/jobs/%s:run", baseURL, cloudRunV1Namespace, jobID)
	req, err := http.NewRequestWithContext(ctx, "POST", runURL, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	page, err := svc.Namespaces.Executions.List("namespaces/" + cloudRunV1Namespace).
		LabelSelector("run.googleapis.com/job=" + jobID).Limit(1).Do()
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.Metadata)
	require.NotEmpty(t, page.Metadata.Continue, "a truncated Knative list carries a continue cursor")

	rest, err := svc.Namespaces.Executions.List("namespaces/" + cloudRunV1Namespace).
		LabelSelector("run.googleapis.com/job=" + jobID).Continue(page.Metadata.Continue).Do()
	require.NoError(t, err)
	require.Len(t, rest.Items, 1)
	assert.NotEqual(t, page.Items[0].Metadata.Name, rest.Items[0].Metadata.Name)
}

// TestCloudRunV1_OperationsWait drives run.projects.locations.operations.wait
// against the operation record the v2 CreateJob returned. One operation store,
// two API-version spellings of the poll.
func TestCloudRunV1_OperationsWait(t *testing.T) {
	svc := newRunV1(t)
	opName := createCloudRunV2JobLRO(t, "v1-wait-job")

	waited, err := svc.Projects.Locations.Operations.Wait(opName, &run.GoogleLongrunningWaitOperationRequest{}).Do()
	require.NoError(t, err)
	assert.Equal(t, opName, waited.Name)
	assert.True(t, waited.Done, "the simulator settles a Cloud Run job create before returning it")

	_, err = svc.Projects.Locations.Operations.Wait(
		"projects/"+cloudRunV1Namespace+"/locations/us-central1/operations/never-created",
		&run.GoogleLongrunningWaitOperationRequest{}).Do()
	require.Error(t, err, "waiting on an operation that does not exist is NOT_FOUND")
}

// TestCloudRunV1_JobsGetIamPolicy drives run.projects.locations.jobs.getIamPolicy
// — the global-path IAM read `gcloud run jobs get-iam-policy` calls — and shows
// that it reads the policy the v2 collection writes.
func TestCloudRunV1_JobsGetIamPolicy(t *testing.T) {
	svc := newRunV1(t)
	const jobID = "v1-iam-job"
	createCloudRunV2JobLRO(t, jobID)
	resource := fmt.Sprintf("projects/%s/locations/us-central1/jobs/%s", cloudRunV1Namespace, jobID)

	initial, err := svc.Projects.Locations.Jobs.GetIamPolicy(resource).Do()
	require.NoError(t, err)
	assert.Empty(t, initial.Bindings, "a newly created job has no IAM bindings")
	assert.NotEmpty(t, initial.Etag, "getIamPolicy always returns an etag")

	set, err := svc.Projects.Locations.Jobs.SetIamPolicy(resource, &run.SetIamPolicyRequest{
		Policy: &run.Policy{Bindings: []*run.Binding{{
			Role:    "roles/run.invoker",
			Members: []string{"user:v1-jobs@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	after, err := svc.Projects.Locations.Jobs.GetIamPolicy(resource).Do()
	require.NoError(t, err)
	require.Len(t, after.Bindings, 1)
	assert.Equal(t, "roles/run.invoker", after.Bindings[0].Role)

	_, err = svc.Projects.Locations.Jobs.GetIamPolicy(
		fmt.Sprintf("projects/%s/locations/us-central1/jobs/never-created", cloudRunV1Namespace)).Do()
	require.Error(t, err, "an IAM read on a job that does not exist is NOT_FOUND, never an empty policy")
}
