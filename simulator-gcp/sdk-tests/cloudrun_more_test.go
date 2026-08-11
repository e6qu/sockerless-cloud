package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// Cloud Run v2 ratchet: worker pools, instances, job update, execution
// delete, and tasks — all driven through the canonical
// cloud.google.com/go/run/apiv2 REST clients (the same the cloudrun
// backend uses) so their path shapes and wire bodies stay pinned to the
// Discovery document.

func newWorkerPoolsClient(t *testing.T) *run.WorkerPoolsClient {
	t.Helper()
	client, err := run.NewWorkerPoolsRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func newInstancesClient(t *testing.T) *run.InstancesClient {
	t.Helper()
	client, err := run.NewInstancesRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func newTasksClient(t *testing.T) *run.TasksClient {
	t.Helper()
	client, err := run.NewTasksRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSDK_CloudRunV2_WorkerPools_RoundTrip(t *testing.T) {
	pools := newWorkerPoolsClient(t)
	revisions := newRevisionsClient(t)
	parent := "projects/test-project/locations/us-central1"
	const id = "v2-wp-roundtrip"
	name := parent + "/workerPools/" + id

	op, err := pools.CreateWorkerPool(ctx, &runpb.CreateWorkerPoolRequest{
		Parent:       parent,
		WorkerPoolId: id,
		WorkerPool: &runpb.WorkerPool{
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{{Image: "gcr.io/test-project/" + id}},
			},
		},
	})
	require.NoError(t, err)
	created, err := op.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, name, created.Name)
	t.Cleanup(func() {
		delOp, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: name})
		if err == nil {
			_, _ = delOp.Wait(ctx)
		}
	})

	got, err := pools.GetWorkerPool(ctx, &runpb.GetWorkerPoolRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	require.NotNil(t, got.Template)
	require.Len(t, got.Template.Containers, 1)
	assert.Equal(t, "gcr.io/test-project/"+id, got.Template.Containers[0].Image)

	// List finds it.
	it := pools.ListWorkerPools(ctx, &runpb.ListWorkerPoolsRequest{Parent: parent})
	var found bool
	for {
		wp, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if wp.Name == name {
			found = true
		}
	}
	assert.True(t, found, "ListWorkerPools must include the created pool")

	// Update bumps the generation and materializes a new revision.
	updOp, err := pools.UpdateWorkerPool(ctx, &runpb.UpdateWorkerPoolRequest{
		WorkerPool: &runpb.WorkerPool{
			Name: name,
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{{Image: "gcr.io/test-project/" + id + ":v2"}},
			},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"template"}},
	})
	require.NoError(t, err)
	updated, err := updOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Generation)

	// Worker-pool revisions are read back via the shared RevisionsClient.
	revIt := revisions.ListRevisions(ctx, &runpb.ListRevisionsRequest{Parent: name})
	var revNames []string
	for {
		rev, err := revIt.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		revNames = append(revNames, rev.Name)
	}
	require.GreaterOrEqual(t, len(revNames), 2, "create + update should yield two revisions")

	rev0, err := revisions.GetRevision(ctx, &runpb.GetRevisionRequest{Name: revNames[0]})
	require.NoError(t, err)
	assert.Equal(t, revNames[0], rev0.Name)

	delRevOp, err := revisions.DeleteRevision(ctx, &runpb.DeleteRevisionRequest{Name: revNames[0]})
	require.NoError(t, err)
	_, err = delRevOp.Wait(ctx)
	require.NoError(t, err)
}

func TestSDK_CloudRunV2_WorkerPools_IAM(t *testing.T) {
	pools := newWorkerPoolsClient(t)
	parent := "projects/test-project/locations/us-central1"
	const id = "v2-wp-iam"
	name := parent + "/workerPools/" + id

	op, err := pools.CreateWorkerPool(ctx, &runpb.CreateWorkerPoolRequest{
		Parent:       parent,
		WorkerPoolId: id,
		WorkerPool: &runpb.WorkerPool{
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{{Image: "gcr.io/test-project/" + id}},
			},
		},
	})
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		delOp, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: name})
		if err == nil {
			_, _ = delOp.Wait(ctx)
		}
	})

	_, err = pools.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: name,
		Policy: &iampb.Policy{
			Bindings: []*iampb.Binding{{Role: "roles/run.invoker", Members: []string{"user:dev@example.com"}}},
		},
	})
	require.NoError(t, err)

	pol, err := pools.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: name})
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
	assert.Equal(t, "roles/run.invoker", pol.Bindings[0].Role)

	perms, err := pools.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
		Resource:    name,
		Permissions: []string{"run.workerpools.get"},
	})
	require.NoError(t, err)
	assert.Contains(t, perms.Permissions, "run.workerpools.get")
}

func TestSDK_CloudRunV2_Instances_RoundTrip(t *testing.T) {
	inst := newInstancesClient(t)
	parent := "projects/test-project/locations/us-central1"
	const id = "v2-inst-roundtrip"
	name := parent + "/instances/" + id

	op, err := inst.CreateInstance(ctx, &runpb.CreateInstanceRequest{
		Parent:     parent,
		InstanceId: id,
		Instance: &runpb.Instance{
			Containers: []*runpb.Container{{Image: "gcr.io/test-project/" + id}},
		},
	})
	require.NoError(t, err)
	created, err := op.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, name, created.Name)
	t.Cleanup(func() {
		delOp, err := inst.DeleteInstance(ctx, &runpb.DeleteInstanceRequest{Name: name})
		if err == nil {
			_, _ = delOp.Wait(ctx)
		}
	})

	got, err := inst.GetInstance(ctx, &runpb.GetInstanceRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	require.Len(t, got.Containers, 1)

	it := inst.ListInstances(ctx, &runpb.ListInstancesRequest{Parent: parent})
	var found bool
	for {
		row, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if row.Name == name {
			found = true
		}
	}
	assert.True(t, found, "ListInstances must include the created instance")

	stopOp, err := inst.StopInstance(ctx, &runpb.StopInstanceRequest{Name: name})
	require.NoError(t, err)
	_, err = stopOp.Wait(ctx)
	require.NoError(t, err)

	startOp, err := inst.StartInstance(ctx, &runpb.StartInstanceRequest{Name: name})
	require.NoError(t, err)
	started, err := startOp.Wait(ctx)
	require.NoError(t, err)
	require.NotNil(t, started.TerminalCondition)
	assert.Equal(t, "Ready", started.TerminalCondition.Type)
}

