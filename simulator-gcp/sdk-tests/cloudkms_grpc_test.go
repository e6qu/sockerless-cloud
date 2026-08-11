package gcp_sdk_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"hash/crc32"
	"testing"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// grpcKmsCRC32CTable is the CRC32C table used to compute integrity checksums
// the KMS gRPC API echoes back (verifiedPlaintextCrc32C, signatureCrc32C, ...).
var grpcKmsCRC32CTable = crc32.MakeTable(crc32.Castagnoli)

// kmsCRC32C computes the CRC32C checksum the KMS API uses for its integrity
// fields.
func kmsCRC32C(b []byte) int64 { return int64(crc32.Checksum(b, grpcKmsCRC32CTable)) }

// newKMSGRPCClient builds the high-level cloud.google.com/go/kms.KeyManagementClient
// over gRPC (its only transport), pointed at the simulator's gRPC port. The KMS
// SDK has no *_EMULATOR_HOST coordinate, so the connection is supplied directly
// via option.WithGRPCConn — the same pattern the logging gRPC test uses.
func newKMSGRPCClient(t *testing.T) *kms.KeyManagementClient {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	cli, err := kms.NewKeyManagementClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { cli.Close() })
	return cli
}

// kmsKeyRingForTest provisions a unique KeyRing under a project/location and
// returns its full name.
func kmsKeyRingForTest(t *testing.T, c *kms.KeyManagementClient, ringID string) string {
	t.Helper()
	parent := "projects/kms-grpc-project/locations/global"
	kr, err := c.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
		Parent:    parent,
		KeyRingId: ringID,
		KeyRing:   &kmspb.KeyRing{},
	})
	require.NoError(t, err)
	require.Equal(t, parent+"/keyRings/"+ringID, kr.GetName())
	return kr.GetName()
}

// kmsListAllCryptoKeyVersions exhausts the iterator the high-level client
// exposes for ListCryptoKeyVersions, so the assertion does not depend on a
// single-page response.
func kmsListAllCryptoKeyVersions(t *testing.T, c *kms.KeyManagementClient, parent string) []*kmspb.CryptoKeyVersion {
	t.Helper()
	it := c.ListCryptoKeyVersions(ctx, &kmspb.ListCryptoKeyVersionsRequest{Parent: parent})
	var out []*kmspb.CryptoKeyVersion
	for {
		v, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		out = append(out, v)
	}
	return out
}

// kmsFirstVersionName returns the full name of the first (lowest-numbered)
// CryptoKeyVersion under a CryptoKey. Asymmetric-sign / asymmetric-decrypt /
// MAC keys have no primary version (only ENCRYPT_DECRYPT keys do), so the test
// must address a specific version by name — CreateCryptoKey auto-provisions
// version 1, which is what this resolves.
func kmsFirstVersionName(t *testing.T, c *kms.KeyManagementClient, keyName string) string {
	t.Helper()
	versions := kmsListAllCryptoKeyVersions(t, c, keyName)
	require.NotEmpty(t, versions, "CryptoKey %s must have at least one version", keyName)
	return versions[0].GetName()
}

