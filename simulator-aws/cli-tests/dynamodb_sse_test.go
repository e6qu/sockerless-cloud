package aws_cli_test

import (
	"strings"
	"testing"
)

// TestDynamoDBCLI_SSEDescription drives the SSE round-trip via the aws CLI:
// create-table with --sse-specification, then read it back from describe-table.
func TestDynamoDBCLI_SSEDescription(t *testing.T) {
	keyArn := "arn:aws:kms:us-east-1:123456789012:key/cli-sse-key"
	runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", "cli-sse-table",
		"--attribute-definitions", "AttributeName=PK,AttributeType=S",
		"--key-schema", "AttributeName=PK,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST",
		"--sse-specification", "Enabled=true,SSEType=KMS,KMSMasterKeyId="+keyArn))

	out := strings.TrimSpace(runCLI(t, awsCLI("dynamodb", "describe-table",
		"--table-name", "cli-sse-table",
		"--query", "Table.SSEDescription.{Status:Status,Type:SSEType,Key:KMSMasterKeyArn}",
		"--output", "text")))
	if !strings.Contains(out, "ENABLED") || !strings.Contains(out, "KMS") || !strings.Contains(out, keyArn) {
		t.Fatalf("SSEDescription did not round-trip: %q", out)
	}
}
