package gcp_sdk_test

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"hash/crc32"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudkms/v1"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

var kmsCRCTable = crc32.MakeTable(crc32.Castagnoli)

func kmsService(t *testing.T) *cloudkms.Service {
	t.Helper()
	svc, err := cloudkms.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestCloudKMSShowEffectiveAutokeyConfigSDK(t *testing.T) {
	// The official client drives GET /v1/folders/{folderAction} for both
	// GetAutokeyConfig and ShowEffectiveAutokeyConfig below.
	resourceManager := crmV3Service(t)
	folderOp, err := resourceManager.Folders.Create(&crm.Folder{
		DisplayName: "Autokey parent",
		Parent:      "organizations/123456789012",
	}).Do()
	require.NoError(t, err)
	folderName := crmOpResourceName(t, folderOp)
	require.NotEmpty(t, folderName)

	const projectID = "autokey-child"
	_, err = resourceManager.Projects.Create(&crm.Project{
		ProjectId:   projectID,
		DisplayName: "Autokey child",
		Parent:      folderName,
	}).Do()
	require.NoError(t, err)

	client := kmsService(t)

	const keyProject = "projects/dedicated-key-project"
	updated, err := client.Folders.UpdateAutokeyConfig(folderName+"/autokeyConfig", &cloudkms.AutokeyConfig{
		Name:                     folderName + "/autokeyConfig",
		KeyProject:               keyProject,
		KeyProjectResolutionMode: "DEDICATED_KEY_PROJECT",
	}).UpdateMask("keyProject,keyProjectResolutionMode").Do()
	require.NoError(t, err)
	require.Equal(t, keyProject, updated.KeyProject)

	stored, err := client.Folders.GetAutokeyConfig(folderName + "/autokeyConfig").Do()
	require.NoError(t, err)
	require.Equal(t, keyProject, stored.KeyProject)

	folderEffective, err := client.Folders.ShowEffectiveAutokeyConfig(folderName).Do()
	require.NoError(t, err)
	require.Equal(t, keyProject, folderEffective.KeyProject)
	require.Equal(t, folderName, folderEffective.Source.Name)

	effective, err := client.Projects.ShowEffectiveAutokeyConfig("projects/" + projectID).Do()
	require.NoError(t, err)
	require.Equal(t, keyProject, effective.KeyProject)
	require.Equal(t, "DEDICATED_KEY_PROJECT", effective.KeyProjectResolutionMode)
	require.Equal(t, folderName, effective.Source.Name)
}

func TestCloudKMSTrustedWrappedKeyExportImportSDK(t *testing.T) {
	// The generated method below drives POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions:importTrustedKeyWrappedCryptoKeyVersion.
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-trusted-wrap")

	wrappingKeyName := ringName + "/cryptoKeys/wrapping-key"
	wrappingKey, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "AES_WRAPPING",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{
			Algorithm:       "AES_256_KWP",
			ProtectionLevel: "HSM_SINGLE_TENANT",
		},
	}).CryptoKeyId("wrapping-key").Do()
	require.NoError(t, err)
	require.Equal(t, wrappingKeyName, wrappingKey.Name)
	wrappingVersion := wrappingKeyName + "/cryptoKeyVersions/1"
	wrappingVersionState, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Get(wrappingVersion).Do()
	require.NoError(t, err)
	require.True(t, wrappingVersionState.HsmTrusted)

	sourceKeyName := ringName + "/cryptoKeys/source-key"
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "RAW_ENCRYPT_DECRYPT",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{
			Algorithm:       "AES_256_GCM",
			ProtectionLevel: "HSM_SINGLE_TENANT",
		},
	}).CryptoKeyId("source-key").TrustedWrappingEnabled(true).Do()
	require.NoError(t, err)
	sourceVersion := sourceKeyName + "/cryptoKeyVersions/1"

	exported, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.
		ExportTrustedKeyWrappedCryptoKeyVersion(sourceVersion).
		WrappingKey(wrappingVersion).
		Do()
	require.NoError(t, err)
	wrappedBytes, err := base64.StdEncoding.DecodeString(exported.WrappedKey)
	require.NoError(t, err)
	require.NotEmpty(t, wrappedBytes)
	require.Equal(t, int64(crc32.Checksum(wrappedBytes, kmsCRCTable)), exported.WrappedKeyCrc32c)

	destinationKeyName := ringName + "/cryptoKeys/destination-key"
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "RAW_ENCRYPT_DECRYPT",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{
			Algorithm:       "AES_256_GCM",
			ProtectionLevel: "HSM_SINGLE_TENANT",
		},
	}).CryptoKeyId("destination-key").SkipInitialVersionCreation(true).Do()
	require.NoError(t, err)

	imported, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.
		ImportTrustedKeyWrappedCryptoKeyVersion(destinationKeyName, &cloudkms.ImportTrustedKeyWrappedCryptoKeyVersionRequest{
			Algorithm:    "AES_256_GCM",
			ImportingKey: wrappingVersion,
			WrappedKey:   exported.WrappedKey,
		}).Do()
	require.NoError(t, err)
	require.True(t, imported.TrustedWrappingEnabled)
	require.True(t, imported.ReimportEligible)

	plaintext := []byte("trusted wrapping preserves this exact key material")
	encrypted, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.RawEncrypt(sourceVersion, &cloudkms.RawEncryptRequest{
		Plaintext: base64.StdEncoding.EncodeToString(plaintext),
	}).Do()
	require.NoError(t, err)
	decrypted, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.RawDecrypt(imported.Name, &cloudkms.RawDecryptRequest{
		Ciphertext:           encrypted.Ciphertext,
		InitializationVector: encrypted.InitializationVector,
		TagLength:            encrypted.TagLength,
	}).Do()
	require.NoError(t, err)
	decryptedBytes, err := base64.StdEncoding.DecodeString(decrypted.Plaintext)
	require.NoError(t, err)
	require.Equal(t, plaintext, decryptedBytes)
}

