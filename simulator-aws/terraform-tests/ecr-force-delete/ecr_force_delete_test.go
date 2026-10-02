package ecrforcedelete_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECRForceDeleteTerraform destroys two aws_ecr_repository resources that
// each hold an image pushed outside Terraform. Amazon ECR refuses the destroy
// of the one without force_delete with RepositoryNotEmptyException and deletes
// the one with force_delete together with its image; once the guarded
// repository's image is gone, its destroy succeeds too.
func TestECRForceDeleteTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	ctx := context.Background()
	client := ecr.New(ecr.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},"layers":[]}`
	for _, repo := range []string{"tf-ecr-forced", "tf-ecr-guarded"} {
		_, err := client.PutImage(ctx, &ecr.PutImageInput{
			RepositoryName: aws.String(repo),
			ImageManifest:  aws.String(manifest),
			ImageTag:       aws.String("v1"),
		})
		require.NoError(t, err)
	}

	out := env.TerraformFails(t, "destroy", "-auto-approve")
	assert.Contains(t, string(out), "RepositoryNotEmptyException")

	_, err := client.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{RepositoryNames: []string{"tf-ecr-forced"}})
	var missing *ecrtypes.RepositoryNotFoundException
	require.ErrorAs(t, err, &missing, "force_delete deletes a repository that holds images")
	guarded, err := client.ListImages(ctx, &ecr.ListImagesInput{RepositoryName: aws.String("tf-ecr-guarded")})
	require.NoError(t, err, "a refused destroy keeps the repository")
	require.Len(t, guarded.ImageIds, 1, "a refused destroy keeps the repository's image")

	_, err = client.BatchDeleteImage(ctx, &ecr.BatchDeleteImageInput{
		RepositoryName: aws.String("tf-ecr-guarded"),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String("v1")}},
	})
	require.NoError(t, err)
	env.Terraform(t, "destroy", "-auto-approve")
}
