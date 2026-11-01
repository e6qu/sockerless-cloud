package gcp_sdk_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hpke"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"strings"
	"testing"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudkms/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Cloud KMS signing with each algorithm's own digest, ML-KEM key
// encapsulation, HPKE import, retired CryptoKey names and Autokey, through
// cloud.google.com/go/kms over gRPC.

func kmsCreateKeyWithAlgorithm(t *testing.T, c *kms.KeyManagementClient, ringName, id string, purpose kmspb.CryptoKey_CryptoKeyPurpose, algorithm kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm, skipInitial bool) *kmspb.CryptoKey {
	t.Helper()
	key, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: id,
		CryptoKey: &kmspb.CryptoKey{
			Purpose:         purpose,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{Algorithm: algorithm},
		},
		SkipInitialVersionCreation: skipInitial,
	})
	require.NoError(t, err)
	return key
}

func kmsPEMPublicKey(t *testing.T, c *kms.KeyManagementClient, versionName string) any {
	t.Helper()
	pub, err := c.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: versionName})
	require.NoError(t, err)
	return parseKMSPEMPublicKey(t, pub.GetPem())
}

func TestCloudKMS_GRPC_AsymmetricSignUsesTheKeysDigest(t *testing.T) {
	t.Parallel()
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-sign-digests")
	message := []byte("each key signs the digest its algorithm names")

	p384 := kmsCreateKeyWithAlgorithm(t, c, ringName, "p384", kmspb.CryptoKey_ASYMMETRIC_SIGN, kmspb.CryptoKeyVersion_EC_SIGN_P384_SHA384, false)
	p384Version := p384.GetName() + "/cryptoKeyVersions/1"
	ecKey := kmsPEMPublicKey(t, c, p384Version).(*ecdsa.PublicKey)
	require.Equal(t, elliptic.P384(), ecKey.Curve)
	sum384 := sha512.Sum384(message)
	signed, err := c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name:         p384Version,
		Digest:       &kmspb.Digest{Digest: &kmspb.Digest_Sha384{Sha384: sum384[:]}},
		DigestCrc32C: wrapperspb.Int64(kmsCRC32C(sum384[:])),
	})
	require.NoError(t, err)
	require.True(t, signed.GetVerifiedDigestCrc32C())
	require.Equal(t, p384Version, signed.GetName())
	require.True(t, ecdsa.VerifyASN1(ecKey, sum384[:], signed.GetSignature()))

	// A message sent as data is hashed with SHA-384 too.
	signed, err = c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{Name: p384Version, Data: message})
	require.NoError(t, err)
	require.True(t, ecdsa.VerifyASN1(ecKey, sum384[:], signed.GetSignature()))

	sum256 := sha256.Sum256(message)
	_, err = c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name:   p384Version,
		Digest: &kmspb.Digest{Digest: &kmspb.Digest_Sha256{Sha256: sum256[:]}},
	})
	requireGRPCCode(t, err, codes.InvalidArgument)
	_, err = c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name:   p384Version,
		Digest: &kmspb.Digest{Digest: &kmspb.Digest_Sha384{Sha384: sum384[:]}},
		Data:   message,
	})
	requireGRPCCode(t, err, codes.InvalidArgument)

	ed := kmsCreateKeyWithAlgorithm(t, c, ringName, "ed25519", kmspb.CryptoKey_ASYMMETRIC_SIGN, kmspb.CryptoKeyVersion_EC_SIGN_ED25519, false)
	edVersion := ed.GetName() + "/cryptoKeyVersions/1"
	edKey := kmsPEMPublicKey(t, c, edVersion).(ed25519.PublicKey)
	signed, err = c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name:       edVersion,
		Data:       message,
		DataCrc32C: wrapperspb.Int64(kmsCRC32C(message)),
	})
	require.NoError(t, err)
	require.True(t, signed.GetVerifiedDataCrc32C())
	require.True(t, ed25519.Verify(edKey, message, signed.GetSignature()), "Ed25519 signs the data itself")
	sum512 := sha512.Sum512(message)
	_, err = c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name:   edVersion,
		Digest: &kmspb.Digest{Digest: &kmspb.Digest_Sha512{Sha512: sum512[:]}},
	})
	requireGRPCCode(t, err, codes.InvalidArgument)

	raw := kmsCreateKeyWithAlgorithm(t, c, ringName, "raw", kmspb.CryptoKey_ASYMMETRIC_SIGN, kmspb.CryptoKeyVersion_RSA_SIGN_RAW_PKCS1_2048, false)
	rawVersion := raw.GetName() + "/cryptoKeyVersions/1"
	rsaKey := kmsPEMPublicKey(t, c, rawVersion).(*rsa.PublicKey)
	signed, err = c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{Name: rawVersion, Data: message})
	require.NoError(t, err)
	require.NoError(t, rsa.VerifyPKCS1v15(rsaKey, crypto.Hash(0), message, signed.GetSignature()),
		"RSA_SIGN_RAW_PKCS1 pads the data itself, with no DigestInfo")

	pss := kmsCreateKeyWithAlgorithm(t, c, ringName, "pss512", kmspb.CryptoKey_ASYMMETRIC_SIGN, kmspb.CryptoKeyVersion_RSA_SIGN_PSS_4096_SHA512, false)
	pssVersion := pss.GetName() + "/cryptoKeyVersions/1"
	pssKey := kmsPEMPublicKey(t, c, pssVersion).(*rsa.PublicKey)
	signed, err = c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name:   pssVersion,
		Digest: &kmspb.Digest{Digest: &kmspb.Digest_Sha512{Sha512: sum512[:]}},
	})
	require.NoError(t, err)
	require.NoError(t, rsa.VerifyPSS(pssKey, crypto.SHA512, sum512[:], signed.GetSignature(), &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}))
}

