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

// TestKMS_CreateEncryptDecryptCycle pins the canonical envelope flow:
// CreateKey → Encrypt → Decrypt round-trips plaintext bytes through the
// sim's structured ciphertext envelope. Real KMS uses HSM-protected key
// material; the sim's envelope is opaque to SDK callers but reversible
// — same wire shape.
func TestKMS_CreateEncryptDecryptCycle(t *testing.T) {
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

// TestKMS_AliasResolution covers the alias path callers commonly use:
// CreateAlias → Encrypt(KeyId=alias/...) → ListAliases → DeleteAlias.
func TestKMS_AliasResolution(t *testing.T) {
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

	// Encrypt addressed by alias should resolve to the target key.
	encOut, err := c.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String("alias/runner-cache"),
		Plaintext: []byte("test"),
	})
	require.NoError(t, err)
	assert.Contains(t, *encOut.KeyId, keyId)

	// Duplicate alias must 400.
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

// TestKMS_ListAliasesKeyIdFilter proves ListAliases honors the KeyId parameter,
// returning only aliases pointing at that key. The sim used to ignore KeyId and
// return every alias in the account.
func TestKMS_ListAliasesKeyIdFilter(t *testing.T) {
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
	// The filtered result must include filter-a and exclude filter-b.
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

// TestKMS_GenerateDataKey covers envelope encryption — caller gets both
// the plaintext data key (to use locally) and a wrapped ciphertext (to
// store alongside the encrypted payload).
func TestKMS_GenerateDataKey(t *testing.T) {
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

	// Round-trip the wrapped data key.
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

// TestKMS_GetKeyPolicyRoundTrip exercises GetKeyPolicy and PutKeyPolicy.
// terraform-provider-aws calls GetKeyPolicy on every key refresh to populate
// the `policy` attribute; PutKeyPolicy is called when the operator sets a
// custom policy document.
func TestKMS_GetKeyPolicyRoundTrip(t *testing.T) {
	c := kmsClient()

	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("policy-test-key")})
	require.NoError(t, err)
	keyId := *createOut.KeyMetadata.KeyId

	// Fresh key returns the default policy.
	getOut, err := c.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{
		KeyId:      aws.String(keyId),
		PolicyName: aws.String("default"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, *getOut.Policy, "default policy must be non-empty")
	assert.Equal(t, "default", *getOut.PolicyName)

	// Put a custom policy and read it back.
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

// TestKMS_KeyRotation pins the automatic-rotation lifecycle that
// terraform-provider-aws drives for `aws_kms_key { enable_key_rotation
// = true }`: GetKeyRotationStatus defaults false, EnableKeyRotation flips
// it true and reports the rotation period, DisableKeyRotation reverts it.
func TestKMS_KeyRotation(t *testing.T) {
	c := kmsClient()

	createOut, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("rotation-key")})
	require.NoError(t, err)
	keyId := *createOut.KeyMetadata.KeyId

	// Fresh key: rotation disabled.
	st, err := c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.False(t, st.KeyRotationEnabled, "new key must default to rotation disabled")

	// Enable with the default annual cadence.
	_, err = c.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)

	st, err = c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.True(t, st.KeyRotationEnabled, "rotation must report enabled after EnableKeyRotation")
	require.NotNil(t, st.RotationPeriodInDays)
	assert.EqualValues(t, 365, *st.RotationPeriodInDays, "default rotation cadence must be 365 days")

	// Re-enable with a custom period.
	_, err = c.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{
		KeyId:                aws.String(keyId),
		RotationPeriodInDays: aws.Int32(90),
	})
	require.NoError(t, err)
	st, err = c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	require.NotNil(t, st.RotationPeriodInDays)
	assert.EqualValues(t, 90, *st.RotationPeriodInDays)

	// An out-of-range period is rejected.
	_, err = c.EnableKeyRotation(ctx, &kms.EnableKeyRotationInput{
		KeyId:                aws.String(keyId),
		RotationPeriodInDays: aws.Int32(10),
	})
	require.Error(t, err, "RotationPeriodInDays below 90 must be rejected")

	// Disable reverts to false.
	_, err = c.DisableKeyRotation(ctx, &kms.DisableKeyRotationInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	st, err = c.GetKeyRotationStatus(ctx, &kms.GetKeyRotationStatusInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.False(t, st.KeyRotationEnabled, "rotation must report disabled after DisableKeyRotation")
}

// TestKMS_Tagging pins the tag round-trip that terraform-provider-aws drives
// for `aws_kms_key { tags = {...} }`: CreateKey --tags must be readable via
// ListResourceTags (the provider polls it until tags propagate), and
// TagResource / UntagResource must mutate them. Pre-fix, CreateKey stored tags
// under the Secrets Manager Key/Value shape so KMS TagKey/TagValue came back
// empty, and TagResource/UntagResource were unregistered.
func TestKMS_Tagging(t *testing.T) {
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

	// CreateKey --tags must round-trip (the bug: came back as "").
	got := tagMap()
	assert.Equal(t, "true", got["edd:managed"], "CreateKey tags must round-trip through ListResourceTags")
	assert.Equal(t, "dev", got["env"])

	// TagResource adds/overwrites.
	_, err = c.TagResource(ctx, &kms.TagResourceInput{
		KeyId: aws.String(keyId),
		Tags: []kmstypes.Tag{
			{TagKey: aws.String("team"), TagValue: aws.String("platform")},
			{TagKey: aws.String("env"), TagValue: aws.String("prod")}, // overwrite
		},
	})
	require.NoError(t, err)
	got = tagMap()
	assert.Equal(t, "platform", got["team"])
	assert.Equal(t, "prod", got["env"], "TagResource must overwrite an existing key")
	assert.Equal(t, "true", got["edd:managed"], "TagResource must preserve untouched tags")

	// UntagResource removes by key.
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
	client := kmsClient()
	_, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{
		KeyId: aws.String("nonexistent-key-id"),
	})
	require.Error(t, err)
	var notFound *kmstypes.NotFoundException
	assert.True(t, errors.As(err, &notFound),
		"KMS NotFoundException must be classified by SDK errors.As; got %T: %v", err, err)
}
