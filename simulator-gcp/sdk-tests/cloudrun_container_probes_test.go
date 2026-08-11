package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runv2 "google.golang.org/api/run/v2"
)

// Container health probes and secret-sourced environment variables driven
// through the official Cloud Run v2 Discovery REST client. Probe and
// EnvVar.valueSource are members of google.cloud.run.v2.Container, the type
// Jobs, Services, Worker Pools and Instances all embed, so every one of the
// four resources must round-trip them through create, get, list and patch: a
// dropped member is invisible to a smoke test but makes terraform re-plan the
// resource on every run.

// probedContainer builds a container carrying all three probes — one per
// handler kind the API defines — plus a literal and a Secret Manager-sourced
// environment variable.
func probedContainer(image string) *runv2.GoogleCloudRunV2Container {
	return &runv2.GoogleCloudRunV2Container{
		Name:  "app",
		Image: image,
		Env: []*runv2.GoogleCloudRunV2EnvVar{
			{Name: "LITERAL", Value: "plain"},
			{Name: "SOURCED", ValueSource: &runv2.GoogleCloudRunV2EnvVarSource{
				SecretKeyRef: &runv2.GoogleCloudRunV2SecretKeySelector{
					Secret:  "projects/" + crV2Project + "/secrets/probe-secret",
					Version: "latest",
				},
			}},
		},
		StartupProbe: &runv2.GoogleCloudRunV2Probe{
			InitialDelaySeconds: 3,
			TimeoutSeconds:      2,
			PeriodSeconds:       10,
			FailureThreshold:    4,
			TcpSocket:           &runv2.GoogleCloudRunV2TCPSocketAction{Port: 8080},
		},
		LivenessProbe: &runv2.GoogleCloudRunV2Probe{
			InitialDelaySeconds: 5,
			TimeoutSeconds:      1,
			PeriodSeconds:       15,
			FailureThreshold:    2,
			HttpGet: &runv2.GoogleCloudRunV2HTTPGetAction{
				Path: "/healthz",
				Port: 8080,
				HttpHeaders: []*runv2.GoogleCloudRunV2HTTPHeader{
					{Name: "X-Probe", Value: "liveness"},
				},
			},
		},
		ReadinessProbe: &runv2.GoogleCloudRunV2Probe{
			TimeoutSeconds:   1,
			PeriodSeconds:    5,
			FailureThreshold: 3,
			Grpc: &runv2.GoogleCloudRunV2GRPCAction{
				Port:    8080,
				Service: "health.v1.Health",
			},
		},
	}
}

// assertProbedContainer requires every probe member and both environment
// variables back, exactly as probedContainer set them.
func assertProbedContainer(t *testing.T, got *runv2.GoogleCloudRunV2Container) {
	t.Helper()
	require.NotNil(t, got.StartupProbe, "startupProbe must survive the round trip")
	assert.Equal(t, int64(3), got.StartupProbe.InitialDelaySeconds)
	assert.Equal(t, int64(2), got.StartupProbe.TimeoutSeconds)
	assert.Equal(t, int64(10), got.StartupProbe.PeriodSeconds)
	assert.Equal(t, int64(4), got.StartupProbe.FailureThreshold)
	require.NotNil(t, got.StartupProbe.TcpSocket, "the tcpSocket handler must survive")
	assert.Equal(t, int64(8080), got.StartupProbe.TcpSocket.Port)

	require.NotNil(t, got.LivenessProbe, "livenessProbe must survive the round trip")
	require.NotNil(t, got.LivenessProbe.HttpGet, "the httpGet handler must survive")
	assert.Equal(t, "/healthz", got.LivenessProbe.HttpGet.Path)
	assert.Equal(t, int64(8080), got.LivenessProbe.HttpGet.Port)
	require.Len(t, got.LivenessProbe.HttpGet.HttpHeaders, 1)
	assert.Equal(t, "X-Probe", got.LivenessProbe.HttpGet.HttpHeaders[0].Name)
	assert.Equal(t, "liveness", got.LivenessProbe.HttpGet.HttpHeaders[0].Value)

	require.NotNil(t, got.ReadinessProbe, "readinessProbe must survive the round trip")
	require.NotNil(t, got.ReadinessProbe.Grpc, "the grpc handler must survive")
	assert.Equal(t, int64(8080), got.ReadinessProbe.Grpc.Port)
	assert.Equal(t, "health.v1.Health", got.ReadinessProbe.Grpc.Service)

	require.Len(t, got.Env, 2)
	byName := map[string]*runv2.GoogleCloudRunV2EnvVar{}
	for _, e := range got.Env {
		byName[e.Name] = e
	}
	require.Contains(t, byName, "LITERAL")
	assert.Equal(t, "plain", byName["LITERAL"].Value)
	assert.Nil(t, byName["LITERAL"].ValueSource, "a literal env var carries no valueSource")

	require.Contains(t, byName, "SOURCED")
	sourced := byName["SOURCED"]
	require.NotNil(t, sourced.ValueSource, "valueSource must survive the round trip")
	require.NotNil(t, sourced.ValueSource.SecretKeyRef)
	assert.Equal(t, "projects/"+crV2Project+"/secrets/probe-secret", sourced.ValueSource.SecretKeyRef.Secret)
	assert.Equal(t, "latest", sourced.ValueSource.SecretKeyRef.Version)
	assert.Empty(t, sourced.Value,
		"value and valueSource are alternatives of one oneof: a sourced env var carries no literal value")
}

