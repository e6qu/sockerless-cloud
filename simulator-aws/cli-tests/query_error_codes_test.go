package aws_cli_test

import (
	"strings"
	"testing"
)

// TestElastiCacheCLI_AndRDSQueryErrorCodes expects the AWS CLI to name each refusal by the code
// the model's awsQueryError trait gives it.
func TestElastiCacheCLI_AndRDSQueryErrorCodes(t *testing.T) {
	t.Parallel()
	for code, args := range map[string][]string{
		"CacheParameterGroupNotFound": {"elasticache", "describe-cache-parameter-groups", "--cache-parameter-group-name", "cli-absent-pg"},
		"SubscriptionNotFound":        {"rds", "delete-event-subscription", "--subscription-name", "cli-absent-sub"},
		"DBShardGroupNotFound":        {"rds", "delete-db-shard-group", "--db-shard-group-identifier", "cli-absent-shard"},
		"ExportTaskNotFound":          {"rds", "cancel-export-task", "--export-task-identifier", "cli-absent-export"},
	} {
		if out := runCLIExpectError(t, awsCLI(args...)); !strings.Contains(out, "("+code+")") {
			t.Fatalf("%s: got %q, want (%s)", strings.Join(args[:2], " "), out, code)
		}
	}
}
