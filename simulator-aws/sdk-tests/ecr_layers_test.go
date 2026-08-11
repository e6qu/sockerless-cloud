package aws_sdk_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECR_RepositoryPolicy covers the `aws_ecr_repository_policy` surface
// : Set → Get round-trip + Delete.
func TestECR_RepositoryPolicy(t *testing.T) {
	c := ecrClient()
	repo := "policy-repo"
	_, err := c.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)

	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"AllowPull","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":["ecr:GetDownloadUrlForLayer","ecr:BatchGetImage"]}]}`
	sp, err := c.SetRepositoryPolicy(ctx, &ecr.SetRepositoryPolicyInput{
		RepositoryName: aws.String(repo),
		PolicyText:     aws.String(policy),
	})
	require.NoError(t, err)
	assert.Equal(t, policy, aws.ToString(sp.PolicyText))

	gp, err := c.GetRepositoryPolicy(ctx, &ecr.GetRepositoryPolicyInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	assert.Equal(t, policy, aws.ToString(gp.PolicyText))

	_, err = c.DeleteRepositoryPolicy(ctx, &ecr.DeleteRepositoryPolicyInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	_, err = c.GetRepositoryPolicy(ctx, &ecr.GetRepositoryPolicyInput{RepositoryName: aws.String(repo)})
	require.Error(t, err, "policy must be gone after delete")
}

// TestECR_LayerUploadPipeline covers the image-layer data plane:
// Initiate → UploadPart → Complete (content-addressed) → GetDownloadUrl, with
// BatchCheckLayerAvailability reflecting the real store before/after.
func TestECR_LayerUploadPipeline(t *testing.T) {
	c := ecrClient()
	repo := "layer-repo"
	_, err := c.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)

	blob := []byte("the-layer-bytes-content-for-sha256")
	digest := ecrLayerDigest(blob)

	// Before any upload the layer is UNAVAILABLE (no longer a blanket AVAILABLE).
	avail, err := c.BatchCheckLayerAvailability(ctx, &ecr.BatchCheckLayerAvailabilityInput{
		RepositoryName: aws.String(repo),
		LayerDigests:   []string{digest},
	})
	require.NoError(t, err)
	require.Len(t, avail.Layers, 1)
	assert.Equal(t, ecrtypes.LayerAvailabilityUnavailable, avail.Layers[0].LayerAvailability)

	init, err := c.InitiateLayerUpload(ctx, &ecr.InitiateLayerUploadInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	uploadID := init.UploadId
	require.NotEmpty(t, aws.ToString(uploadID))

	_, err = c.UploadLayerPart(ctx, &ecr.UploadLayerPartInput{
		RepositoryName: aws.String(repo),
		UploadId:       uploadID,
		PartFirstByte:  aws.Int64(0),
		PartLastByte:   aws.Int64(int64(len(blob) - 1)),
		LayerPartBlob:  blob,
	})
	require.NoError(t, err)

	comp, err := c.CompleteLayerUpload(ctx, &ecr.CompleteLayerUploadInput{
		RepositoryName: aws.String(repo),
		UploadId:       uploadID,
		LayerDigests:   []string{digest},
	})
	require.NoError(t, err)
	assert.Equal(t, digest, aws.ToString(comp.LayerDigest))

	// Now AVAILABLE.
	avail2, err := c.BatchCheckLayerAvailability(ctx, &ecr.BatchCheckLayerAvailabilityInput{
		RepositoryName: aws.String(repo),
		LayerDigests:   []string{digest},
	})
	require.NoError(t, err)
	assert.Equal(t, ecrtypes.LayerAvailabilityAvailable, avail2.Layers[0].LayerAvailability)

	dl, err := c.GetDownloadUrlForLayer(ctx, &ecr.GetDownloadUrlForLayerInput{
		RepositoryName: aws.String(repo),
		LayerDigest:    aws.String(digest),
	})
	require.NoError(t, err)
	assert.Equal(t, digest, aws.ToString(dl.LayerDigest))
	assert.NotEmpty(t, aws.ToString(dl.DownloadUrl))

	// A digest that doesn't match the uploaded bytes is rejected.
	bad, err := c.InitiateLayerUpload(ctx, &ecr.InitiateLayerUploadInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	_, err = c.UploadLayerPart(ctx, &ecr.UploadLayerPartInput{
		RepositoryName: aws.String(repo),
		UploadId:       bad.UploadId,
		PartFirstByte:  aws.Int64(0),
		PartLastByte:   aws.Int64(int64(len(blob) - 1)),
		LayerPartBlob:  blob,
	})
	require.NoError(t, err)
	_, err = c.CompleteLayerUpload(ctx, &ecr.CompleteLayerUploadInput{
		RepositoryName: aws.String(repo),
		UploadId:       bad.UploadId,
		LayerDigests:   []string{"sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
	})
	require.Error(t, err, "digest mismatch must be rejected")
}

func ecrLayerDigest(b []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(b))
}

// TestECR_RepositoryConfiguration covers the aws_ecr_repository read-back fields
// — image tag mutability, scan-on-push, and encryption config
// round-trip through CreateRepository + DescribeRepositories.
func TestECR_RepositoryConfiguration(t *testing.T) {
	c := ecrClient()

	repo := "config-repo"
	created, err := c.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName:             aws.String(repo),
		ImageTagMutability:         ecrtypes.ImageTagMutabilityImmutable,
		ImageScanningConfiguration: &ecrtypes.ImageScanningConfiguration{ScanOnPush: true},
		EncryptionConfiguration:    &ecrtypes.EncryptionConfiguration{EncryptionType: ecrtypes.EncryptionTypeAes256},
	})
	require.NoError(t, err)
	assert.Equal(t, ecrtypes.ImageTagMutabilityImmutable, created.Repository.ImageTagMutability)
	require.NotNil(t, created.Repository.ImageScanningConfiguration)
	assert.True(t, created.Repository.ImageScanningConfiguration.ScanOnPush)

	desc, err := c.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{RepositoryNames: []string{repo}})
	require.NoError(t, err)
	require.Len(t, desc.Repositories, 1)
	got := desc.Repositories[0]
	assert.Equal(t, ecrtypes.ImageTagMutabilityImmutable, got.ImageTagMutability)
	require.NotNil(t, got.ImageScanningConfiguration)
	assert.True(t, got.ImageScanningConfiguration.ScanOnPush)
	require.NotNil(t, got.EncryptionConfiguration)
	assert.Equal(t, ecrtypes.EncryptionTypeAes256, got.EncryptionConfiguration.EncryptionType)

	// A bare repository gets the AWS defaults (MUTABLE / AES256 / no scan).
	def, err := c.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String("config-default-repo")})
	require.NoError(t, err)
	assert.Equal(t, ecrtypes.ImageTagMutabilityMutable, def.Repository.ImageTagMutability)
	assert.Equal(t, ecrtypes.EncryptionTypeAes256, def.Repository.EncryptionConfiguration.EncryptionType)
}