func requireKMSErrorCode(t *testing.T, err error, code int) {
	t.Helper()
	require.Error(t, err)
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	require.Equal(t, code, gerr.Code)
}

func TestCloudKMSKeyRingLifecycleSDK(t *testing.T) {
	svc := kmsService(t)
	parent := "projects/sdk-kms-project/locations/global"
	ringID := "sdk-ring-lifecycle"
	ringName := parent + "/keyRings/" + ringID

	created, err := svc.Projects.Locations.KeyRings.Create(parent, &cloudkms.KeyRing{}).KeyRingId(ringID).Do()
	require.NoError(t, err)
	require.Equal(t, ringName, created.Name)
	require.NotEmpty(t, created.CreateTime)

	// Re-create the same ring → ALREADY_EXISTS.
	_, err = svc.Projects.Locations.KeyRings.Create(parent, &cloudkms.KeyRing{}).KeyRingId(ringID).Do()
	requireKMSErrorCode(t, err, 409)

	got, err := svc.Projects.Locations.KeyRings.Get(ringName).Do()
	require.NoError(t, err)
	require.Equal(t, ringName, got.Name)

	// GET on an uncreated ring must 404, not 200.
	_, err = svc.Projects.Locations.KeyRings.Get(parent + "/keyRings/never-created").Do()
	requireKMSErrorCode(t, err, 404)

	list, err := svc.Projects.Locations.KeyRings.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, kr := range list.KeyRings {
		if kr.Name == ringName {
			found = true
		}
	}
	require.True(t, found)
}

