package aws_cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestS3CLI_ObjectAnnotations covers the object `?annotation` subresource
// through `aws s3api`: put-object-annotation, get-object-annotation,
// list-object-annotations (with the name-prefix filter) and
// delete-object-annotation.
func TestS3CLI_ObjectAnnotations(t *testing.T) {
	bucket := fmt.Sprintf("cli-annotations-%d", time.Now().UnixNano())
	key := "reports/q1.csv"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	body := filepath.Join(t.TempDir(), "object.csv")
	if err := os.WriteFile(body, []byte("a,b,c\n1,2,3\n"), 0o600); err != nil {
		t.Fatalf("write object body: %v", err)
	}
	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", key, "--body", body))

	payload := filepath.Join(t.TempDir(), "annotation.json")
	if err := os.WriteFile(payload, []byte(`{"level":"internal"}`), 0o600); err != nil {
		t.Fatalf("write annotation payload: %v", err)
	}
	runCLI(t, awsCLI("s3api", "put-object-annotation",
		"--bucket", bucket, "--key", key,
		"--annotation-name", "classification",
		"--annotation-payload", payload))
	runCLI(t, awsCLI("s3api", "put-object-annotation",
		"--bucket", bucket, "--key", key,
		"--annotation-name", "classification-review",
		"--annotation-payload", payload))
	runCLI(t, awsCLI("s3api", "put-object-annotation",
		"--bucket", bucket, "--key", key,
		"--annotation-name", "provenance",
		"--annotation-payload", payload))

	out := filepath.Join(t.TempDir(), "downloaded.json")
	runCLI(t, awsCLI("s3api", "get-object-annotation",
		"--bucket", bucket, "--key", key,
		"--annotation-name", "classification", out))
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read downloaded annotation: %v", err)
	}
	if string(got) != `{"level":"internal"}` {
		t.Fatalf("annotation payload = %q", string(got))
	}

	count := strings.TrimSpace(runCLI(t, awsCLI("s3api", "list-object-annotations",
		"--bucket", bucket, "--key", key,
		"--query", "length(Annotations)", "--output", "text")))
	if count != "3" {
		t.Fatalf("Annotations length = %q, want 3", count)
	}
	first := strings.TrimSpace(runCLI(t, awsCLI("s3api", "list-object-annotations",
		"--bucket", bucket, "--key", key,
		"--query", "Annotations[0].AnnotationName", "--output", "text")))
	if first != "classification" {
		t.Fatalf("Annotations[0].AnnotationName = %q", first)
	}
	filtered := strings.TrimSpace(runCLI(t, awsCLI("s3api", "list-object-annotations",
		"--bucket", bucket, "--key", key,
		"--annotation-prefix", "classification",
		"--query", "length(Annotations)", "--output", "text")))
	if filtered != "2" {
		t.Fatalf("prefix-filtered Annotations length = %q, want 2", filtered)
	}

	runCLI(t, awsCLI("s3api", "delete-object-annotation",
		"--bucket", bucket, "--key", key, "--annotation-name", "provenance"))
	remaining := strings.TrimSpace(runCLI(t, awsCLI("s3api", "list-object-annotations",
		"--bucket", bucket, "--key", key,
		"--query", "length(Annotations)", "--output", "text")))
	if remaining != "2" {
		t.Fatalf("after delete, Annotations length = %q, want 2", remaining)
	}
	if err := awsCLI("s3api", "get-object-annotation",
		"--bucket", bucket, "--key", key, "--annotation-name", "provenance",
		filepath.Join(t.TempDir(), "gone.json")).Run(); err == nil {
		t.Fatal("get-object-annotation should fail for a deleted annotation")
	}
}

// TestS3CLI_MetadataAnnotationTableConfiguration covers
// update-bucket-metadata-annotation-table-configuration and reading the state
// back from get-bucket-metadata-configuration.
func TestS3CLI_MetadataAnnotationTableConfiguration(t *testing.T) {
	bucket := fmt.Sprintf("cli-annotation-table-%d", time.Now().UnixNano())
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))

	metadataConfig := map[string]any{
		"JournalTableConfiguration": map[string]any{
			"RecordExpiration": map[string]any{"Expiration": "ENABLED", "Days": 7},
		},
	}
	metadataFile := writeJSONDoc(t, "metadata-configuration.json", metadataConfig)
	runCLI(t, awsCLI("s3api", "create-bucket-metadata-configuration",
		"--bucket", bucket, "--metadata-configuration", "file://"+metadataFile))

	roleArn := "arn:aws:iam::000000000000:role/S3MetadataAnnotationTableRole"
	annotationConfig := map[string]any{"ConfigurationState": "ENABLED", "Role": roleArn}
	annotationFile := writeJSONDoc(t, "annotation-table.json", annotationConfig)
	runCLI(t, awsCLI("s3api", "update-bucket-metadata-annotation-table-configuration",
		"--bucket", bucket, "--annotation-table-configuration", "file://"+annotationFile))

	state := strings.TrimSpace(runCLI(t, awsCLI("s3api", "get-bucket-metadata-configuration",
		"--bucket", bucket,
		"--query", "GetBucketMetadataConfigurationResult.MetadataConfigurationResult.AnnotationTableConfigurationResult.ConfigurationState",
		"--output", "text")))
	if state != "ENABLED" {
		t.Fatalf("AnnotationTableConfigurationResult.ConfigurationState = %q, want ENABLED", state)
	}
	role := strings.TrimSpace(runCLI(t, awsCLI("s3api", "get-bucket-metadata-configuration",
		"--bucket", bucket,
		"--query", "GetBucketMetadataConfigurationResult.MetadataConfigurationResult.AnnotationTableConfigurationResult.Role",
		"--output", "text")))
	if role != roleArn {
		t.Fatalf("AnnotationTableConfigurationResult.Role = %q", role)
	}
}
