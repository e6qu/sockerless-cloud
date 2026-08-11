package gcp_sdk_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	runv1 "google.golang.org/api/run/v1"
	runv2 "google.golang.org/api/run/v2"
)

// Cloud Run v2 Worker Pools and Instances driven through the official
// Discovery REST clients (google.golang.org/api/run/v2 and /run/v1). The
// generated clients emit the exact method URIs the Discovery document
// declares — including the ":verb" IAM spellings and the PATCH update
// paths — so every registered worker-pool and instance route is pinned to
// its documented wire shape. The gRPC-transcoding client
// (cloud.google.com/go/run/apiv2) covers the same resources from the other
// canonical client; both must agree.

const (
	crV2Project  = "test-project"
	crV2Location = "us-central1"
	crV2Parent   = "projects/" + crV2Project + "/locations/" + crV2Location
)

func newRunV2RESTService(t *testing.T) *runv2.Service {
	t.Helper()
	svc, err := runv2.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func newRunV1RESTService(t *testing.T) *runv1.APIService {
	t.Helper()
	svc, err := runv1.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// awaitRunV2Operation re-reads a long-running operation through the real
// projects.locations.operations.get method and returns it once done. Cloud
// Run's LROs are addressed by their own resource name, so this is the poll
// a client performs after any create/patch/delete — never a local shortcut
// around the returned operation.
func awaitRunV2Operation(t *testing.T, svc *runv2.Service, op *runv2.GoogleLongrunningOperation) *runv2.GoogleLongrunningOperation {
	t.Helper()
	require.NotEmpty(t, op.Name, "operation must carry a resource name to poll")
	for i := 0; i < 30; i++ {
		got, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
		require.NoError(t, err)
		if got.Done {
			require.Nil(t, got.Error, "operation must not fail")
			return got
		}
	}
	t.Fatalf("operation %s never reached done", op.Name)
	return nil
}

// lroResponseName decodes the resource name out of an operation's
// google.protobuf.Any response payload.
func lroResponseName(t *testing.T, op *runv2.GoogleLongrunningOperation) string {
	t.Helper()
	var resource struct {
		Type string `json:"@type"`
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(op.Response, &resource))
	assert.NotEmpty(t, resource.Type, "an Any-typed LRO response carries @type")
	return resource.Name
}

func TestSDK_RunV2REST_WorkerPools_FullSurface(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "rest-wp-surface"
	name := crV2Parent + "/workerPools/" + id

	// Create → LRO polled through operations.get.
	op, err := svc.Projects.Locations.WorkerPools.Create(crV2Parent, &runv2.GoogleCloudRunV2WorkerPool{
		Description: "worker pool under REST-client test",
		Labels:      map[string]string{"suite": "rest"},
		Scaling:     &runv2.GoogleCloudRunV2WorkerPoolScaling{ManualInstanceCount: 2},
		Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + id}},
		},
	}).WorkerPoolId(id).Do()
	require.NoError(t, err)
	done := awaitRunV2Operation(t, svc, op)
	require.NotEmpty(t, done.Response, "create operation must carry the created worker pool")
	assert.Equal(t, name, lroResponseName(t, done))

	t.Cleanup(func() {
		delOp, err := svc.Projects.Locations.WorkerPools.Delete(name).Do()
		if err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	// Get returns every field the create body carried plus the server-set ones.
	got, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, "worker pool under REST-client test", got.Description)
	assert.Equal(t, "rest", got.Labels["suite"])
	assert.Equal(t, int64(1), got.Generation)
	assert.NotEmpty(t, got.Uid)
	assert.NotEmpty(t, got.CreateTime)
	require.NotNil(t, got.Scaling)
	assert.Equal(t, int64(2), got.Scaling.ManualInstanceCount)
	require.NotNil(t, got.TerminalCondition)
	assert.Equal(t, "CONDITION_SUCCEEDED", got.TerminalCondition.State)
	require.NotEmpty(t, got.InstanceSplitStatuses, "a ready worker pool reports its instance split")
	assert.Equal(t, int64(100), got.InstanceSplitStatuses[0].Percent)
	assert.Equal(t, name+"/revisions/"+id+"-00001-abc", got.LatestReadyRevision)

	// List.
	list, err := svc.Projects.Locations.WorkerPools.List(crV2Parent).Do()
	require.NoError(t, err)
	var found bool
	for _, wp := range list.WorkerPools {
		if wp.Name == name {
			found = true
		}
	}
	assert.True(t, found, "ListWorkerPools must include the created pool")

	// Patch with an updateMask — the masked field changes, the rest survives.
	patchOp, err := svc.Projects.Locations.WorkerPools.Patch(name, &runv2.GoogleCloudRunV2WorkerPool{
		Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + id + ":v2"}},
		},
	}).UpdateMask("template").Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	patched, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), patched.Generation)
	require.NotNil(t, patched.Template)
	require.Len(t, patched.Template.Containers, 1)
	assert.Equal(t, "gcr.io/"+crV2Project+"/"+id+":v2", patched.Template.Containers[0].Image)
	assert.Equal(t, "rest", patched.Labels["suite"], "an unmasked field must survive the patch")
	require.NotNil(t, patched.Scaling)
	assert.Equal(t, int64(2), patched.Scaling.ManualInstanceCount, "an unmasked field must survive the patch")

	// Revisions: list, get, delete — one per deploy.
	revs, err := svc.Projects.Locations.WorkerPools.Revisions.List(name).Do()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(revs.Revisions), 2, "create + patch each materialize a revision")

	rev, err := svc.Projects.Locations.WorkerPools.Revisions.Get(revs.Revisions[0].Name).Do()
	require.NoError(t, err)
	assert.Equal(t, revs.Revisions[0].Name, rev.Name)

	delRevOp, err := svc.Projects.Locations.WorkerPools.Revisions.Delete(revs.Revisions[0].Name).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, delRevOp)

	_, err = svc.Projects.Locations.WorkerPools.Revisions.Get(revs.Revisions[0].Name).Do()
	require.Error(t, err, "a deleted worker-pool revision must not read back")
}

