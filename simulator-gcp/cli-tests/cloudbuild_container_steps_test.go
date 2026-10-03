package gcp_cli_test

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud builds submit` of a zip archive on Cloud Storage runs each step of
// the config as a container of its builder image over the build's workspace:
// the first step writes a file the second reads, and the build succeeds only
// if it did.
func TestCloudBuildCLI_SubmitRunsContainerStepsOverAZipSource(t *testing.T) {
	const bucket = "cli-cloudbuild-container-steps"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))

	dir := t.TempDir()
	archive, err := os.Create(filepath.Join(dir, "source.zip"))
	require.NoError(t, err)
	zw := zip.NewWriter(archive)
	w, err := zw.Create("app/input.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("from the zip\n"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, archive.Close())
	runCLI(t, gcloudCLI("storage", "cp", filepath.Join(dir, "source.zip"), "gs://"+bucket+"/source.zip"))

	config := filepath.Join(dir, "cloudbuild.yaml")
	require.NoError(t, os.WriteFile(config, []byte(`steps:
- name: `+cliWorkloadImage+`
  entrypoint: sh
  dir: app
  args: ["-c", "grep -q 'from the zip' input.txt && echo copied > copied.txt"]
- name: `+cliWorkloadImage+`
  entrypoint: sh
  args: ["-c", "test -s /workspace/app/copied.txt"]
`), 0o644))

	var submitted struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("builds", "submit", "gs://"+bucket+"/source.zip",
		"--config="+config, "--async", "--format=json")), &submitted)
	require.NotEmpty(t, submitted.ID)

	// Cloud Build offers the CLI no wait on a build, so its status is read
	// until it is terminal.
	var status string
	require.Eventually(t, func() bool {
		status = buildStatusJSON(t, submitted.ID)
		return status != "QUEUED" && status != "WORKING"
	}, 180*time.Second, time.Second, "the build never finished")
	assert.Equal(t, "SUCCESS", status)
}
