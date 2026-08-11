package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECRCLI_ImageScanAndConfig drives the AWS CLI through the
// image-tag-mutability, image-scanning-configuration, on-demand scan, and
// scan-findings read-back paths. The simulator runs no scanner, so the scan
// completes with an honest empty findings result.
func TestECRCLI_ImageScanAndConfig(t *testing.T) {
	repo := "cli-ecr-scan-repo"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))

	runCLI(t, awsCLI("ecr", "put-image-tag-mutability",
		"--repository-name", repo, "--image-tag-mutability", "IMMUTABLE"))

	runCLI(t, awsCLI("ecr", "put-image-scanning-configuration",
		"--repository-name", repo, "--image-scanning-configuration", "scanOnPush=true"))

	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"layers":[]}`
	runCLI(t, awsCLI("ecr", "put-image",
		"--repository-name", repo, "--image-tag", "scanme", "--image-manifest", manifest))

	status := strings.TrimSpace(runCLI(t, awsCLI("ecr", "start-image-scan",
		"--repository-name", repo, "--image-id", "imageTag=scanme",
		"--query", "imageScanStatus.status", "--output", "text")))
	assert.Equal(t, "COMPLETE", status)

	findStatus := strings.TrimSpace(runCLI(t, awsCLI("ecr", "describe-image-scan-findings",
		"--repository-name", repo, "--image-id", "imageTag=scanme",
		"--query", "imageScanStatus.status", "--output", "text")))
	assert.Equal(t, "COMPLETE", findStatus)
}

// TestECRCLI_LifecyclePolicyPreview puts a lifecycle policy and drives the
// preview start + get paths.
func TestECRCLI_LifecyclePolicyPreview(t *testing.T) {
	repo := "cli-ecr-lifecycle-preview-repo"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))

	policy := `{"rules":[{"rulePriority":1,"description":"expire untagged","selection":{"tagStatus":"untagged","countType":"sinceImagePushed","countUnit":"days","countNumber":14},"action":{"type":"expire"}}]}`
	runCLI(t, awsCLI("ecr", "put-lifecycle-policy",
		"--repository-name", repo, "--lifecycle-policy-text", policy))

	runCLI(t, awsCLI("ecr", "start-lifecycle-policy-preview", "--repository-name", repo))

	name := strings.TrimSpace(runCLI(t, awsCLI("ecr", "get-lifecycle-policy-preview",
		"--repository-name", repo, "--query", "repositoryName", "--output", "text")))
	assert.Equal(t, repo, name)
}

// TestECRCLI_RegistryPolicyAndReplication drives the registry-level CLI ops:
// registry permissions policy CRUD, replication configuration, and the
// DescribeRegistry / DescribeImageReplicationStatus read-backs.
func TestECRCLI_RegistryPolicyAndReplication(t *testing.T) {
	regPolicy := `{"Version":"2012-10-17","Statement":[{"Sid":"AllowReplicate","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":["ecr:CreateRepository","ecr:ReplicateImage"],"Resource":"*"}]}`
	runCLI(t, awsCLI("ecr", "put-registry-policy", "--policy-text", regPolicy))

	got := strings.TrimSpace(runCLI(t, awsCLI("ecr", "get-registry-policy",
		"--query", "registryId", "--output", "text")))
	require.NotEmpty(t, got)

	replCfg := `{"rules":[{"destinations":[{"region":"us-west-2","registryId":"000000000000"}]}]}`
	runCLI(t, awsCLI("ecr", "put-replication-configuration",
		"--replication-configuration", replCfg))

	region := strings.TrimSpace(runCLI(t, awsCLI("ecr", "describe-registry",
		"--query", "replicationConfiguration.rules[0].destinations[0].region", "--output", "text")))
	assert.Equal(t, "us-west-2", region)

	repo := "cli-ecr-replication-repo"
	runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},"layers":[]}`
	runCLI(t, awsCLI("ecr", "put-image",
		"--repository-name", repo, "--image-tag", "v1", "--image-manifest", manifest))

	statusRegion := strings.TrimSpace(runCLI(t, awsCLI("ecr", "describe-image-replication-status",
		"--repository-name", repo, "--image-id", "imageTag=v1",
		"--query", "replicationStatuses[0].region", "--output", "text")))
	assert.Equal(t, "us-west-2", statusRegion)

	runCLI(t, awsCLI("ecr", "delete-registry-policy"))
}
