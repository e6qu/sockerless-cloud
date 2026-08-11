package aws_cli_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kmsCreateKeyCLI(t *testing.T, args ...string) string {
	t.Helper()
	out := runCLI(t, awsCLI(append([]string{"kms", "create-key", "--output", "json"}, args...)...))
	var created struct {
		KeyMetadata struct {
			KeyId    string `json:"KeyId"`
			KeyState string `json:"KeyState"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.KeyMetadata.KeyId)
	t.Cleanup(func() {
		_ = awsCLI("kms", "schedule-key-deletion", "--key-id", created.KeyMetadata.KeyId,
			"--pending-window-in-days", "7").Run()
	})
	return created.KeyMetadata.KeyId
}

func TestKMSCLI_EnableDisableKey(t *testing.T) {
	keyId := kmsCreateKeyCLI(t)

	runCLI(t, awsCLI("kms", "disable-key", "--key-id", keyId))
	descOut := runCLI(t, awsCLI("kms", "describe-key", "--key-id", keyId, "--output", "json"))
	var desc struct {
		KeyMetadata struct {
			KeyState string `json:"KeyState"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, descOut, &desc)
	assert.Equal(t, "Disabled", desc.KeyMetadata.KeyState)

	runCLI(t, awsCLI("kms", "enable-key", "--key-id", keyId))
	descOut = runCLI(t, awsCLI("kms", "describe-key", "--key-id", keyId, "--output", "json"))
	parseJSON(t, descOut, &desc)
	assert.Equal(t, "Enabled", desc.KeyMetadata.KeyState)
}

func TestKMSCLI_CancelKeyDeletion(t *testing.T) {
	keyId := kmsCreateKeyCLI(t)

	runCLI(t, awsCLI("kms", "schedule-key-deletion", "--key-id", keyId, "--pending-window-in-days", "7"))
	cancelOut := runCLI(t, awsCLI("kms", "cancel-key-deletion", "--key-id", keyId, "--output", "json"))
	var cancel struct {
		KeyId string `json:"KeyId"`
	}
	parseJSON(t, cancelOut, &cancel)
	assert.Equal(t, keyId, cancel.KeyId)

	descOut := runCLI(t, awsCLI("kms", "describe-key", "--key-id", keyId, "--output", "json"))
	var desc struct {
		KeyMetadata struct {
			KeyState string `json:"KeyState"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, descOut, &desc)
	assert.Equal(t, "Disabled", desc.KeyMetadata.KeyState)
}

func TestKMSCLI_UpdateKeyDescription(t *testing.T) {
	keyId := kmsCreateKeyCLI(t)

	runCLI(t, awsCLI("kms", "update-key-description", "--key-id", keyId, "--description", "cli-updated"))
	descOut := runCLI(t, awsCLI("kms", "describe-key", "--key-id", keyId, "--output", "json"))
	var desc struct {
		KeyMetadata struct {
			Description string `json:"Description"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, descOut, &desc)
	assert.Equal(t, "cli-updated", desc.KeyMetadata.Description)
}

func TestKMSCLI_UpdateAlias(t *testing.T) {
	idA := kmsCreateKeyCLI(t)
	idB := kmsCreateKeyCLI(t)
	aliasName := "alias/cli-update-alias"

	runCLI(t, awsCLI("kms", "create-alias", "--alias-name", aliasName, "--target-key-id", idA))
	t.Cleanup(func() { _ = awsCLI("kms", "delete-alias", "--alias-name", aliasName).Run() })

	runCLI(t, awsCLI("kms", "update-alias", "--alias-name", aliasName, "--target-key-id", idB))

	encOut := runCLI(t, awsCLI("kms", "encrypt", "--key-id", aliasName, "--plaintext", "x",
		"--cli-binary-format", "raw-in-base64-out", "--output", "json"))
	var enc struct {
		KeyId string `json:"KeyId"`
	}
	parseJSON(t, encOut, &enc)
	assert.Contains(t, enc.KeyId, idB)
}

func TestKMSCLI_GenerateRandom(t *testing.T) {
	out := runCLI(t, awsCLI("kms", "generate-random", "--number-of-bytes", "48", "--output", "json"))
	var res struct {
		Plaintext string `json:"Plaintext"`
	}
	parseJSON(t, out, &res)
	raw, err := base64.StdEncoding.DecodeString(res.Plaintext)
	require.NoError(t, err)
	assert.Len(t, raw, 48, "GenerateRandom must return the requested byte count")
}

func TestKMSCLI_ListKeyPolicies(t *testing.T) {
	keyId := kmsCreateKeyCLI(t)
	out := runCLI(t, awsCLI("kms", "list-key-policies", "--key-id", keyId, "--output", "json"))
	var res struct {
		PolicyNames []string `json:"PolicyNames"`
	}
	parseJSON(t, out, &res)
	assert.Equal(t, []string{"default"}, res.PolicyNames)
}

func TestKMSCLI_RotateKeyOnDemandAndList(t *testing.T) {
	keyId := kmsCreateKeyCLI(t)

	listOut := runCLI(t, awsCLI("kms", "list-key-rotations", "--key-id", keyId, "--output", "json"))
	var list struct {
		Rotations []struct {
			RotationType string `json:"RotationType"`
		} `json:"Rotations"`
	}
	parseJSON(t, listOut, &list)
	assert.Empty(t, list.Rotations)

	rotOut := runCLI(t, awsCLI("kms", "rotate-key-on-demand", "--key-id", keyId, "--output", "json"))
	var rot struct {
		KeyId string `json:"KeyId"`
	}
	parseJSON(t, rotOut, &rot)
	assert.Equal(t, keyId, rot.KeyId)

	listOut = runCLI(t, awsCLI("kms", "list-key-rotations", "--key-id", keyId, "--output", "json"))
	parseJSON(t, listOut, &list)
	require.Len(t, list.Rotations, 1)
	assert.Equal(t, "ON_DEMAND", list.Rotations[0].RotationType)
}

func TestKMSCLI_ImportKeyMaterialCycle(t *testing.T) {
	keyId := kmsCreateKeyCLI(t, "--origin", "EXTERNAL")

	paramsOut := runCLI(t, awsCLI("kms", "get-parameters-for-import", "--key-id", keyId,
		"--wrapping-algorithm", "RSAES_OAEP_SHA_256", "--wrapping-key-spec", "RSA_2048", "--output", "json"))
	var params struct {
		ImportToken string `json:"ImportToken"`
		PublicKey   string `json:"PublicKey"`
	}
	parseJSON(t, paramsOut, &params)
	require.NotEmpty(t, params.ImportToken)
	require.NotEmpty(t, params.PublicKey)

	dir := t.TempDir()
	tokenRaw, err := base64.StdEncoding.DecodeString(params.ImportToken)
	require.NoError(t, err)
	tokenFile := filepath.Join(dir, "token.bin")
	require.NoError(t, os.WriteFile(tokenFile, tokenRaw, 0o600))

	pubDER, err := base64.StdEncoding.DecodeString(params.PublicKey)
	require.NoError(t, err)
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	require.NoError(t, err)
	pubRSA, ok := pubAny.(*rsa.PublicKey)
	require.True(t, ok, "PublicKey must be an RSA public key")

	material := make([]byte, 32)
	_, err = rand.Read(material)
	require.NoError(t, err)
	encryptedMaterial, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pubRSA, material, nil)
	require.NoError(t, err)

	matFile := filepath.Join(dir, "material.bin")
	require.NoError(t, os.WriteFile(matFile, encryptedMaterial, 0o600))

	runCLI(t, awsCLI("kms", "import-key-material", "--key-id", keyId,
		"--import-token", "fileb://"+tokenFile,
		"--encrypted-key-material", "fileb://"+matFile,
		"--expiration-model", "KEY_MATERIAL_DOES_NOT_EXPIRE"))

	descOut := runCLI(t, awsCLI("kms", "describe-key", "--key-id", keyId, "--output", "json"))
	var desc struct {
		KeyMetadata struct {
			KeyState string `json:"KeyState"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, descOut, &desc)
	assert.Equal(t, "Enabled", desc.KeyMetadata.KeyState)

	runCLI(t, awsCLI("kms", "delete-imported-key-material", "--key-id", keyId))
	descOut = runCLI(t, awsCLI("kms", "describe-key", "--key-id", keyId, "--output", "json"))
	parseJSON(t, descOut, &desc)
	assert.Equal(t, "PendingImport", desc.KeyMetadata.KeyState)
}
