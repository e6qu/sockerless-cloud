package aws_cli_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// b64 encodes standard base64. The aws CLI takes blob inputs (Message,
// Signature, Mac) base64-encoded, so the tests pass base64 messages and the
// base64 Signature or Mac the previous call returned, never mixing raw and
// base64 blobs on one command.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func kmsCLICreateKey(t *testing.T, extraFlags ...string) string {
	t.Helper()
	args := append([]string{"kms", "create-key", "--output", "json"}, extraFlags...)
	out := runCLI(t, awsCLI(args...))
	var created struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.KeyMetadata.KeyId)
	keyId := created.KeyMetadata.KeyId
	t.Cleanup(func() {
		_ = awsCLI("kms", "schedule-key-deletion",
			"--key-id", keyId, "--pending-window-in-days", "7").Run()
	})
	return keyId
}

// TestKMSCLI_SignVerifyAndGetPublicKey signs and verifies with RSA, and a
// tampered message fails verification.
func TestKMSCLI_SignVerifyAndGetPublicKey(t *testing.T) {
	t.Parallel()
	keyId := kmsCLICreateKey(t,
		"--key-spec", "RSA_2048",
		"--key-usage", "SIGN_VERIFY")

	pubOut := runCLI(t, awsCLI("kms", "get-public-key",
		"--key-id", keyId, "--output", "json"))
	var pub struct {
		PublicKey         string   `json:"PublicKey"`
		SigningAlgorithms []string `json:"SigningAlgorithms"`
		KeySpec           string   `json:"KeySpec"`
	}
	parseJSON(t, pubOut, &pub)
	require.NotEmpty(t, pub.PublicKey, "GetPublicKey must return a DER public key")
	assert.Contains(t, pub.SigningAlgorithms, "RSASSA_PSS_SHA_256")
	assert.Equal(t, "RSA_2048", pub.KeySpec)

	signOut := runCLI(t, awsCLI("kms", "sign",
		"--key-id", keyId,
		"--message", b64("cli-message"),
		"--message-type", "RAW",
		"--signing-algorithm", "RSASSA_PSS_SHA_256",
		"--output", "json"))
	var sign struct {
		Signature        string `json:"Signature"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}
	parseJSON(t, signOut, &sign)
	require.NotEmpty(t, sign.Signature)

	verifyOut := runCLI(t, awsCLI("kms", "verify",
		"--key-id", keyId,
		"--message", b64("cli-message"),
		"--message-type", "RAW",
		"--signature", sign.Signature,
		"--signing-algorithm", "RSASSA_PSS_SHA_256",
		"--output", "json"))
	var verify struct {
		SignatureValid bool `json:"SignatureValid"`
	}
	parseJSON(t, verifyOut, &verify)
	assert.True(t, verify.SignatureValid, "Verify of a real signature must succeed")

	err := awsCLI("kms", "verify",
		"--key-id", keyId,
		"--message", b64("cli-message-TAMPERED"),
		"--message-type", "RAW",
		"--signature", sign.Signature,
		"--signing-algorithm", "RSASSA_PSS_SHA_256").Run()
	require.Error(t, err, "Verify of a tampered message must fail")
}

func TestKMSCLI_GenerateVerifyMac(t *testing.T) {
	t.Parallel()
	keyId := kmsCLICreateKey(t,
		"--key-spec", "HMAC_256",
		"--key-usage", "GENERATE_VERIFY_MAC")

	macOut := runCLI(t, awsCLI("kms", "generate-mac",
		"--key-id", keyId,
		"--message", b64("mac-this"),
		"--mac-algorithm", "HMAC_SHA_256",
		"--output", "json"))
	var mac struct {
		Mac          string `json:"Mac"`
		MacAlgorithm string `json:"MacAlgorithm"`
	}
	parseJSON(t, macOut, &mac)
	require.NotEmpty(t, mac.Mac)

	verifyOut := runCLI(t, awsCLI("kms", "verify-mac",
		"--key-id", keyId,
		"--message", b64("mac-this"),
		"--mac", mac.Mac,
		"--mac-algorithm", "HMAC_SHA_256",
		"--output", "json"))
	var verify struct {
		MacValid bool `json:"MacValid"`
	}
	parseJSON(t, verifyOut, &verify)
	assert.True(t, verify.MacValid)

	out, err := awsCLI("kms", "verify-mac",
		"--key-id", keyId,
		"--message", b64("mac-this-TAMPERED"),
		"--mac", mac.Mac,
		"--mac-algorithm", "HMAC_SHA_256").CombinedOutput()
	require.Error(t, err, "VerifyMac of a tampered message must fail: %s", out)
	assert.Contains(t, string(out), "KMSInvalidMacException",
		"the MAC must be refused as invalid, not fail some other way: %s", out)
}

func TestKMSCLI_DataKeyPair(t *testing.T) {
	t.Parallel()
	keyId := kmsCLICreateKey(t)

	pairOut := runCLI(t, awsCLI("kms", "generate-data-key-pair",
		"--key-id", keyId,
		"--key-pair-spec", "RSA_2048",
		"--output", "json"))
	var pair struct {
		PublicKey                string `json:"PublicKey"`
		PrivateKeyPlaintext      string `json:"PrivateKeyPlaintext"`
		PrivateKeyCiphertextBlob string `json:"PrivateKeyCiphertextBlob"`
	}
	parseJSON(t, pairOut, &pair)
	require.NotEmpty(t, pair.PublicKey)
	require.NotEmpty(t, pair.PrivateKeyPlaintext)
	require.NotEmpty(t, pair.PrivateKeyCiphertextBlob)

	noPlainOut := runCLI(t, awsCLI("kms", "generate-data-key-pair-without-plaintext",
		"--key-id", keyId,
		"--key-pair-spec", "ECC_NIST_P256",
		"--output", "json"))
	var noPlain struct {
		PublicKey                string `json:"PublicKey"`
		PrivateKeyCiphertextBlob string `json:"PrivateKeyCiphertextBlob"`
	}
	parseJSON(t, noPlainOut, &noPlain)
	require.NotEmpty(t, noPlain.PublicKey)
	require.NotEmpty(t, noPlain.PrivateKeyCiphertextBlob)
}

// TestKMSCLI_DeriveSharedSecret derives a shared secret from a KEY_AGREEMENT
// EC key against its own public key, which ECDH allows.
func TestKMSCLI_DeriveSharedSecret(t *testing.T) {
	t.Parallel()
	keyId := kmsCLICreateKey(t,
		"--key-spec", "ECC_NIST_P256",
		"--key-usage", "KEY_AGREEMENT")

	pubOut := runCLI(t, awsCLI("kms", "get-public-key",
		"--key-id", keyId, "--output", "json"))
	var pub struct {
		PublicKey              string   `json:"PublicKey"`
		KeyAgreementAlgorithms []string `json:"KeyAgreementAlgorithms"`
	}
	parseJSON(t, pubOut, &pub)
	require.NotEmpty(t, pub.PublicKey)
	assert.Contains(t, pub.KeyAgreementAlgorithms, "ECDH")

	derivedOut := runCLI(t, awsCLI("kms", "derive-shared-secret",
		"--key-id", keyId,
		"--key-agreement-algorithm", "ECDH",
		"--public-key", pub.PublicKey,
		"--output", "json"))
	var derived struct {
		SharedSecret          string `json:"SharedSecret"`
		KeyAgreementAlgorithm string `json:"KeyAgreementAlgorithm"`
	}
	parseJSON(t, derivedOut, &derived)
	require.NotEmpty(t, derived.SharedSecret, "DeriveSharedSecret must return a shared secret")
	assert.Equal(t, "ECDH", derived.KeyAgreementAlgorithm)
}

// TestKMSCLI_CustomKeyStores covers the custom key store CRUD and its
// connection-state machine.
func TestKMSCLI_CustomKeyStores(t *testing.T) {
	t.Parallel()
	createOut := runCLI(t, awsCLI("kms", "create-custom-key-store",
		"--custom-key-store-name", "cli-cks",
		"--custom-key-store-type", "EXTERNAL_KEY_STORE",
		"--output", "json"))
	var created struct {
		CustomKeyStoreId string `json:"CustomKeyStoreId"`
	}
	parseJSON(t, createOut, &created)
	cksId := created.CustomKeyStoreId
	require.NotEmpty(t, cksId)
	t.Cleanup(func() {
		_ = awsCLI("kms", "disconnect-custom-key-store", "--custom-key-store-id", cksId).Run()
		_ = awsCLI("kms", "delete-custom-key-store", "--custom-key-store-id", cksId).Run()
	})

	describe := func() string {
		out := runCLI(t, awsCLI("kms", "describe-custom-key-stores",
			"--custom-key-store-id", cksId, "--output", "json"))
		var res struct {
			CustomKeyStores []struct {
				ConnectionState    string `json:"ConnectionState"`
				CustomKeyStoreName string `json:"CustomKeyStoreName"`
			} `json:"CustomKeyStores"`
		}
		parseJSON(t, out, &res)
		require.Len(t, res.CustomKeyStores, 1)
		return res.CustomKeyStores[0].ConnectionState
	}

	assert.Equal(t, "DISCONNECTED", describe())

	runCLI(t, awsCLI("kms", "connect-custom-key-store", "--custom-key-store-id", cksId))
	assert.Equal(t, "CONNECTED", describe())

	runCLI(t, awsCLI("kms", "disconnect-custom-key-store", "--custom-key-store-id", cksId))
	assert.Equal(t, "DISCONNECTED", describe())

	runCLI(t, awsCLI("kms", "update-custom-key-store",
		"--custom-key-store-id", cksId,
		"--new-custom-key-store-name", "cli-cks-renamed"))

	runCLI(t, awsCLI("kms", "delete-custom-key-store", "--custom-key-store-id", cksId))
}

func TestKMSCLI_MultiRegionAndRetireGrant(t *testing.T) {
	t.Parallel()
	keyId := kmsCLICreateKey(t, "--multi-region")

	repOut := runCLI(t, awsCLI("kms", "replicate-key",
		"--key-id", keyId,
		"--replica-region", "us-west-2",
		"--output", "json"))
	var rep struct {
		ReplicaKeyMetadata struct {
			KeyId       string `json:"KeyId"`
			MultiRegion bool   `json:"MultiRegion"`
		} `json:"ReplicaKeyMetadata"`
	}
	parseJSON(t, repOut, &rep)
	assert.Equal(t, keyId, rep.ReplicaKeyMetadata.KeyId)
	assert.True(t, rep.ReplicaKeyMetadata.MultiRegion)

	runCLI(t, awsCLI("kms", "update-primary-region",
		"--key-id", keyId,
		"--primary-region", "us-west-2"))

	grantKey := kmsCLICreateKey(t)
	retiring := "arn:aws:iam::000000000000:role/cli-retirer"
	grantOut := runCLI(t, awsCLI("kms", "create-grant",
		"--key-id", grantKey,
		"--grantee-principal", "arn:aws:iam::000000000000:role/cli-grantee",
		"--retiring-principal", retiring,
		"--operations", "Encrypt",
		"--output", "json"))
	var grant struct {
		GrantId string `json:"GrantId"`
	}
	parseJSON(t, grantOut, &grant)
	require.NotEmpty(t, grant.GrantId)

	listOut := runCLI(t, awsCLI("kms", "list-retirable-grants",
		"--retiring-principal", retiring, "--output", "json"))
	var list struct {
		Grants []struct {
			GrantId string `json:"GrantId"`
		} `json:"Grants"`
	}
	parseJSON(t, listOut, &list)
	require.GreaterOrEqual(t, len(list.Grants), 1)

	runCLI(t, awsCLI("kms", "retire-grant",
		"--key-id", grantKey,
		"--grant-id", grant.GrantId))
}
