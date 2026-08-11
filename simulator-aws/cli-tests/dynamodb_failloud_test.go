package aws_cli_test

import (
	"strings"
	"testing"
)

// TestDynamoDBCLI_ExpressionsFailLoud is lever 2 of the #652 "silent
// incompleteness" prevention work, exercised through the aws CLI: a malformed
// expression, or one referencing an undefined :value, must fail with a
// ValidationException rather than returning an empty result that reads as "no
// data". Uses CombinedOutput directly because the calls are expected to exit
// non-zero (runCLI fatals on a non-zero exit).
func TestDynamoDBCLI_ExpressionsFailLoud(t *testing.T) {
	table := "cli-ddb-failloud"
	runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", table,
		"--attribute-definitions", "AttributeName=PK,AttributeType=S",
		"--key-schema", "AttributeName=PK,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST"))
	t.Cleanup(func() {
		_ = awsCLI("dynamodb", "delete-table", "--table-name", table).Run()
	})

	// Malformed FilterExpression.
	out, err := awsCLI("dynamodb", "scan",
		"--table-name", table,
		"--filter-expression", "PK ==").CombinedOutput()
	if err == nil {
		t.Fatalf("scan with a malformed filter-expression should fail; got success:\n%s", out)
	}
	if !strings.Contains(string(out), "ValidationException") {
		t.Fatalf("expected ValidationException; got:\n%s", out)
	}

	// FilterExpression referencing an undefined :value would otherwise silently
	// return zero items.
	out, err = awsCLI("dynamodb", "scan",
		"--table-name", table,
		"--filter-expression", "PK = :missing").CombinedOutput()
	if err == nil {
		t.Fatalf("scan referencing an undefined :value should fail; got success:\n%s", out)
	}
	if !strings.Contains(string(out), "ValidationException") {
		t.Fatalf("expected ValidationException; got:\n%s", out)
	}
}
