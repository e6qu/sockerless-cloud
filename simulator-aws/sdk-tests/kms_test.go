package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kmsClient() *kms.Client {
	return kms.NewFromConfig(sdkConfig(), func(o *kms.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestKMS_CreateEncryptDecryptCycle round-trips plaintext through Encrypt and
// Decrypt; the ciphertext is opaque to the caller.
func TestKMS_CreateEncryptDecryptCycle(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("runner-secret-key"),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.KeyMetadata)
	keyId := *createOut.KeyMetadata.KeyId
	assert.Contains(t, *createOut.KeyMetadata.Arn, "arn:aws:kms:")
	assert.Equal(t, "Enabled", string(createOut.KeyMetadata.KeyState))

	plaintext := []byte("hunter2")
	encOut, err := c.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(keyId),
		Plaintext: plaintext,
	})
	require.NoError(t, err)
	require.NotEmpty(t, encOut.CiphertextBlob, "Encrypt must return a non-empty ciphertext blob")

	decOut, err := c.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: encOut.CiphertextBlob,
	})
	require.NoError(t, err)
	assert.Equal(t, plaintext, decOut.Plaintext, "Decrypted plaintext must round-trip exactly")
	assert.Contains(t, *decOut.KeyId, keyId, "Decrypt KeyId must reference the encrypting key")
}

func TestKMS_AliasResolution(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("alias-target")})
	require.NoError(t, err)
	keyId := *createOut.KeyMetadata.KeyId

	_, err = c.CreateAlias(ctx, &kms.CreateAliasInput{
		AliasName:   aws.String("alias/runner-cache"),
		TargetKeyId: aws.String(keyId),
	})
	require.NoError(t, err)
	defer c.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: aws.String("alias/runner-cache")})

	encOut, err := c.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String("alias/runner-cache"),
		Plaintext: []byte("test"),
	})
	require.NoError(t, err)
	assert.Contains(t, *encOut.KeyId, keyId)

	// A duplicate alias is a 400.
	_, err = c.CreateAlias(ctx, &kms.CreateAliasInput{
		AliasName:   aws.String("alias/runner-cache"),
		TargetKeyId: aws.String(keyId),
	})
	require.Error(t, err, "duplicate alias must fail like real KMS (AlreadyExistsException)")

	listOut, err := c.ListAliases(ctx, &kms.ListAliasesInput{})
	require.NoError(t, err)
	found := false
	for _, a := range listOut.Aliases {
		if a.AliasName != nil && *a.AliasName == "alias/runner-cache" {
			found = true
			break
		}
	}
	assert.True(t, found, "ListAliases must include the just-created alias")
}

// TestKMS_ListAliasesKeyIdFilter expects ListAliases to return only the aliases
// of the KeyId given.
func TestKMS_ListAliasesKeyIdFilter(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	keyA, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("filter-a")})
	require.NoError(t, err)
	keyB, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("filter-b")})
	require.NoError(t, err)
	idA, idB := *keyA.KeyMetadata.KeyId, *keyB.KeyMetadata.KeyId

	_, err = c.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String("alias/filter-a"), TargetKeyId: aws.String(idA)})
	require.NoError(t, err)
	defer c.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: aws.String("alias/filter-a")})
	_, err = c.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String("alias/filter-b"), TargetKeyId: aws.String(idB)})
	require.NoError(t, err)
	defer c.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: aws.String("alias/filter-b")})

	out, err := c.ListAliases(ctx, &kms.ListAliasesInput{KeyId: aws.String(idA)})
	require.NoError(t, err)
	require.NotEmpty(t, out.Aliases)
	for _, a := range out.Aliases {
		assert.Equal(t, idA, aws.ToString(a.TargetKeyId),
			"KeyId filter must return only aliases for key %s, got alias for %s", idA, aws.ToString(a.TargetKeyId))
	}
	var sawA, sawB bool
	for _, a := range out.Aliases {
		switch aws.ToString(a.AliasName) {
		case "alias/filter-a":
			sawA = true
		case "alias/filter-b":
			sawB = true
		}
	}
	assert.True(t, sawA, "KeyId=idA must list alias/filter-a")
	assert.False(t, sawB, "KeyId=idA must not list alias/filter-b")
}

