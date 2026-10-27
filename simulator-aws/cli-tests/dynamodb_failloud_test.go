package aws_cli_test

import (
	"strings"
	"testing"
)

// TestDynamoDBCLI_ExpressionsFailLoud expects a malformed expression, or one
// naming an undefined :value, to fail with ValidationException rather than
// return an empty result. runCLI fails the test on a non-zero exit, so the
// calls use CombinedOutput.
func TestDynamoDBCLI_ExpressionsFailLoud(t *testing.T) {
	t.Parallel()
	table := "cli-ddb-failloud"
	runCLI(t, awsCLI("dynamodb", "create-table",
		"--table-name", table,
		"--attribute-definitions", "AttributeName=PK,AttributeType=S",
		"--key-schema", "AttributeName=PK,KeyType=HASH",
		"--billing-mode", "PAY_PER_REQUEST"))
	t.Cleanup(func() {
		_ = awsCLI("dynamodb", "delete-table", "--table-name", table).Run()
	})

	out, err := awsCLI("dynamodb", "scan",
		"--table-name", table,
		"--filter-expression", "PK ==").CombinedOutput()
	if err == nil {
		t.Fatalf("scan with a malformed filter-expression should fail; got success:\n%s", out)
	}
	if !strings.Contains(string(out), "ValidationException") {
		t.Fatalf("expected ValidationException; got:\n%s", out)
	}

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