func TestSDK_RunV2REST_Job_ProbesAndEnvValueSource(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "probe-job"
	name := crV2Parent + "/jobs/" + id

	op, err := svc.Projects.Locations.Jobs.Create(crV2Parent, &runv2.GoogleCloudRunV2Job{
		Template: &runv2.GoogleCloudRunV2ExecutionTemplate{
			Template: &runv2.GoogleCloudRunV2TaskTemplate{
				Containers: []*runv2.GoogleCloudRunV2Container{probedContainer("gcr.io/" + crV2Project + "/" + id)},
			},
		},
	}).JobId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		if delOp, err := svc.Projects.Locations.Jobs.Delete(name).Do(); err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	got, err := svc.Projects.Locations.Jobs.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.Template)
	require.NotNil(t, got.Template.Template)
	require.Len(t, got.Template.Template.Containers, 1)
	assertProbedContainer(t, got.Template.Template.Containers[0])

	list, err := svc.Projects.Locations.Jobs.List(crV2Parent).Do()
	require.NoError(t, err)
	var listed *runv2.GoogleCloudRunV2Job
	for _, j := range list.Jobs {
		if j.Name == name {
			listed = j
		}
	}
	require.NotNil(t, listed, "ListJobs must include the created job")
	require.Len(t, listed.Template.Template.Containers, 1)
	assertProbedContainer(t, listed.Template.Template.Containers[0])
}

func TestSDK_RunV2REST_Service_ProbesAndEnvValueSource(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "probe-svc"
	name := crV2Parent + "/services/" + id

	op, err := svc.Projects.Locations.Services.Create(crV2Parent, &runv2.GoogleCloudRunV2Service{
		Template: &runv2.GoogleCloudRunV2RevisionTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{probedContainer("gcr.io/" + crV2Project + "/" + id)},
		},
	}).ServiceId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		if delOp, err := svc.Projects.Locations.Services.Delete(name).Do(); err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	got, err := svc.Projects.Locations.Services.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.Template)
	require.Len(t, got.Template.Containers, 1)
	assertProbedContainer(t, got.Template.Containers[0])

	// A patch that does not name the template must leave the probes alone.
	patchOp, err := svc.Projects.Locations.Services.Patch(name, &runv2.GoogleCloudRunV2Service{
		Labels: map[string]string{"suite": "probes"},
	}).UpdateMask("labels").Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	patched, err := svc.Projects.Locations.Services.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "probes", patched.Labels["suite"])
	require.Len(t, patched.Template.Containers, 1)
	assertProbedContainer(t, patched.Template.Containers[0])
}

func TestSDK_RunV2REST_WorkerPool_ProbesAndEnvValueSource(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "probe-wp"
	name := crV2Parent + "/workerPools/" + id

	op, err := svc.Projects.Locations.WorkerPools.Create(crV2Parent, &runv2.GoogleCloudRunV2WorkerPool{
		Template: &runv2.GoogleCloudRunV2WorkerPoolRevisionTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{probedContainer("gcr.io/" + crV2Project + "/" + id)},
		},
	}).WorkerPoolId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		if delOp, err := svc.Projects.Locations.WorkerPools.Delete(name).Do(); err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	got, err := svc.Projects.Locations.WorkerPools.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.Template)
	require.Len(t, got.Template.Containers, 1)
	assertProbedContainer(t, got.Template.Containers[0])

	// The revision a deploy materializes carries the same containers.
	revs, err := svc.Projects.Locations.WorkerPools.Revisions.List(name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, revs.Revisions, "a deploy materializes a revision")
	require.Len(t, revs.Revisions[0].Containers, 1)
	assertProbedContainer(t, revs.Revisions[0].Containers[0])
}

func TestSDK_RunV2REST_Instance_ProbesAndEnvValueSource(t *testing.T) {
	svc := newRunV2RESTService(t)
	const id = "probe-inst"
	name := crV2Parent + "/instances/" + id

	op, err := svc.Projects.Locations.Instances.Create(crV2Parent, &runv2.GoogleCloudRunV2Instance{
		Containers: []*runv2.GoogleCloudRunV2Container{probedContainer("gcr.io/" + crV2Project + "/" + id)},
	}).InstanceId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		if delOp, err := svc.Projects.Locations.Instances.Delete(name).Do(); err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})

	got, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	require.Len(t, got.Containers, 1)
	assertProbedContainer(t, got.Containers[0])

	// Patching the containers replaces them wholesale, probes included.
	patchOp, err := svc.Projects.Locations.Instances.Patch(name, &runv2.GoogleCloudRunV2Instance{
		Containers: []*runv2.GoogleCloudRunV2Container{probedContainer("gcr.io/" + crV2Project + "/" + id + ":v2")},
	}).UpdateMask("containers").Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, patchOp)

	patched, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	require.Len(t, patched.Containers, 1)
	assert.Equal(t, "gcr.io/"+crV2Project+"/"+id+":v2", patched.Containers[0].Image)
	assertProbedContainer(t, patched.Containers[0])
}