func TestSDK_RunV2REST_WorkerPools_IAMVerbs(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "rest-wp-iam"
	name := crV2Parent + "/workerPools/" + id

	op, err := svc.Projects.Locations.WorkerPools.Create(crV2Parent, &runv2.GoogleCloudRunV2WorkerPool{
		Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + id}},
		},
	}).WorkerPoolId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		delOp, err := svc.Projects.Locations.WorkerPools.Delete(name).Do()
		if err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	set, err := svc.Projects.Locations.WorkerPools.SetIamPolicy(name, &runv2.GoogleIamV1SetIamPolicyRequest{
		Policy: &runv2.GoogleIamV1Policy{
			Bindings: []*runv2.GoogleIamV1Binding{{
				Role:    "roles/run.invoker",
				Members: []string{"user:rest@example.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	assert.NotEmpty(t, set.Etag, "setIamPolicy must return the new etag")

	pol, err := svc.Projects.Locations.WorkerPools.GetIamPolicy(name).Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
	assert.Equal(t, "roles/run.invoker", pol.Bindings[0].Role)
	assert.Equal(t, []string{"user:rest@example.com"}, pol.Bindings[0].Members)

	perms, err := svc.Projects.Locations.WorkerPools.TestIamPermissions(name,
		&runv2.GoogleIamV1TestIamPermissionsRequest{
			Permissions: []string{"run.workerpools.get", "run.workerpools.update"},
		}).Do()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"run.workerpools.get", "run.workerpools.update"}, perms.Permissions)
}

// TestSDK_RunV1REST_WorkerPools_IAMVerbs exercises the Cloud Run Admin v1
// spelling of the same worker pool's IAM triple
// (run.projects.locations.workerpools.*, lowercase collection). It is the
// surface the gcloud CLI calls, and it must address the same resource the v2
// methods do: a policy written through v1 reads back through v2.
func TestSDK_RunV1REST_WorkerPools_IAMVerbs(t *testing.T) {
	v2svc := newRunV2RESTService(t)
	v1svc := newRunV1RESTService(t)
	const id = "rest-wp-v1-iam"
	v2Name := crV2Parent + "/workerPools/" + id
	v1Name := crV2Parent + "/workerpools/" + id

	op, err := v2svc.Projects.Locations.WorkerPools.Create(crV2Parent, &runv2.GoogleCloudRunV2WorkerPool{
		Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + id}},
		},
	}).WorkerPoolId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, v2svc, op)
	t.Cleanup(func() {
		delOp, err := v2svc.Projects.Locations.WorkerPools.Delete(v2Name).Do()
		if err == nil {
			awaitRunV2Operation(t, v2svc, delOp)
		}
	})

	set, err := v1svc.Projects.Locations.Workerpools.SetIamPolicy(v1Name, &runv1.SetIamPolicyRequest{
		Policy: &runv1.Policy{
			Bindings: []*runv1.Binding{{
				Role:    "roles/run.developer",
				Members: []string{"user:v1@example.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	v1Pol, err := v1svc.Projects.Locations.Workerpools.GetIamPolicy(v1Name).Do()
	require.NoError(t, err)
	require.Len(t, v1Pol.Bindings, 1)
	assert.Equal(t, "roles/run.developer", v1Pol.Bindings[0].Role)

	// The v1 and v2 collections address one resource, so the v2 read sees it.
	v2Pol, err := v2svc.Projects.Locations.WorkerPools.GetIamPolicy(v2Name).Do()
	require.NoError(t, err)
	require.Len(t, v2Pol.Bindings, 1)
	assert.Equal(t, "roles/run.developer", v2Pol.Bindings[0].Role)
	assert.Equal(t, v1Pol.Etag, v2Pol.Etag, "one resource, one policy etag across API versions")

	perms, err := v1svc.Projects.Locations.Workerpools.TestIamPermissions(v1Name,
		&runv1.TestIamPermissionsRequest{Permissions: []string{"run.workerpools.get"}}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"run.workerpools.get"}, perms.Permissions)
}

func TestSDK_RunV2REST_Instances_FullSurface(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "rest-inst-surface"
	name := crV2Parent + "/instances/" + id

	op, err := svc.Projects.Locations.Instances.Create(crV2Parent, &runv2.GoogleCloudRunV2Instance{
		Description: "instance under REST-client test",
		Labels:      map[string]string{"suite": "rest"},
		Containers:  []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + id}},
	}).InstanceId(id).Do()
	require.NoError(t, err)
	done := awaitRunV2Operation(t, svc, op)
	require.NotEmpty(t, done.Response)
	assert.Equal(t, name, lroResponseName(t, done))

	t.Cleanup(func() {
		delOp, err := svc.Projects.Locations.Instances.Delete(name).Do()
		if err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	got, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, "instance under REST-client test", got.Description)
	assert.NotEmpty(t, got.Uid)
	assert.Equal(t, int64(1), got.Generation)
	require.Len(t, got.Containers, 1)
	require.NotEmpty(t, got.Urls, "an instance without defaultUriDisabled advertises its URL")
	require.NotNil(t, got.TerminalCondition)
	assert.Equal(t, "CONDITION_SUCCEEDED", got.TerminalCondition.State)

	list, err := svc.Projects.Locations.Instances.List(crV2Parent).Do()
	require.NoError(t, err)
	var found bool
	for _, in := range list.Instances {
		if in.Name == name {
			found = true
		}
	}
	assert.True(t, found, "ListInstances must include the created instance")

	// Patch with an updateMask.
	patchOp, err := svc.Projects.Locations.Instances.Patch(name, &runv2.GoogleCloudRunV2Instance{
		Containers: []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + id + ":v2"}},
	}).UpdateMask("containers").Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	patched, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), patched.Generation)
	require.Len(t, patched.Containers, 1)
	assert.Equal(t, "gcr.io/"+crV2Project+"/"+id+":v2", patched.Containers[0].Image)
	assert.Equal(t, "rest", patched.Labels["suite"], "an unmasked field must survive the patch")
	assert.Equal(t, got.Urls, patched.Urls, "the instance keeps its URL across a patch")

	// Stop drives the terminal condition away from ready; start restores it.
	stopOp, err := svc.Projects.Locations.Instances.Stop(name, &runv2.GoogleCloudRunV2StopInstanceRequest{}).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, stopOp)
	stopped, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, stopped.TerminalCondition)
	assert.Equal(t, "CONDITION_PENDING", stopped.TerminalCondition.State)
	assert.Equal(t, "Stopped", stopped.TerminalCondition.Reason)

	startOp, err := svc.Projects.Locations.Instances.Start(name, &runv2.GoogleCloudRunV2StartInstanceRequest{}).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, startOp)
	started, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, started.TerminalCondition)
	assert.Equal(t, "CONDITION_SUCCEEDED", started.TerminalCondition.State)
}

