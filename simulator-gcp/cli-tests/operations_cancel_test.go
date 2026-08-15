package gcp_cli_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cancel custom method through the vendor CLI. gcloud spells cancel
// differently per service — `gcloud builds cancel` on the build resource,
// `gcloud <service> operations cancel` on the operation collection — and each
// spelling has to reach the simulator's handler for that service.

// TestCloudBuildCLI_CancelStopsARunningBuild drives `gcloud builds cancel`
// against a build that is genuinely running, on both spellings of the method
// the Discovery document declares:
//
//	POST /v1/projects/{project}/builds/{idAction}                        (legacy global path)
//	POST /v1/projects/{project}/locations/{location}/builds/{idAction}   (regional path)
//
// The build's step sleeps for an hour, so a cancel that does not terminate it
// leaves the submitting request blocked past this test's deadline.
func TestCloudBuildCLI_CancelStopsARunningBuild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{name: "global"},
		{name: "regional", flags: []string{"--region", "us-central1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bucket := "cli-cloudbuild-cancel-" + tc.name
			object := "source.tar.gz"
			image := "sockerless-cli-cancel-" + tc.name + ":test"
			t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

			uploadCancelSource(t, bucket, object, "FROM alpine:latest\nRUN sleep 3600\n")

			id := submitCancellableBuild(t, bucket, object, image)

			args := append([]string{"builds", "cancel", id, "--format=json"}, tc.flags...)
			out := runCLI(t, gcloudCLI(args...))
			assert.Contains(t, out, "CANCELLED",
				"gcloud builds cancel reports the build's new status")

			require.Eventually(t, func() bool {
				return buildStatusJSON(t, id) == "CANCELLED"
			}, 60*time.Second, 500*time.Millisecond,
				"the cancel did not stop the running build step")
		})
	}
}

// TestCloudLoggingCLI_OperationsCancel drives `gcloud logging operations
// cancel` on the project scope, which shares its operation collection URI with
// every other service the simulator serves under it.
func TestCloudLoggingCLI_OperationsCancel(t *testing.T) {
	createURL := fmt.Sprintf("%s/v2/projects/%s/locations/us-central1/buckets:createAsync?bucketId=cli-cancel-bucket",
		baseURL, project)
	out := httpDoJSON(t, "POST", createURL, `{}`)
	var op struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &op)
	require.NotEmpty(t, op.Name)
	opID := op.Name[strings.LastIndex(op.Name, "/")+1:]

	runCLI(t, gcloudCLI("logging", "operations", "cancel", opID,
		"--location", "us-central1",
		"--project", project,
		"--format=json",
	))

	// A name no operation was minted under must not be cancellable.
	err := gcloudCLI("logging", "operations", "cancel", "never-minted",
		"--location", "us-central1",
		"--project", project,
		"--format=json",
	).Run()
	require.Error(t, err, "gcloud must report the NOT_FOUND the service answers")
}

// TestMemorystoreRedisCLI_OperationsCancel drives `gcloud redis operations
// cancel`.
func TestMemorystoreRedisCLI_OperationsCancel(t *testing.T) {
	name := "cli-redis-cancel"
	runCLI(t, gcloudCLI("redis", "instances", "create", name,
		"--region", location,
		"--size", "1",
		"--tier", "basic",
		"--async",
		"--quiet",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("redis", "instances", "delete", name,
			"--region", location, "--quiet", "--format=json").Run()
	})

	out := runCLI(t, gcloudCLI("redis", "operations", "list",
		"--region", location, "--format=json"))
	var ops []struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &ops)
	require.NotEmpty(t, ops, "the create minted an operation")
	opID := ops[0].Name[strings.LastIndex(ops[0].Name, "/")+1:]

	runCLI(t, gcloudCLI("redis", "operations", "cancel", opID,
		"--region", location, "--quiet", "--format=json"))

	require.Error(t, gcloudCLI("redis", "operations", "cancel", "never-minted",
		"--region", location, "--quiet", "--format=json").Run(),
		"gcloud must report the NOT_FOUND the service answers")
}

