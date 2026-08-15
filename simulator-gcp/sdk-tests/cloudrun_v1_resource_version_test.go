package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	run "google.golang.org/api/run/v1"
	runv2 "google.golang.org/api/run/v2"
)

// Optimistic concurrency on the Cloud Run Admin v1 (Knative) replace methods,
// driven by the canonical google.golang.org/api/run/v1 client.
//
// Each replace method documents the contract: "May provide
// metadata.resourceVersion to enforce update from last read for optimistic
// concurrency control." So the shape is one contract across the family — an
// omitted resourceVersion is unconditional, one matching the version the client
// last read proceeds, and one the resource has moved past is a modification
// conflict answered 409.
//
// resourceVersion is the v1 spelling of the fingerprint the v2 collections
// spell as etag, over the SAME record: a write through either API version moves
// the resource on, so a v1 replace holding a resourceVersion read before a v2
// patch is refused exactly as a v2 patch holding the pre-patch etag would be.

// requireRunV1Conflict asserts a replace was refused as a modification
// conflict rather than silently overwriting the resource.
func requireRunV1Conflict(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err, "a stale resourceVersion must not silently overwrite the resource")
	assert.Contains(t, err.Error(), "409")
	assert.Contains(t, err.Error(), "resourceVersion")
}

func TestCloudRunV1_Service_ResourceVersionOptimisticConcurrency(t *testing.T) {
	svc := newRunV1(t)
	const id = "rv-knative-service"
	parent := "namespaces/" + cloudRunV1Namespace
	name := parent + "/services/" + id

	body := func(image, resourceVersion string) *run.Service {
		return &run.Service{
			ApiVersion: "serving.knative.dev/v1",
			Kind:       "Service",
			Metadata:   &run.ObjectMeta{Name: id, ResourceVersion: resourceVersion},
			Spec: &run.ServiceSpec{
				Template: &run.RevisionTemplate{
					Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: image}}},
				},
			},
		}
	}

	created, err := svc.Namespaces.Services.Create(parent, body("alpine:latest", "")).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Namespaces.Services.Delete(name).Do() })
	require.NotEmpty(t, created.Metadata.ResourceVersion,
		"the Knative projection reports the version a client sends back")

	// A replace naming the version the client last read proceeds and moves the
	// service on.
	replaced, err := svc.Namespaces.Services.ReplaceService(name,
		body("alpine:3.20", created.Metadata.ResourceVersion)).Do()
	require.NoError(t, err)
	require.NotEqual(t, created.Metadata.ResourceVersion, replaced.Metadata.ResourceVersion,
		"a replace mints a new resourceVersion")

	// The version the client still holds is now stale.
	_, err = svc.Namespaces.Services.ReplaceService(name,
		body("alpine:3.19", created.Metadata.ResourceVersion)).Do()
	requireRunV1Conflict(t, err)

	// The service is untouched by the refused replace.
	after, err := svc.Namespaces.Services.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, replaced.Metadata.ResourceVersion, after.Metadata.ResourceVersion)
	assert.Equal(t, "alpine:3.20", after.Spec.Template.Spec.Containers[0].Image)

	// An omitted resourceVersion disables conflict detection, which is what the
	// ObjectMeta member's own description says it does.
	unconditional, err := svc.Namespaces.Services.ReplaceService(name, body("alpine:3.21", "")).Do()
	require.NoError(t, err)
	assert.Equal(t, "alpine:3.21", unconditional.Spec.Template.Spec.Containers[0].Image)
}