func TestCloudKMS_GRPC_DecapsulateMLKEM(t *testing.T) {
	t.Parallel()
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-decapsulate")

	for _, tc := range []struct {
		id        string
		algorithm kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm
		size      int
	}{
		{"ml-kem-768", kmspb.CryptoKeyVersion_ML_KEM_768, mlkem.EncapsulationKeySize768},
		{"ml-kem-1024", kmspb.CryptoKeyVersion_ML_KEM_1024, mlkem.EncapsulationKeySize1024},
	} {
		key := kmsCreateKeyWithAlgorithm(t, c, ringName, tc.id, kmspb.CryptoKey_KEY_ENCAPSULATION, tc.algorithm, false)
		versionName := key.GetName() + "/cryptoKeyVersions/1"

		// A PQC public key has no default format.
		_, err := c.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: versionName})
		requireGRPCCode(t, err, codes.InvalidArgument)
		pub, err := c.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: versionName, PublicKeyFormat: kmspb.PublicKey_NIST_PQC})
		require.NoError(t, err)
		require.Equal(t, kmspb.PublicKey_NIST_PQC, pub.GetPublicKeyFormat())
		require.Equal(t, tc.algorithm, pub.GetAlgorithm())
		data := pub.GetPublicKey().GetData()
		require.Len(t, data, tc.size)
		require.Equal(t, kmsCRC32C(data), pub.GetPublicKey().GetCrc32CChecksum().GetValue())

		var secret, ciphertext []byte
		if tc.algorithm == kmspb.CryptoKeyVersion_ML_KEM_768 {
			ek, err := mlkem.NewEncapsulationKey768(data)
			require.NoError(t, err)
			secret, ciphertext = ek.Encapsulate()
		} else {
			ek, err := mlkem.NewEncapsulationKey1024(data)
			require.NoError(t, err)
			secret, ciphertext = ek.Encapsulate()
		}
		resp, err := c.Decapsulate(ctx, &kmspb.DecapsulateRequest{
			Name:             versionName,
			Ciphertext:       ciphertext,
			CiphertextCrc32C: wrapperspb.Int64(kmsCRC32C(ciphertext)),
		})
		require.NoError(t, err)
		require.Equal(t, versionName, resp.GetName())
		require.True(t, resp.GetVerifiedCiphertextCrc32C())
		require.Equal(t, secret, resp.GetSharedSecret())
		require.Equal(t, kmsCRC32C(secret), resp.GetSharedSecretCrc32C())
	}

	symmetric := kmsCreateKeyWithAlgorithm(t, c, ringName, "symmetric", kmspb.CryptoKey_ENCRYPT_DECRYPT, kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION, false)
	_, err := c.Decapsulate(ctx, &kmspb.DecapsulateRequest{
		Name:       symmetric.GetName() + "/cryptoKeyVersions/1",
		Ciphertext: kmsRandomBytes(t, mlkem.CiphertextSize768),
	})
	requireGRPCCode(t, err, codes.FailedPrecondition)
}