// TestKMS_GenerateDataKey covers envelope encryption: the plaintext data key to
// use locally and its wrapped ciphertext to store with the payload.
func TestKMS_GenerateDataKey(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{})
	require.NoError(t, err)
	keyId := *createOut.KeyMetadata.KeyId

	out, err := c.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId:         aws.String(keyId),
		NumberOfBytes: aws.Int32(32),
	})
	require.NoError(t, err)
	assert.Len(t, out.Plaintext, 32, "Plaintext must match requested NumberOfBytes")
	assert.NotEmpty(t, out.CiphertextBlob, "CiphertextBlob must be present")

	decOut, err := c.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: out.CiphertextBlob,
	})
	require.NoError(t, err)
	assert.Equal(t, out.Plaintext, decOut.Plaintext)

	out2, err := c.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId:         aws.String(keyId),
		NumberOfBytes: aws.Int32(32),
	})
	require.NoError(t, err)
	assert.NotEqual(t, out.Plaintext, out2.Plaintext,
		"GenerateDataKey must return fresh key material on each call")
}

func TestKMS_DescribeAndScheduleDeletion(t *testing.T) {
	t.Parallel()
	c := kmsClient()
	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{})
	require.NoError(t, err)
	keyId := *createOut.KeyMetadata.KeyId

	desc, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, keyId, *desc.KeyMetadata.KeyId)

	listOut, err := c.ListKeys(ctx, &kms.ListKeysInput{})
	require.NoError(t, err)
	found := false
	for _, k := range listOut.Keys {
		if k.KeyId != nil && *k.KeyId == keyId {
			found = true
			break
		}
	}
	assert.True(t, found, "ListKeys must include the new key")

	delOut, err := c.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
		KeyId:               aws.String(keyId),
		PendingWindowInDays: aws.Int32(7),
	})
	require.NoError(t, err)
	assert.Equal(t, "PendingDeletion", string(delOut.KeyState))
}