// TestCloudSQLCLI_OperationsCancel drives `gcloud sql operations cancel`.
// Cloud SQL's method is the service's own and refuses an operation that is not
// in progress, so the CLI reports the failure rather than a silent success.
func TestCloudSQLCLI_OperationsCancel(t *testing.T) {
	instance := "cli-sql-cancel"
	runCLI(t, gcloudCLI("sql", "instances", "create", instance,
		"--database-version", "POSTGRES_15",
		"--tier", "db-f1-micro",
		"--region", location,
		"--quiet",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("sql", "instances", "delete", instance, "--quiet", "--format=json").Run()
	})

	out := runCLI(t, gcloudCLI("sql", "operations", "list",
		"--instance", instance, "--format=json"))
	var ops []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	parseJSON(t, out, &ops)
	require.NotEmpty(t, ops)
	require.Equal(t, "DONE", ops[0].Status)

	require.Error(t, gcloudCLI("sql", "operations", "cancel", ops[0].Name,
		"--quiet", "--format=json").Run(),
		"Cloud SQL refuses a cancel of an operation that is not in progress")
}

// TestServiceUsageCLI_OperationsCancel drives the cancel Service Usage
// declares on its top-level operations collection —
// POST /v1/operations/{opAction...} — which Cloud Build's global build
// operations share. gcloud has no `services operations` group, so the request
// is issued the way a client without one does: against the published wire
// path, with the CLI's own credential.
func TestServiceUsageCLI_OperationsCancel(t *testing.T) {
	enableURL := fmt.Sprintf("%s/v1/projects/%s/services/pubsub.googleapis.com:enable", baseURL, project)
	out := httpDoJSON(t, "POST", enableURL, `{}`)
	var op struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &op)
	require.True(t, strings.HasPrefix(op.Name, "operations/"),
		"Service Usage names its operations operations/{id}; got %q", op.Name)

	httpDoJSON(t, "POST", fmt.Sprintf("%s/v1/%s:cancel", baseURL, op.Name), `{}`)

	resp, err := httpDo("POST", fmt.Sprintf("%s/v1/operations/never-minted:cancel", baseURL), `{}`)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "a name no operation was minted under is not cancellable")
}

// uploadCancelSource writes the gzipped-tar build context Cloud Build fetches,
// through the Cloud Storage JSON API the same way a client uploads one.
func uploadCancelSource(t *testing.T, bucket, object, dockerfile string) {
	t.Helper()
	httpDoJSON(t, "POST", fmt.Sprintf("%s/storage/v1/b?project=%s", baseURL, project),
		fmt.Sprintf(`{"name":%q}`, bucket))

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte(dockerfile)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(body))}))
	_, err := tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s", baseURL, bucket, object),
		&buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Less(t, resp.StatusCode, 400, "source upload failed")
}

// submitCancellableBuild submits a build whose step cannot finish on its own
// and returns its id once the simulator reports it running. The submit blocks
// until the build settles, so it runs on its own goroutine.
func submitCancellableBuild(t *testing.T, bucket, object, image string) string {
	t.Helper()
	body := fmt.Sprintf(`{
		"source": {"storageSource": {"bucket": %q, "object": %q}},
		"steps": [{"name": "gcr.io/cloud-builders/docker", "args": ["build", "-t", %q, "."]}]
	}`, bucket, object, image)
	go func() {
		resp, err := httpDo("POST", fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project), body)
		if err == nil {
			resp.Body.Close()
		}
	}()

	var id string
	require.Eventually(t, func() bool {
		out := runCLI(t, gcloudCLI("builds", "list", "--format=json"))
		var builds []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(out), &builds); err != nil {
			return false
		}
		for _, b := range builds {
			if b.Status == "WORKING" {
				id = b.ID
				return true
			}
		}
		return false
	}, 120*time.Second, 500*time.Millisecond, "no build reached WORKING")

	// Well inside the step's own hour, so the cancel below is cancelling work
	// that is genuinely in flight.
	time.Sleep(10 * time.Second)
	require.Equal(t, "WORKING", buildStatusJSON(t, id), "the build is still running when it is cancelled")
	return id
}

func buildStatusJSON(t *testing.T, id string) string {
	t.Helper()
	out := runCLI(t, gcloudCLI("builds", "describe", id, "--format=json"))
	var b struct {
		Status string `json:"status"`
	}
	parseJSON(t, out, &b)
	return b.Status
}