// TestCloudRunV1_Service_ResourceVersionTracksV2Writes proves the two spellings
// guard one record: a v2 patch moves the version a v1 client is holding, and
// the v1 replace refuses it afterwards.
func TestCloudRunV1_Service_ResourceVersionTracksV2Writes(t *testing.T) {
	v1 := newRunV1(t)
	v2 := newRunV2RESTService(t)
	const id = "rv-cross-version-service"
	parent := "namespaces/" + cloudRunV1Namespace
	name := parent + "/services/" + id

	created, err := v1.Namespaces.Services.Create(parent, &run.Service{
		ApiVersion: "serving.knative.dev/v1",
		Kind:       "Service",
		Metadata:   &run.ObjectMeta{Name: id},
		Spec: &run.ServiceSpec{
			Template: &run.RevisionTemplate{
				Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: "alpine:latest"}}},
			},
		},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = v1.Namespaces.Services.Delete(name).Do() })
	held := created.Metadata.ResourceVersion
	require.NotEmpty(t, held)

	v2Name := crV2Parent + "/services/" + id
	patchOp, err := v2.Projects.Locations.Services.Patch(v2Name, &runv2.GoogleCloudRunV2Service{
		Template: &runv2.GoogleCloudRunV2RevisionTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: "alpine:3.20"}},
		},
	}).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, v2, patchOp)

	moved, err := v1.Namespaces.Services.Get(name).Do()
	require.NoError(t, err)
	require.NotEqual(t, held, moved.Metadata.ResourceVersion,
		"a v2 write moves the version the v1 surface reports for the same record")

	_, err = v1.Namespaces.Services.ReplaceService(name, &run.Service{
		ApiVersion: "serving.knative.dev/v1",
		Kind:       "Service",
		Metadata:   &run.ObjectMeta{Name: id, ResourceVersion: held},
		Spec: &run.ServiceSpec{
			Template: &run.RevisionTemplate{
				Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: "alpine:3.19"}}},
			},
		},
	}).Do()
	requireRunV1Conflict(t, err)
}

// TestCloudRunV1_RegionalService_ResourceVersionOptimisticConcurrency covers
// the second replaceService spelling — the projects.locations path the
// regional endpoint publishes — over the same record.
func TestCloudRunV1_RegionalService_ResourceVersionOptimisticConcurrency(t *testing.T) {
	svc := newRunV1(t)
	const id = "rv-regional-service"
	parent := "projects/" + cloudRunV1Namespace + "/locations/us-central1"
	name := parent + "/services/" + id

	body := func(image, resourceVersion string) *run.Service {
		return &run.Service{
			ApiVersion: "serving.knative.dev/v1",
			Kind:       "Service",
			Metadata:   &run.ObjectMeta{Name: id, ResourceVersion: resourceVersion},
			Spec: &run.ServiceSpec{
				Template: &run.RevisionTemplate{
					Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: image}}},
				},
			},
		}
	}

	created, err := svc.Projects.Locations.Services.Create(parent, body("alpine:latest", "")).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Projects.Locations.Services.Delete(name).Do() })
	require.NotEmpty(t, created.Metadata.ResourceVersion)

	replaced, err := svc.Projects.Locations.Services.ReplaceService(name,
		body("alpine:3.20", created.Metadata.ResourceVersion)).Do()
	require.NoError(t, err)
	require.NotEqual(t, created.Metadata.ResourceVersion, replaced.Metadata.ResourceVersion)

	_, err = svc.Projects.Locations.Services.ReplaceService(name,
		body("alpine:3.19", created.Metadata.ResourceVersion)).Do()
	requireRunV1Conflict(t, err)
}