func TestCloudKMS_GRPC_HPKEImport(t *testing.T) {
	t.Parallel()
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-hpke-import")

	for _, tc := range []struct {
		id     string
		method kmspb.ImportJob_ImportMethod
		kem    hpke.KEM
		format kmspb.PublicKey_PublicKeyFormat
	}{
		{"ml-kem-768", kmspb.ImportJob_HPKE_KEM_ML_KEM_768_HKDF_SHA256_AES_256_GCM, hpke.MLKEM768(), kmspb.PublicKey_NIST_PQC},
		{"ml-kem-1024", kmspb.ImportJob_HPKE_KEM_ML_KEM_1024_HKDF_SHA256_AES_256_GCM, hpke.MLKEM1024(), kmspb.PublicKey_NIST_PQC},
		{"xwing", kmspb.ImportJob_HPKE_KEM_XWING_HKDF_SHA256_AES_256_GCM, hpke.MLKEM768X25519(), kmspb.PublicKey_XWING_RAW_BYTES},
	} {
		_, err := c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
			Parent:      ringName,
			ImportJobId: tc.id + "-hsm",
			ImportJob:   &kmspb.ImportJob{ImportMethod: tc.method, ProtectionLevel: kmspb.ProtectionLevel_HSM},
		})
		requireGRPCCode(t, err, codes.InvalidArgument)

		job, err := c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
			Parent:      ringName,
			ImportJobId: tc.id,
			ImportJob:   &kmspb.ImportJob{ImportMethod: tc.method, ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE},
		})
		require.NoError(t, err)
		require.Equal(t, kmspb.ImportJob_ACTIVE, job.GetState())

		got, err := c.GetImportJob(ctx, &kmspb.GetImportJobRequest{Name: job.GetName(), PublicKeyFormat: tc.format})
		require.NoError(t, err)
		require.Equal(t, tc.format, got.GetPublicKeyFormat())
		_, err = c.GetImportJob(ctx, &kmspb.GetImportJobRequest{Name: job.GetName(), PublicKeyFormat: kmspb.PublicKey_PEM})
		requireGRPCCode(t, err, codes.InvalidArgument)

		// Wrap a P-256 signing key with HPKE.Seal, import it, and prove the
		// imported version signs with exactly that key.
		target, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		pkcs8, err := x509.MarshalPKCS8PrivateKey(target)
		require.NoError(t, err)
		recipient, err := tc.kem.NewPublicKey(got.GetPublicKey().GetData())
		require.NoError(t, err)
		wrapped, err := hpke.Seal(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), nil, pkcs8)
		require.NoError(t, err)

		key := kmsCreateKeyWithAlgorithm(t, c, ringName, "imported-"+tc.id, kmspb.CryptoKey_ASYMMETRIC_SIGN, kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256, true)
		version, err := c.ImportCryptoKeyVersion(ctx, &kmspb.ImportCryptoKeyVersionRequest{
			Parent:     key.GetName(),
			ImportJob:  job.GetName(),
			Algorithm:  kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256,
			WrappedKey: wrapped,
		})
		require.NoError(t, err)
		require.Equal(t, kmspb.CryptoKeyVersion_ENABLED, version.GetState())
		require.Equal(t, job.GetName(), version.GetImportJob())

		message := []byte("signed by the imported key")
		sum := sha256.Sum256(message)
		signed, err := c.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
			Name:   version.GetName(),
			Digest: &kmspb.Digest{Digest: &kmspb.Digest_Sha256{Sha256: sum[:]}},
		})
		require.NoError(t, err)
		require.True(t, ecdsa.VerifyASN1(&target.PublicKey, sum[:], signed.GetSignature()))
	}
}

func TestCloudKMS_GRPC_RetiredResources(t *testing.T) {
	t.Parallel()
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-retired")
	location := strings.Split(ringName, "/keyRings/")[0]

	key := kmsCreateKeyWithAlgorithm(t, c, ringName, "retire-me", kmspb.CryptoKey_ENCRYPT_DECRYPT, kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION, true)
	op, err := c.DeleteCryptoKey(ctx, &kmspb.DeleteCryptoKeyRequest{Name: key.GetName()})
	require.NoError(t, err)
	require.NoError(t, op.Wait(ctx))
	metadata, err := op.Metadata()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(metadata.GetRetiredResource(), location+"/retiredResources/"), metadata.GetRetiredResource())

	rr, err := c.GetRetiredResource(ctx, &kmspb.GetRetiredResourceRequest{Name: metadata.GetRetiredResource()})
	require.NoError(t, err)
	require.Equal(t, key.GetName(), rr.GetOriginalResource())
	require.Equal(t, "cloudkms.googleapis.com/CryptoKey", rr.GetResourceType())
	require.NotNil(t, rr.GetDeleteTime())

	var listed []string
	it := c.ListRetiredResources(ctx, &kmspb.ListRetiredResourcesRequest{Parent: location})
	for {
		next, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		listed = append(listed, next.GetName())
	}
	require.Contains(t, listed, rr.GetName())

	_, err = c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "retire-me",
		CryptoKey:   &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
	})
	requireGRPCCode(t, err, codes.AlreadyExists)

	_, err = c.GetRetiredResource(ctx, &kmspb.GetRetiredResourceRequest{Name: location + "/retiredResources/never"})
	requireGRPCCode(t, err, codes.NotFound)
}

