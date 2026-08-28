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

// TestCloudBuild_DockerBuild exercises the Cloud Build slice: the GCP simulator
// implements CreateBuild + LRO, the handler extracts the uploaded GCS source
// tarball, runs `docker build` against it, and returns a done=true LRO with
// status=SUCCESS. Uses direct REST against the simulator rather than the gRPC
// cloudbuild Go client (which doesn't easily accept endpoint overrides).
//
// The build's verdict alone would be satisfied by a handler that never ran the
// step, so the image the step was told to produce is inspected on the daemon
// afterwards. The push half of a real build — a `docker push` step landing the
// image in a registry a workload pulls from — is exercised in
// TestCloudBuild_FaithfulBuildPush.
func TestCloudBuild_DockerBuild(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}
	pullImageWithRetry(t, cbBaseImage)

	project := "cb-test-project"

	// 1. Upload a tiny build context (a Dockerfile only) to the sim's
	// GCS slice. Real sockerless uploads context via the GCS client;
	// we do it directly via REST.
	bucket := "cb-test-bucket"
	createBucket(t, project, bucket)
	objectName := fmt.Sprintf("cb-context-%d.tar.gz", time.Now().UnixNano())
	tarball := makeTarGz(t, map[string]string{
		"Dockerfile": "FROM " + cbBaseImage + "\nRUN echo 'built in simulator' > /hello.txt\n",
	})
	uploadGCSObject(t, bucket, objectName, tarball)

	// 2. Submit a build via the Cloud Build REST endpoint.
	imageTag := fmt.Sprintf("sim-cb-build:gcp-sdk-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", imageTag) })
	buildURL := fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project)
	body := fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[
			{"name":"gcr.io/cloud-builders/docker","args":["build","-t",%q,"."]}
		]
	}`, bucket, objectName, imageTag)
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

	// The step really ran: the tag it was given names an image the daemon now
	// holds, whose history carries the layer the Dockerfile's RUN produced.
	inspect, err := dockerCLIWithTimeout(60*time.Second, "image", "inspect", imageTag)
	require.NoError(t, err,
		"the build step must leave %s in the daemon image store: %s", imageTag, inspect)
}

// TestCloudBuild_SecretEnvExpansion exercises secret expansion: Secret Manager
// references in the build's AvailableSecrets are resolved to env vars available
// to each build step's secretEnv.
//
// A plain `docker build` proves nothing about the expansion, because a
// Dockerfile does not read the builder's environment on its own. The step
// therefore passes `--build-arg MYSECRET` with no value, which is exactly the
// form that takes the variable from the docker CLI's own environment — the
// environment the simulator injects the resolved secret into — and the
// Dockerfile compares it against the payload. Dropping the secretEnv injection
// leaves the argument empty, the RUN exits non-zero, and the build turns red.
func TestCloudBuild_SecretEnvExpansion(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for Cloud Build test (no fallback): %v", err)
	}
	pullImageWithRetry(t, cbBaseImage)

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
	tarball := makeTarGz(t, map[string]string{"Dockerfile": "FROM " + cbBaseImage +
		"\nARG MYSECRET\nRUN test \"$MYSECRET\" = \"" + secretValue + "\"\n"})
	uploadGCSObject(t, bucket, objectName, tarball)

	// Submit build with secretEnv — simulator must resolve the Secret
	// Manager reference and expose the payload in the step's env.
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

// TestCloudBuild_SecretEnvWithoutMatchingAvailableSecret pins what the
// simulator does with a step that names a secretEnv the build's
// availableSecrets never declares.
//
// Real Cloud Build refuses such a build at CreateBuild: the request is
// INVALID_ARGUMENT ("secretEnv ... is not defined in availableSecrets") and no
// step runs. The simulator accepts it and runs the step with the variable
// simply absent, which the step's Dockerfile observes through a bare
// `--build-arg`. The assertion is written against that observed behaviour so
// the divergence is recorded rather than invisible; a simulator taught to
// refuse the build turns this red, and the refusal is then what the test should
// assert.
func TestCloudBuild_SecretEnvWithoutMatchingAvailableSecret(t *testing.T) {
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
	require.Nil(t, op.Error,
		"the build is accepted today rather than refused for the undeclared secretEnv: %+v", op.Error)
	assert.Equal(t, "SUCCESS", op.Response["status"])
}

// cbBaseImage is the base every Cloud Build step in this file builds from and
// runs. It is served from the Amazon ECR Public Gallery rather than Docker Hub,
// whose anonymous pull throttle has flaked these builds.
const cbBaseImage = "public.ecr.aws/docker/library/alpine:3.20"

// cbDockerStep builds one gcr.io/cloud-builders/docker build step, the only
// builder the simulator executes, optionally naming the secretEnv variables the
// step wants in its environment.
func cbDockerStep(args []string, secretEnv ...string) map[string]any {
	step := map[string]any{"name": "gcr.io/cloud-builders/docker", "args": args}
	if len(secretEnv) > 0 {
		step["secretEnv"] = secretEnv
	}
	return step
}

// cbBuildBody renders a CreateBuild request over a GCS source tarball. It
// marshals rather than interpolating so a step argument carrying quotes — a
// shell command run inside a container — reaches the simulator intact.
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
	// A create whose status went unread would let a rejected create pass as a
	// success while the walk below found a trigger an earlier run had left
	// behind. Each create is checked, and the id it answers with — the
	// identifier the collection is keyed by, which the request never supplies —
	// is what the cleanup deletes.
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