func TestCloudKMSEncryptDecryptSDK(t *testing.T) {
	svc := kmsService(t)
	parent := "projects/sdk-kms-project/locations/global"
	ringID := "sdk-ring-crypto"
	ringName := parent + "/keyRings/" + ringID
	_, err := svc.Projects.Locations.KeyRings.Create(parent, &cloudkms.KeyRing{}).KeyRingId(ringID).Do()
	require.NoError(t, err)

	keyID := "sdk-key"
	keyName := ringName + "/cryptoKeys/" + keyID
	key, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "ENCRYPT_DECRYPT",
	}).CryptoKeyId(keyID).Do()
	require.NoError(t, err)
	require.Equal(t, keyName, key.Name)
	require.NotNil(t, key.Primary, "ENCRYPT_DECRYPT key must auto-create a primary version")
	require.Equal(t, "ENABLED", key.Primary.State)
	require.Equal(t, "GOOGLE_SYMMETRIC_ENCRYPTION", key.Primary.Algorithm)

	// Creating a key in a non-existent ring must 404.
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Create(parent+"/keyRings/missing", &cloudkms.CryptoKey{
		Purpose: "ENCRYPT_DECRYPT",
	}).CryptoKeyId("x").Do()
	requireKMSErrorCode(t, err, 404)

	plaintext := []byte("super-secret-payload-\x00\x01\x02")
	ptB64 := base64.StdEncoding.EncodeToString(plaintext)
	encResp, err := svc.Projects.Locations.KeyRings.CryptoKeys.Encrypt(keyName, &cloudkms.EncryptRequest{
		Plaintext:       ptB64,
		PlaintextCrc32c: int64(crc32.Checksum(plaintext, kmsCRCTable)),
	}).Do()
	require.NoError(t, err)
	require.True(t, encResp.VerifiedPlaintextCrc32c, "server must verify the supplied plaintext CRC32C")
	require.NotEmpty(t, encResp.Ciphertext)
	require.NotZero(t, encResp.CiphertextCrc32c, "encrypt response must carry ciphertextCrc32c")
	require.Equal(t, keyName+"/cryptoKeyVersions/1", encResp.Name)

	// The ciphertext must be opaque (not contain the plaintext).
	require.NotContains(t, encResp.Ciphertext, ptB64)

	decResp, err := svc.Projects.Locations.KeyRings.CryptoKeys.Decrypt(keyName, &cloudkms.DecryptRequest{
		Ciphertext: encResp.Ciphertext,
	}).Do()
	require.NoError(t, err)
	require.NotZero(t, decResp.PlaintextCrc32c, "decrypt response must carry plaintextCrc32c")
	gotPlain, err := base64.StdEncoding.DecodeString(decResp.Plaintext)
	require.NoError(t, err)
	require.Equal(t, plaintext, gotPlain)

	// Additional authenticated data must round-trip and be enforced.
	aad := []byte("context-binding")
	aadB64 := base64.StdEncoding.EncodeToString(aad)
	encAAD, err := svc.Projects.Locations.KeyRings.CryptoKeys.Encrypt(keyName, &cloudkms.EncryptRequest{
		Plaintext:                   ptB64,
		AdditionalAuthenticatedData: aadB64,
	}).Do()
	require.NoError(t, err)
	// Decrypt with matching AAD succeeds.
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Decrypt(keyName, &cloudkms.DecryptRequest{
		Ciphertext:                  encAAD.Ciphertext,
		AdditionalAuthenticatedData: aadB64,
	}).Do()
	require.NoError(t, err)
	// Decrypt with the wrong AAD fails the integrity check.
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Decrypt(keyName, &cloudkms.DecryptRequest{
		Ciphertext:                  encAAD.Ciphertext,
		AdditionalAuthenticatedData: base64.StdEncoding.EncodeToString([]byte("wrong-context")),
	}).Do()
	require.Error(t, err)

	// A malformed ciphertext is rejected.
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Decrypt(keyName, &cloudkms.DecryptRequest{
		Ciphertext: base64.StdEncoding.EncodeToString([]byte("garbage")),
	}).Do()
	require.Error(t, err)
}

