package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runv1 "google.golang.org/api/run/v1"
	runv2 "google.golang.org/api/run/v2"
)

// Cloud Run Admin v1 instances IAM aliases
// (run.projects.locations.instances.{get,set,test}IamPolicy) and the
// collapsed-origin service resolution they need.
//
// Real Google Cloud serves `/v1/projects/{p}/locations/{l}/instances/…` from
// two hosts: run.googleapis.com publishes the IAM triple above, and
// redis.googleapis.com publishes the Memorystore for Redis instance lifecycle.
// The simulator serves both from one origin, so it resolves the owning service
// from the request `Host` when the client resolved a real Google host, and
// otherwise from the AIP-136 custom method the URI carries — the two services'
// custom methods are disjoint. Both spellings are exercised here.

// gcpDataPlaneRequest issues a raw request against the simulator with an
// explicit `Host`, the way a client that resolved a Google API hostname to this
// address sends it. The URL still addresses the simulator's coordinate; only
// the host the client believes it is talking to differs.
func gcpDataPlaneRequest(t *testing.T, method, host, path, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(data)
}

// createRunV2Instance creates a Cloud Run instance and registers its teardown.
func createRunV2Instance(t *testing.T, id string) string {
	t.Helper()
	svc := newRunV2RESTService(t)
	name := crV2Parent + "/instances/" + id
	op, err := svc.Projects.Locations.Instances.Create(crV2Parent, &runv2.GoogleCloudRunV2Instance{
		Containers: []*runv2.GoogleCloudRunV2Container{{Image: "gcr.io/" + crV2Project + "/" + id}},
	}).InstanceId(id).Do()
	require.NoError(t, err)
	awaitRunV2Operation(t, svc, op)
	t.Cleanup(func() {
		if delOp, err := svc.Projects.Locations.Instances.Delete(name).Do(); err == nil {
			awaitRunV2Operation(t, svc, delOp)
		}
	})
	return name
}

// TestSDK_RunV1REST_Instances_IAMVerbs drives the v1 aliases through the
// official Discovery REST client and requires that the v1 and v2 collections
// address one resource's single policy.
func TestSDK_RunV1REST_Instances_IAMVerbs(t *testing.T) {
	name := createRunV2Instance(t, "rest-inst-v1-iam")
	v1svc := newRunV1RESTService(t)
	v2svc := newRunV2RESTService(t)

	set, err := v1svc.Projects.Locations.Instances.SetIamPolicy(name, &runv1.SetIamPolicyRequest{
		Policy: &runv1.Policy{
			Bindings: []*runv1.Binding{{
				Role:    "roles/run.developer",
				Members: []string{"user:v1-inst@example.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	v1Pol, err := v1svc.Projects.Locations.Instances.GetIamPolicy(name).Do()
	require.NoError(t, err)
	require.Len(t, v1Pol.Bindings, 1)
	assert.Equal(t, "roles/run.developer", v1Pol.Bindings[0].Role)
	assert.Equal(t, []string{"user:v1-inst@example.com"}, v1Pol.Bindings[0].Members)

	v2Pol, err := v2svc.Projects.Locations.Instances.GetIamPolicy(name).Do()
	require.NoError(t, err)
	require.Len(t, v2Pol.Bindings, 1)
	assert.Equal(t, "roles/run.developer", v2Pol.Bindings[0].Role)
	assert.Equal(t, v1Pol.Etag, v2Pol.Etag, "one resource, one policy etag across API versions")

	perms, err := v1svc.Projects.Locations.Instances.TestIamPermissions(name,
		&runv1.TestIamPermissionsRequest{Permissions: []string{"run.instances.get"}}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"run.instances.get"}, perms.Permissions)
}

// TestSDK_RunV1REST_Instances_IAMOnMissingInstance pins the error an IAM verb
// returns for an instance that was never created: NOT_FOUND, never an empty
// policy for a name that does not exist.
func TestSDK_RunV1REST_Instances_IAMOnMissingInstance(t *testing.T) {
	v1svc := newRunV1RESTService(t)
	_, err := v1svc.Projects.Locations.Instances.GetIamPolicy(crV2Parent + "/instances/never-created").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// TestSDK_RunV1Instances_HostResolvesOwningService proves the shared
// `/v1/.../instances/…` prefix answers as whichever service the request's
// `Host` names, and that both services keep working on it.
func TestSDK_RunV1Instances_HostResolvesOwningService(t *testing.T) {
	const id = "host-split-inst"
	createRunV2Instance(t, id)

	// A Memorystore for Redis instance of the same name on the same prefix.
	redisPath := "/v1/projects/" + crV2Project + "/locations/" + crV2Location + "/instances"
	status, body := gcpDataPlaneRequest(t, http.MethodPost, "redis.googleapis.com",
		redisPath+"?instanceId="+id, `{"tier":"BASIC","memorySizeGb":1}`)
	require.Equal(t, http.StatusOK, status, body)
	t.Cleanup(func() {
		gcpDataPlaneRequest(t, http.MethodDelete, "redis.googleapis.com", redisPath+"/"+id, "")
	})

	// run.googleapis.com owns the IAM triple on this prefix.
	status, body = gcpDataPlaneRequest(t, http.MethodGet, "run.googleapis.com", redisPath+"/"+id+":getIamPolicy", "")
	require.Equal(t, http.StatusOK, status, body)
	var policy struct {
		Etag string `json:"etag"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &policy))
	assert.NotEmpty(t, policy.Etag, "getIamPolicy always returns an etag")

	// …and publishes nothing else there: a plain read on run.googleapis.com is
	// a method Cloud Run does not serve, not a Memorystore lookup.
	status, body = gcpDataPlaneRequest(t, http.MethodGet, "run.googleapis.com", redisPath+"/"+id, "")
	assert.Equal(t, http.StatusNotFound, status, body)
	assert.Contains(t, body, "Method not found.")

	// redis.googleapis.com owns the instance lifecycle on the same prefix.
	status, body = gcpDataPlaneRequest(t, http.MethodGet, "redis.googleapis.com", redisPath+"/"+id, "")
	require.Equal(t, http.StatusOK, status, body)
	var instance struct {
		Name  string `json:"name"`
		Tier  string `json:"tier"`
		State string `json:"state"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &instance))
	assert.Equal(t, "projects/"+crV2Project+"/locations/"+crV2Location+"/instances/"+id, instance.Name)
	assert.Equal(t, "READY", instance.State)

	// …and publishes no IAM triple: on redis.googleapis.com the ":getIamPolicy"
	// suffix is part of the instance id, which does not exist.
	status, body = gcpDataPlaneRequest(t, http.MethodGet, "redis.googleapis.com", redisPath+"/"+id+":getIamPolicy", "")
	assert.Equal(t, http.StatusNotFound, status, body)
	assert.Contains(t, body, "instance not found")

	// The regional Google API hostname resolves to the same service.
	status, body = gcpDataPlaneRequest(t, http.MethodGet, crV2Location+"-run.googleapis.com",
		redisPath+"/"+id+":getIamPolicy", "")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, policy.Etag)
}
