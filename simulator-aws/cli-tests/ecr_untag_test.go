package aws_cli_test

import (
	"strings"
	"testing"
)

// TestECRCLI_UntagResource covers ecr untag-resource over the CLI surface (the
// op was unimplemented → 400).
func TestECRCLI_UntagResource(t *testing.T) {
	repo := "cli-untag-repo"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo,
		"--tags", "Key=keep,Value=yes", "Key=drop,Value=soon"))
	defer runCLI(t, awsCLI("ecr", "delete-repository", "--repository-name", repo, "--force"))

	arn := strings.TrimSpace(runCLI(t, awsCLI("ecr", "describe-repositories", "--repository-names", repo,
		"--query", "repositories[0].repositoryArn", "--output", "text")))
	runCLI(t, awsCLI("ecr", "untag-resource", "--resource-arn", arn, "--tag-keys", "drop"))

	out := runCLI(t, awsCLI("ecr", "list-tags-for-resource", "--resource-arn", arn))
	if strings.Contains(out, "drop") {
		t.Fatalf("untagged key 'drop' still present: %s", out)
	}
	if !strings.Contains(out, "keep") {
		t.Fatalf("kept tag 'keep' missing: %s", out)
	}
}
