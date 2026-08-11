package aws_cli_test

import (
	"strings"
	"testing"
)

// TestCloudWatchLogsCLI_KmsKey drives CreateLogGroup --kms-key-id round-trip
// via the aws CLI.
func TestCloudWatchLogsCLI_KmsKey(t *testing.T) {
	name := "/probe/cli-kms-test"
	kms := "arn:aws:kms:us-east-1:123456789012:key/d8ce7e2f-fc3e-4b45-0ff0-7d4b53ff3a40"
	runCLI(t, awsCLI("logs", "create-log-group",
		"--log-group-name", name, "--kms-key-id", kms))

	got := strings.TrimSpace(runCLI(t, awsCLI("logs", "describe-log-groups",
		"--log-group-name-prefix", name,
		"--query", "logGroups[0].kmsKeyId", "--output", "text")))
	if got != kms {
		t.Fatalf("log group kmsKeyId = %q, want %q", got, kms)
	}
}
