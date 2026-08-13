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

// Cloud Run Admin v1 (Knative) instances and worker pools, driven by the
// canonical google.golang.org/api/run/v1 client.
//
// An instance and a worker pool are each ONE resource with two API-version
// spellings. These tests deploy through the Knative surface and read the same
// record back through the v2 collection (and the reverse), which is what makes
// the projection load-bearing rather than decorative.

// getCloudRunV2JSON reads a v2 Cloud Run resource over the wire as a map, so
// the v1-created resource can be inspected in its v2 spelling.
func getCloudRunV2JSON(t *testing.T, path string) map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s", path)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func TestCloudRunV1_InstancesLifecycle(t *testing.T) {
	svc := newRunV1(t)
	const id = "v1-knative-instance"
	parent := "namespaces/" + cloudRunV1Namespace

	created, err := svc.Namespaces.Instances.Create(parent, &run.Instance{
		Metadata: &run.ObjectMeta{Name: id, Labels: map[string]string{"team": "platform"}},
		Spec: &run.InstanceSpec{
			Containers:         []*run.Container{{Image: "gcr.io/test-project/worker"}},
			ServiceAccountName: "runner@test-project.iam.gserviceaccount.com",
		},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Namespaces.Instances.Delete(parent + "/instances/" + id).Do()
	})
	assert.Equal(t, "run.googleapis.com/v1", created.ApiVersion)
	assert.Equal(t, "Instance", created.Kind)
	assert.Equal(t, id, created.Metadata.Name)
	assert.Equal(t, cloudRunV1Namespace, created.Metadata.Namespace)
	require.NotNil(t, created.Status)
	require.NotEmpty(t, created.Status.Urls)

	// The same resource, through the v2 collection.
	v2 := getCloudRunV2JSON(t, fmt.Sprintf("/v2/projects/%s/locations/us-central1/instances/%s", cloudRunV1Namespace, id))
	assert.Equal(t,
		fmt.Sprintf("projects/%s/locations/us-central1/instances/%s", cloudRunV1Namespace, id),
		v2["name"], "the Knative create writes the one v2-owned instance record")
	containers, _ := v2["containers"].([]any)
	require.Len(t, containers, 1)
	assert.Equal(t, "gcr.io/test-project/worker", containers[0].(map[string]any)["image"])

	got, err := svc.Namespaces.Instances.Get(parent + "/instances/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, "platform", got.Metadata.Labels["team"])
	require.NotNil(t, got.Spec)
	assert.Equal(t, "runner@test-project.iam.gserviceaccount.com", got.Spec.ServiceAccountName)

	list, err := svc.Namespaces.Instances.List(parent).LabelSelector("team=platform").Do()
	require.NoError(t, err)
	assert.Equal(t, "InstanceList", list.Kind)
	require.Len(t, list.Items, 1)
	assert.Equal(t, id, list.Items[0].Metadata.Name)

	replaced, err := svc.Namespaces.Instances.ReplaceInstance(parent+"/instances/"+id, &run.Instance{
		Metadata: &run.ObjectMeta{Name: id, Labels: map[string]string{"team": "infra"}},
		Spec: &run.InstanceSpec{
			Containers: []*run.Container{{Image: "gcr.io/test-project/worker:v2"}},
		},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "infra", replaced.Metadata.Labels["team"])
	assert.Equal(t, int64(2), replaced.Metadata.Generation, "ReplaceInstance advances the generation")
	require.Len(t, replaced.Spec.Containers, 1)
	assert.Equal(t, "gcr.io/test-project/worker:v2", replaced.Spec.Containers[0].Image)

	// stop/start flip the readiness condition — exactly what the v2
	// collection's StopInstance/StartInstance do.
	stopped, err := svc.Namespaces.Instances.Stop(parent+"/instances/"+id, &run.StopInstanceRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, stopped.Status.Conditions)
	assert.Equal(t, "Unknown", stopped.Status.Conditions[0].Status)
	assert.Equal(t, "Stopped", stopped.Status.Conditions[0].Reason)

	started, err := svc.Namespaces.Instances.Start(parent+"/instances/"+id, &run.StartInstanceRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, started.Status.Conditions)
	assert.Equal(t, "True", started.Status.Conditions[0].Status)

	status, err := svc.Namespaces.Instances.Delete(parent + "/instances/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, "Success", status.Status)

	_, err = svc.Namespaces.Instances.Get(parent + "/instances/" + id).Do()
	require.Error(t, err, "the deleted instance is gone from the one store both versions read")
}

// TestCloudRunV1_InstancesDryRun pins that dryRun=all validates without
// persisting, and that any other value is rejected rather than ignored.
func TestCloudRunV1_InstancesDryRun(t *testing.T) {
	svc := newRunV1(t)
	const id = "v1-dryrun-instance"
	parent := "namespaces/" + cloudRunV1Namespace

	created, err := svc.Namespaces.Instances.Create(parent, &run.Instance{
		Metadata: &run.ObjectMeta{Name: id},
		Spec:     &run.InstanceSpec{Containers: []*run.Container{{Image: "gcr.io/test-project/dry"}}},
	}).DryRun("all").Do()
	require.NoError(t, err)
	assert.Equal(t, id, created.Metadata.Name, "a dry run returns the resource it would have created")

	_, err = svc.Namespaces.Instances.Get(parent + "/instances/" + id).Do()
	require.Error(t, err, "a dry run persists nothing")

	_, err = svc.Namespaces.Instances.Create(parent, &run.Instance{
		Metadata: &run.ObjectMeta{Name: id},
		Spec:     &run.InstanceSpec{Containers: []*run.Container{{Image: "gcr.io/test-project/dry"}}},
	}).DryRun("some").Do()
	require.Error(t, err, "an unsupported dryRun value is rejected, never silently ignored")
}

func TestCloudRunV1_WorkerPoolsLifecycle(t *testing.T) {
	svc := newRunV1(t)
	const id = "v1-knative-pool"
	parent := "namespaces/" + cloudRunV1Namespace

	created, err := svc.Namespaces.Workerpools.Create(parent, &run.WorkerPool{
		Metadata: &run.ObjectMeta{Name: id, Labels: map[string]string{"tier": "batch"}},
		Spec: &run.WorkerPoolSpec{Template: &run.RevisionTemplate{
			Spec: &run.RevisionSpec{
				Containers:         []*run.Container{{Image: "gcr.io/test-project/pool"}},
				ServiceAccountName: "pool@test-project.iam.gserviceaccount.com",
			},
		}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Namespaces.Workerpools.Delete(parent + "/workerpools/" + id).Do()
	})
	assert.Equal(t, "run.googleapis.com/v1", created.ApiVersion)
	assert.Equal(t, "WorkerPool", created.Kind)
	require.NotNil(t, created.Status)
	assert.Equal(t, id+"-00001-abc", created.Status.LatestReadyRevisionName)
	require.Len(t, created.Status.InstanceSplits, 1)
	assert.True(t, created.Status.InstanceSplits[0].LatestRevision,
		"the v2 LATEST allocation type renders as the Knative latestRevision flag")

	// The same worker pool, through the v2 collection.
	v2 := getCloudRunV2JSON(t, fmt.Sprintf("/v2/projects/%s/locations/us-central1/workerPools/%s", cloudRunV1Namespace, id))
	assert.Equal(t,
		fmt.Sprintf("projects/%s/locations/us-central1/workerPools/%s", cloudRunV1Namespace, id),
		v2["name"])
	template, _ := v2["template"].(map[string]any)
	require.NotNil(t, template)
	assert.Equal(t, "pool@test-project.iam.gserviceaccount.com", template["serviceAccount"])

	// The revision the deploy materialized is readable over the v2 collection.
	revision := getCloudRunV2JSON(t, fmt.Sprintf(
		"/v2/projects/%s/locations/us-central1/workerPools/%s/revisions/%s-00001-abc", cloudRunV1Namespace, id, id))
	assert.Contains(t, revision["name"], id+"-00001-abc")

	got, err := svc.Namespaces.Workerpools.Get(parent + "/workerpools/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, "batch", got.Metadata.Labels["tier"])

	list, err := svc.Namespaces.Workerpools.List(parent).LabelSelector("tier=batch").Do()
	require.NoError(t, err)
	assert.Equal(t, "WorkerPoolList", list.Kind)
	require.Len(t, list.Items, 1)

	replaced, err := svc.Namespaces.Workerpools.ReplaceWorkerPool(parent+"/workerpools/"+id, &run.WorkerPool{
		Metadata: &run.ObjectMeta{Name: id, Labels: map[string]string{"tier": "batch2"}},
		Spec: &run.WorkerPoolSpec{Template: &run.RevisionTemplate{
			Spec: &run.RevisionSpec{Containers: []*run.Container{{Image: "gcr.io/test-project/pool:v2"}}},
		}},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), replaced.Metadata.Generation)
	assert.Equal(t, id+"-00002-abc", replaced.Status.LatestCreatedRevisionName)

	status, err := svc.Namespaces.Workerpools.Delete(parent + "/workerpools/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, "Success", status.Status)

	_, err = svc.Namespaces.Workerpools.Get(parent + "/workerpools/" + id).Do()
	require.Error(t, err)
}

// TestCloudRunV1_WorkerPoolFromV2IsVisible deploys through the v2 collection
// and reads the Knative spelling — the projection in the other direction.
func TestCloudRunV1_WorkerPoolFromV2IsVisible(t *testing.T) {
	svc := newRunV1(t)
	const id = "v2-then-v1-pool"
	body := `{"template":{"containers":[{"image":"gcr.io/test-project/v2pool"}]},"labels":{"origin":"v2"}}`
	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/v2/projects/%s/locations/us-central1/workerPools?workerPoolId=%s", baseURL, cloudRunV1Namespace, id),
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Cleanup(func() {
		_, _ = svc.Namespaces.Workerpools.Delete("namespaces/" + cloudRunV1Namespace + "/workerpools/" + id).Do()
	})

	got, err := svc.Namespaces.Workerpools.Get("namespaces/" + cloudRunV1Namespace + "/workerpools/" + id).Do()
	require.NoError(t, err)
	assert.Equal(t, id, got.Metadata.Name)
	assert.Equal(t, "v2", got.Metadata.Labels["origin"])
	require.NotNil(t, got.Spec)
	require.NotNil(t, got.Spec.Template)
	require.NotNil(t, got.Spec.Template.Spec)
	require.Len(t, got.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, "gcr.io/test-project/v2pool", got.Spec.Template.Spec.Containers[0].Image)
}