func TestCloudKMS_GRPC_KeyRingCryptoKeyCRUD(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-ring-crud")

	// GetKeyRing returns what CreateKeyRing stored.
	kr, err := c.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: ringName})
	require.NoError(t, err)
	require.Equal(t, ringName, kr.GetName())
	require.NotNil(t, kr.GetCreateTime())

	// Re-create the same ring → AlreadyExists.
	_, err = c.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
		Parent:    "projects/kms-grpc-project/locations/global",
		KeyRingId: "grpc-ring-crud",
		KeyRing:   &kmspb.KeyRing{},
	})
	requireGRPCCode(t, err, codes.AlreadyExists)

	// CreateCryptoKey (symmetric, auto primary version).
	keyName := ringName + "/cryptoKeys/sym"
	ck, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "sym",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION,
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, keyName, ck.GetName())
	require.NotNil(t, ck.GetPrimary(), "ENCRYPT_DECRYPT key must get an initial primary version")
	require.Equal(t, kmspb.CryptoKeyVersion_ENABLED, ck.GetPrimary().GetState())

	// GetCryptoKey returns the same key, with the primary.
	got, err := c.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: keyName})
	require.NoError(t, err)
	require.Equal(t, keyName, got.GetName())
	require.NotNil(t, got.GetPrimary())

	// ListCryptoKeys (via the high-level iterator) includes the key.
	keyIt := c.ListCryptoKeys(ctx, &kmspb.ListCryptoKeysRequest{Parent: ringName})
	found := false
	for {
		k, err := keyIt.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if k.GetName() == keyName {
			found = true
		}
	}
	require.True(t, found, "ListCryptoKeys must include the created key")

	// ListKeyRings includes the ring.
	ringIt := c.ListKeyRings(ctx, &kmspb.ListKeyRingsRequest{Parent: "projects/kms-grpc-project/locations/global"})
	ringFound := false
	for {
		r, err := ringIt.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		if r.GetName() == ringName {
			ringFound = true
		}
	}
	require.True(t, ringFound, "ListKeyRings must include the created ring")

	// GetKeyRing on a missing ring → NotFound.
	_, err = c.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: "projects/kms-grpc-project/locations/global/keyRings/never"})
	requireGRPCCode(t, err, codes.NotFound)
}

func TestCloudKMS_GRPC_SymmetricEncryptDecrypt(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-sym-ed")
	keyName := ringName + "/cryptoKeys/sym"

	ck, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "sym",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION,
			},
		},
	})
	require.NoError(t, err)

	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	aad := []byte("associated-data")
	enc, err := c.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:                        keyName,
		Plaintext:                   plaintext,
		AdditionalAuthenticatedData: aad,
		PlaintextCrc32C:             wrapperspb.Int64(kmsCRC32C(plaintext)),
	})
	require.NoError(t, err)
	require.NotEmpty(t, enc.GetCiphertext())
	require.Equal(t, kmsCRC32C(enc.GetCiphertext()), enc.GetCiphertextCrc32C().GetValue())
	require.Equal(t, ck.GetPrimary().GetName(), enc.GetName())

	// Decrypt recovers the original plaintext.
	dec, err := c.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:                        keyName,
		Ciphertext:                  enc.GetCiphertext(),
		AdditionalAuthenticatedData: aad,
	})
	require.NoError(t, err)
	require.Equal(t, plaintext, dec.GetPlaintext())
	require.Equal(t, kmsCRC32C(dec.GetPlaintext()), dec.GetPlaintextCrc32C().GetValue())

	// Decrypt with the wrong AAD must fail (real GCM authentication).
	_, err = c.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:                        keyName,
		Ciphertext:                  enc.GetCiphertext(),
		AdditionalAuthenticatedData: []byte("wrong-aad"),
	})
	require.Error(t, err, "GCM must reject ciphertext whose AAD does not match")
}

