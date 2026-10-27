package gcp_cli_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcloud spells cancel differently per service, `gcloud builds cancel` on the
// build and `gcloud <service> operations cancel` on the operation collection,
// and each spelling must reach that service's handler.

// TestCloudBuildCLI_CancelStopsARunningBuild cancels a running build on both
// paths the Discovery document declares:
//
//	POST /v1/projects/{project}/builds/{idAction}                        (legacy global path)
//	POST /v1/projects/{project}/locations/{location}/builds/{idAction}   (regional path)
//
// The build's step sleeps for an hour, so the cancel lands on a running build.
func TestCloudBuildCLI_CancelStopsARunningBuild(t *testing.T) {
	t.Parallel()
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
			assert.Equal(t, "CANCELLED", buildStatusJSON(t, id),
				"gcloud builds describe reads the cancel back")
		})
	}
}

// TestCloudLoggingCLI_OperationsCancel cancels on the project scope, whose
// operation collection URI every service under it shares.
func TestCloudLoggingCLI_OperationsCancel(t *testing.T) {
	t.Parallel()
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

	// The operation was complete before the client saw its name, so CancelOperation
	// leaves the recorded result standing; read it off the operation, not off the
	// cancel's empty body.
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

func TestMemorystoreRedisCLI_OperationsCancel(t *testing.T) {
	t.Parallel()
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

	// Every service's operations share the region-wide list, so pick the one whose
	// metadata targets this instance.
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

	// The create finished inside its request, so the cancel leaves the settled
	// record as it stands.
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

// assertOperationCompletedDespiteCancel pins what CancelOperation documents for
// finished work: same name, done, no error, and the resource its method
// produced.
func assertOperationCompletedDespiteCancel(t *testing.T, out, wantName, wantResource string) {
	t.Helper()
	var settled struct {
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	assertOperationSettledDespiteCancel(t, out, wantName, &settled)
	assert.Equal(t, wantResource, settled.Response.Name,
		"the recorded result stands after the cancel: %s", out)
}

// assertOperationSettledDespiteCancel decodes the settled record into result
// for the caller to check the response its service defines.
func assertOperationSettledDespiteCancel(t *testing.T, out, wantName string, result any) {
	t.Helper()
	var settled struct {
		Name  string `json:"name"`
		Done  bool   `json:"done"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	parseJSONObject(t, out, &settled)
	parseJSONObject(t, out, result)
	assert.Equal(t, wantName, settled.Name, "output: %s", out)
	assert.True(t, settled.Done, "the operation stays settled after the cancel: %s", out)
	assert.Nil(t, settled.Error, "a completed operation is not turned into a failure by a cancel: %s", out)
}

// TestCloudSQLCLI_OperationsCancel: Cloud SQL's own cancel refuses an
// operation that is not in progress, so the CLI reports a failure.
func TestCloudSQLCLI_OperationsCancel(t *testing.T) {
	t.Parallel()
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

// TestServiceUsageCLI_OperationsCancel cancels on Service Usage's top-level
// POST /v1/operations/{opAction...}, which Cloud Build's global build operations
// share. gcloud has no `services operations` group, so send the request to the
// published path with the CLI's own credential.
func TestServiceUsageCLI_OperationsCancel(t *testing.T) {
	t.Parallel()
	enableURL := fmt.Sprintf("%s/v1/projects/%s/services/pubsub.googleapis.com:enable", baseURL, project)
	out := httpDoJSON(t, "POST", enableURL, `{}`)
	var op struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &op)
	require.True(t, strings.HasPrefix(op.Name, "operations/"),
		"Service Usage names its operations operations/{id}; got %q", op.Name)

	httpDoJSON(t, "POST", fmt.Sprintf("%s/v1/%s:cancel", baseURL, op.Name), `{}`)

	// The enable finished inside its request, so the record still carries the
	// EnableServiceResponse, which names the service under `service`.
	settledOut := httpDoJSON(t, "GET", fmt.Sprintf("%s/v1/%s", baseURL, op.Name), "")
	var enabled struct {
		Response struct {
			Type    string `json:"@type"`
			Service struct {
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"service"`
		} `json:"response"`
	}
	assertOperationSettledDespiteCancel(t, settledOut, op.Name, &enabled)
	assert.Equal(t, "type.googleapis.com/google.api.serviceusage.v1.EnableServiceResponse", enabled.Response.Type)
	assert.Equal(t, fmt.Sprintf("projects/%s/services/pubsub.googleapis.com", project), enabled.Response.Service.Name,
		"the recorded result stands after the cancel: %s", settledOut)
	assert.Equal(t, "ENABLED", enabled.Response.Service.State)

	resp, err := httpDo("POST", fmt.Sprintf("%s/v1/operations/never-minted:cancel", baseURL), `{}`)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode, "a name no operation was minted under is not cancellable")
}

// TestServiceUsageCLI_OperationsList lists with the CLI's credential, since
// gcloud has no `services operations` group: the list holds the enable Service
// Usage started and not the project create Cloud Resource Manager started.
func TestServiceUsageCLI_OperationsList(t *testing.T) {
	t.Parallel()
	projectID := uniquename.New("su-ops-list")
	runCLI(t, gcloudCLI("projects", "create", projectID, "--organization="+cliOrganizationID, "--format=json"))
	out := httpDoJSON(t, "POST",
		fmt.Sprintf("%s/v1/projects/%s/services/pubsub.googleapis.com:enable", baseURL, projectID), `{}`)
	var enable struct {
		Name string `json:"name"`
	}
	parseJSON(t, out, &enable)

	var listed struct {
		Operations []struct {
			Name     string         `json:"name"`
			Metadata map[string]any `json:"metadata"`
		} `json:"operations"`
	}
	parseJSON(t, httpDoJSON(t, "GET", baseURL+"/v1/operations?name="+enable.Name, ""), &listed)
	require.Len(t, listed.Operations, 1, "the name scopes the list to the enable")
	assert.Equal(t, enable.Name, listed.Operations[0].Name)

	parseJSON(t, httpDoJSON(t, "GET", baseURL+"/v1/operations", ""), &listed)
	for _, op := range listed.Operations {
		assert.Equal(t, "type.googleapis.com/google.api.serviceusage.v1.OperationMetadata", op.Metadata["@type"],
			"Service Usage lists only operations it started, got %s", op.Name)
	}
}

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

// submitCancellableBuild returns the id once the build's step runs.
// CreateBuild answers at once with an operation whose BuildOperationMetadata
// names the build.
func submitCancellableBuild(t *testing.T, bucket, object, image string) string {
	t.Helper()
	body := fmt.Sprintf(`{
		"source": {"storageSource": {"bucket": %q, "object": %q}},
		"steps": [{"name": "gcr.io/cloud-builders/docker", "args": ["build", "-t", %q, "."]}]
	}`, bucket, object, image)
	var op struct {
		Done     bool `json:"done"`
		Metadata struct {
			Build struct {
				ID string `json:"id"`
			} `json:"build"`
		} `json:"metadata"`
	}
	parseJSON(t, httpDoJSON(t, "POST", fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project), body), &op)
	require.False(t, op.Done, "the operation comes back before the build runs")
	id := op.Metadata.Build.ID
	require.NotEmpty(t, id, "the operation's metadata names the build")

	// Cloud Build signals no step start, so read the step's status until it runs.
	require.Eventually(t, func() bool {
		return buildFirstStepStatusJSON(t, id) == "WORKING"
	}, 120*time.Second, 50*time.Millisecond, "the build's first step never started running")
	require.Equal(t, "WORKING", buildStatusJSON(t, id), "the build is still running when it is cancelled")
	return id
}

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
