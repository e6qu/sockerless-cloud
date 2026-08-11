package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKMSCLI_KeyAliasAndCrypto(t *testing.T) {
	createOut := runCLI(t, awsCLI("kms", "create-key",
		"--description", "cli-kms-key",
		"--output", "json"))
	var createResult struct {
		KeyMetadata struct {
			KeyId    string `json:"KeyId"`
			Arn      string `json:"Arn"`
			KeyState string `json:"KeyState"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, createOut, &createResult)
	require.NotEmpty(t, createResult.KeyMetadata.KeyId)
	require.Contains(t, createResult.KeyMetadata.Arn, ":key/")
	require.Equal(t, "Enabled", createResult.KeyMetadata.KeyState)

	aliasName := "alias/cli-kms-coverage"
	runCLI(t, awsCLI("kms", "create-alias",
		"--alias-name", aliasName,
		"--target-key-id", createResult.KeyMetadata.KeyId))
	t.Cleanup(func() {
		_ = awsCLI("kms", "delete-alias", "--alias-name", aliasName).Run()
		_ = awsCLI("kms", "schedule-key-deletion",
			"--key-id", createResult.KeyMetadata.KeyId,
			"--pending-window-in-days", "7").Run()
	})

	encryptOut := runCLI(t, awsCLI("kms", "encrypt",
		"--key-id", aliasName,
		"--plaintext", "cli-secret",
		"--cli-binary-format", "raw-in-base64-out",
		"--output", "json"))
	var encryptResult struct {
		CiphertextBlob string `json:"CiphertextBlob"`
		KeyId          string `json:"KeyId"`
	}
	parseJSON(t, encryptOut, &encryptResult)
	require.NotEmpty(t, encryptResult.CiphertextBlob)
	require.Contains(t, encryptResult.KeyId, createResult.KeyMetadata.KeyId)

	decryptOut := runCLI(t, awsCLI("kms", "decrypt",
		"--ciphertext-blob", encryptResult.CiphertextBlob,
		"--output", "json"))
	var decryptResult struct {
		Plaintext string `json:"Plaintext"`
		KeyId     string `json:"KeyId"`
	}
	parseJSON(t, decryptOut, &decryptResult)
	require.Equal(t, "Y2xpLXNlY3JldA==", decryptResult.Plaintext)
	require.Contains(t, decryptResult.KeyId, createResult.KeyMetadata.KeyId)
}

func TestKMSCLI_KeyRotation(t *testing.T) {
	createOut := runCLI(t, awsCLI("kms", "create-key", "--output", "json"))
	var created struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, createOut, &created)
	keyId := created.KeyMetadata.KeyId
	require.NotEmpty(t, keyId)

	// Default: rotation disabled.
	statusOut := runCLI(t, awsCLI("kms", "get-key-rotation-status",
		"--key-id", keyId, "--output", "json"))
	var status struct {
		KeyRotationEnabled   bool `json:"KeyRotationEnabled"`
		RotationPeriodInDays int  `json:"RotationPeriodInDays"`
	}
	parseJSON(t, statusOut, &status)
	assert.False(t, status.KeyRotationEnabled)

	// Enable, then verify the reported cadence.
	runCLI(t, awsCLI("kms", "enable-key-rotation", "--key-id", keyId))
	statusOut = runCLI(t, awsCLI("kms", "get-key-rotation-status",
		"--key-id", keyId, "--output", "json"))
	parseJSON(t, statusOut, &status)
	assert.True(t, status.KeyRotationEnabled)
	assert.Equal(t, 365, status.RotationPeriodInDays)

	// Disable reverts to false.
	runCLI(t, awsCLI("kms", "disable-key-rotation", "--key-id", keyId))
	statusOut = runCLI(t, awsCLI("kms", "get-key-rotation-status",
		"--key-id", keyId, "--output", "json"))
	parseJSON(t, statusOut, &status)
	assert.False(t, status.KeyRotationEnabled)
}

func TestKMSCLI_Tagging(t *testing.T) {
	createOut := runCLI(t, awsCLI("kms", "create-key",
		"--tags", "TagKey=edd:managed,TagValue=true",
		"--output", "json"))
	var created struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, createOut, &created)
	keyId := created.KeyMetadata.KeyId
	require.NotEmpty(t, keyId)

	listTags := func() map[string]string {
		out := runCLI(t, awsCLI("kms", "list-resource-tags", "--key-id", keyId, "--output", "json"))
		var res struct {
			Tags []struct {
				TagKey   string `json:"TagKey"`
				TagValue string `json:"TagValue"`
			} `json:"Tags"`
		}
		parseJSON(t, out, &res)
		m := map[string]string{}
		for _, tg := range res.Tags {
			m[tg.TagKey] = tg.TagValue
		}
		return m
	}

	// CreateKey --tags must round-trip (not come back empty).
	assert.Equal(t, "true", listTags()["edd:managed"],
		"create-key --tags must round-trip through list-resource-tags")

	runCLI(t, awsCLI("kms", "tag-resource", "--key-id", keyId,
		"--tags", "TagKey=team,TagValue=platform"))
	assert.Equal(t, "platform", listTags()["team"])

	runCLI(t, awsCLI("kms", "untag-resource", "--key-id", keyId,
		"--tag-keys", "edd:managed"))
	_, present := listTags()["edd:managed"]
	assert.False(t, present, "untag-resource must remove the tag")
}

func TestKMSCLI_GetKeyPolicy(t *testing.T) {
	createOut := runCLI(t, awsCLI("kms", "create-key", "--output", "json"))
	var created struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, createOut, &created)
	keyId := created.KeyMetadata.KeyId
	require.NotEmpty(t, keyId)

	// Default policy must be non-empty and reference "default" as the policy name.
	out := runCLI(t, awsCLI("kms", "get-key-policy",
		"--key-id", keyId,
		"--policy-name", "default",
		"--output", "json"))
	var result struct {
		Policy     string `json:"Policy"`
		PolicyName string `json:"PolicyName"`
	}
	parseJSON(t, out, &result)
	assert.NotEmpty(t, result.Policy, "Policy must be non-empty")
	assert.Equal(t, "default", result.PolicyName)
}
