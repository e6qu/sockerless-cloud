package aws_cli_test

import (
	"strings"
	"testing"
)

// TestResourceTagsCLI_CreateTimeRoundTrip drives the create-time tag round-trip
// for CloudWatch Logs, DynamoDB, and ECR via the aws CLI.
func TestResourceTagsCLI_CreateTimeRoundTrip(t *testing.T) {
	t.Run("CloudWatchLogs", func(t *testing.T) {
		name := "/cli-tags/app"
		runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", name,
			"--tags", "Name=test,env=ci"))
		arn := strings.TrimSpace(runCLI(t, awsCLI("logs", "describe-log-groups",
			"--log-group-name-prefix", name, "--query", "logGroups[0].arn", "--output", "text")))
		got := strings.TrimSpace(runCLI(t, awsCLI("logs", "list-tags-for-resource",
			"--resource-arn", arn, "--query", "tags.Name", "--output", "text")))
		if got != "test" {
			t.Fatalf("CloudWatch Logs tags.Name = %q, want test", got)
		}
	})

	t.Run("DynamoDB", func(t *testing.T) {
		runCLI(t, awsCLI("dynamodb", "create-table", "--table-name", "cli-tags-tbl",
			"--billing-mode", "PAY_PER_REQUEST",
			"--attribute-definitions", "AttributeName=PK,AttributeType=S",
			"--key-schema", "AttributeName=PK,KeyType=HASH",
			"--tags", "Key=Name,Value=test"))
		arn := strings.TrimSpace(runCLI(t, awsCLI("dynamodb", "describe-table",
			"--table-name", "cli-tags-tbl", "--query", "Table.TableArn", "--output", "text")))
		got := strings.TrimSpace(runCLI(t, awsCLI("dynamodb", "list-tags-of-resource",
			"--resource-arn", arn, "--query", "Tags[?Key=='Name'].Value | [0]", "--output", "text")))
		if got != "test" {
			t.Fatalf("DynamoDB tag Name = %q, want test", got)
		}
	})

	t.Run("ECR", func(t *testing.T) {
		runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", "cli-tags-repo",
			"--tags", "Key=Name,Value=test"))
		arn := strings.TrimSpace(runCLI(t, awsCLI("ecr", "describe-repositories",
			"--repository-names", "cli-tags-repo", "--query", "repositories[0].repositoryArn", "--output", "text")))
		got := strings.TrimSpace(runCLI(t, awsCLI("ecr", "list-tags-for-resource",
			"--resource-arn", arn, "--query", "tags[?Key=='Name'].Value | [0]", "--output", "text")))
		if got != "test" {
			t.Fatalf("ECR tag Name = %q, want test", got)
		}
	})
}