func TestCloudRunV1_Job_ResourceVersionOptimisticConcurrency(t *testing.T) {
	svc := newRunV1(t)
	const id = "rv-knative-job"
	parent := "namespaces/" + cloudRunV1Namespace
	name := parent + "/jobs/" + id

	body := func(image, resourceVersion string) *run.Job {
		return &run.Job{
			ApiVersion: "run.googleapis.com/v1",
			Kind:       "Job",
			Metadata:   &run.ObjectMeta{Name: id, ResourceVersion: resourceVersion},
			Spec: &run.JobSpec{
				Template: &run.ExecutionTemplateSpec{
					Spec: &run.ExecutionSpec{
						Template: &run.TaskTemplateSpec{
							Spec: &run.TaskSpec{Containers: []*run.Container{{Image: image}}},
						},
					},
				},
			},
		}
	}

	created, err := svc.Namespaces.Jobs.Create(parent, body("alpine:latest", "")).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Namespaces.Jobs.Delete(name).Do() })
	require.NotEmpty(t, created.Metadata.ResourceVersion)

	replaced, err := svc.Namespaces.Jobs.ReplaceJob(name,
		body("alpine:3.20", created.Metadata.ResourceVersion)).Do()
	require.NoError(t, err)
	require.NotEqual(t, created.Metadata.ResourceVersion, replaced.Metadata.ResourceVersion)

	_, err = svc.Namespaces.Jobs.ReplaceJob(name,
		body("alpine:3.19", created.Metadata.ResourceVersion)).Do()
	requireRunV1Conflict(t, err)

	unconditional, err := svc.Namespaces.Jobs.ReplaceJob(name, body("alpine:3.21", "")).Do()
	require.NoError(t, err)
	require.NotEqual(t, replaced.Metadata.ResourceVersion, unconditional.Metadata.ResourceVersion)
}

func TestCloudRunV1_Instance_ResourceVersionOptimisticConcurrency(t *testing.T) {
	svc := newRunV1(t)
	const id = "rv-knative-instance"
	parent := "namespaces/" + cloudRunV1Namespace
	name := parent + "/instances/" + id

	body := func(image, resourceVersion string) *run.Instance {
		return &run.Instance{
			ApiVersion: "run.googleapis.com/v1",
			Kind:       "Instance",
			Metadata:   &run.ObjectMeta{Name: id, ResourceVersion: resourceVersion},
			Spec:       &run.InstanceSpec{Containers: []*run.Container{{Image: image}}},
		}
	}

	created, err := svc.Namespaces.Instances.Create(parent, body("alpine:latest", "")).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Namespaces.Instances.Delete(name).Do() })
	require.NotEmpty(t, created.Metadata.ResourceVersion)

	replaced, err := svc.Namespaces.Instances.ReplaceInstance(name,
		body("alpine:3.20", created.Metadata.ResourceVersion)).Do()
	require.NoError(t, err)
	require.NotEqual(t, created.Metadata.ResourceVersion, replaced.Metadata.ResourceVersion)

	_, err = svc.Namespaces.Instances.ReplaceInstance(name,
		body("alpine:3.19", created.Metadata.ResourceVersion)).Do()
	requireRunV1Conflict(t, err)
}

func TestCloudRunV1_WorkerPool_ResourceVersionOptimisticConcurrency(t *testing.T) {
	svc := newRunV1(t)
	const id = "rv-knative-workerpool"
	parent := "namespaces/" + cloudRunV1Namespace
	name := parent + "/workerpools/" + id

	body := func(image, resourceVersion string) *run.WorkerPool {
		return &run.WorkerPool{
			ApiVersion: "run.googleapis.com/v1",
			Kind:       "WorkerPool",
			Metadata:   &run.ObjectMeta{Name: id, ResourceVersion: resourceVersion},
			Spec: &run.WorkerPoolSpec{
				Template: &run.RevisionTemplate{
					Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: image}}},
				},
			},
		}
	}

	created, err := svc.Namespaces.Workerpools.Create(parent, body("alpine:latest", "")).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Namespaces.Workerpools.Delete(name).Do() })
	require.NotEmpty(t, created.Metadata.ResourceVersion)

	replaced, err := svc.Namespaces.Workerpools.ReplaceWorkerPool(name,
		body("alpine:3.20", created.Metadata.ResourceVersion)).Do()
	require.NoError(t, err)
	require.NotEqual(t, created.Metadata.ResourceVersion, replaced.Metadata.ResourceVersion)

	_, err = svc.Namespaces.Workerpools.ReplaceWorkerPool(name,
		body("alpine:3.19", created.Metadata.ResourceVersion)).Do()
	requireRunV1Conflict(t, err)
}
