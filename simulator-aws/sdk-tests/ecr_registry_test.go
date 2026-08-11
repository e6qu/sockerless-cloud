package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ecrPutTestImage creates a repository (ignoring already-exists) and
// pushes one image, returning its digest. Used by the scan/replication
// tests that need a real stored image to operate on.
func ecrPutTestImage(t *testing.T, client *ecr.Client, repo, tag string) string {
	t.Helper()
	_, _ = client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"layers":[]}`
	out, err := client.PutImage(ctx, &ecr.PutImageInput{
		RepositoryName: aws.String(repo),
		ImageManifest:  aws.String(manifest),
		ImageTag:       aws.String(tag),
	})
	require.NoError(t, err)
	return aws.ToString(out.Image.ImageId.ImageDigest)
}

// TestECR_ImageTagMutabilityAndScanningConfig drives PutImageTagMutability
// and PutImageScanningConfiguration, then reads the settings back through
// DescribeRepositories to confirm they round-trip.
func TestECR_ImageTagMutabilityAndScanningConfig(t *testing.T) {
	client := ecrClient()
	repo := "ecr-config-repo"
	_, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)

	mut, err := client.PutImageTagMutability(ctx, &ecr.PutImageTagMutabilityInput{
		RepositoryName:     aws.String(repo),
		ImageTagMutability: ecrtypes.ImageTagMutabilityImmutable,
	})
	require.NoError(t, err)
	assert.Equal(t, ecrtypes.ImageTagMutabilityImmutable, mut.ImageTagMutability)

	scanCfg, err := client.PutImageScanningConfiguration(ctx, &ecr.PutImageScanningConfigurationInput{
		RepositoryName:             aws.String(repo),
		ImageScanningConfiguration: &ecrtypes.ImageScanningConfiguration{ScanOnPush: true},
	})
	require.NoError(t, err)
	require.NotNil(t, scanCfg.ImageScanningConfiguration)
	assert.True(t, scanCfg.ImageScanningConfiguration.ScanOnPush)

	desc, err := client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
		RepositoryNames: []string{repo},
	})
	require.NoError(t, err)
	require.Len(t, desc.Repositories, 1)
	assert.Equal(t, ecrtypes.ImageTagMutabilityImmutable, desc.Repositories[0].ImageTagMutability)
	require.NotNil(t, desc.Repositories[0].ImageScanningConfiguration)
	assert.True(t, desc.Repositories[0].ImageScanningConfiguration.ScanOnPush)
}

// TestECR_ImageScan starts an on-demand scan and reads the findings back.
// The simulator runs no scanner, so the scan completes with an honest
// empty findings result (no fabricated CVEs).
func TestECR_ImageScan(t *testing.T) {
	client := ecrClient()
	repo := "ecr-scan-repo"
	ecrPutTestImage(t, client, repo, "scanme")

	start, err := client.StartImageScan(ctx, &ecr.StartImageScanInput{
		RepositoryName: aws.String(repo),
		ImageId:        &ecrtypes.ImageIdentifier{ImageTag: aws.String("scanme")},
	})
	require.NoError(t, err)
	require.NotNil(t, start.ImageScanStatus)
	assert.Equal(t, ecrtypes.ScanStatusComplete, start.ImageScanStatus.Status)

	findings, err := client.DescribeImageScanFindings(ctx, &ecr.DescribeImageScanFindingsInput{
		RepositoryName: aws.String(repo),
		ImageId:        &ecrtypes.ImageIdentifier{ImageTag: aws.String("scanme")},
	})
	require.NoError(t, err)
	require.NotNil(t, findings.ImageScanStatus)
	assert.Equal(t, ecrtypes.ScanStatusComplete, findings.ImageScanStatus.Status)
	require.NotNil(t, findings.ImageScanFindings)
	assert.Empty(t, findings.ImageScanFindings.Findings)
}

// TestECR_LifecyclePolicyPreview puts a lifecycle policy, starts a preview,
// then reads the preview back.
func TestECR_LifecyclePolicyPreview(t *testing.T) {
	client := ecrClient()
	repo := "ecr-lifecycle-preview-repo"
	_, err := client.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)

	policy := `{"rules":[{"rulePriority":1,"description":"expire untagged","selection":{"tagStatus":"untagged","countType":"sinceImagePushed","countUnit":"days","countNumber":14},"action":{"type":"expire"}}]}`
	_, err = client.PutLifecyclePolicy(ctx, &ecr.PutLifecyclePolicyInput{
		RepositoryName:      aws.String(repo),
		LifecyclePolicyText: aws.String(policy),
	})
	require.NoError(t, err)

	start, err := client.StartLifecyclePolicyPreview(ctx, &ecr.StartLifecyclePolicyPreviewInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)
	assert.Equal(t, repo, aws.ToString(start.RepositoryName))

	preview, err := client.GetLifecyclePolicyPreview(ctx, &ecr.GetLifecyclePolicyPreviewInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)
	assert.Equal(t, policy, aws.ToString(preview.LifecyclePolicyText))
	require.NotNil(t, preview.Summary)
}

// TestECR_RegistryPolicyAndReplication drives the registry-level operations:
// the registry permissions policy CRUD, the replication configuration, and
// DescribeRegistry / DescribeImageReplicationStatus read-backs.
func TestECR_RegistryPolicyAndReplication(t *testing.T) {
	client := ecrClient()

	regPolicy := `{"Version":"2012-10-17","Statement":[{"Sid":"AllowReplicate","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":["ecr:CreateRepository","ecr:ReplicateImage"],"Resource":"*"}]}`
	put, err := client.PutRegistryPolicy(ctx, &ecr.PutRegistryPolicyInput{
		PolicyText: aws.String(regPolicy),
	})
	require.NoError(t, err)
	assert.Equal(t, regPolicy, aws.ToString(put.PolicyText))

	got, err := client.GetRegistryPolicy(ctx, &ecr.GetRegistryPolicyInput{})
	require.NoError(t, err)
	assert.Equal(t, regPolicy, aws.ToString(got.PolicyText))

	repl, err := client.PutReplicationConfiguration(ctx, &ecr.PutReplicationConfigurationInput{
		ReplicationConfiguration: &ecrtypes.ReplicationConfiguration{
			Rules: []ecrtypes.ReplicationRule{
				{
					Destinations: []ecrtypes.ReplicationDestination{
						{Region: aws.String("us-west-2"), RegistryId: aws.String("000000000000")},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, repl.ReplicationConfiguration)
	require.Len(t, repl.ReplicationConfiguration.Rules, 1)

	reg, err := client.DescribeRegistry(ctx, &ecr.DescribeRegistryInput{})
	require.NoError(t, err)
	require.NotNil(t, reg.ReplicationConfiguration)
	require.Len(t, reg.ReplicationConfiguration.Rules, 1)
	require.Len(t, reg.ReplicationConfiguration.Rules[0].Destinations, 1)
	assert.Equal(t, "us-west-2", aws.ToString(reg.ReplicationConfiguration.Rules[0].Destinations[0].Region))

	// An image's replication status reflects the configured destination.
	repo := "ecr-replication-repo"
	ecrPutTestImage(t, client, repo, "v1")
	status, err := client.DescribeImageReplicationStatus(ctx, &ecr.DescribeImageReplicationStatusInput{
		RepositoryName: aws.String(repo),
		ImageId:        &ecrtypes.ImageIdentifier{ImageTag: aws.String("v1")},
	})
	require.NoError(t, err)
	assert.Equal(t, repo, aws.ToString(status.RepositoryName))
	require.NotEmpty(t, status.ReplicationStatuses)
	assert.Equal(t, "us-west-2", aws.ToString(status.ReplicationStatuses[0].Region))

	del, err := client.DeleteRegistryPolicy(ctx, &ecr.DeleteRegistryPolicyInput{})
	require.NoError(t, err)
	assert.Equal(t, regPolicy, aws.ToString(del.PolicyText))
}
