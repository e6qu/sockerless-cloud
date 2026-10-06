package ecrimagesize_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECRImageSizeTerraform reads an image pushed into an aws_ecr_repository
// through the aws_ecr_image data source, whose image_size_in_bytes is
// DescribeImages' imageSizeInBytes: the image's layers and its config.
func TestECRImageSizeTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	client := ecr.New(ecr.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",` +
		`"config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":1469,"digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"},` +
		`"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":3623807,"digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222"},` +
		`{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":409,"digest":"sha256:3333333333333333333333333333333333333333333333333333333333333333"}]}`
	_, err := client.PutImage(context.Background(), &ecr.PutImageInput{
		RepositoryName: aws.String("tf-ecr-image-size"),
		ImageManifest:  aws.String(manifest),
		ImageTag:       aws.String("v1"),
	})
	require.NoError(t, err)

	env.Terraform(t, "apply", "-auto-approve", "-var", "read_image=true")
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	assert.Equal(t, "3625685", outputs["image_size_in_bytes"].Value, "two layers of 3623807 and 409 bytes and a 1469-byte config")

	env.Terraform(t, "destroy", "-auto-approve", "-var", "read_image=true")
}
