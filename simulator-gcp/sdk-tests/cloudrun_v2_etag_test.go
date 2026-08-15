package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runv2 "google.golang.org/api/run/v2"
)

// The etag every Cloud Run v2 resource whose schema declares one reports, and
// the optimistic concurrency it buys. The shape is one contract across the
// family: a read carries the fingerprint of the version the store holds, any
// write mints a new one, a request naming the fingerprint the client last read
// proceeds, a request naming one the resource has moved past is a modification
// conflict answered ABORTED, and a request naming none is unconditional.
//
// Where the fingerprint travels differs by resource, and follows the document:
// Service, WorkerPool and Instance declare a writable etag, so it rides the
// update body and the start/stop bodies as well as the delete's query
// parameter; Revision, Execution and Task declare it output-only, so a client
// can only send it back on the delete.

// requireEtagRotates asserts that a write moved the resource's fingerprint on.
func requireEtagRotates(t *testing.T, before, after string) {
	t.Helper()
	require.NotEmpty(t, before, "the resource must report a fingerprint a client can send back")
	require.NotEmpty(t, after)
	require.NotEqual(t, before, after, "a write mints a new fingerprint")
}

// TestSDK_RunV2REST_Service_EtagOptimisticConcurrency covers Service and,
// through the revision each deploy materializes, Revision.
func TestSDK_RunV2REST_Service_EtagOptimisticConcurrency(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "etag-service"
	name := crV2Parent + "/services/" + id

	body := func(image string) *runv2.GoogleCloudRunV2Service {
		return &runv2.GoogleCloudRunV2Service{
			Template: &runv2.GoogleCloudRunV2RevisionTemplate{
				Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
			},
		}
	}

	op, err := svc.Projects.Locations.Services.Create(crV2Parent, body("alpine:latest")).ServiceId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)

	created, err := svc.Projects.Locations.Services.Get(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Etag, "the Service reports the fingerprint a client sends back")

	// A patch naming the current fingerprint proceeds and moves the service on.
	current := body("alpine:3.20")
	current.Etag = created.Etag
	patchOp, err := svc.Projects.Locations.Services.Patch(name, current).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	moved, err := svc.Projects.Locations.Services.Get(name).Do()
	require.NoError(t, err)
	requireEtagRotates(t, created.Etag, moved.Etag)

	// The etag the client still holds is now stale.
	stale := body("alpine:3.19")
	stale.Etag = created.Etag
	_, err = svc.Projects.Locations.Services.Patch(name, stale).Do()
	require.Error(t, err, "a stale etag must not silently overwrite the service")
	assert.Contains(t, err.Error(), "409")

	// Revisions are output-only-etag resources: the fingerprint comes back on a
	// read and goes out on the delete's query parameter.
	revs, err := svc.Projects.Locations.Services.Revisions.List(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, revs.Revisions)
	rev := revs.Revisions[0]
	require.NotEmpty(t, rev.Etag, "the Revision reports a fingerprint")

	_, err = svc.Projects.Locations.Services.Revisions.Delete(rev.Name).Etag("not-the-current-etag").Do()
	require.Error(t, err, "a stale etag must not silently delete the revision")
	assert.Contains(t, err.Error(), "409")

	delRev, err := svc.Projects.Locations.Services.Revisions.Delete(rev.Name).Etag(rev.Etag).Do()
	require.NoError(t, err, "the fingerprint the client read deletes the revision")
	awaitRunV2Operation(t, svc, delRev)

	// The service's own delete is conditional the same way.
	_, err = svc.Projects.Locations.Services.Delete(name).Etag(created.Etag).Do()
	require.Error(t, err, "a stale etag must not silently delete the service")
	assert.Contains(t, err.Error(), "409")

	delOp, err := svc.Projects.Locations.Services.Delete(name).Etag(moved.Etag).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, delOp)
}

// TestSDK_RunV2REST_WorkerPool_EtagOptimisticConcurrency covers WorkerPool and
// its revisions.
func TestSDK_RunV2REST_WorkerPool_EtagOptimisticConcurrency(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "etag-workerpool"
	name := crV2Parent + "/workerPools/" + id

	body := func(image string) *runv2.GoogleCloudRunV2WorkerPool {
		return &runv2.GoogleCloudRunV2WorkerPool{
			Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
				Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
			},
		}
	}

	op, err := svc.Projects.Locations.WorkerPools.Create(crV2Parent, body("alpine:latest")).WorkerPoolId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)

	created, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Etag)

	current := body("alpine:3.20")
	current.Etag = created.Etag
	patchOp, err := svc.Projects.Locations.WorkerPools.Patch(name, current).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	moved, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	requireEtagRotates(t, created.Etag, moved.Etag)

	stale := body("alpine:3.19")
	stale.Etag = created.Etag
	_, err = svc.Projects.Locations.WorkerPools.Patch(name, stale).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")

	revs, err := svc.Projects.Locations.WorkerPools.Revisions.List(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, revs.Revisions)
	rev := revs.Revisions[0]
	require.NotEmpty(t, rev.Etag)

	_, err = svc.Projects.Locations.WorkerPools.Revisions.Delete(rev.Name).Etag("not-the-current-etag").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")

	delRev, err := svc.Projects.Locations.WorkerPools.Revisions.Delete(rev.Name).Etag(rev.Etag).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, delRev)

	_, err = svc.Projects.Locations.WorkerPools.Delete(name).Etag(created.Etag).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")

	delOp, err := svc.Projects.Locations.WorkerPools.Delete(name).Etag(moved.Etag).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, delOp)
}