func TestSDK_CloudRunV2_Instances_IAM(t *testing.T) {
	inst := newInstancesClient(t)
	parent := "projects/test-project/locations/us-central1"
	const id = "v2-inst-iam"
	name := parent + "/instances/" + id

	op, err := inst.CreateInstance(ctx, &runpb.CreateInstanceRequest{
		Parent:     parent,
		InstanceId: id,
		Instance: &runpb.Instance{
			Containers: []*runpb.Container{{Image: "gcr.io/test-project/" + id}},
		},
	})
	require.NoError(t, err)
	_, err = op.Wait(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		delOp, err := inst.DeleteInstance(ctx, &runpb.DeleteInstanceRequest{Name: name})
		if err == nil {
			_, _ = delOp.Wait(ctx)
		}
	})

	// The run/apiv2 InstancesClient does not wrap the instance IAM verbs,
	// so they are exercised over the raw v2 wire paths the Discovery
	// document defines (the same shapes a generic IAM client emits).
	setBody := `{"policy":{"bindings":[{"role":"roles/run.invoker","members":["user:dev@example.com"]}]}}`
	setResp, err := http.Post(baseURL+"/v2/"+name+":setIamPolicy", "application/json", strings.NewReader(setBody))
	require.NoError(t, err)
	defer setResp.Body.Close()
	require.Equal(t, http.StatusOK, setResp.StatusCode)

	getResp, err := http.Get(baseURL + "/v2/" + name + ":getIamPolicy")
	require.NoError(t, err)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	var pol struct {
		Bindings []struct {
			Role string `json:"role"`
		} `json:"bindings"`
	}
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&pol))
	require.Len(t, pol.Bindings, 1)
	assert.Equal(t, "roles/run.invoker", pol.Bindings[0].Role)

	testResp, err := http.Post(baseURL+"/v2/"+name+":testIamPermissions", "application/json",
		strings.NewReader(`{"permissions":["run.instances.get"]}`))
	require.NoError(t, err)
	defer testResp.Body.Close()
	require.Equal(t, http.StatusOK, testResp.StatusCode)
	var perms struct {
		Permissions []string `json:"permissions"`
	}
	require.NoError(t, json.NewDecoder(testResp.Body).Decode(&perms))
	assert.Contains(t, perms.Permissions, "run.instances.get")
}

func TestSDK_CloudRunV2_JobUpdate_And_ExecutionTasks(t *testing.T) {
	jobs := newJobsClient(t)
	execs := newExecutionsClient(t)
	tasks := newTasksClient(t)
	parent := "projects/test-project/locations/us-central1"
	const id = "v2-job-update-tasks"
	jobName := parent + "/jobs/" + id

	op, err := jobs.CreateJob(ctx, &runpb.CreateJobRequest{
		Parent: parent,
		JobId:  id,
		Job: &runpb.Job{
			Template: &runpb.ExecutionTemplate{
				TaskCount: 2,
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
		delOp, err := jobs.DeleteJob(ctx, &runpb.DeleteJobRequest{Name: jobName})
		if err == nil {
			_, _ = delOp.Wait(ctx)
		}
	})

	// UpdateJob bumps the generation.
	updOp, err := jobs.UpdateJob(ctx, &runpb.UpdateJobRequest{
		Job: &runpb.Job{
			Name:   jobName,
			Labels: map[string]string{"team": "ratchet"},
			Template: &runpb.ExecutionTemplate{
				TaskCount: 3,
				Template: &runpb.TaskTemplate{
					Containers: []*runpb.Container{{Image: "alpine:3.20"}},
				},
			},
		},
	})
	require.NoError(t, err)
	updated, err := updOp.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Generation)
	assert.Equal(t, "ratchet", updated.Labels["team"])

	// Run an execution; the sim materializes one task per index.
	runOp, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: jobName})
	require.NoError(t, err)
	exec, err := runOp.Wait(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, exec.Name)

	// ListTasks returns the execution's tasks.
	it := tasks.ListTasks(ctx, &runpb.ListTasksRequest{Parent: exec.Name})
	var taskNames []string
	for {
		tk, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		taskNames = append(taskNames, tk.Name)
	}
	require.Equal(t, 3, len(taskNames), "task count from the updated template")

	tk0, err := tasks.GetTask(ctx, &runpb.GetTaskRequest{Name: taskNames[0]})
	require.NoError(t, err)
	assert.Equal(t, taskNames[0], tk0.Name)
	assert.Equal(t, exec.Name, tk0.Execution)

	// DeleteExecution removes the execution (and its tasks).
	delExecOp, err := execs.DeleteExecution(ctx, &runpb.DeleteExecutionRequest{Name: exec.Name})
	require.NoError(t, err)
	_, err = delExecOp.Wait(ctx)
	require.NoError(t, err)

	_, err = execs.GetExecution(ctx, &runpb.GetExecutionRequest{Name: exec.Name})
	require.Error(t, err, "execution must be gone after delete")
}
