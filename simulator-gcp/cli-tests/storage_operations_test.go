package gcp_cli_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud storage buckets relocate` and the `gcloud storage operations` group,
// driven as real CLI invocations against the bucket operations collection:
//
//	POST /storage/v1/b/{bucket}/relocate
//	GET  /storage/v1/b/{bucket}/operations
//	GET  /storage/v1/b/{bucket}/operations/{operationId}
//	POST /storage/v1/b/{bucket}/operations/{operationId}/cancel
//	POST /storage/v1/b/{bucket}/operations/{operationId}/advanceRelocateBucket
//
// The relocation is what mints an operation, and every operations command then
// addresses that record by the `projects/_/buckets/{bucket}/operations/{id}`
// name the relocation returned — which is exactly how the commands' own
// examples spell it.

// gcloudCLIFails runs a gcloud command that is expected to fail and returns its
// combined output.
func gcloudCLIFails(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "command unexpectedly succeeded: %s", out)
	return string(out)
}

// operationNameFrom picks the operation resource name out of a gcloud command's
// combined output, which also carries the relocation's write-downtime warnings.
func operationNameFrom(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "projects/_/buckets/") {
			return line
		}
	}
	t.Fatalf("no operation name in gcloud output: %s", out)
	return ""
}

// assertStorageOperationStillDone reads a bucket operation back and pins the
// record a best-effort advance or cancel leaves behind: the operation is still
// there, still done, and still carries no error.
func assertStorageOperationStillDone(t *testing.T, name string) {
	t.Helper()
	out := runCLI(t, gcloudCLI("storage", "operations", "describe", name, "--format=json"))
	var op struct {
		Name  string `json:"name"`
		Done  bool   `json:"done"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	parseJSONObject(t, out, &op)
	assert.Equal(t, name, op.Name, "output: %s", out)
	assert.True(t, op.Done, "the operation stays settled: %s", out)
	assert.Nil(t, op.Error, "the operation is not turned into a failure: %s", out)
}

func TestGCSCLI_BucketRelocateAndOperations(t *testing.T) {
	const bucket = "cli-storage-ops-bucket"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us"))
	t.Cleanup(func() {
		_ = gcloudCLI("storage", "buckets", "delete", "gs://"+bucket).Run()
	})

	// A bucket that has started no long-running work has no operations.
	empty := runCLI(t, gcloudCLI("storage", "operations", "list",
		"projects/_/buckets/"+bucket, "--format=value(name)"))
	assert.NotContains(t, empty, "projects/_/buckets/"+bucket+"/operations/")

	// The relocation both moves the bucket and records the operation that did.
	relocated := runCLI(t, gcloudCLI("storage", "buckets", "relocate", "gs://"+bucket,
		"--location=us-east1", "--format=value(name)"))
	name := operationNameFrom(t, relocated)
	require.True(t, strings.HasPrefix(name, "projects/_/buckets/"+bucket+"/operations/"), "name: %s", name)

	location := runCLI(t, gcloudCLI("storage", "buckets", "describe", "gs://"+bucket,
		"--format=value(location)"))
	assert.Equal(t, "US-EAST1", strings.TrimSpace(location),
		"the relocation moved the bucket it reported moving")

	described := runCLI(t, gcloudCLI("storage", "operations", "describe", name,
		"--format=value(name,done)"))
	assert.Contains(t, described, name)

	listed := runCLI(t, gcloudCLI("storage", "operations", "list",
		"projects/_/buckets/"+bucket, "--format=value(name)"))
	assert.Contains(t, listed, name)

	// The AIP-160 term `--server-filter` documents.
	done := runCLI(t, gcloudCLI("storage", "operations", "list",
		"projects/_/buckets/"+bucket, "--server-filter=done = true", "--format=value(name)"))
	assert.Contains(t, done, name)
	running := runCLI(t, gcloudCLI("storage", "operations", "list",
		"projects/_/buckets/"+bucket, "--server-filter=done = false", "--format=value(name)"))
	assert.NotContains(t, running, name)

	// Advancing and cancelling both resolve the operation first. The
	// relocation finished inside the request that started it, so both are the
	// late calls the methods contemplate: each answers an empty body and
	// leaves the settled record exactly as it stands, which is read back off
	// the operation rather than inferred from the exit status.
	runCLI(t, gcloudCLI("storage", "buckets", "relocate",
		"--operation="+name, "--finalize", "--ttl=7h"))
	assertStorageOperationStillDone(t, name)

	runCLI(t, gcloudCLI("storage", "operations", "cancel", name))
	assertStorageOperationStillDone(t, name)

	// And the bucket the relocation moved is still where it moved it.
	assert.Equal(t, "US-EAST1", strings.TrimSpace(runCLI(t, gcloudCLI(
		"storage", "buckets", "describe", "gs://"+bucket, "--format=value(location)"))))

	// An identifier the service never minted is not an operation, from any of
	// the three commands.
	missing := "projects/_/buckets/" + bucket + "/operations/never-minted"
	assert.Contains(t, gcloudCLIFails(t, gcloudCLI("storage", "operations", "describe", missing)), "404")
	assert.Contains(t, gcloudCLIFails(t, gcloudCLI("storage", "operations", "cancel", missing)), "404")
	assert.Contains(t, gcloudCLIFails(t, gcloudCLI("storage", "buckets", "relocate",
		"--operation="+missing, "--finalize", "--ttl=7h")), "404")
}
