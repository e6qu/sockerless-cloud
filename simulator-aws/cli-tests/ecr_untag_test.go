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

// TestECRCLI_DeleteRepositoryRequiresForceWhileItHoldsImages runs aws ecr
// delete-repository against a repository holding an image: the CLI reports
// RepositoryNotEmptyException until the call passes --force.
func TestECRCLI_DeleteRepositoryRequiresForceWhileItHoldsImages(t *testing.T) {
	repo := "cli-not-empty-repo"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},"layers":[]}`
	runCLI(t, awsCLI("ecr", "put-image", "--repository-name", repo,
		"--image-tag", "v1", "--image-manifest", manifest))

	out := runCLIExpectError(t, awsCLI("ecr", "delete-repository", "--repository-name", repo))
	if !strings.Contains(out, "RepositoryNotEmptyException") ||
		!strings.Contains(out, "cannot be deleted because it still contains images") {
		t.Fatalf("delete-repository without --force did not report RepositoryNotEmptyException: %s", out)
	}
	images := runCLI(t, awsCLI("ecr", "list-images", "--repository-name", repo,
		"--query", "length(imageIds)", "--output", "text"))
	if strings.TrimSpace(images) != "1" {
		t.Fatalf("a refused delete must keep the repository's image, list-images counted %q", images)
	}

	runCLI(t, awsCLI("ecr", "delete-repository", "--repository-name", repo, "--force"))
	if out := runCLIExpectError(t, awsCLI("ecr", "describe-repositories", "--repository-names", repo)); !strings.Contains(out, "RepositoryNotFoundException") {
		t.Fatalf("a forced delete left the repository behind: %s", out)
	}
}