func TestCloudKMSCryptoKeyManagementSDK(t *testing.T) {
	svc := kmsService(t)
	parent := "projects/sdk-kms-project/locations/global"
	ringID := "sdk-ring-mgmt"
	ringName := parent + "/keyRings/" + ringID
	_, err := svc.Projects.Locations.KeyRings.Create(parent, &cloudkms.KeyRing{}).KeyRingId(ringID).Do()
	require.NoError(t, err)

	keyName := ringName + "/cryptoKeys/mgmt-key"
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "ENCRYPT_DECRYPT",
	}).CryptoKeyId("mgmt-key").Do()
	require.NoError(t, err)

	keys, err := svc.Projects.Locations.KeyRings.CryptoKeys.List(ringName).Do()
	require.NoError(t, err)
	require.Len(t, keys.CryptoKeys, 1)
	require.Equal(t, keyName, keys.CryptoKeys[0].Name)

	versions, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.List(keyName).Do()
	require.NoError(t, err)
	require.Len(t, versions.CryptoKeyVersions, 1)
	v1Name := keyName + "/cryptoKeyVersions/1"
	require.Equal(t, v1Name, versions.CryptoKeyVersions[0].Name)

	gotVer, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Get(v1Name).Do()
	require.NoError(t, err)
	require.Equal(t, "ENABLED", gotVer.State)

	// Patch rotation period via update mask.
	patched, err := svc.Projects.Locations.KeyRings.CryptoKeys.Patch(keyName, &cloudkms.CryptoKey{
		RotationPeriod: "604800s",
	}).UpdateMask("rotationPeriod").Do()
	require.NoError(t, err)
	require.Equal(t, "604800s", patched.RotationPeriod)

	// Destroy the version → DESTROY_SCHEDULED with a real destroyTime.
	destroyed, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Destroy(v1Name, &cloudkms.DestroyCryptoKeyVersionRequest{}).Do()
	require.NoError(t, err)
	require.Equal(t, "DESTROY_SCHEDULED", destroyed.State)
	require.NotEmpty(t, destroyed.DestroyTime)

	// Encrypt now fails because the primary version is no longer enabled.
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Encrypt(keyName, &cloudkms.EncryptRequest{
		Plaintext: base64.StdEncoding.EncodeToString([]byte("x")),
	}).Do()
	require.Error(t, err)
}

// kmsNewRing creates a fresh keyRing and returns its full name.
func kmsNewRing(t *testing.T, svc *cloudkms.Service, ringID string) (string, string) {
	t.Helper()
	parent := "projects/sdk-kms-project/locations/global"
	ringName := parent + "/keyRings/" + ringID
	_, err := svc.Projects.Locations.KeyRings.Create(parent, &cloudkms.KeyRing{}).KeyRingId(ringID).Do()
	require.NoError(t, err)
	return parent, ringName
}

func TestCloudKMSIamPolicySDK(t *testing.T) {
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-iam")

	// An unset policy reads back as an empty (etag-bearing) policy, not 404.
	pol, err := svc.Projects.Locations.KeyRings.GetIamPolicy(ringName).Do()
	require.NoError(t, err)
	require.Empty(t, pol.Bindings)

	// Set a binding, then read it back.
	want := &cloudkms.Policy{
		Bindings: []*cloudkms.Binding{{
			Role:    "roles/cloudkms.cryptoKeyEncrypterDecrypter",
			Members: []string{"user:alice@example.com"},
		}},
	}
	set, err := svc.Projects.Locations.KeyRings.SetIamPolicy(ringName, &cloudkms.SetIamPolicyRequest{Policy: want}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)
	require.Equal(t, "roles/cloudkms.cryptoKeyEncrypterDecrypter", set.Bindings[0].Role)

	got, err := svc.Projects.Locations.KeyRings.GetIamPolicy(ringName).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	require.Equal(t, []string{"user:alice@example.com"}, got.Bindings[0].Members)

	tip, err := svc.Projects.Locations.KeyRings.TestIamPermissions(ringName, &cloudkms.TestIamPermissionsRequest{
		Permissions: []string{"cloudkms.cryptoKeys.get"},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, []string{"cloudkms.cryptoKeys.get"}, tip.Permissions)

	// CryptoKey-scoped IAM round-trips too.
	keyName := ringName + "/cryptoKeys/iam-key"
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{Purpose: "ENCRYPT_DECRYPT"}).CryptoKeyId("iam-key").Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.SetIamPolicy(keyName, &cloudkms.SetIamPolicyRequest{Policy: want}).Do()
	require.NoError(t, err)
	kpol, err := svc.Projects.Locations.KeyRings.CryptoKeys.GetIamPolicy(keyName).Do()
	require.NoError(t, err)
	require.Len(t, kpol.Bindings, 1)
}

