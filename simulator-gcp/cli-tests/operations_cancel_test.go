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
			image := "sockerless-cli-cancel-" + tc.name + ":gcp-cli"
			t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

			uploadCancelSource(t, bucket, object, "FROM "+cliWorkloadImage+"\nRUN sleep 3600\n")

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

	// What the cancel did is read off the operation, not off the cancel's own
	// empty body: this operation was already complete when its name reached
	// the client, so the outcome the method documents is "the operation
	// completed despite cancellation" — the recorded result stands untouched.
	// A cancel that dropped the record, blanked its response or turned it into
	// an error does not survive this read.
	out = runCLI(t, gcloudCLI("logging", "operations", "describe", opID,
		"--location", "us-central1",
		"--project", project,
		"--format=json",
	))
	assertOperationCompletedDespiteCancel(t, out, op.Name,
		fmt.Sprintf("projects/%s/locations/us-central1/buckets/cli-cancel-bucket", project))

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

	// The list is region-wide and every service's operations share it, so the
	// create's operation is the one whose metadata targets this instance —
	// not whichever row happens to come first.
	instanceName := fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, name)
	out := runCLI(t, gcloudCLI("redis", "operations", "list",
		"--region", location, "--format=json"))
	var ops []struct {
		Name     string `json:"name"`
		Metadata struct {
			Target string `json:"target"`
		} `json:"metadata"`
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &ops)
	opName := ""
	for _, op := range ops {
		if op.Metadata.Target == instanceName || op.Response.Name == instanceName {
			opName = op.Name
		}
	}
	require.NotEmpty(t, opName, "the create minted an operation targeting %s: %s", instanceName, out)
	opID := opName[strings.LastIndex(opName, "/")+1:]

	runCLI(t, gcloudCLI("redis", "operations", "cancel", opID,
		"--region", location, "--quiet", "--format=json"))

	// Read the operation back: the create finished inside the request that
	// returned it, so the cancel leaves the settled record exactly as it
	// stands rather than erasing or failing it.
	described := runCLI(t, gcloudCLI("redis", "operations", "describe", opID,
		"--region", location, "--format=json"))
	assertOperationCompletedDespiteCancel(t, described, opName, instanceName)

	badOut, badErr := gcloudCLI("redis", "operations", "cancel", "never-minted",
		"--region", location, "--quiet", "--format=json").CombinedOutput()
	require.Error(t, badErr, "cancelling a name no operation was minted under must fail: %s", badOut)
	assert.Contains(t, string(badOut), "NOT_FOUND",
		"gcloud must report the NOT_FOUND the service answers: %s", badOut)
	assert.Contains(t, string(badOut), "never-minted")
}

// assertOperationCompletedDespiteCancel reads a cancelled operation's record
// and pins the outcome CancelOperation documents for work that had already
// finished: the operation is still named what it was, still done, carries no
// error, and still carries the resource its method produced.
func assertOperationCompletedDespiteCancel(t *testing.T, out, wantName, wantResource string) {
	t.Helper()
	var settled struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	parseJSONObject(t, out, &settled)
	assert.Equal(t, wantName, settled.Name, "output: %s", out)
	assert.True(t, settled.Done, "the operation stays settled after the cancel: %s", out)
	assert.Nil(t, settled.Error, "a completed operation is not turned into a failure by a cancel: %s", out)
	assert.Equal(t, wantResource, settled.Response.Name,
		"the recorded result stands after the cancel: %s", out)
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

	// The enable finished inside the request that returned the operation, so
	// the cancel is the late one the method describes: the record still stands
	// after it, carrying the EnableServiceResponse the enable produced.
	assertOperationCompletedDespiteCancel(t,
		httpDoJSON(t, "GET", fmt.Sprintf("%s/v1/%s", baseURL, op.Name), ""),
		op.Name, fmt.Sprintf("projects/%s/services/pubsub.googleapis.com", project))

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

	// WORKING is recorded before the source is even fetched, so it alone does
	// not mean a step is executing. The step's own status is what says the RUN
	// is under way, and it is what makes the cancel below cancel work that is
	// genuinely in flight.
	require.Eventually(t, func() bool {
		return buildFirstStepStatusJSON(t, id) == "WORKING"
	}, 120*time.Second, 50*time.Millisecond, "the build's first step never started running")
	require.Equal(t, "WORKING", buildStatusJSON(t, id), "the build is still running when it is cancelled")
	return id
}

// buildFirstStepStatusJSON reports the status the build gives its first step,
// which is how a client sees which part of a running build is executing.
func buildFirstStepStatusJSON(t *testing.T, id string) string {
	t.Helper()
	var build struct {
		Steps []struct {
			Status string `json:"status"`
		} `json:"steps"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("builds", "describe", id, "--format=json")), &build)
	if len(build.Steps) == 0 {
		return ""
	}
	return build.Steps[0].Status
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