func TestCloudKMS_GRPC_AsymmetricSignAndVerify(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-asym-sign")
	keyName := ringName + "/cryptoKeys/signer"

	ck, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "signer",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ASYMMETRIC_SIGN,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm:       kmspb.CryptoKeyVersion_RSA_SIGN_PKCS1_2048_SHA256,
				ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
			},
		},
	})
	require.NoError(t, err)
	versionName := kmsFirstVersionName(t, c, keyName)
	require.Equal(t, kmspb.CryptoKey_ASYMMETRIC_SIGN, ck.GetPurpose())

	// Sign a digest.
	msg := []byte("message to sign")
	digest := sha256.Sum256(msg)
	signResp, err := c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name: versionName,
		Digest: &kmspb.Digest{
			Digest: &kmspb.Digest_Sha256{Sha256: digest[:]},
		},
		DigestCrc32C: wrapperspb.Int64(kmsCRC32C(digest[:])),
	})
	require.NoError(t, err)
	require.NotEmpty(t, signResp.GetSignature())
	require.Equal(t, kmsCRC32C(signResp.GetSignature()), signResp.GetSignatureCrc32C().GetValue())

	// Fetch the real public key and verify the signature with crypto/rsa.
	pkResp, err := c.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: versionName})
	require.NoError(t, err)
	require.NotEmpty(t, pkResp.GetPem())
	rsaPub := parseKMSPEMPublicKey(t, pkResp.GetPem()).(*rsa.PublicKey)

	err = rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, digest[:], signResp.GetSignature())
	require.NoError(t, err, "the sim's signature must verify against the real public key")

	// Tampering with the signature must fail verification.
	tampered := append([]byte{}, signResp.GetSignature()...)
	tampered[0] ^= 0xff
	err = rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, digest[:], tampered)
	require.Error(t, err, "a tampered signature must NOT verify")
}

func TestCloudKMS_GRPC_AsymmetricDecrypt(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-asym-decrypt")
	keyName := ringName + "/cryptoKeys/decryptor"

	ck, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "decryptor",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ASYMMETRIC_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm:       kmspb.CryptoKeyVersion_RSA_DECRYPT_OAEP_2048_SHA256,
				ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
			},
		},
	})
	require.NoError(t, err)
	versionName := kmsFirstVersionName(t, c, keyName)
	require.Equal(t, kmspb.CryptoKey_ASYMMETRIC_DECRYPT, ck.GetPurpose())

	// Fetch the public key and encrypt a message with it (OAEP/SHA-256).
	pkResp, err := c.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: versionName})
	require.NoError(t, err)
	rsaPub := parseKMSPEMPublicKey(t, pkResp.GetPem()).(*rsa.PublicKey)

	plaintext := []byte("secret payload for asymmetric decrypt")
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaPub, plaintext, nil)
	require.NoError(t, err)

	// The sim decrypts with the real private key.
	dec, err := c.AsymmetricDecrypt(ctx, &kmspb.AsymmetricDecryptRequest{
		Name:             versionName,
		Ciphertext:       ciphertext,
		CiphertextCrc32C: wrapperspb.Int64(kmsCRC32C(ciphertext)),
	})
	require.NoError(t, err)
	require.Equal(t, plaintext, dec.GetPlaintext())
}

func TestCloudKMS_GRPC_MacSignVerify(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-mac")
	keyName := ringName + "/cryptoKeys/mac"

	ck, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "mac",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_MAC,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_HMAC_SHA256,
			},
		},
	})
	require.NoError(t, err)
	versionName := kmsFirstVersionName(t, c, keyName)
	require.Equal(t, kmspb.CryptoKey_MAC, ck.GetPurpose())

	data := []byte("data to mac")
	signResp, err := c.MacSign(ctx, &kmspb.MacSignRequest{
		Name:       versionName,
		Data:       data,
		DataCrc32C: wrapperspb.Int64(kmsCRC32C(data)),
	})
	require.NoError(t, err)
	require.NotEmpty(t, signResp.GetMac())

	// MacVerify with the real tag → true.
	verifyResp, err := c.MacVerify(ctx, &kmspb.MacVerifyRequest{
		Name:       versionName,
		Data:       data,
		Mac:        signResp.GetMac(),
		DataCrc32C: wrapperspb.Int64(kmsCRC32C(data)),
		MacCrc32C:  wrapperspb.Int64(kmsCRC32C(signResp.GetMac())),
	})
	require.NoError(t, err)
	require.True(t, verifyResp.GetSuccess(), "MacVerify with the matching tag must return true")

	// MacVerify with a tampered tag → false.
	tampered := append([]byte{}, signResp.GetMac()...)
	tampered[0] ^= 0xff
	verifyResp, err = c.MacVerify(ctx, &kmspb.MacVerifyRequest{
		Name: versionName,
		Data: data,
		Mac:  tampered,
	})
	require.NoError(t, err)
	require.False(t, verifyResp.GetSuccess(), "MacVerify with a tampered tag must return false")
}