func TestCloudKMSGenerateRandomBytesSDK(t *testing.T) {
	svc := kmsService(t)
	location := "projects/sdk-kms-project/locations/global"

	resp, err := svc.Projects.Locations.GenerateRandomBytes(location, &cloudkms.GenerateRandomBytesRequest{
		LengthBytes:     32,
		ProtectionLevel: "HSM",
	}).Do()
	require.NoError(t, err)
	data, err := base64.StdEncoding.DecodeString(resp.Data)
	require.NoError(t, err)
	require.Len(t, data, 32)
	// The returned CRC32C must match the data — real randomness, real checksum.
	require.Equal(t, int64(crc32.Checksum(data, kmsCRCTable)), resp.DataCrc32c)

	// Two calls must not return identical bytes (genuine randomness).
	resp2, err := svc.Projects.Locations.GenerateRandomBytes(location, &cloudkms.GenerateRandomBytesRequest{LengthBytes: 32}).Do()
	require.NoError(t, err)
	require.NotEqual(t, resp.Data, resp2.Data)

	// Out-of-range length is rejected.
	_, err = svc.Projects.Locations.GenerateRandomBytes(location, &cloudkms.GenerateRandomBytesRequest{LengthBytes: 4}).Do()
	requireKMSErrorCode(t, err, 400)
}

func TestCloudKMSMacSignVerifySDK(t *testing.T) {
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-mac")

	keyName := ringName + "/cryptoKeys/mac-key"
	_, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "MAC",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{
			Algorithm: "HMAC_SHA256",
		},
	}).CryptoKeyId("mac-key").Do()
	require.NoError(t, err)
	versionName := keyName + "/cryptoKeyVersions/1"

	data := []byte("authenticate-me")
	signResp, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.MacSign(versionName, &cloudkms.MacSignRequest{
		Data:       base64.StdEncoding.EncodeToString(data),
		DataCrc32c: int64(crc32.Checksum(data, kmsCRCTable)),
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, signResp.Mac)
	require.True(t, signResp.VerifiedDataCrc32c)

	// Verify the genuine HMAC round-trips.
	verifyResp, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.MacVerify(versionName, &cloudkms.MacVerifyRequest{
		Data: base64.StdEncoding.EncodeToString(data),
		Mac:  signResp.Mac,
	}).Do()
	require.NoError(t, err)
	require.True(t, verifyResp.Success, "a MAC produced by macSign must verify")

	// A tampered MAC must not verify.
	badMac := base64.StdEncoding.EncodeToString([]byte("not-the-real-mac-bytes-0000000000"))
	verifyBad, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.MacVerify(versionName, &cloudkms.MacVerifyRequest{
		Data: base64.StdEncoding.EncodeToString(data),
		Mac:  badMac,
	}).Do()
	require.NoError(t, err)
	require.False(t, verifyBad.Success)
}

