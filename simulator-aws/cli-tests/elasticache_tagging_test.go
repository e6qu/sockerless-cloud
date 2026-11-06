package aws_cli_test

import (
	"strings"
	"testing"
)

// TestElastiCacheCLI_TaggingFaults expects InvalidARN, TagNotFound and the
// not-found fault of the ARN's resource type from the tagging operations.
func TestElastiCacheCLI_TaggingFaults(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	arn := q("elasticache", "create-cache-parameter-group", "--cache-parameter-group-name", "cli-tag-faults",
		"--cache-parameter-group-family", "redis7", "--description", "tagged",
		"--query", "CacheParameterGroup.ARN", "--output", "text")
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-parameter-group", "--cache-parameter-group-name", "cli-tag-faults").Run()
	})
	runCLI(t, awsCLI("elasticache", "add-tags-to-resource", "--resource-name", arn, "--tags", "Key=team,Value=cache"))
	for code, args := range map[string][]string{
		"InvalidARN":                  {"elasticache", "list-tags-for-resource", "--resource-name", "not-an-arn"},
		"TagNotFound":                 {"elasticache", "remove-tags-from-resource", "--resource-name", arn, "--tag-keys", "absent"},
		"CacheParameterGroupNotFound": {"elasticache", "list-tags-for-resource", "--resource-name", strings.Replace(arn, "cli-tag-faults", "cli-absent", 1)},
	} {
		if out := runCLIExpectError(t, awsCLI(args...)); !strings.Contains(out, "("+code+")") {
			t.Fatalf("%s: got %q, want (%s)", args[1], out, code)
		}
	}
	if got := q("elasticache", "list-tags-for-resource", "--resource-name", arn, "--query", "TagList[0].Value", "--output", "text"); got != "cache" {
		t.Fatalf("list-tags-for-resource: got %q", got)
	}
}
