package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	types "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func smClient() *secretsmanager.Client {
	return secretsmanager.NewFromConfig(sdkConfig(), func(o *secretsmanager.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestSecretsManager_CreateGetUpdateDelete covers a secret a workflow creates,
// a runner reads, the workflow rotates and then deletes.
func TestSecretsManager_CreateGetUpdateDelete(t *testing.T) {
	t.Parallel()
	c := smClient()

	createOut, err := c.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String("runner/db-password"),
		Description:  aws.String("Runner DB credential"),
		SecretString: aws.String("hunter2"),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.ARN)
	assert.Contains(t, *createOut.ARN, "arn:aws:secretsmanager:")
	assert.Contains(t, *createOut.ARN, ":secret:runner/db-password-")

	getByName, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String("runner/db-password"),
	})
	require.NoError(t, err)
	assert.Equal(t, "hunter2", *getByName.SecretString)
	assert.NotEmpty(t, *getByName.VersionId)

	// Secrets Manager accepts the full ARN as SecretId.
	getByArn, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: createOut.ARN,
	})
	require.NoError(t, err)
	assert.Equal(t, "hunter2", *getByArn.SecretString)

	updateOut, err := c.UpdateSecret(ctx, &secretsmanager.UpdateSecretInput{
		SecretId:     aws.String("runner/db-password"),
		SecretString: aws.String("rotated-secret"),
	})
	require.NoError(t, err)
	require.NotNil(t, updateOut.VersionId)
	assert.NotEqual(t, *createOut.VersionId, *updateOut.VersionId, "VersionId must change on rotation")

	rotated, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String("runner/db-password"),
	})
	require.NoError(t, err)
	assert.Equal(t, "rotated-secret", *rotated.SecretString)

	desc, err := c.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String("runner/db-password"),
	})
	require.NoError(t, err)
	assert.Equal(t, "Runner DB credential", *desc.Description)
	require.Contains(t, desc.VersionIdsToStages, *rotated.VersionId)
	assert.Contains(t, desc.VersionIdsToStages[*rotated.VersionId], "AWSCURRENT")

	_, err = c.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String("runner/db-password"),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	require.NoError(t, err)

	_, err = c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String("runner/db-password"),
	})
	require.Error(t, err, "GetSecretValue after delete must 404")
}

func TestSecretsManager_DuplicateNameRejected(t *testing.T) {
	t.Parallel()
	c := smClient()
	defer c.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String("dup-name"),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	_, err := c.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String("dup-name"),
		SecretString: aws.String("first"),
	})
	require.NoError(t, err)
	_, err = c.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String("dup-name"),
		SecretString: aws.String("second"),
	})
	requireAWSErrorCode(t, err, "ResourceExistsException")
}

func TestSecretsManager_ListAndTag(t *testing.T) {
	t.Parallel()
	c := smClient()

	defer c.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String("list-test"),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	_, err := c.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String("list-test"),
		SecretString: aws.String("v1"),
	})
	require.NoError(t, err)

	listOut, err := c.ListSecrets(ctx, &secretsmanager.ListSecretsInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.SecretList {
		if s.Name != nil && *s.Name == "list-test" {
			found = true
			break
		}
	}
	assert.True(t, found, "ListSecrets must include the just-created secret")

	// PutSecretValue changes the value and leaves the metadata.
	put, err := c.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String("list-test"),
		SecretString: aws.String("v2"),
	})
	require.NoError(t, err)
	require.NotNil(t, put.VersionId)

	_, err = c.TagResource(ctx, &secretsmanager.TagResourceInput{
		SecretId: aws.String("list-test"),
		Tags: []types.Tag{
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
	})
	require.NoError(t, err)

	desc, err := c.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String("list-test"),
	})
	require.NoError(t, err)
	require.Len(t, desc.Tags, 1)
	assert.Equal(t, "env", *desc.Tags[0].Key)

	_, err = c.UntagResource(ctx, &secretsmanager.UntagResourceInput{
		SecretId: aws.String("list-test"),
		TagKeys:  []string{"env"},
	})
	require.NoError(t, err)
}

// TestSecretsManager_GetResourcePolicy expects ResourcePolicy omitted when no
// policy is attached. terraform-provider-aws reads it after CreateSecret for
// the policy attribute, and an empty string fails it with "unexpected end of
// JSON input".
func TestSecretsManager_GetResourcePolicy(t *testing.T) {
	t.Parallel()
	c := smClient()

	createOut, err := c.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String("grp-test-secret"),
		SecretString: aws.String("value"),
	})
	require.NoError(t, err)
	secretArn := *createOut.ARN
	t.Cleanup(func() {
		_, _ = c.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
			SecretId:                   aws.String("grp-test-secret"),
			ForceDeleteWithoutRecovery: aws.Bool(true),
		})
	})

	out, err := c.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{
		SecretId: aws.String("grp-test-secret"),
	})
	require.NoError(t, err)
	assert.Equal(t, secretArn, *out.ARN, "ARN must match the created secret")
	assert.Equal(t, "grp-test-secret", *out.Name)
	assert.Nil(t, out.ResourcePolicy, "ResourcePolicy must be nil when no policy is attached")

	outByArn, err := c.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{
		SecretId: aws.String(secretArn),
	})
	require.NoError(t, err)
	assert.Equal(t, secretArn, *outByArn.ARN)
}

func TestSM_ListSecrets_Pagination(t *testing.T) {
	t.Parallel()
	client := smClient()
	names := []string{"pag-sm-a", "pag-sm-b", "pag-sm-c"}
	for _, n := range names {
		_, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
			Name:         aws.String(n),
			SecretString: aws.String("val"),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
				SecretId:                   aws.String(n),
				ForceDeleteWithoutRecovery: aws.Bool(true),
			})
		})
	}

	seen := map[string]bool{}
	pager := secretsmanager.NewListSecretsPaginator(client, &secretsmanager.ListSecretsInput{MaxResults: aws.Int32(1)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.SecretList {
			seen[aws.ToString(s.Name)] = true
		}
	}
	for _, n := range names {
		assert.True(t, seen[n], "secret %s should appear via pagination", n)
	}
}

func TestSM_GetSecret_NotFound_ErrorClassification(t *testing.T) {
	t.Parallel()
	client := smClient()
	_, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String("nonexistent-secret"),
	})
	require.Error(t, err)
	var notFound *types.ResourceNotFoundException
	assert.True(t, errors.As(err, &notFound),
		"SecretsManager ResourceNotFoundException must be classified by SDK errors.As; got %T: %v", err, err)
}