func TestCloudKMSRawEncryptDecryptSDK(t *testing.T) {
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-raw")

	keyName := ringName + "/cryptoKeys/raw-key"
	_, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "RAW_ENCRYPT_DECRYPT",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{
			Algorithm: "AES_256_GCM",
		},
	}).CryptoKeyId("raw-key").Do()
	require.NoError(t, err)
	versionName := keyName + "/cryptoKeyVersions/1"

	plaintext := []byte("raw-mode-secret")
	encResp, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.RawEncrypt(versionName, &cloudkms.RawEncryptRequest{
		Plaintext:       base64.StdEncoding.EncodeToString(plaintext),
		PlaintextCrc32c: int64(crc32.Checksum(plaintext, kmsCRCTable)),
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, encResp.Ciphertext)
	require.NotEmpty(t, encResp.InitializationVector, "rawEncrypt must return the IV it used")
	require.True(t, encResp.VerifiedPlaintextCrc32c)

	decResp, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.RawDecrypt(versionName, &cloudkms.RawDecryptRequest{
		Ciphertext:           encResp.Ciphertext,
		InitializationVector: encResp.InitializationVector,
	}).Do()
	require.NoError(t, err)
	gotPlain, err := base64.StdEncoding.DecodeString(decResp.Plaintext)
	require.NoError(t, err)
	require.Equal(t, plaintext, gotPlain, "AES-GCM raw round-trip must recover the plaintext")
}

func TestCloudKMSAsymmetricSignSDK(t *testing.T) {
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-asym")

	keyName := ringName + "/cryptoKeys/asym-key"
	_, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "ASYMMETRIC_SIGN",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{
			Algorithm: "RSA_SIGN_PKCS1_2048_SHA256",
		},
	}).CryptoKeyId("asym-key").Do()
	require.NoError(t, err)
	versionName := keyName + "/cryptoKeyVersions/1"

	// Fetch the genuine public key PEM.
	pub, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.GetPublicKey(versionName).Do()
	require.NoError(t, err)
	require.Contains(t, pub.Pem, "BEGIN PUBLIC KEY")

	// Sign a message; the signature must verify against the public key with
	// Go's crypto/rsa — proving the sim signed with the matching private key.
	data := []byte("sign-this-payload")
	signResp, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.AsymmetricSign(versionName, &cloudkms.AsymmetricSignRequest{
		Data:       base64.StdEncoding.EncodeToString(data),
		DataCrc32c: int64(crc32.Checksum(data, kmsCRCTable)),
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, signResp.Signature)

	block, _ := pem.Decode([]byte(pub.Pem))
	require.NotNil(t, block)
	pubKeyAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	rsaPub, ok := pubKeyAny.(*rsa.PublicKey)
	require.True(t, ok)
	sig, err := base64.StdEncoding.DecodeString(signResp.Signature)
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	require.NoError(t, rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, digest[:], sig),
		"a signature from asymmetricSign must verify against the published public key")
}

func TestCloudKMSAsymmetricDecryptSDK(t *testing.T) {
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-asymdec")

	keyName := ringName + "/cryptoKeys/asymdec-key"
	_, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "ASYMMETRIC_DECRYPT",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{
			Algorithm: "RSA_DECRYPT_OAEP_2048_SHA256",
		},
	}).CryptoKeyId("asymdec-key").Do()
	require.NoError(t, err)
	versionName := keyName + "/cryptoKeyVersions/1"

	pub, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.GetPublicKey(versionName).Do()
	require.NoError(t, err)
	block, _ := pem.Decode([]byte(pub.Pem))
	require.NotNil(t, block)
	pubKeyAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	rsaPub := pubKeyAny.(*rsa.PublicKey)

	// Encrypt locally with the published public key, then the sim decrypts with
	// its private half — a genuine RSA-OAEP round-trip.
	plaintext := []byte("oaep-secret")
	ct, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPub, plaintext, nil)
	require.NoError(t, err)
	decResp, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.AsymmetricDecrypt(versionName, &cloudkms.AsymmetricDecryptRequest{
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
	}).Do()
	require.NoError(t, err)
	gotPlain, err := base64.StdEncoding.DecodeString(decResp.Plaintext)
	require.NoError(t, err)
	require.Equal(t, plaintext, gotPlain)
}