func TestSDK_RunV2REST_Instances_IAMVerbs(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "rest-inst-iam"
	name := crV2Parent + "/instances/" + id

	op, err := svc.Projects.Locations.Instances.Create(crV2Parent, &runv2.GoogleCloudRunV2Instance{
		Containers: []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + id}},
	}).InstanceId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		delOp, err := svc.Projects.Locations.Instances.Delete(name).Do()
		if err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	set, err := svc.Projects.Locations.Instances.SetIamPolicy(name, &runv2.GoogleIamV1SetIamPolicyRequest{
		Policy: &runv2.GoogleIamV1Policy{
			Bindings: []*runv2.GoogleIamV1Binding{{
				Role:    "roles/run.invoker",
				Members: []string{"serviceAccount:runner@test-project.iam.gserviceaccount.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	pol, err := svc.Projects.Locations.Instances.GetIamPolicy(name).Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
	assert.Equal(t, "roles/run.invoker", pol.Bindings[0].Role)

	perms, err := svc.Projects.Locations.Instances.TestIamPermissions(name,
		&runv2.GoogleIamV1TestIamPermissionsRequest{
			Permissions: []string{"run.instances.get"},
		}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"run.instances.get"}, perms.Permissions)
}

// TestSDK_RunV2REST_WorkerPool_FullBodyRoundTrip sends every writable field of
// a WorkerPool and its revision template and requires each one back. A field
// the simulator silently drops is invisible to a create/get smoke test but
// makes terraform re-plan the resource forever, so the round-trip is asserted
// field by field.
func TestSDK_RunV2REST_WorkerPool_FullBodyRoundTrip(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "rest-wp-full-body"
	name := crV2Parent + "/workerPools/" + id

	want := &runv2.GoogleCloudRunV2WorkerPool{
		Description:     "every writable field",
		Labels:          map[string]string{"team": "platform"},
		Annotations:     map[string]string{"example.com/owner": "platform"},
		LaunchStage:     "GA",
		Client:          "terraform",
		ClientVersion:   "7.36.0",
		CustomAudiences: []string{"https://worker.example.com"},
		BinaryAuthorization: &runv2.GoogleCloudRunV2BinaryAuthorization{
			UseDefault:              true,
			BreakglassJustification: "coverage",
		},
		Scaling: &runv2.GoogleCloudRunV2WorkerPoolScaling{ManualInstanceCount: 3},
		Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
			Labels:         map[string]string{"revision_tier": "worker"},
			Annotations:    map[string]string{"example.com/rev": "1"},
			ServiceAccount: "worker@test-project.iam.gserviceaccount.com",
			EncryptionKey:  "projects/test-project/locations/us-central1/keyRings/kr/cryptoKeys/ck",
			ServiceMesh:    &runv2.GoogleCloudRunV2ServiceMesh{Mesh: "projects/test-project/locations/global/meshes/m"},
			NodeSelector:   &runv2.GoogleCloudRunV2NodeSelector{Accelerator: "nvidia-l4"},
			Containers: []*runv2.GoogleCloudRunV2Container{
				{
					Name:       "worker",
					Image:      "gcr.io/test-project/" + id,
					Command:    []string{"/worker"},
					Args:       []string{"--mode", "pull"},
					WorkingDir: "/srv",
					Env:        []*runv2.GoogleCloudRunV2EnvVar{{Name: "MODE", Value: "pull"}},
					Resources: &runv2.GoogleCloudRunV2ResourceRequirements{
						Limits:  map[string]string{"cpu": "1000m", "memory": "512Mi"},
						CpuIdle: true,
					},
					VolumeMounts: []*runv2.GoogleCloudRunV2VolumeMount{
						{Name: "scratch", MountPath: "/scratch", SubPath: "inner"},
						{Name: "creds", MountPath: "/creds"},
						{Name: "sql", MountPath: "/cloudsql"},
					},
				},
				{
					Name:      "sidecar",
					Image:     "gcr.io/test-project/" + id + "-sidecar",
					DependsOn: []string{"worker"},
				},
			},
			Volumes: []*runv2.GoogleCloudRunV2Volume{
				{
					Name:     "scratch",
					EmptyDir: &runv2.GoogleCloudRunV2EmptyDirVolumeSource{Medium: "MEMORY", SizeLimit: "128Mi"},
				},
				{
					Name: "creds",
					Secret: &runv2.GoogleCloudRunV2SecretVolumeSource{
						Secret:      "projects/test-project/secrets/tf-secret",
						DefaultMode: 292,
						Items: []*runv2.GoogleCloudRunV2VersionToPath{
							{Path: "creds.json", Version: "1", Mode: 256},
						},
					},
				},
				{
					Name: "sql",
					CloudSqlInstance: &runv2.GoogleCloudRunV2CloudSqlInstance{
						Instances: []string{"test-project:us-central1:db"},
					},
				},
			},
			VpcAccess: &runv2.GoogleCloudRunV2VpcAccess{
				Egress: "ALL_TRAFFIC",
				NetworkInterfaces: []*runv2.GoogleCloudRunV2NetworkInterface{
					{Network: "default", Subnetwork: "default", Tags: []string{"worker"}},
				},
			},
		},
	}

	op, err := svc.Projects.Locations.WorkerPools.Create(crV2Parent, want).WorkerPoolId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		delOp, err := svc.Projects.Locations.WorkerPools.Delete(name).Do()
		if err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	got, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)

	assert.Equal(t, want.Description, got.Description)
	assert.Equal(t, want.Labels, got.Labels)
	assert.Equal(t, want.Annotations, got.Annotations)
	assert.Equal(t, want.Client, got.Client)
	assert.Equal(t, want.ClientVersion, got.ClientVersion)
	assert.Equal(t, want.CustomAudiences, got.CustomAudiences)
	require.NotNil(t, got.BinaryAuthorization)
	assert.True(t, got.BinaryAuthorization.UseDefault)
	assert.Equal(t, "coverage", got.BinaryAuthorization.BreakglassJustification)
	require.NotNil(t, got.Scaling)
	assert.Equal(t, int64(3), got.Scaling.ManualInstanceCount)

	tmpl := got.Template
	require.NotNil(t, tmpl)
	assert.Equal(t, want.Template.Labels, tmpl.Labels)
	assert.Equal(t, want.Template.Annotations, tmpl.Annotations)
	assert.Equal(t, want.Template.ServiceAccount, tmpl.ServiceAccount)
	assert.Equal(t, want.Template.EncryptionKey, tmpl.EncryptionKey)
	require.NotNil(t, tmpl.ServiceMesh)
	assert.Equal(t, want.Template.ServiceMesh.Mesh, tmpl.ServiceMesh.Mesh)
	require.NotNil(t, tmpl.NodeSelector)
	assert.Equal(t, "nvidia-l4", tmpl.NodeSelector.Accelerator)

	require.Len(t, tmpl.Containers, 2)
	worker, sidecar := tmpl.Containers[0], tmpl.Containers[1]
	assert.Equal(t, "/srv", worker.WorkingDir)
	assert.Equal(t, []string{"/worker"}, worker.Command)
	assert.Equal(t, []string{"--mode", "pull"}, worker.Args)
	require.NotNil(t, worker.Resources)
	assert.True(t, worker.Resources.CpuIdle)
	assert.Equal(t, map[string]string{"cpu": "1000m", "memory": "512Mi"}, worker.Resources.Limits)
	require.Len(t, worker.VolumeMounts, 3)
	assert.Equal(t, "inner", worker.VolumeMounts[0].SubPath)
	assert.Equal(t, []string{"worker"}, sidecar.DependsOn)

	require.Len(t, tmpl.Volumes, 3)
	require.NotNil(t, tmpl.Volumes[0].EmptyDir)
	assert.Equal(t, "MEMORY", tmpl.Volumes[0].EmptyDir.Medium)
	assert.Equal(t, "128Mi", tmpl.Volumes[0].EmptyDir.SizeLimit)
	require.NotNil(t, tmpl.Volumes[1].Secret)
	assert.Equal(t, int64(292), tmpl.Volumes[1].Secret.DefaultMode)
	require.Len(t, tmpl.Volumes[1].Secret.Items, 1)
	assert.Equal(t, "creds.json", tmpl.Volumes[1].Secret.Items[0].Path)
	assert.Equal(t, int64(256), tmpl.Volumes[1].Secret.Items[0].Mode)
	require.NotNil(t, tmpl.Volumes[2].CloudSqlInstance)
	assert.Equal(t, []string{"test-project:us-central1:db"}, tmpl.Volumes[2].CloudSqlInstance.Instances)

	require.NotNil(t, tmpl.VpcAccess)
	require.Len(t, tmpl.VpcAccess.NetworkInterfaces, 1)
	assert.Equal(t, "default", tmpl.VpcAccess.NetworkInterfaces[0].Network)
	assert.Equal(t, []string{"worker"}, tmpl.VpcAccess.NetworkInterfaces[0].Tags)
}

// TestSDK_RunV2REST_Instance_FullBodyRoundTrip is the Instance counterpart of
// the worker-pool full-body round-trip.
func TestSDK_RunV2REST_Instance_FullBodyRoundTrip(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "rest-inst-full-body"
	name := crV2Parent + "/instances/" + id

	want := &runv2.GoogleCloudRunV2Instance{
		Description:   "every writable field",
		Labels:        map[string]string{"team": "platform"},
		Annotations:   map[string]string{"example.com/owner": "platform"},
		LaunchStage:   "GA",
		Ingress:       "INGRESS_TRAFFIC_ALL",
		Client:        "terraform",
		ClientVersion: "7.36.0",
		BinaryAuthorization: &runv2.GoogleCloudRunV2BinaryAuthorization{
			UseDefault: true,
		},
		EncryptionKey:      "projects/test-project/locations/us-central1/keyRings/kr/cryptoKeys/ck",
		IapEnabled:         true,
		InvokerIamDisabled: true,
		RestartPolicy:      "INSTANCE_RESTART_POLICY_ALWAYS",
		ServiceAccount:     "runner@test-project.iam.gserviceaccount.com",
		NodeSelector:       &runv2.GoogleCloudRunV2NodeSelector{Accelerator: "nvidia-l4"},
		Containers: []*runv2.GoogleCloudRunV2Container{{
			Name:       "app",
			Image:      "gcr.io/test-project/" + id,
			WorkingDir: "/srv",
			VolumeMounts: []*runv2.GoogleCloudRunV2VolumeMount{
				{Name: "scratch", MountPath: "/scratch", SubPath: "inner"},
			},
		}},
		Volumes: []*runv2.GoogleCloudRunV2Volume{{
			Name:     "scratch",
			EmptyDir: &runv2.GoogleCloudRunV2EmptyDirVolumeSource{Medium: "MEMORY"},
		}},
		VpcAccess: &runv2.GoogleCloudRunV2VpcAccess{
			Egress: "ALL_TRAFFIC",
			NetworkInterfaces: []*runv2.GoogleCloudRunV2NetworkInterface{
				{Network: "default", Subnetwork: "default"},
			},
		},
	}

	op, err := svc.Projects.Locations.Instances.Create(crV2Parent, want).InstanceId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		delOp, err := svc.Projects.Locations.Instances.Delete(name).Do()
		if err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	got, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)

	assert.Equal(t, want.Description, got.Description)
	assert.Equal(t, want.Labels, got.Labels)
	assert.Equal(t, want.Annotations, got.Annotations)
	assert.Equal(t, want.Ingress, got.Ingress)
	assert.Equal(t, want.Client, got.Client)
	assert.Equal(t, want.ClientVersion, got.ClientVersion)
	assert.Equal(t, want.EncryptionKey, got.EncryptionKey)
	assert.True(t, got.IapEnabled)
	assert.True(t, got.InvokerIamDisabled)
	assert.Equal(t, want.RestartPolicy, got.RestartPolicy)
	assert.Equal(t, want.ServiceAccount, got.ServiceAccount)
	require.NotNil(t, got.BinaryAuthorization)
	assert.True(t, got.BinaryAuthorization.UseDefault)
	require.NotNil(t, got.NodeSelector)
	assert.Equal(t, "nvidia-l4", got.NodeSelector.Accelerator)

	require.Len(t, got.Containers, 1)
	assert.Equal(t, "/srv", got.Containers[0].WorkingDir)
	require.Len(t, got.Containers[0].VolumeMounts, 1)
	assert.Equal(t, "inner", got.Containers[0].VolumeMounts[0].SubPath)

	require.Len(t, got.Volumes, 1)
	require.NotNil(t, got.Volumes[0].EmptyDir)
	assert.Equal(t, "MEMORY", got.Volumes[0].EmptyDir.Medium)

	require.NotNil(t, got.VpcAccess)
	require.Len(t, got.VpcAccess.NetworkInterfaces, 1)
	assert.Equal(t, "default", got.VpcAccess.NetworkInterfaces[0].Network)
}

// TestSDK_RunV2REST_WorkerPoolsInstances_NotFound pins the error shape both
// collections return for an absent resource: HTTP 404, not a 200 with an
// empty body.
func TestSDK_RunV2REST_WorkerPoolsInstances_NotFound(t *testing.T) {
	svc := newRunV2RESTService(t)

	_, err := svc.Projects.Locations.WorkerPools.Get(crV2Parent + "/workerPools/no-such-pool").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")

	_, err = svc.Projects.Locations.Instances.Get(crV2Parent + "/instances/no-such-instance").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")

	_, err = svc.Projects.Locations.WorkerPools.Patch(crV2Parent+"/workerPools/no-such-pool",
		&runv2.GoogleCloudRunV2WorkerPool{}).UpdateMask("labels").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")

	_, err = svc.Projects.Locations.Instances.Patch(crV2Parent+"/instances/no-such-instance",
		&runv2.GoogleCloudRunV2Instance{}).UpdateMask("labels").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
