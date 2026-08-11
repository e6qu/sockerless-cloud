package gcp_sdk_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/option"
)

// TestCloudBuild_DockerBuildAndPush exercises the Cloud Build slice:
// the GCP simulator implements CreateBuild + LRO, the handler extracts
// the uploaded GCS source tarball, runs `docker build` against it,
// and returns a done=true LRO with status=SUCCESS. Uses direct REST
// against the simulator rather than the gRPC cloudbuild Go client
// (which doesn't easily accept endpoint overrides).
func TestCloudBuild_DockerBuildAndPush(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}

	project := "cb-test-project"

	// 1. Upload a tiny build context (a Dockerfile only) to the sim's
	// GCS slice. Real sockerless uploads context via the GCS client;
	// we do it directly via REST.
	bucket := "cb-test-bucket"
	createBucket(t, project, bucket)
	objectName := fmt.Sprintf("cb-context-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{
		"Dockerfile": "FROM alpine:latest\nRUN echo 'built in simulator' > /hello.txt\n",
	})
	uploadGCSObject(t, bucket, objectName, tarball)

	// 2. Submit a build via the Cloud Build REST endpoint.
	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[
			{"name":"gcr.io/cloud-builders/docker","args":["build","-t","sim-cb-build:test","."]}
		],
		"images":["sim-cb-build:test"]
	}`, bucket, objectName)
	resp := httpPOST(t, buildURL, body)

	var op struct {
		Name     string         `json:"name"`
		Done     bool           `json:"done"`
		Response map[string]any `json:"response"`
		Error    *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp), &op))
	require.True(t, op.Done, "LRO should be done; simulator executes synchronously")
	require.Nil(t, op.Error, "build should succeed: %+v", op.Error)
	assert.Equal(t, "SUCCESS", op.Response["status"])
}

// TestCloudBuild_SecretEnvExpansion exercises secret expansion:
// Secret Manager references in the build's AvailableSecrets are
// resolved to env vars available to each build step's secretEnv.
// The Dockerfile uses an ARG, but the step's env passes the
// resolved secret through as a
// regular env var; docker build tolerates unused env vars.
func TestCloudBuild_SecretEnvExpansion(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}

	project := "cb-secret-project"
	bucket := "cb-secret-bucket"
	createBucket(t, project, bucket)

	// Seed a Secret Manager secret + version.
	secretID := "mysecret"
	secretValue := "s3cret-payload"
	smCreateURL := fmt.Sprintf("%s/v1/projects/%s/secrets?secretId=%s", baseURL, project, secretID)
	httpPOST(t, smCreateURL, `{}`)
	addVerURL := fmt.Sprintf("%s/v1/projects/%s/secrets/%s:addVersion", baseURL, project, secretID)
	addVerBody := fmt.Sprintf(`{"payload":{"data":%q}}`, base64.StdEncoding.EncodeToString([]byte(secretValue)))
	httpPOST(t, addVerURL, addVerBody)

	// Upload a trivial Dockerfile.
	objectName := fmt.Sprintf("secret-cb-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{"Dockerfile": "FROM alpine:latest\nCMD [\"true\"]\n"})
	uploadGCSObject(t, bucket, objectName, tarball)

	// Submit build with secretEnv — simulator must resolve the Secret
	// Manager reference and expose the payload in the step's env.
	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[
			{"name":"gcr.io/cloud-builders/docker","args":["build","-t","sim-cb-secret:test","."],
			 "secretEnv":["MYSECRET"]}
		],
		"images":["sim-cb-secret:test"],
		"availableSecrets":{"secretManager":[
			{"versionName":"projects/%s/secrets/%s/versions/latest","env":"MYSECRET"}
		]}
	}`, bucket, objectName, project, secretID)
	resp := httpPOST(t, buildURL, body)

	var op struct {
		Done  bool `json:"done"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Response map[string]any `json:"response"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp), &op))
	require.True(t, op.Done)
	require.Nil(t, op.Error, "build should succeed with resolved secret: %+v", op.Error)
	assert.Equal(t, "SUCCESS", op.Response["status"])
}

// TestCloudBuild_MissingSecretFails asserts that an unresolvable
// Secret Manager reference fails the build with a clear error (rather
// than silently dropping the secret env var).
func TestCloudBuild_MissingSecretFails(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}
	project := "cb-missing-secret-project"
	bucket := "cb-missing-secret-bucket"
	createBucket(t, project, bucket)

	objectName := fmt.Sprintf("missing-cb-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{"Dockerfile": "FROM alpine:latest\n"})
	uploadGCSObject(t, bucket, objectName, tarball)

	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[{"name":"gcr.io/cloud-builders/docker","args":["build","-t","x","."],
			 "secretEnv":["NOPE"]}],
		"availableSecrets":{"secretManager":[
			{"versionName":"projects/%s/secrets/doesnotexist/versions/1","env":"NOPE"}
		]}
	}`, bucket, objectName, project)
	resp := httpPOST(t, buildURL, body)

	var op struct {
		Done  bool `json:"done"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp), &op))
	require.True(t, op.Done)
	require.NotNil(t, op.Error, "build should fail when secret reference unresolvable")
	assert.Contains(t, op.Error.Message, "resolve secret")
}

