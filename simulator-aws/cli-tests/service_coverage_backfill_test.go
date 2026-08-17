package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		"--table-input", `{"Name":"clicovt","PartitionKeys":[{"Name":"dt","Type":"string"}],"StorageDescriptor":{"Columns":[{"Name":"c","Type":"string"}]}}`))
	runCLI(t, awsCLI("glue", "create-partition-index", "--database-name", "clicovdb", "--table-name", "clicovt",
		"--partition-index", `{"Keys":["dt"],"IndexName":"cli-cov-index"}`))

	// The index just created is the one the reader returns, with the key it was
	// created over — an empty or canned descriptor list fails here.
	out := runCLI(t, awsCLI("glue", "get-partition-indexes", "--database-name", "clicovdb", "--table-name", "clicovt",
		"--output", "json"))
	var indexes struct {
		PartitionIndexDescriptorList []struct {
			IndexName string `json:"IndexName"`
			Keys      []struct {
				Name string `json:"Name"`
			} `json:"Keys"`
			IndexStatus string `json:"IndexStatus"`
		} `json:"PartitionIndexDescriptorList"`
	}
	parseJSON(t, out, &indexes)
	require.Len(t, indexes.PartitionIndexDescriptorList, 1)
	index := indexes.PartitionIndexDescriptorList[0]
	assert.Equal(t, "cli-cov-index", index.IndexName)
	require.Len(t, index.Keys, 1)
	assert.Equal(t, "dt", index.Keys[0].Name)
	assert.NotEmpty(t, index.IndexStatus)

	// Deleting it empties the list, so the read above reflects the table's own
	// indexes rather than a fixed answer.
	runCLI(t, awsCLI("glue", "delete-partition-index", "--database-name", "clicovdb", "--table-name", "clicovt",
		"--index-name", "cli-cov-index"))
	parseJSON(t, runCLI(t, awsCLI("glue", "get-partition-indexes", "--database-name", "clicovdb", "--table-name", "clicovt",
		"--output", "json")), &indexes)
	assert.Empty(t, indexes.PartitionIndexDescriptorList)
}

func TestCodeBuildCLI_ListBuilds(t *testing.T) {
	project := "cli-cov-listbuilds"
	runCLI(t, awsCLI("codebuild", "create-project", "--name", project,
		"--source", `{"type":"NO_SOURCE","buildspec":"version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\n"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"public.ecr.aws/docker/library/busybox:latest","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", "arn:aws:iam::123456789012:role/cli-cov-codebuild"))
	t.Cleanup(func() { _ = awsCLI("codebuild", "delete-project", "--name", project).Run() })

	buildID := strings.TrimSpace(runCLI(t, awsCLI("codebuild", "start-build", "--project-name", project,
		"--query", "build.id", "--output", "text")))
	require.NotEmpty(t, buildID)

	// The build just started is in the account's build list, and in the
	// project's own — a reader that answered with an empty list, or with every
	// project's builds under one project, fails one of the two.
	var all struct {
		IDs []string `json:"ids"`
	}
	parseJSON(t, runCLI(t, awsCLI("codebuild", "list-builds", "--output", "json")), &all)
	assert.Contains(t, all.IDs, buildID)

	var forProject struct {
		IDs []string `json:"ids"`
	}
	parseJSON(t, runCLI(t, awsCLI("codebuild", "list-builds-for-project", "--project-name", project, "--output", "json")), &forProject)
	assert.Equal(t, []string{buildID}, forProject.IDs)
}

func TestSFNCLI_VersionsAndValidate(t *testing.T) {
	const def = `{"StartAt":"x","States":{"x":{"Type":"Pass","End":true}}}`
	sm := strings.TrimSpace(runCLI(t, awsCLI("stepfunctions", "create-state-machine", "--name", "cli-cov-sm",
		"--definition", def, "--role-arn", "arn:aws:iam::123456789012:role/r", "--query", "stateMachineArn", "--output", "text")))
	// A published version appears in the listing under the state machine that
	// published it; a machine with no published version lists none.
	var versions struct {
		StateMachineVersions []struct {
			StateMachineVersionArn string `json:"stateMachineVersionArn"`
		} `json:"stateMachineVersions"`
	}
	parseJSON(t, runCLI(t, awsCLI("stepfunctions", "list-state-machine-versions", "--state-machine-arn", sm, "--output", "json")), &versions)
	assert.Empty(t, versions.StateMachineVersions, "no version is published yet")

	versionArn := strings.TrimSpace(runCLI(t, awsCLI("stepfunctions", "publish-state-machine-version",
		"--state-machine-arn", sm, "--query", "stateMachineVersionArn", "--output", "text")))
	require.Equal(t, sm+":1", versionArn)

	parseJSON(t, runCLI(t, awsCLI("stepfunctions", "list-state-machine-versions", "--state-machine-arn", sm, "--output", "json")), &versions)
	require.Len(t, versions.StateMachineVersions, 1)
	assert.Equal(t, versionArn, versions.StateMachineVersions[0].StateMachineVersionArn)
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
