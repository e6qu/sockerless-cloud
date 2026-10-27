package aws_cli_test

import (
	"strings"
	"testing"
)

func TestECRCLI_RepositoryPolicy(t *testing.T) {
	t.Parallel()
	repo := "cli-policy-repo"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"AllowPull","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":["ecr:GetDownloadUrlForLayer","ecr:BatchGetImage"]}]}`
	runCLI(t, awsCLI("ecr", "set-repository-policy",
		"--repository-name", repo, "--policy-text", policy, "--force"))

	name := strings.TrimSpace(runCLI(t, awsCLI("ecr", "get-repository-policy",
		"--repository-name", repo, "--query", "repositoryName", "--output", "text")))
	if name != repo {
		t.Fatalf("get-repository-policy returned %q, want %q", name, repo)
	}

	runCLI(t, awsCLI("ecr", "delete-repository-policy", "--repository-name", repo))
}

// TestECRCLI_InitiateLayerUpload checks only the uploadId; the SDK test covers
// the binary part-upload round trip.
func TestECRCLI_InitiateLayerUpload(t *testing.T) {
	t.Parallel()
	repo := "cli-layer-repo"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))
	uploadID := strings.TrimSpace(runCLI(t, awsCLI("ecr", "initiate-layer-upload",
		"--repository-name", repo, "--query", "uploadId", "--output", "text")))
	if uploadID == "" {
		t.Fatal("initiate-layer-upload returned no uploadId")
	}
}