// ---- helpers (local to this test file) ----

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func createBucket(t *testing.T, project, name string) {
	t.Helper()
	url := fmt.Sprintf("%s/storage/v1/b?project=%s", baseURL, project)
	httpPOST(t, url, fmt.Sprintf(`{"name":%q}`, name))
}

func uploadGCSObject(t *testing.T, bucket, object string, data []byte) {
	t.Helper()
	url := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		baseURL, bucket, object)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 400, "upload failed: %d %s", resp.StatusCode, string(body))
}

func httpPOST(t *testing.T, url, body string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, 400, "POST %s: %d %s", url, resp.StatusCode, string(data))
	return string(data)
}

func cloudbuildService(t *testing.T) *cloudbuild.Service {
	t.Helper()
	svc, err := cloudbuild.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestCloudBuild_TriggerCRUD(t *testing.T) {
	svc := cloudbuildService(t)
	project := "cb-trigger-project"

	// Create a trigger.
	trigger := &cloudbuild.BuildTrigger{
		Name:     "sdk-test-trigger",
		Filename: "cloudbuild.yaml",
		TriggerTemplate: &cloudbuild.RepoSource{
			RepoName:   "sdk-repo",
			BranchName: "main",
		},
	}
	created, err := svc.Projects.Triggers.Create(project, trigger).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-test-trigger", created.Name)
	require.NotEmpty(t, created.Id)

	// Get trigger.
	got, err := svc.Projects.Triggers.Get(project, created.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Id, got.Id)
	assert.Equal(t, "sdk-test-trigger", got.Name)
	assert.Equal(t, "cloudbuild.yaml", got.Filename)

	// List triggers — must contain created trigger.
	list, err := svc.Projects.Triggers.List(project).Do()
	require.NoError(t, err)
	found := false
	for _, tr := range list.Triggers {
		if tr.Id == created.Id {
			found = true
		}
	}
	assert.True(t, found, "created trigger must appear in list")

	// Patch — update the filename.
	patched, err := svc.Projects.Triggers.Patch(project, created.Id,
		&cloudbuild.BuildTrigger{
			Name:     "sdk-test-trigger",
			Filename: "ci/cloudbuild.yaml",
		}).Do()
	require.NoError(t, err)
	assert.Equal(t, "ci/cloudbuild.yaml", patched.Filename)

	// Delete trigger.
	_, err = svc.Projects.Triggers.Delete(project, created.Id).Do()
	require.NoError(t, err)

	// Get after delete must fail.
	_, err = svc.Projects.Triggers.Get(project, created.Id).Do()
	require.Error(t, err)
}

func TestCloudBuild_ListTriggers_Pagination(t *testing.T) {
	const project = "test-project"
	for _, name := range []string{"pag-trig-a", "pag-trig-b", "pag-trig-c"} {
		body, _ := json.Marshal(map[string]any{
			"name": name,
			"github": map[string]any{
				"owner": "owner",
				"name":  "repo",
				"push":  map[string]any{"branch": "main"},
			},
		})
		req, _ := http.NewRequest("POST",
			fmt.Sprintf("%s/v1/projects/%s/locations/global/triggers", baseURL, project),
			bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		n := name
		t.Cleanup(func() {
			req, _ := http.NewRequest("DELETE",
				fmt.Sprintf("%s/v1/projects/%s/locations/global/triggers/%s", baseURL, project, n), nil)
			http.DefaultClient.Do(req)
		})
	}

	seen := map[string]bool{}
	var pageToken string
	for {
		u := fmt.Sprintf("%s/v1/projects/%s/locations/global/triggers?pageSize=1", baseURL, project)
		if pageToken != "" {
			u += "&pageToken=" + pageToken
		}
		req, _ := http.NewRequest("GET", u, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var body struct {
			Triggers      []map[string]any `json:"triggers"`
			NextPageToken string           `json:"nextPageToken"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		resp.Body.Close()
		for _, trig := range body.Triggers {
			if n, ok := trig["name"].(string); ok {
				seen[n] = true
			}
		}
		pageToken = body.NextPageToken
		if pageToken == "" {
			break
		}
	}
	for _, n := range []string{"pag-trig-a", "pag-trig-b", "pag-trig-c"} {
		assert.True(t, seen[n], "trigger %s should appear via pagination", n)
	}
}
