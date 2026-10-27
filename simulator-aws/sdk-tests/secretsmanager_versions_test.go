package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSecretsManager_ListSecretVersionIds covers version staging:
//
//  1. CreateSecret creates version A as AWSCURRENT.
//  2. PutSecretValue adds B: A becomes AWSPREVIOUS, B AWSCURRENT.
//  3. PutSecretValue adds C: A loses its stages, B becomes AWSPREVIOUS, C
//     AWSCURRENT.
//  4. ListSecretVersionIds omits the unstaged A unless IncludeDeprecated.
//  5. GetSecretValue by VersionId returns that version's value.
func TestSecretsManager_ListSecretVersionIds(t *testing.T) {
	t.Parallel()
	client := smClient()
	name := "versioning-test-secret"

	create, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String("value-A"),
	})
	require.NoError(t, err)
	require.NotNil(t, create.VersionId)
	versionA := *create.VersionId

	t.Cleanup(func() {
		_, _ = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
			SecretId:                   aws.String(name),
			ForceDeleteWithoutRecovery: aws.Bool(true),
		})
	})

	putB, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String("value-B"),
	})
	require.NoError(t, err)
	versionB := *putB.VersionId

	putC, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String("value-C"),
	})
	require.NoError(t, err)
	versionC := *putC.VersionId

	// By default the listing holds B and C; A has no stages after C.
	list, err := client.ListSecretVersionIds(ctx, &secretsmanager.ListSecretVersionIdsInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	require.Equal(t, name, aws.ToString(list.Name))
	require.Len(t, list.Versions, 2,
		"expected 2 staged versions (B = AWSPREVIOUS, C = AWSCURRENT); got %d", len(list.Versions))

	stagesByID := map[string][]string{}
	for _, v := range list.Versions {
		stagesByID[aws.ToString(v.VersionId)] = v.VersionStages
	}
	assert.Equal(t, []string{"AWSPREVIOUS"}, stagesByID[versionB],
		"version B should have stage AWSPREVIOUS after C was added")
	assert.Equal(t, []string{"AWSCURRENT"}, stagesByID[versionC],
		"version C (newest) should have stage AWSCURRENT")
	_, hasA := stagesByID[versionA]
	assert.False(t, hasA, "deprecated version A should be excluded by default")

	listAll, err := client.ListSecretVersionIds(ctx, &secretsmanager.ListSecretVersionIdsInput{
		SecretId:          aws.String(name),
		IncludeDeprecated: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, listAll.Versions, 3)

	getA, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:  aws.String(name),
		VersionId: aws.String(versionA),
	})
	require.NoError(t, err)
	assert.Equal(t, "value-A", aws.ToString(getA.SecretString),
		"GetSecretValue with VersionId selector should return that exact version's bytes")

	getB, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:  aws.String(name),
		VersionId: aws.String(versionB),
	})
	require.NoError(t, err)
	assert.Equal(t, "value-B", aws.ToString(getB.SecretString))

	getCurrent, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:     aws.String(name),
		VersionStage: aws.String("AWSCURRENT"),
	})
	require.NoError(t, err)
	assert.Equal(t, "value-C", aws.ToString(getCurrent.SecretString))

	getPrev, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:     aws.String(name),
		VersionStage: aws.String("AWSPREVIOUS"),
	})
	require.NoError(t, err)
	assert.Equal(t, "value-B", aws.ToString(getPrev.SecretString))
}

func TestSecretsManager_GetRandomPassword(t *testing.T) {
	t.Parallel()
	client := smClient()

	// The default length is 32.
	out, err := client.GetRandomPassword(ctx, &secretsmanager.GetRandomPasswordInput{})
	require.NoError(t, err)
	require.NotNil(t, out.RandomPassword)
	assert.Len(t, aws.ToString(out.RandomPassword), 32)

	// Excluding punctuation and numbers leaves only letters.
	out2, err := client.GetRandomPassword(ctx, &secretsmanager.GetRandomPasswordInput{
		PasswordLength:     aws.Int64(16),
		ExcludePunctuation: aws.Bool(true),
		ExcludeNumbers:     aws.Bool(true),
	})
	require.NoError(t, err)
	pw := aws.ToString(out2.RandomPassword)
	assert.Len(t, pw, 16)
	for _, ch := range pw {
		assert.True(t,
			(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z'),
			"password char %q should be letter only", string(ch))
	}

	out3, err := client.GetRandomPassword(ctx, &secretsmanager.GetRandomPasswordInput{
		PasswordLength:     aws.Int64(8),
		ExcludeUppercase:   aws.Bool(true),
		ExcludeLowercase:   aws.Bool(true),
		ExcludePunctuation: aws.Bool(true),
	})
	require.NoError(t, err)
	pw3 := aws.ToString(out3.RandomPassword)
	assert.Len(t, pw3, 8)
	for _, ch := range pw3 {
		assert.True(t, ch >= '0' && ch <= '9', "expected digit-only, got %q", string(ch))
	}

	// A length out of range is refused.
	_, err = client.GetRandomPassword(ctx, &secretsmanager.GetRandomPasswordInput{
		PasswordLength: aws.Int64(1),
	})
	requireAWSErrorCode(t, err, "InvalidParameterException")
	assert.True(t, strings.Contains(err.Error(), "PasswordLength"),
		"expected error to mention PasswordLength; got %v", err)
}