func newKMSAutokeyGRPCClient(t *testing.T) *kms.AutokeyClient {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, simGRPCDialOptions(simTokenSource())...)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	cli, err := kms.NewAutokeyClient(ctx, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { cli.Close() })
	return cli
}

// kmsEnableAutokey turns on same-project key storage for a project.
func kmsEnableAutokey(t *testing.T, project string) {
	t.Helper()
	ensureProject(t, project)
	_, err := kmsService(t).Projects.UpdateAutokeyConfig("projects/"+project+"/autokeyConfig", &cloudkms.AutokeyConfig{
		KeyProjectResolutionMode: "RESOURCE_PROJECT",
	}).UpdateMask("keyProjectResolutionMode").Do()
	require.NoError(t, err)
}

func TestCloudKMS_GRPC_AutokeyProvisionsAKey(t *testing.T) {
	t.Parallel()
	const project = "kms-autokey-grpc"
	kmsEnableAutokey(t, project)
	autokey := newKMSAutokeyGRPCClient(t)
	c := newKMSGRPCClient(t)
	parent := "projects/" + project + "/locations/us-central1"

	op, err := autokey.CreateKeyHandle(ctx, &kmspb.CreateKeyHandleRequest{
		Parent:      parent,
		KeyHandleId: "disk-handle",
		KeyHandle:   &kmspb.KeyHandle{ResourceTypeSelector: "compute.googleapis.com/Disk"},
	})
	require.NoError(t, err)
	handle, err := op.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, parent+"/keyHandles/disk-handle", handle.GetName())
	require.True(t, strings.HasPrefix(handle.GetKmsKey(), parent+"/keyRings/autokey/cryptoKeys/"), handle.GetKmsKey())

	ring, err := c.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: parent + "/keyRings/autokey"})
	require.NoError(t, err)
	require.Equal(t, parent+"/keyRings/autokey", ring.GetName())
	key, err := c.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: handle.GetKmsKey()})
	require.NoError(t, err)
	require.Equal(t, kmspb.ProtectionLevel_HSM, key.GetVersionTemplate().GetProtectionLevel())
	require.Equal(t, int64(365*24*3600), key.GetRotationPeriod().GetSeconds())
	require.Equal(t, kmspb.CryptoKeyVersion_ENABLED, key.GetPrimary().GetState())

	plaintext := []byte("protected by an Autokey key")
	enc, err := c.Encrypt(ctx, &kmspb.EncryptRequest{Name: handle.GetKmsKey(), Plaintext: plaintext})
	require.NoError(t, err)
	dec, err := c.Decrypt(ctx, &kmspb.DecryptRequest{Name: handle.GetKmsKey(), Ciphertext: enc.GetCiphertext()})
	require.NoError(t, err)
	require.Equal(t, plaintext, dec.GetPlaintext())

	got, err := autokey.GetKeyHandle(ctx, &kmspb.GetKeyHandleRequest{Name: handle.GetName()})
	require.NoError(t, err)
	require.Equal(t, handle.GetKmsKey(), got.GetKmsKey())
	it := autokey.ListKeyHandles(ctx, &kmspb.ListKeyHandlesRequest{
		Parent: parent,
		Filter: `resource_type_selector="compute.googleapis.com/Disk"`,
	})
	first, err := it.Next()
	require.NoError(t, err)
	require.Equal(t, handle.GetName(), first.GetName())
	_, err = it.Next()
	require.Equal(t, iterator.Done, err)

	// A project no AutokeyConfig enables gets no key.
	ensureProject(t, "kms-grpc-project")
	_, err = autokey.CreateKeyHandle(ctx, &kmspb.CreateKeyHandleRequest{
		Parent:    "projects/kms-grpc-project/locations/us-central1",
		KeyHandle: &kmspb.KeyHandle{ResourceTypeSelector: "compute.googleapis.com/Disk"},
	})
	requireGRPCCode(t, err, codes.FailedPrecondition)
}
