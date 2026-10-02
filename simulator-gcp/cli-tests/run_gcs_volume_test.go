package gcp_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud run jobs create --add-volume type=cloud-storage` mounts a bucket
// into a Cloud Run job, and what the job writes, removes and renames through
// the mount `gcloud run jobs execute --wait` returns from as object changes,
// as Cloud Storage FUSE makes them. The second volume mounts one directory of
// the bucket through the only-dir mount option.
func TestCloudRun_CLI_JobWritesThroughCloudStorageVolume(t *testing.T) {
	const bucket = "cli-run-gcs-volume"
	const job = "cli-gcs-volume-job"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))
	for name, data := range map[string]string{"seed.txt": "seed\n", "rename-me.txt": "renamed\n", "delete-me.txt": "doomed\n"} {
		source := filepath.Join(tmpDir, "gcs-volume-"+name)
		require.NoError(t, os.WriteFile(source, []byte(data), 0o644))
		runCLI(t, gcloudCLI("storage", "cp", source, "gs://"+bucket+"/"+name))
	}

	// The script holds commas, so the list flag takes another delimiter
	// (gcloud topic escaping).
	script := strings.Join([]string{
		"set -e",
		"cat /mnt/bucket/seed.txt > /mnt/results/copy.txt",
		"echo appended >> /mnt/bucket/seed.txt",
		"mkdir /mnt/bucket/made",
		"echo inside > /mnt/bucket/made/inside.txt",
		"mv /mnt/bucket/rename-me.txt /mnt/bucket/renamed.txt",
		"rm /mnt/bucket/delete-me.txt",
	}, "\n")
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "create", job,
		"--region="+location,
		"--image=alpine:latest",
		"--command=sh",
		"--args=^|^-c|"+script,
		"--max-retries=0",
		"--task-timeout=60s",
		"--add-volume=name=bucket,type=cloud-storage,bucket="+bucket,
		"--add-volume=name=results,type=cloud-storage,bucket="+bucket+",mount-options=only-dir=results",
		"--add-volume-mount=volume=bucket,mount-path=/mnt/bucket",
		"--add-volume-mount=volume=results,mount-path=/mnt/results",
		"--quiet",
	))
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", jobURL(job), "")
		if err == nil {
			resp.Body.Close()
		}
	})
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "execute", job, "--region="+location, "--wait", "--quiet"))

	listed := runCLI(t, gcloudCLI("storage", "objects", "list", "gs://"+bucket+"/**", "--format=value(name)"))
	names := strings.Fields(listed)
	assert.ElementsMatch(t, []string{"made/", "made/inside.txt", "renamed.txt", "results/copy.txt", "seed.txt"}, names, listed)
	assert.Equal(t, "seed\nappended\n", runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/seed.txt")))
	assert.Equal(t, "seed\n", runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/results/copy.txt")))
	assert.Equal(t, "inside\n", runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/made/inside.txt")))
	assert.Equal(t, "renamed\n", runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/renamed.txt")))
	described := runCLI(t, gcloudCLI("storage", "objects", "describe", "gs://"+bucket+"/seed.txt", "--format=json"))
	assert.Contains(t, described, "gcsfuse_mtime", "the generation records the file's modification time")
}
