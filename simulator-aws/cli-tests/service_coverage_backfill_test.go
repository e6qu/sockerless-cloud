package aws_cli_test

import (
	"strings"
	"testing"
)

// CLI coverage backfill for operations that had no test, grouped by service.
// Each function's name routes it to the matching CI shard.

func TestECRCLI_ListDescribeDeleteImages(t *testing.T) {
	const manifest = `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":7,"digest":"sha256:clicfg"},"layers":[]}`
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", "cli-cov-img"))
	runCLI(t, awsCLI("ecr", "put-image", "--repository-name", "cli-cov-img", "--image-tag", "v1", "--image-manifest", manifest))

	if got := strings.TrimSpace(runCLI(t, awsCLI("ecr", "list-images", "--repository-name", "cli-cov-img", "--query", "imageIds[0].imageTag", "--output", "text"))); got != "v1" {
		t.Fatalf("list-images tag = %q, want v1", got)
	}
	if got := strings.TrimSpace(runCLI(t, awsCLI("ecr", "describe-images", "--repository-name", "cli-cov-img", "--query", "imageDetails[0].imageTags[0]", "--output", "text"))); got != "v1" {
		t.Fatalf("describe-images tag = %q, want v1", got)
	}
	runCLI(t, awsCLI("ecr", "batch-delete-image", "--repository-name", "cli-cov-img", "--image-ids", "imageTag=v1"))
	if got := strings.TrimSpace(runCLI(t, awsCLI("ecr", "describe-images", "--repository-name", "cli-cov-img", "--query", "length(imageDetails)", "--output", "text"))); got != "0" {
		t.Fatalf("images after batch-delete = %q, want 0", got)
	}
}

func TestSSMCLI_GetParameters(t *testing.T) {
	runCLI(t, awsCLI("ssm", "put-parameter", "--name", "/cli-cov/p1", "--value", "v1", "--type", "String"))
	runCLI(t, awsCLI("ssm", "put-parameter", "--name", "/cli-cov/p2", "--value", "v2", "--type", "String"))
	vals := strings.Fields(runCLI(t, awsCLI("ssm", "get-parameters", "--names", "/cli-cov/p1", "/cli-cov/p2", "--query", "Parameters[].Value", "--output", "text")))
	if len(vals) != 2 {
		t.Fatalf("get-parameters returned %d values, want 2: %v", len(vals), vals)
	}
	inv := strings.TrimSpace(runCLI(t, awsCLI("ssm", "get-parameters", "--names", "/cli-cov/p1", "/cli-cov/missing", "--query", "InvalidParameters[0]", "--output", "text")))
	if inv != "/cli-cov/missing" {
		t.Fatalf("InvalidParameters = %q, want /cli-cov/missing", inv)
	}
}

func TestGlueCLI_GetPartitionIndexes(t *testing.T) {
	runCLI(t, awsCLI("glue", "create-database", "--database-input", "Name=clicovdb"))
	runCLI(t, awsCLI("glue", "create-table", "--database-name", "clicovdb",
		"--table-input", `{"Name":"clicovt","StorageDescriptor":{"Columns":[{"Name":"c","Type":"string"}]}}`))
	// The op must be wired and return the descriptor list (empty here).
	out := runCLI(t, awsCLI("glue", "get-partition-indexes", "--database-name", "clicovdb", "--table-name", "clicovt",
		"--query", "PartitionIndexDescriptorList", "--output", "json"))
	if !strings.Contains(out, "[") {
		t.Fatalf("get-partition-indexes did not return a descriptor list: %s", out)
	}
}

func TestCodeBuildCLI_ListBuilds(t *testing.T) {
	out := runCLI(t, awsCLI("codebuild", "list-builds", "--query", "ids", "--output", "json"))
	if !strings.Contains(out, "[") {
		t.Fatalf("list-builds did not return an ids list: %s", out)
	}
}

func TestSFNCLI_VersionsAndValidate(t *testing.T) {
	const def = `{"StartAt":"x","States":{"x":{"Type":"Pass","End":true}}}`
	sm := strings.TrimSpace(runCLI(t, awsCLI("stepfunctions", "create-state-machine", "--name", "cli-cov-sm",
		"--definition", def, "--role-arn", "arn:aws:iam::123456789012:role/r", "--query", "stateMachineArn", "--output", "text")))
	out := runCLI(t, awsCLI("stepfunctions", "list-state-machine-versions", "--state-machine-arn", sm, "--query", "stateMachineVersions", "--output", "json"))
	if !strings.Contains(out, "[") {
		t.Fatalf("list-state-machine-versions did not return a versions list: %s", out)
	}
	res := strings.TrimSpace(runCLI(t, awsCLI("stepfunctions", "validate-state-machine-definition", "--definition", def, "--query", "result", "--output", "text")))
	if res != "OK" {
		t.Fatalf("validate-state-machine-definition result = %q, want OK", res)
	}
}

func TestLogsCLI_PutRetentionPolicy(t *testing.T) {
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", "/cli-cov/retention"))
	runCLI(t, awsCLI("logs", "put-retention-policy", "--log-group-name", "/cli-cov/retention", "--retention-in-days", "14"))
	got := strings.TrimSpace(runCLI(t, awsCLI("logs", "describe-log-groups", "--log-group-name-prefix", "/cli-cov/retention",
		"--query", "logGroups[0].retentionInDays", "--output", "text")))
	if got != "14" {
		t.Fatalf("retentionInDays = %q, want 14", got)
	}
}

func TestSQSCLI_SetQueueAttributes(t *testing.T) {
	q := strings.TrimSpace(runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-cov-q", "--query", "QueueUrl", "--output", "text")))
	runCLI(t, awsCLI("sqs", "set-queue-attributes", "--queue-url", q, "--attributes", "VisibilityTimeout=120"))
	got := strings.TrimSpace(runCLI(t, awsCLI("sqs", "get-queue-attributes", "--queue-url", q, "--attribute-names", "VisibilityTimeout",
		"--query", "Attributes.VisibilityTimeout", "--output", "text")))
	if got != "120" {
		t.Fatalf("VisibilityTimeout = %q, want 120", got)
	}
}

func TestElastiCacheCLI_RemoveTagsFromResource(t *testing.T) {
	arn := strings.TrimSpace(runCLI(t, awsCLI("elasticache", "create-cache-cluster", "--cache-cluster-id", "cli-cov-cc",
		"--engine", "redis", "--cache-node-type", "cache.t3.micro", "--num-cache-nodes", "1",
		"--tags", "Key=Name,Value=cov", "--query", "CacheCluster.ARN", "--output", "text")))
	runCLI(t, awsCLI("elasticache", "add-tags-to-resource", "--resource-name", arn, "--tags", "Key=env,Value=ci"))
	runCLI(t, awsCLI("elasticache", "remove-tags-from-resource", "--resource-name", arn, "--tag-keys", "env"))
	keys := runCLI(t, awsCLI("elasticache", "list-tags-for-resource", "--resource-name", arn, "--query", "TagList[].Key", "--output", "text"))
	if strings.Contains(keys, "env") {
		t.Fatalf("env tag still present after remove: %q", keys)
	}
	if !strings.Contains(keys, "Name") {
		t.Fatalf("Name tag missing after removing env: %q", keys)
	}
}