// TestSDK_RunV2REST_Instance_EtagOptimisticConcurrency covers Instance,
// including the etag StartInstanceRequest and StopInstanceRequest carry.
func TestSDK_RunV2REST_Instance_EtagOptimisticConcurrency(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "etag-instance"
	name := crV2Parent + "/instances/" + id

	body := func(image string) *runv2.GoogleCloudRunV2Instance {
		return &runv2.GoogleCloudRunV2Instance{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
		}
	}

	op, err := svc.Projects.Locations.Instances.Create(crV2Parent, body("alpine:latest")).InstanceId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)

	created, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Etag)

	// StopInstanceRequest carries the etag: the current one stops the
	// instance, and a stale one is a conflict.
	stopOp, err := svc.Projects.Locations.Instances.Stop(name,
		&runv2.GoogleCloudRunV2StopInstanceRequest{Etag: created.Etag}).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, stopOp)

	stopped, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	requireEtagRotates(t, created.Etag, stopped.Etag)

	_, err = svc.Projects.Locations.Instances.Start(name,
		&runv2.GoogleCloudRunV2StartInstanceRequest{Etag: created.Etag}).Do()
	require.Error(t, err, "a stale etag must not silently start the instance")
	assert.Contains(t, err.Error(), "409")

	startOp, err := svc.Projects.Locations.Instances.Start(name,
		&runv2.GoogleCloudRunV2StartInstanceRequest{Etag: stopped.Etag}).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, startOp)

	started, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	requireEtagRotates(t, stopped.Etag, started.Etag)

	// A request naming no etag is unconditional.
	unconditional, err := svc.Projects.Locations.Instances.Stop(name,
		&runv2.GoogleCloudRunV2StopInstanceRequest{}).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, unconditional)

	stale := body("alpine:3.19")
	stale.Etag = created.Etag
	_, err = svc.Projects.Locations.Instances.Patch(name, stale).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")

	latest, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)

	_, err = svc.Projects.Locations.Instances.Delete(name).Etag(created.Etag).Do()
	require.Error(t, err, "a stale etag must not silently delete the instance")
	assert.Contains(t, err.Error(), "409")

	delOp, err := svc.Projects.Locations.Instances.Delete(name).Etag(latest.Etag).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, delOp)
}

// TestSDK_RunV2REST_ExecutionTask_EtagOptimisticConcurrency covers Execution
// and Task — both output-only-etag resources — and the etag
// CancelExecutionRequest carries.
func TestSDK_RunV2REST_ExecutionTask_EtagOptimisticConcurrency(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "etag-exec-job"
	jobName := crV2Parent + "/jobs/" + id

	op, err := svc.Projects.Locations.Jobs.Create(crV2Parent, &runv2.GoogleCloudRunV2Job{
		Template: &runv2.GoogleCloudRunV2ExecutionTemplate{
			Template: &runv2.GoogleCloudRunV2TaskTemplate{
				Containers: []*runv2.GoogleCloudRunV2Container{{Image: "alpine:latest"}},
			},
		},
	}).JobId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		if delOp, err := svc.Projects.Locations.Jobs.Delete(jobName).Do(); err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	runOp, err := svc.Projects.Locations.Jobs.Run(jobName, &runv2.GoogleCloudRunV2RunJobRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, runOp.Name)

	execs, err := svc.Projects.Locations.Jobs.Executions.List(jobName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, execs.Executions)
	exec := execs.Executions[0]
	require.NotEmpty(t, exec.Etag, "the Execution reports a fingerprint")

	tasks, err := svc.Projects.Locations.Jobs.Executions.Tasks.List(exec.Name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, tasks.Tasks)
	require.NotEmpty(t, tasks.Tasks[0].Etag, "the Task reports a fingerprint")

	// CancelExecutionRequest carries an etag: a stale one is a conflict.
	_, err = svc.Projects.Locations.Jobs.Executions.Cancel(exec.Name,
		&runv2.GoogleCloudRunV2CancelExecutionRequest{Etag: "not-the-current-etag"}).Do()
	require.Error(t, err, "a stale etag must not silently cancel the execution")
	assert.Contains(t, err.Error(), "409")

	cancelOp, err := svc.Projects.Locations.Jobs.Executions.Cancel(exec.Name,
		&runv2.GoogleCloudRunV2CancelExecutionRequest{Etag: exec.Etag}).Do()
	require.NoError(t, err, "the fingerprint the client read cancels the execution")
	awaitRunV2Operation(t, svc, cancelOp)

	cancelled, err := svc.Projects.Locations.Jobs.Executions.Get(exec.Name).Do()
	require.NoError(t, err)
	requireEtagRotates(t, exec.Etag, cancelled.Etag)

	// The execution delete is conditional on the fingerprint too.
	_, err = svc.Projects.Locations.Jobs.Executions.Delete(exec.Name).Etag(exec.Etag).Do()
	require.Error(t, err, "a stale etag must not silently delete the execution")
	assert.Contains(t, err.Error(), "409")

	delOp, err := svc.Projects.Locations.Jobs.Executions.Delete(exec.Name).Etag(cancelled.Etag).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, delOp)
}
