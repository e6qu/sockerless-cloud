package gcp_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGCSCLI_BucketAndObjectLifecycle(t *testing.T) {
	t.Parallel()
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

	// gcloud logs a failed task as the exception's repr, which carries no
	// message; the debug traceback carries the service's.
	refused := gcloudCLIFails(t, gcloudCLI("storage", "buckets", "delete", "gs://"+bucket, "--verbosity=debug"))
	require.Contains(t, refused, "GcsConflictError", "deleting a bucket that holds an object conflicts")
	require.Contains(t, refused, "The bucket you tried to delete is not empty.",
		"gcloud reads the message from the JSON API's error envelope")

	runCLI(t, gcloudCLI("storage", "rm", "gs://"+bucket+"/"+object))
	runCLI(t, gcloudCLI("storage", "buckets", "delete", "gs://"+bucket))
}

// Soft delete through the vendor CLI. `gcloud storage rm` retires the object
// under the bucket's policy, `--soft-deleted` lists what can be brought back,
// and `gcloud storage restore` brings it back with its bytes.
func TestGCSCLI_SoftDeleteListAndRestore(t *testing.T) {
	t.Parallel()
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
	// gcloud renders its own snake_case projection; the SDK test pins the wire
	// spelling.
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

// TestGCSCLI_PredefinedObjectACL: `gcloud storage cp --predefined-acl` gives
// the new object that predefined ACL in place of the bucket's default object
// ACL, with the uploader as its owner.
func TestGCSCLI_PredefinedObjectACL(t *testing.T) {
	t.Parallel()
	bucket := "cli-predefined-acl-bucket"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))

	source := filepath.Join(tmpDir, "predefined-acl-source.txt")
	if err := os.WriteFile(source, []byte("public"), 0644); err != nil {
		t.Fatalf("failed to write source object: %v", err)
	}
	runCLI(t, gcloudCLI("storage", "cp", "--predefined-acl=publicRead", source, "gs://"+bucket+"/public.txt"))
	runCLI(t, gcloudCLI("storage", "cp", source, "gs://"+bucket+"/default.txt"))

	var public, defaulted struct {
		ACL []struct {
			Entity string `json:"entity"`
			Role   string `json:"role"`
		} `json:"acl"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, gcloudCLI("storage", "objects", "describe",
		"gs://"+bucket+"/public.txt", "--format=json"))), &public); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(runCLI(t, gcloudCLI("storage", "objects", "describe",
		"gs://"+bucket+"/default.txt", "--format=json"))), &defaulted); err != nil {
		t.Fatal(err)
	}
	entities := func(acl []struct {
		Entity string `json:"entity"`
		Role   string `json:"role"`
	}) map[string]string {
		out := map[string]string{}
		for _, entry := range acl {
			out[entry.Entity] = entry.Role
		}
		return out
	}
	got := entities(public.ACL)
	if len(got) != 2 || got["allUsers"] != "READER" {
		t.Fatalf("publicRead gives the owner OWNER and allUsers READER, got %v", got)
	}
	viewers := 0
	for entity, role := range entities(defaulted.ACL) {
		if strings.HasPrefix(entity, "project-viewers-") && role == "READER" {
			viewers++
		}
	}
	if viewers != 1 || len(defaulted.ACL) != 4 {
		t.Fatalf("the default object ACL is projectPrivate plus the owner, got %v", entities(defaulted.ACL))
	}

	runCLI(t, gcloudCLI("storage", "rm", "gs://"+bucket+"/public.txt", "gs://"+bucket+"/default.txt"))
	runCLI(t, gcloudCLI("storage", "buckets", "delete", "gs://"+bucket))
}

// TestGCSCLI_ObjectACLGrants: `gcloud storage objects update --add-acl-grant`
// writes one entry and `--remove-acl-grant` removes it, each reading the
// object's ACL back.
func TestGCSCLI_ObjectACLGrants(t *testing.T) {
	t.Parallel()
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
