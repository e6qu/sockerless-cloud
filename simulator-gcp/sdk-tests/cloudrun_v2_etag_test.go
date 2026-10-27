package gcp_sdk_test

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runv2 "google.golang.org/api/run/v2"
)

// Every Cloud Run v2 resource whose schema declares an etag holds one contract:
// a read carries the stored version's fingerprint, any write mints a new one, a
// request naming the fingerprint the client last read proceeds, one naming a
// fingerprint the resource has moved past is a conflict answered ABORTED, and
// one naming none is unconditional.
//
// Service, WorkerPool and Instance declare a writable etag, so it rides the
// update body, the start and stop bodies and the delete's query parameter;
// Revision, Execution and Task declare it output-only, so only the delete
// sends it back.

func requireEtagRotates(t *testing.T, before, after string) {
	t.Helper()
	require.NotEmpty(t, before, "the resource must report a fingerprint a client can send back")
	require.NotEmpty(t, after)
	require.NotEqual(t, before, after, "a write mints a new fingerprint")
}

func TestSDK_RunV2REST_Service_EtagOptimisticConcurrency(t *testing.T) {
	t.Parallel()
	svc := newRunV2RESTService(t)
	id := uniquename.New("etag-service")
	name := crV2Parent + "/services/" + id

	body := func(image string) *runv2.GoogleCloudRunV2Service {
		return &runv2.GoogleCloudRunV2Service{
			Template: &runv2.GoogleCloudRunV2RevisionTemplate{
				Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
			},
		}
	}

	op, err := svc.Projects.Locations.Services.Create(crV2Parent, body("mirror.gcr.io/library/alpine:latest")).ServiceId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)

	created, err := svc.Projects.Locations.Services.Get(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Etag, "the Service reports the fingerprint a client sends back")

	// A patch naming the current fingerprint proceeds and moves the service on.
	current := body("mirror.gcr.io/library/alpine:3.20")
	current.Etag = created.Etag
	patchOp, err := svc.Projects.Locations.Services.Patch(name, current).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	moved, err := svc.Projects.Locations.Services.Get(name).Do()
	require.NoError(t, err)
	requireEtagRotates(t, created.Etag, moved.Etag)

	// The etag the client still holds is now stale.
	stale := body("mirror.gcr.io/library/alpine:3.19")
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

func TestSDK_RunV2REST_WorkerPool_EtagOptimisticConcurrency(t *testing.T) {
	t.Parallel()
	svc := newRunV2RESTService(t)
	id := uniquename.New("etag-workerpool")
	name := crV2Parent + "/workerPools/" + id

	body := func(image string) *runv2.GoogleCloudRunV2WorkerPool {
		return &runv2.GoogleCloudRunV2WorkerPool{
			Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
				Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
			},
		}
	}

	op, err := svc.Projects.Locations.WorkerPools.Create(crV2Parent, body("mirror.gcr.io/library/alpine:latest")).WorkerPoolId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)

	created, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Etag)

	current := body("mirror.gcr.io/library/alpine:3.20")
	current.Etag = created.Etag
	patchOp, err := svc.Projects.Locations.WorkerPools.Patch(name, current).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	moved, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	requireEtagRotates(t, created.Etag, moved.Etag)

	stale := body("mirror.gcr.io/library/alpine:3.19")
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

func TestSDK_RunV2REST_Instance_EtagOptimisticConcurrency(t *testing.T) {
	t.Parallel()
	svc := newRunV2RESTService(t)
	id := uniquename.New("etag-instance")
	name := crV2Parent + "/instances/" + id

	body := func(image string) *runv2.GoogleCloudRunV2Instance {
		return &runv2.GoogleCloudRunV2Instance{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
		}
	}

	op, err := svc.Projects.Locations.Instances.Create(crV2Parent, &runv2.GoogleCloudRunV2Instance{
		Containers: []*runv2.GoogleCloudRunV2Container{servingContainerREST()},
	}).InstanceId(id).Do()
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

	stale := body("mirror.gcr.io/library/alpine:3.19")
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

func TestSDK_RunV2REST_ExecutionTask_EtagOptimisticConcurrency(t *testing.T) {
	t.Parallel()
	svc := newRunV2RESTService(t)
	id := uniquename.New("etag-exec-job")
	jobName := crV2Parent + "/jobs/" + id

	// The container holds for the whole test: an execution that finished between a
	// read and a write would rotate its fingerprint under the client.
	op, err := svc.Projects.Locations.Jobs.Create(crV2Parent, &runv2.GoogleCloudRunV2Job{
		Template: &runv2.GoogleCloudRunV2ExecutionTemplate{
			Template: &runv2.GoogleCloudRunV2TaskTemplate{
				Containers: []*runv2.GoogleCloudRunV2Container{{
					Image:   "mirror.gcr.io/library/alpine:latest",
					Command: []string{"sleep"},
					Args:    []string{"30"},
				}},
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