func TestCloudKMS_GRPC_RawEncryptDecrypt(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-raw")

	ck, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "raw",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_RAW_ENCRYPT_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_AES_256_GCM,
			},
		},
	})
	require.NoError(t, err)
	versionName := ck.GetPrimary().GetName()

	plaintext := []byte("raw encrypt/decrypt round trip")
	aad := []byte("raw-aad")
	iv := make([]byte, 12)
	_, _ = rand.Read(iv)

	enc, err := c.RawEncrypt(ctx, &kmspb.RawEncryptRequest{
		Name:                        versionName,
		Plaintext:                   plaintext,
		AdditionalAuthenticatedData: aad,
		InitializationVector:        iv,
	})
	require.NoError(t, err)
	require.NotEmpty(t, enc.GetCiphertext())
	require.Equal(t, int32(16), enc.GetTagLength(), "AES-GCM tag is 16 bytes")

	// RawDecrypt reverses it.
	dec, err := c.RawDecrypt(ctx, &kmspb.RawDecryptRequest{
		Name:                        versionName,
		Ciphertext:                  enc.GetCiphertext(),
		AdditionalAuthenticatedData: aad,
		InitializationVector:        iv,
		TagLength:                   enc.GetTagLength(),
	})
	require.NoError(t, err)
	require.Equal(t, plaintext, dec.GetPlaintext())

	// RawDecrypt with a corrupted ciphertext must fail.
	corrupt := append([]byte{}, enc.GetCiphertext()...)
	corrupt[0] ^= 0xff
	_, err = c.RawDecrypt(ctx, &kmspb.RawDecryptRequest{
		Name:                        versionName,
		Ciphertext:                  corrupt,
		AdditionalAuthenticatedData: aad,
		InitializationVector:        iv,
		TagLength:                   enc.GetTagLength(),
	})
	require.Error(t, err)
}

func TestCloudKMS_GRPC_GenerateRandomBytes(t *testing.T) {
	c := newKMSGRPCClient(t)
	location := "projects/kms-grpc-project/locations/global"

	resp, err := c.GenerateRandomBytes(ctx, &kmspb.GenerateRandomBytesRequest{
		Location:        location,
		LengthBytes:     32,
		ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetData(), 32, "GenerateRandomBytes must return exactly N bytes")
	require.Equal(t, kmsCRC32C(resp.GetData()), resp.GetDataCrc32C().GetValue())

	// Two calls must differ (real randomness).
	other, err := c.GenerateRandomBytes(ctx, &kmspb.GenerateRandomBytesRequest{
		Location:    location,
		LengthBytes: 32,
	})
	require.NoError(t, err)
	require.NotEqual(t, resp.GetData(), other.GetData(), "two GenerateRandomBytes calls must differ")

	// Out-of-range length → InvalidArgument.
	_, err = c.GenerateRandomBytes(ctx, &kmspb.GenerateRandomBytesRequest{
		Location:    location,
		LengthBytes: 1,
	})
	requireGRPCCode(t, err, codes.InvalidArgument)
}

func TestCloudKMS_GRPC_CreateCryptoKeyVersionAndDestroyRestore(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-ckv-lifecycle")
	keyName := ringName + "/cryptoKeys/sym"

	// Skip the initial version so CreateCryptoKeyVersion provisions the first.
	_, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:                     ringName,
		CryptoKeyId:                "sym",
		SkipInitialVersionCreation: true,
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION,
			},
		},
	})
	require.NoError(t, err)

	// CreateCryptoKeyVersion mints version 1.
	v1, err := c.CreateCryptoKeyVersion(ctx, &kmspb.CreateCryptoKeyVersionRequest{
		Parent: keyName,
		CryptoKeyVersion: &kmspb.CryptoKeyVersion{
			State: kmspb.CryptoKeyVersion_ENABLED,
		},
	})
	require.NoError(t, err)
	require.Equal(t, keyName+"/cryptoKeyVersions/1", v1.GetName())
	require.Equal(t, kmspb.CryptoKeyVersion_ENABLED, v1.GetState())

	// GetCryptoKeyVersion returns the same version.
	got, err := c.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: v1.GetName()})
	require.NoError(t, err)
	require.Equal(t, v1.GetName(), got.GetName())

	// ListCryptoKeyVersions includes it.
	listed := kmsListAllCryptoKeyVersions(t, c, keyName)
	require.Len(t, listed, 1)

	// Destroy → DESTROY_SCHEDULED.
	destroyed, err := c.DestroyCryptoKeyVersion(ctx, &kmspb.DestroyCryptoKeyVersionRequest{Name: v1.GetName()})
	require.NoError(t, err)
	require.Equal(t, kmspb.CryptoKeyVersion_DESTROY_SCHEDULED, destroyed.GetState())
	require.NotNil(t, destroyed.GetDestroyTime())

	// Destroying an already-destroyed-scheduled version is rejected.
	_, err = c.DestroyCryptoKeyVersion(ctx, &kmspb.DestroyCryptoKeyVersionRequest{Name: v1.GetName()})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	// Restore → DISABLED.
	restored, err := c.RestoreCryptoKeyVersion(ctx, &kmspb.RestoreCryptoKeyVersionRequest{Name: v1.GetName()})
	require.NoError(t, err)
	require.Equal(t, kmspb.CryptoKeyVersion_DISABLED, restored.GetState())
	require.Nil(t, restored.GetDestroyTime())
}