// TestKMS_GetKeyPolicyRoundTrip: terraform-provider-aws reads GetKeyPolicy on
// every key refresh for the `policy` attribute and calls PutKeyPolicy for a
// custom document.
func TestKMS_GetKeyPolicyRoundTrip(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("policy-test-key")})
	require.NoError(t, err)
	keyId := *createOut.KeyMetadata.KeyId

	// A fresh key has the default policy.
	getOut, err := c.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{
		KeyId:      aws.String(keyId),
		PolicyName: aws.String("default"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, *getOut.Policy, "default policy must be non-empty")
	assert.Equal(t, "default", *getOut.PolicyName)

	customPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"kms:*","Resource":"*"}]}`
	_, err = c.PutKeyPolicy(ctx, &kms.PutKeyPolicyInput{
		KeyId:      aws.String(keyId),
		PolicyName: aws.String("default"),
		Policy:     aws.String(customPolicy),
	})
	require.NoError(t, err)

	getCustom, err := c.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{
		KeyId:      aws.String(keyId),
		PolicyName: aws.String("default"),
	})
	require.NoError(t, err)
	assert.Equal(t, customPolicy, *getCustom.Policy, "GetKeyPolicy must return the stored custom policy verbatim")
}

func TestKMS_ListKeys_Pagination(t *testing.T) {
	t.Parallel()
	client := kmsClient()
	var created []string
	for i := 0; i < 3; i++ {
		out, err := client.CreateKey(ctx, &kms.CreateKeyInput{
			Description: aws.String("pag-key"),
		})
		require.NoError(t, err)
		created = append(created, aws.ToString(out.KeyMetadata.KeyId))
		keyID := aws.ToString(out.KeyMetadata.KeyId)
		t.Cleanup(func() {
			client.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
				KeyId: aws.String(keyID), PendingWindowInDays: aws.Int32(7),
			})
		})
	}

	seen := map[string]bool{}
	pager := kms.NewListKeysPaginator(client, &kms.ListKeysInput{Limit: aws.Int32(1)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, k := range page.Keys {
			seen[aws.ToString(k.KeyId)] = true
		}
	}
	for _, id := range created {
		assert.True(t, seen[id], "key %s should appear via pagination", id)
	}
}

// TestKMS_KeyRotation covers the rotation lifecycle terraform-provider-aws
// drives for `aws_kms_key { enable_key_rotation = true }`.
func TestKMS_KeyRotation(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("rotation-key")})
	require.NoError(t, err)
	keyId := *createOut.KeyMetadata.KeyId

	st, err := c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.False(t, st.KeyRotationEnabled, "new key must default to rotation disabled")

	_, err = c.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)

	st, err = c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.True(t, st.KeyRotationEnabled, "rotation must report enabled after EnableKeyRotation")
	require.NotNil(t, st.RotationPeriodInDays)
	assert.EqualValues(t, 365, *st.RotationPeriodInDays, "default rotation cadence must be 365 days")

	_, err = c.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{
		KeyId:                aws.String(keyId),
		RotationPeriodInDays: aws.Int32(90),
	})
	require.NoError(t, err)
	st, err = c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	require.NotNil(t, st.RotationPeriodInDays)
	assert.EqualValues(t, 90, *st.RotationPeriodInDays)

	_, err = c.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{
		KeyId:                aws.String(keyId),
		RotationPeriodInDays: aws.Int32(10),
	})
	requireAWSErrorCode(t, err, "ValidationException")

	_, err = c.DisableKeyRotation(ctx, &kms.DisableKeyRotationInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	st, err = c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.False(t, st.KeyRotationEnabled, "rotation must report disabled after DisableKeyRotation")
}

// TestKMS_Tagging covers the tags terraform-provider-aws drives for
// `aws_kms_key { tags = {...} }`: CreateKey's tags read back through
// ListResourceTags, which the provider polls until they appear, and
// TagResource and UntagResource change them.
func TestKMS_Tagging(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("tagged-key"),
		Tags: []kmstypes.Tag{
			{TagKey: aws.String("edd:managed"), TagValue: aws.String("true")},
			{TagKey: aws.String("env"), TagValue: aws.String("dev")},
		},
	})
	require.NoError(t, err)
	keyId := *createOut.KeyMetadata.KeyId

	tagMap := func() map[string]string {
		out, lerr := c.ListResourceTags(ctx, &kms.ListResourceTagsInput{KeyId: aws.String(keyId)})
		require.NoError(t, lerr)
		m := map[string]string{}
		for _, tg := range out.Tags {
			m[aws.ToString(tg.TagKey)] = aws.ToString(tg.TagValue)
		}
		return m
	}

	got := tagMap()
	assert.Equal(t, "true", got["edd:managed"], "CreateKey tags must round-trip through ListResourceTags")
	assert.Equal(t, "dev", got["env"])

	_, err = c.TagResource(ctx, &kms.TagResourceInput{
		KeyId: aws.String(keyId),
		Tags: []kmstypes.Tag{
			{TagKey: aws.String("team"), TagValue: aws.String("platform")},
			{TagKey: aws.String("env"), TagValue: aws.String("prod")}, // overwrites
		},
	})
	require.NoError(t, err)
	got = tagMap()
	assert.Equal(t, "platform", got["team"])
	assert.Equal(t, "prod", got["env"], "TagResource must overwrite an existing key")
	assert.Equal(t, "true", got["edd:managed"], "TagResource must preserve untouched tags")

	_, err = c.UntagResource(ctx, &kms.UntagResourceInput{
		KeyId:   aws.String(keyId),
		TagKeys: []string{"edd:managed"},
	})
	require.NoError(t, err)
	got = tagMap()
	_, present := got["edd:managed"]
	assert.False(t, present, "UntagResource must remove the named tag")
	assert.Equal(t, "platform", got["team"], "UntagResource must leave other tags intact")
}

func TestKMS_DescribeKey_NotFound_ErrorClassification(t *testing.T) {
	t.Parallel()
	client := kmsClient()
	_, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{
		KeyId: aws.String("nonexistent-key-id"),
	})
	require.Error(t, err)
	var notFound *kmstypes.NotFoundException
	assert.True(t, errors.As(err, &notFound),
		"KMS NotFoundException must be classified by SDK errors.As; got %T: %v", err, err)
}