func TestCloudKMSImportJobSDK(t *testing.T) {
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-import")

	job, err := svc.Projects.Locations.KeyRings.ImportJobs.Create(ringName, &cloudkms.ImportJob{
		ImportMethod:    "RSA_OAEP_3072_SHA256",
		ProtectionLevel: "SOFTWARE",
	}).ImportJobId("imp-1").Do()
	require.NoError(t, err)
	require.Equal(t, ringName+"/importJobs/imp-1", job.Name)
	require.Equal(t, "ACTIVE", job.State)
	require.NotNil(t, job.PublicKey)
	require.Contains(t, job.PublicKey.Pem, "BEGIN PUBLIC KEY", "import job must expose a real wrapping public key")

	got, err := svc.Projects.Locations.KeyRings.ImportJobs.Get(job.Name).Do()
	require.NoError(t, err)
	require.Equal(t, job.Name, got.Name)

	list, err := svc.Projects.Locations.KeyRings.ImportJobs.List(ringName).Do()
	require.NoError(t, err)
	require.Len(t, list.ImportJobs, 1)

	// ImportJob-scoped IAM round-trips.
	_, err = svc.Projects.Locations.KeyRings.ImportJobs.SetIamPolicy(job.Name, &cloudkms.SetIamPolicyRequest{
		Policy: &cloudkms.Policy{Bindings: []*cloudkms.Binding{{Role: "roles/viewer", Members: []string{"user:bob@example.com"}}}},
	}).Do()
	require.NoError(t, err)
	ipol, err := svc.Projects.Locations.KeyRings.ImportJobs.GetIamPolicy(job.Name).Do()
	require.NoError(t, err)
	require.Len(t, ipol.Bindings, 1)

	keyName := ringName + "/cryptoKeys/imported-raw-key"
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{
		Purpose: "RAW_ENCRYPT_DECRYPT",
		VersionTemplate: &cloudkms.CryptoKeyVersionTemplate{
			Algorithm: "AES_256_GCM",
		},
	}).CryptoKeyId("imported-raw-key").SkipInitialVersionCreation(true).Do()
	require.NoError(t, err)

	block, _ := pem.Decode([]byte(job.PublicKey.Pem))
	require.NotNil(t, block)
	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	rsaPublicKey, ok := publicKey.(*rsa.PublicKey)
	require.True(t, ok)
	importedMaterial := make([]byte, 32)
	_, err = rand.Read(importedMaterial)
	require.NoError(t, err)
	wrappedMaterial, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPublicKey, importedMaterial, nil)
	require.NoError(t, err)

	imported, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Import(keyName, &cloudkms.ImportCryptoKeyVersionRequest{
		Algorithm:  "AES_256_GCM",
		ImportJob:  job.Name,
		WrappedKey: base64.StdEncoding.EncodeToString(wrappedMaterial),
	}).Do()
	require.NoError(t, err)
	require.True(t, imported.ReimportEligible)

	plaintext := []byte("the imported material must be the caller's exact key")
	encrypted, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.RawEncrypt(imported.Name, &cloudkms.RawEncryptRequest{
		Plaintext: base64.StdEncoding.EncodeToString(plaintext),
	}).Do()
	require.NoError(t, err)
	iv, err := base64.StdEncoding.DecodeString(encrypted.InitializationVector)
	require.NoError(t, err)
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted.Ciphertext)
	require.NoError(t, err)
	aesBlock, err := aes.NewCipher(importedMaterial)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(aesBlock)
	require.NoError(t, err)
	decrypted, err := gcm.Open(nil, iv, ciphertext, nil)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted)
}

