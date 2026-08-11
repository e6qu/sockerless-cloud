package aws_cli_test

import (
	"strings"
	"testing"
)

func ddbCovTable(t *testing.T, name string) {
	t.Helper()
	runCLI(t, awsCLI("dynamodb", "create-table", "--table-name", name,
		"--billing-mode", "PAY_PER_REQUEST",
		"--attribute-definitions", "AttributeName=PK,AttributeType=S",
		"--key-schema", "AttributeName=PK,KeyType=HASH"))
}

// TestDynamoDBCLI_UpdateItemExpression drives UpdateExpression via the aws CLI —
// the path that was a silent no-op (only legacy AttributeUpdates was handled).
func TestDynamoDBCLI_UpdateItemExpression(t *testing.T) {
	ddbCovTable(t, "cli-cov-update")
	get := func(q string) string {
		return strings.TrimSpace(runCLI(t, awsCLI("dynamodb", "get-item", "--table-name", "cli-cov-update",
			"--key", `{"PK":{"S":"a"}}`, "--query", q, "--output", "text")))
	}

	runCLI(t, awsCLI("dynamodb", "update-item", "--table-name", "cli-cov-update", "--key", `{"PK":{"S":"a"}}`,
		"--update-expression", "SET #c = :v", "--expression-attribute-names", `{"#c":"cnt"}`,
		"--expression-attribute-values", `{":v":{"N":"5"}}`))
	if got := get("Item.cnt.N"); got != "5" {
		t.Fatalf("after SET, cnt = %q, want 5", got)
	}

	runCLI(t, awsCLI("dynamodb", "update-item", "--table-name", "cli-cov-update", "--key", `{"PK":{"S":"a"}}`,
		"--update-expression", "SET #c = #c + :i", "--expression-attribute-names", `{"#c":"cnt"}`,
		"--expression-attribute-values", `{":i":{"N":"3"}}`))
	if got := get("Item.cnt.N"); got != "8" {
		t.Fatalf("after increment, cnt = %q, want 8", got)
	}

	runCLI(t, awsCLI("dynamodb", "update-item", "--table-name", "cli-cov-update", "--key", `{"PK":{"S":"a"}}`,
		"--update-expression", "REMOVE #c", "--expression-attribute-names", `{"#c":"cnt"}`))
	if got := get("Item.cnt.N"); got != "None" {
		t.Fatalf("after REMOVE, cnt = %q, want None", got)
	}
}

// TestDynamoDBCLI_TimeToLiveAndContinuousBackups covers the TTL + PITR read/write paths.
func TestDynamoDBCLI_TimeToLiveAndContinuousBackups(t *testing.T) {
	ddbCovTable(t, "cli-cov-ttl")
	runCLI(t, awsCLI("dynamodb", "update-time-to-live", "--table-name", "cli-cov-ttl",
		"--time-to-live-specification", "Enabled=true,AttributeName=ttl"))
	status := strings.TrimSpace(runCLI(t, awsCLI("dynamodb", "describe-time-to-live", "--table-name", "cli-cov-ttl",
		"--query", "TimeToLiveDescription.TimeToLiveStatus", "--output", "text")))
	if status != "ENABLED" {
		t.Fatalf("TTL status = %q, want ENABLED", status)
	}

	runCLI(t, awsCLI("dynamodb", "update-continuous-backups", "--table-name", "cli-cov-ttl",
		"--point-in-time-recovery-specification", "PointInTimeRecoveryEnabled=true"))
	pitr := strings.TrimSpace(runCLI(t, awsCLI("dynamodb", "describe-continuous-backups", "--table-name", "cli-cov-ttl",
		"--query", "ContinuousBackupsDescription.PointInTimeRecoveryDescription.PointInTimeRecoveryStatus", "--output", "text")))
	if pitr != "ENABLED" {
		t.Fatalf("PITR status = %q, want ENABLED", pitr)
	}
}
