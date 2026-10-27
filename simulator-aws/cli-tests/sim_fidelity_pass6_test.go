package aws_cli_test

import (
	"strings"
	"testing"
)

// TestLambda_GetFunctionRepositoryTypeCLI expects Code.RepositoryType "S3" for
// a ZIP package.
func TestLambda_GetFunctionRepositoryTypeCLI(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)
	name := "cli-repotype-func"
	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", name, "--runtime", "nodejs18.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"), "--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath))
	defer runCLI(t, awsCLI("lambda", "delete-function", "--function-name", name))

	rt := strings.TrimSpace(runCLI(t, awsCLI("lambda", "get-function",
		"--function-name", name, "--query", "Code.RepositoryType", "--output", "text")))
	if rt != "S3" {
		t.Fatalf("Code.RepositoryType = %q, want S3", rt)
	}
}

// TestEventBridgeCLI_EventBusKMS expects describe-event-bus to return the
// kms-key-identifier create-event-bus set.
func TestEventBridgeCLI_EventBusKMS(t *testing.T) {
	t.Parallel()
	name := "cli-kms-bus"
	kms := "arn:aws:kms:us-east-1:123456789012:key/cli-eb"
	runCLI(t, awsCLI("events", "create-event-bus", "--name", name, "--kms-key-identifier", kms))
	defer runCLI(t, awsCLI("events", "delete-event-bus", "--name", name))

	got := strings.TrimSpace(runCLI(t, awsCLI("events", "describe-event-bus",
		"--name", name, "--query", "KmsKeyIdentifier", "--output", "text")))
	if got != kms {
		t.Fatalf("describe-event-bus KmsKeyIdentifier = %q, want %q", got, kms)
	}
}
