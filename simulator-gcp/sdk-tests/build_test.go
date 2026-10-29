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

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbuild/v1"
)

// TestCloudBuild_DockerBuild: CreateBuild answers at once with the build's
// operation, the build extracts the Cloud Storage source tarball and runs
// `docker build` on it, and the operation completes with the SUCCESS build.
// A verdict alone would pass a handler that never ran the step, so inspect the
// image on the daemon. TestCloudBuild_FaithfulBuildPush covers the push.
func TestCloudBuild_DockerBuild(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}
	pullImageWithRetry(t, cbBaseImage)

	project := "cb-test-project"

	bucket := "cb-test-bucket"
	createBucket(t, project, bucket)
	objectName := fmt.Sprintf("cb-context-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{
		"Dockerfile": "FROM " + cbBaseImage + "\nRUN echo 'built in simulator' > /hello.txt\n",
	})
	uploadGCSObject(t, bucket, objectName, tarball)

	imageTag := fmt.Sprintf("sim-cb-build:gcp-sdk-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", imageTag) })
	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[
			{"name":"gcr.io/cloud-builders/docker","args":["build","-t",%q,"."]}
		]
	}`, bucket, objectName, imageTag)
	started := readStartedBuild(t, httpPOST(t, buildURL, body))

	built, err := waitBuild(t, started)
	require.NoError(t, err, "build should succeed")
	assert.Equal(t, started.BuildID, built.GetId())
	assert.Equal(t, cloudbuildpb.Build_SUCCESS, built.GetStatus())

	// The step really ran: the tag it was given names an image the daemon now
	// holds, whose history carries the layer the Dockerfile's RUN produced.
	inspect, err := dockerCLIWithTimeout(60*time.Second, "image", "inspect", imageTag)
	require.NoError(t, err,
		"the build step must leave %s in the daemon image store: %s", imageTag, inspect)
}

// TestCloudBuild_SecretEnvExpansion: Cloud Build resolves availableSecrets into
// each step's secretEnv. A Dockerfile does not read the builder's environment,
// so the step passes a bare `--build-arg MYSECRET`, which docker takes from its
// own environment, and the Dockerfile compares it with the payload.
func TestCloudBuild_SecretEnvExpansion(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}
	pullImageWithRetry(t, cbBaseImage)

	project := "cb-secret-project"
	bucket := "cb-secret-bucket"
	createBucket(t, project, bucket)

	secretID := "mysecret"
	secretValue := "s3cret-payload"
	smCreateURL := fmt.Sprintf("%s/v1/projects/%s/secrets?secretId=%s", baseURL, project, secretID)
	httpPOST(t, smCreateURL, `{}`)
	addVerURL := fmt.Sprintf("%s/v1/projects/%s/secrets/%s:addVersion", baseURL, project, secretID)
	addVerBody := fmt.Sprintf(`{"payload":{"data":%q}}`, base64.StdEncoding.EncodeToString([]byte(secretValue)))
	httpPOST(t, addVerURL, addVerBody)

	objectName := fmt.Sprintf("secret-cb-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{"Dockerfile": "FROM " + cbBaseImage +
		"\nARG MYSECRET\nRUN test \"$MYSECRET\" = \"" + secretValue + "\"\n"})
	uploadGCSObject(t, bucket, objectName, tarball)

	imageTag := fmt.Sprintf("sim-cb-secret:gcp-sdk-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", imageTag) })
	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := cbBuildBody(t, bucket, objectName,
		[]any{
			// A bare `--build-arg MYSECRET` takes the value from the docker
			// CLI's environment, so the Dockerfile's comparison holds only if
			// the simulator put the resolved secret there.
			cbDockerStep([]string{
				"build", "--build-arg", "MYSECRET", "-t", imageTag, ".",
			}, "MYSECRET"),
		},
		map[string]any{"secretManager": []any{map[string]any{
			"versionName": fmt.Sprintf("projects/%s/secrets/%s/versions/latest", project, secretID),
			"env":         "MYSECRET",
		}}})
	built, err := waitBuild(t, readStartedBuild(t, httpPOST(t, buildURL, body)))
	require.NoError(t, err, "build should succeed with resolved secret")
	assert.Equal(t, cloudbuildpb.Build_SUCCESS, built.GetStatus())
}

// TestCloudBuild_SecretEnvWithoutMatchingAvailableSecret pins a divergence:
// real Cloud Build refuses at CreateBuild with INVALID_ARGUMENT ("secretEnv ...
// is not defined in availableSecrets") a step naming a secretEnv that
// availableSecrets never declares, while the simulator runs the step with the
// variable absent. A simulator taught to refuse turns this red, and the refusal
// is then what to assert.
func TestCloudBuild_SecretEnvWithoutMatchingAvailableSecret(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}
	pullImageWithRetry(t, cbBaseImage)

	project := "cb-undeclared-secret-project"
	bucket := "cb-undeclared-secret-bucket"
	createBucket(t, project, bucket)

	secretID := "declared"
	smCreateURL := fmt.Sprintf("%s/v1/projects/%s/secrets?secretId=%s", baseURL, project, secretID)
	httpPOST(t, smCreateURL, `{}`)
	addVerURL := fmt.Sprintf("%s/v1/projects/%s/secrets/%s:addVersion", baseURL, project, secretID)
	httpPOST(t, addVerURL, fmt.Sprintf(`{"payload":{"data":%q}}`,
		base64.StdEncoding.EncodeToString([]byte("declared-payload"))))

	objectName := fmt.Sprintf("undeclared-cb-%d.tar.gz", time.Now().UnixNano())
	uploadGCSObject(t, bucket, objectName, makeTarGz(t, map[string]string{
		"Dockerfile": "FROM " + cbBaseImage +
			"\nARG SIM_UNDECLARED_SECRET\nRUN test -z \"$SIM_UNDECLARED_SECRET\"\n",
	}))

	// availableSecrets declares DECLARED; the step asks for a name nothing
	// declares.
	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := cbBuildBody(t, bucket, objectName,
		[]any{cbDockerStep([]string{
			"build", "--build-arg", "SIM_UNDECLARED_SECRET",
			"-t", fmt.Sprintf("sim-cb-undeclared:gcp-sdk-%d", time.Now().UnixNano()), ".",
		}, "SIM_UNDECLARED_SECRET")},
		map[string]any{"secretManager": []any{map[string]any{
			"versionName": fmt.Sprintf("projects/%s/secrets/%s/versions/latest", project, secretID),
			"env":         "DECLARED",
		}}})
	built, err := waitBuild(t, readStartedBuild(t, httpPOST(t, buildURL, body)))
	require.NoError(t, err,
		"the build is accepted today rather than refused for the undeclared secretEnv")
	assert.Equal(t, cloudbuildpb.Build_SUCCESS, built.GetStatus())
}

// cbBaseImage comes from mirror.gcr.io, so Docker Hub's anonymous pull
// throttle cannot fail these builds.
const cbBaseImage = simWorkloadImage

// cbDockerStep builds one gcr.io/cloud-builders/docker build step, optionally
// naming the secretEnv variables the step wants in its environment.
func cbDockerStep(args []string, secretEnv ...string) map[string]any {
	step := map[string]any{"name": "gcr.io/cloud-builders/docker", "args": args}
	if len(secretEnv) > 0 {
		step["secretEnv"] = secretEnv
	}
	return step
}

// cbBuildBody marshals rather than interpolates, so a step argument carrying
// quotes reaches the simulator intact.
func cbBuildBody(t *testing.T, bucket, object string, steps []any, availableSecrets map[string]any) string {
	t.Helper()
	build := map[string]any{
		"source": map[string]any{
			"storageSource": map[string]any{"bucket": bucket, "object": object},
		},
		"steps": steps,
	}
	if availableSecrets != nil {
		build["availableSecrets"] = availableSecrets
	}
	raw, err := json.Marshal(build)
	require.NoError(t, err)
	return string(raw)
}

func TestCloudBuild_MissingSecretFails(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}
	project := "cb-missing-secret-project"
	bucket := "cb-missing-secret-bucket"
	createBucket(t, project, bucket)

	objectName := fmt.Sprintf("missing-cb-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{"Dockerfile": "FROM " + cbBaseImage + "\n"})
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
	_, err := waitBuild(t, readStartedBuild(t, httpPOST(t, buildURL, body)))
	require.Error(t, err, "build should fail when secret reference unresolvable")
	assert.Contains(t, err.Error(), "resolve secret")
}

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

// createBucket accepts the 409 ALREADY_EXISTS Cloud Storage answers for an
// existing bucket, so a test can rerun in one process (`-count`).
func createBucket(t *testing.T, project, name string) {
	t.Helper()
	ensureProject(t, project)
	requireProject(t, project)
	url := fmt.Sprintf("%s/storage/v1/b?project=%s", baseURL, project)
	req, err := http.NewRequest("POST", url, strings.NewReader(fmt.Sprintf(`{"name":%q}`, name)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusConflict {
		return
	}
	require.Less(t, resp.StatusCode, 400, "create bucket %s: %d %s", name, resp.StatusCode, string(body))
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
	svc, err := cloudbuild.NewService(ctx, serviceHostOptions(cloudBuildHost)...)
	require.NoError(t, err)
	return svc
}

func TestCloudBuild_TriggerCRUD(t *testing.T) {
	t.Parallel()
	ensureProject(t, "cb-trigger-project")
	svc := cloudbuildService(t)
	project := "cb-trigger-project"

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

	got, err := svc.Projects.Triggers.Get(project, created.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Id, got.Id)
	assert.Equal(t, "sdk-test-trigger", got.Name)
	assert.Equal(t, "cloudbuild.yaml", got.Filename)

	list, err := svc.Projects.Triggers.List(project).Do()
	require.NoError(t, err)
	found := false
	for _, tr := range list.Triggers {
		if tr.Id == created.Id {
			found = true
		}
	}
	assert.True(t, found, "created trigger must appear in list")

	patched, err := svc.Projects.Triggers.Patch(project, created.Id,
		&cloudbuild.BuildTrigger{
			Name:     "sdk-test-trigger",
			Filename: "ci/cloudbuild.yaml",
		}).Do()
	require.NoError(t, err)
	assert.Equal(t, "ci/cloudbuild.yaml", patched.Filename)

	_, err = svc.Projects.Triggers.Delete(project, created.Id).Do()
	require.NoError(t, err)

	_, err = svc.Projects.Triggers.Get(project, created.Id).Do()
	require.Error(t, err)
}

func TestCloudBuild_ListTriggers_Pagination(t *testing.T) {
	t.Parallel()
	const project = "test-project"
	// Check each create, and clean up by the id it answers with, which the request
	// never supplies: an unchecked create would let a trigger an earlier run left
	// behind pass the walk below.
	for _, name := range []string{"pag-trig-a", "pag-trig-b", "pag-trig-c"} {
		body, err := json.Marshal(map[string]any{
			"name": name,
			"github": map[string]any{
				"owner": "owner",
				"name":  "repo",
				"push":  map[string]any{"branch": "main"},
			},
		})
		require.NoError(t, err)
		req, _ := http.NewRequest("POST",
			fmt.Sprintf("%s/v1/projects/%s/locations/global/triggers", baseURL, project),
			bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		created, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"create trigger %s: %s", name, created)
		var trigger struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(created, &trigger))
		require.NotEmpty(t, trigger.ID, "create must answer with the trigger's id: %s", created)
		require.Equal(t, name, trigger.Name)
		id := trigger.ID
		t.Cleanup(func() {
			req, _ := http.NewRequest("DELETE",
				fmt.Sprintf("%s/v1/projects/%s/locations/global/triggers/%s", baseURL, project, id), nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		})
	}

	seen := map[string]bool{}
	var pageToken string
	pages := 0
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
		require.LessOrEqual(t, len(body.Triggers), 1, "pageSize=1 must never answer with more than one trigger")
		pages++
		require.Less(t, pages, 1000, "pagination did not terminate")
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
	require.GreaterOrEqual(t, pages, 3, "three triggers at pageSize=1 must take at least three pages")
	for _, n := range []string{"pag-trig-a", "pag-trig-b", "pag-trig-c"} {
		assert.True(t, seen[n], "trigger %s should appear via pagination", n)
	}
}
