package gcp_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGCSCLI_BucketAndObjectLifecycle(t *testing.T) {
	bucket := "cli-gcs-bucket"
	object := "hello.txt"

	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))

	source := filepath.Join(tmpDir, "gcs-source.txt")
	if err := os.WriteFile(source, []byte("hello from gcloud storage"), 0644); err != nil {
		t.Fatalf("failed to write source object: %v", err)
	}

	runCLI(t, gcloudCLI("storage", "cp", source, "gs://"+bucket+"/"+object))

	out := runCLI(t, gcloudCLI("storage", "objects", "list", "gs://"+bucket, "--format=json"))
	if !strings.Contains(out, object) {
		t.Fatalf("gcloud storage objects list did not include %q: %s", object, out)
	}

	got := runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/"+object))
	if strings.TrimSpace(got) != "hello from gcloud storage" {
		t.Fatalf("gcloud storage cat returned %q", got)
	}

	runCLI(t, gcloudCLI("storage", "rm", "gs://"+bucket+"/"+object))
	runCLI(t, gcloudCLI("storage", "buckets", "delete", "gs://"+bucket))
}
