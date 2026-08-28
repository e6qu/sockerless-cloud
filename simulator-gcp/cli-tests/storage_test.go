package gcp_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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

// Soft delete through the vendor CLI. `gcloud storage rm` retires the object
// under the bucket's policy, `--soft-deleted` lists what can be brought back,
// and `gcloud storage restore` brings it back with its bytes.
func TestGCSCLI_SoftDeleteListAndRestore(t *testing.T) {
	bucket := "cli-soft-delete-bucket"
	object := "retired.txt"

	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))

	source := filepath.Join(tmpDir, "soft-delete-source.txt")
	if err := os.WriteFile(source, []byte("retained bytes"), 0644); err != nil {
		t.Fatalf("failed to write source object: %v", err)
	}
	runCLI(t, gcloudCLI("storage", "cp", source, "gs://"+bucket+"/"+object))
	runCLI(t, gcloudCLI("storage", "rm", "gs://"+bucket+"/"+object))

	live := runCLI(t, gcloudCLI("storage", "objects", "list", "gs://"+bucket, "--format=json"))
	if strings.Contains(live, object) {
		t.Fatalf("a deleted object must not appear in the live listing: %s", live)
	}

	retired := runCLI(t, gcloudCLI("storage", "objects", "list", "gs://"+bucket,
		"--soft-deleted", "--format=json"))
	if !strings.Contains(retired, object) {
		t.Fatalf("gcloud storage objects list --soft-deleted did not include %q: %s", object, retired)
	}
	// gcloud renders the resource in its own snake_case projection, so the
	// assertion is on what the CLI shows rather than on the wire spelling the
	// SDK test already pins.
	if !strings.Contains(retired, "soft_delete_time") || !strings.Contains(retired, "hard_delete_time") {
		t.Fatalf("a soft-deleted listing must carry both delete times: %s", retired)
	}

	generation := cliObjectGeneration(t, retired)
	runCLI(t, gcloudCLI("storage", "restore", "gs://"+bucket+"/"+object+"#"+generation))

	got := runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/"+object))
	if strings.TrimSpace(got) != "retained bytes" {
		t.Fatalf("the restored object lost its payload: %q", got)
	}

	runCLI(t, gcloudCLI("storage", "rm", "gs://"+bucket+"/"+object))
	runCLI(t, gcloudCLI("storage", "buckets", "delete", "gs://"+bucket))
}

// cliObjectGeneration pulls the generation out of a `--format=json` listing so
// the restore addresses the exact retired generation, the way the command
// requires.
func cliObjectGeneration(t *testing.T, listing string) string {
	t.Helper()
	var objects []struct {
		Generation any `json:"generation"`
	}
	if err := json.Unmarshal([]byte(listing), &objects); err != nil {
		t.Fatalf("parse soft-deleted listing: %v\n%s", err, listing)
	}
	if len(objects) != 1 {
		t.Fatalf("expected one soft-deleted object, got %d: %s", len(objects), listing)
	}
	switch generation := objects[0].Generation.(type) {
	case string:
		return generation
	case float64:
		return strconv.FormatInt(int64(generation), 10)
	default:
		t.Fatalf("soft-deleted listing carried no generation: %s", listing)
		return ""
	}
}

// Per-object access controls through the vendor CLI. `gcloud storage objects
// update --add-acl-grant` writes one entry and `--remove-acl-grant` takes it
// away; both read the object's ACL back, which is the surface that used to be
// answered by the object handler.
func TestGCSCLI_ObjectACLGrants(t *testing.T) {
	bucket := "cli-object-acl-bucket"
	object := "granted.txt"

	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))

	source := filepath.Join(tmpDir, "object-acl-source.txt")
	if err := os.WriteFile(source, []byte("granted"), 0644); err != nil {
		t.Fatalf("failed to write source object: %v", err)
	}
	runCLI(t, gcloudCLI("storage", "cp", source, "gs://"+bucket+"/"+object))

	runCLI(t, gcloudCLI("storage", "objects", "update", "gs://"+bucket+"/"+object,
		"--add-acl-grant=entity=allUsers,role=READER"))

	described := runCLI(t, gcloudCLI("storage", "objects", "describe", "gs://"+bucket+"/"+object,
		"--format=json"))
	if !strings.Contains(described, "allUsers") {
		t.Fatalf("the granted entity is missing from the object's ACL: %s", described)
	}

	runCLI(t, gcloudCLI("storage", "objects", "update", "gs://"+bucket+"/"+object,
		"--remove-acl-grant=allUsers"))

	described = runCLI(t, gcloudCLI("storage", "objects", "describe", "gs://"+bucket+"/"+object,
		"--format=json"))
	if strings.Contains(described, "allUsers") {
		t.Fatalf("the revoked entity is still on the object's ACL: %s", described)
	}

	runCLI(t, gcloudCLI("storage", "rm", "gs://"+bucket+"/"+object))
	runCLI(t, gcloudCLI("storage", "buckets", "delete", "gs://"+bucket))
}