func TestCloudKMSEkmConnectionSDK(t *testing.T) {
	svc := kmsService(t)
	location := "projects/sdk-kms-project/locations/us-central1"

	conn, err := svc.Projects.Locations.EkmConnections.Create(location, &cloudkms.EkmConnection{
		KeyManagementMode: "MANUAL",
	}).EkmConnectionId("conn-1").Do()
	require.NoError(t, err)
	require.Equal(t, location+"/ekmConnections/conn-1", conn.Name)
	require.NotEmpty(t, conn.Etag)

	got, err := svc.Projects.Locations.EkmConnections.Get(conn.Name).Do()
	require.NoError(t, err)
	require.Equal(t, conn.Name, got.Name)

	list, err := svc.Projects.Locations.EkmConnections.List(location).Do()
	require.NoError(t, err)
	require.Len(t, list.EkmConnections, 1)

	// EkmConfig get + patch.
	cfg, err := svc.Projects.Locations.GetEkmConfig(location + "/ekmConfig").Do()
	require.NoError(t, err)
	require.Equal(t, location+"/ekmConfig", cfg.Name)

	updated, err := svc.Projects.Locations.UpdateEkmConfig(location+"/ekmConfig", &cloudkms.EkmConfig{
		DefaultEkmConnection: conn.Name,
	}).UpdateMask("defaultEkmConnection").Do()
	require.NoError(t, err)
	require.Equal(t, conn.Name, updated.DefaultEkmConnection)
}

func TestCloudKMSKeyHandleSDK(t *testing.T) {
	svc := kmsService(t)
	location := "projects/sdk-kms-project/locations/us-east1"

	op, err := svc.Projects.Locations.KeyHandles.Create(location, &cloudkms.KeyHandle{
		ResourceTypeSelector: "storage.googleapis.com/Bucket",
	}).KeyHandleId("kh-1").Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	got, err := svc.Projects.Locations.KeyHandles.Get(location + "/keyHandles/kh-1").Do()
	require.NoError(t, err)
	require.Equal(t, location+"/keyHandles/kh-1", got.Name)
	require.NotEmpty(t, got.KmsKey, "Autokey must provision a CMEK path for the handle")

	list, err := svc.Projects.Locations.KeyHandles.List(location).Do()
	require.NoError(t, err)
	require.Len(t, list.KeyHandles, 1)
}

func TestCloudKMSUpdatePrimaryVersionSDK(t *testing.T) {
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-primary")

	keyName := ringName + "/cryptoKeys/primary-key"
	_, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{Purpose: "ENCRYPT_DECRYPT"}).CryptoKeyId("primary-key").Do()
	require.NoError(t, err)

	// Add a second version and promote it to primary.
	v2, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Create(keyName, &cloudkms.CryptoKeyVersion{}).Do()
	require.NoError(t, err)
	require.Equal(t, keyName+"/cryptoKeyVersions/2", v2.Name)

	updated, err := svc.Projects.Locations.KeyRings.CryptoKeys.UpdatePrimaryVersion(keyName, &cloudkms.UpdateCryptoKeyPrimaryVersionRequest{
		CryptoKeyVersionId: "2",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, updated.Primary)
	require.Equal(t, keyName+"/cryptoKeyVersions/2", updated.Primary.Name)
}

func TestCloudKMSDeleteCryptoKeyAndVersionSDK(t *testing.T) {
	svc := kmsService(t)
	_, ringName := kmsNewRing(t, svc, "sdk-ring-delete")

	keyName := ringName + "/cryptoKeys/del-key"
	_, err := svc.Projects.Locations.KeyRings.CryptoKeys.Create(ringName, &cloudkms.CryptoKey{Purpose: "ENCRYPT_DECRYPT"}).CryptoKeyId("del-key").Do()
	require.NoError(t, err)
	v2, err := svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Create(keyName, &cloudkms.CryptoKeyVersion{}).Do()
	require.NoError(t, err)

	// Delete the second version.
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Delete(v2.Name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.CryptoKeyVersions.Get(v2.Name).Do()
	requireKMSErrorCode(t, err, 404)

	// Delete the whole key.
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Delete(keyName).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.KeyRings.CryptoKeys.Get(keyName).Do()
	requireKMSErrorCode(t, err, 404)
}