func TestCloudKMS_GRPC_DecryptWrongKeyFails(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-wrong-key")
	keyName := ringName + "/cryptoKeys/sym"

	_, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "sym",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION,
			},
		},
	})
	require.NoError(t, err)

	enc, err := c.Encrypt(ctx, &kmspb.EncryptRequest{Name: keyName, Plaintext: []byte("p")})
	require.NoError(t, err)

	// Decrypt under a different CryptoKey must fail: the ciphertext carries the
	// source version number, whose material does not exist under the other key.
	otherRing := kmsKeyRingForTest(t, c, "grpc-wrong-key-other")
	otherKey, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      otherRing,
		CryptoKeyId: "other",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION,
			},
		},
	})
	require.NoError(t, err)
	_, err = c.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:       otherKey.GetName(),
		Ciphertext: enc.GetCiphertext(),
	})
	require.Error(t, err, "decrypting under the wrong key must fail")
}

func TestCloudKMS_GRPC_UpdateCryptoKeyLabels(t *testing.T) {
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-update-ck")
	keyName := ringName + "/cryptoKeys/labeled"

	_, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "labeled",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION,
			},
		},
	})
	require.NoError(t, err)

	updated, err := c.UpdateCryptoKey(ctx, &kmspb.UpdateCryptoKeyRequest{
		CryptoKey: &kmspb.CryptoKey{
			Name:   keyName,
			Labels: map[string]string{"env": "test", "team": "sim"},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"labels"}},
	})
	require.NoError(t, err)
	require.Equal(t, "test", updated.GetLabels()["env"])
	require.Equal(t, "sim", updated.GetLabels()["team"])
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// requireGRPCCode asserts the error is a gRPC error with the expected code.
func requireGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "error must be a gRPC status error, got %T: %v", err, err)
	require.Equal(t, want, st.Code())
}

// parseKMSPEMPublicKey parses a PKIX PEM-encoded public key (the form KMS
// returns from GetPublicKey).
func parseKMSPEMPublicKey(t *testing.T, pemStr string) any {
	t.Helper()
	block, _ := pem.Decode([]byte(pemStr))
	require.NotNil(t, block, "public key PEM must decode")
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	return pub
}
