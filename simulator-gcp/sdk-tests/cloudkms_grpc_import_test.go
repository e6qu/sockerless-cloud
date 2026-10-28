package gcp_sdk_test

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"io"
	"testing"
	"time"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The Cloud KMS gRPC surface beyond the key lifecycle and crypto: the ImportJob
// family and its two imports, the trusted-key-wrapped export and import pair,
// the deletes (which answer with an operation), the RetiredResource reads and
// the Decapsulate refusal. cloud.google.com/go/kms speaks gRPC only.

// kmsWrapForImportJob RSA-OAEP encrypts the material under the job's wrapping
// public key, the whole wrapped blob for the direct RSA_OAEP_*_SHA256 import
// methods.
func kmsWrapForImportJob(t *testing.T, jobPublicKeyPEM string, material []byte) []byte {
	t.Helper()
	pub, ok := parseKMSPEMPublicKey(t, jobPublicKeyPEM).(*rsa.PublicKey)
	require.True(t, ok, "an ImportJob wrapping key is RSA")
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, material, nil)
	require.NoError(t, err)
	return wrapped
}

func kmsRandomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	require.NoError(t, err)
	return b
}

func kmsMacSign(t *testing.T, c *kms.KeyManagementClient, versionName string, data []byte) []byte {
	t.Helper()
	resp, err := c.MacSign(ctx, &kmspb.MacSignRequest{
		Name:       versionName,
		Data:       data,
		DataCrc32C: wrapperspb.Int64(kmsCRC32C(data)),
	})
	require.NoError(t, err)
	require.Equal(t, kmsCRC32C(resp.GetMac()), resp.GetMacCrc32C().GetValue())
	return resp.GetMac()
}

func TestCloudKMS_GRPC_ImportJobLifecycle(t *testing.T) {
	t.Parallel()
	ensureProject(t, "kms-grpc-project")
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-import-jobs")

	job, err := c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
		Parent:      ringName,
		ImportJobId: "job-1",
		ImportJob: &kmspb.ImportJob{
			ImportMethod:    kmspb.ImportJob_RSA_OAEP_3072_SHA256,
			ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
		},
	})
	require.NoError(t, err)
	require.Equal(t, ringName+"/importJobs/job-1", job.GetName())
	require.Equal(t, kmspb.ImportJob_RSA_OAEP_3072_SHA256, job.GetImportMethod())
	require.Equal(t, kmspb.ProtectionLevel_SOFTWARE, job.GetProtectionLevel())
	require.Equal(t, kmspb.ImportJob_ACTIVE, job.GetState())
	require.NotEmpty(t, job.GetPublicKey().GetPem(), "an ACTIVE ImportJob publishes its wrapping public key")
	require.NotNil(t, job.GetCreateTime())
	require.NotNil(t, job.GetGenerateTime())

	// The wrapping public key is a real 3072-bit RSA key, the size the import
	// method names.
	pub, ok := parseKMSPEMPublicKey(t, job.GetPublicKey().GetPem()).(*rsa.PublicKey)
	require.True(t, ok)
	require.Equal(t, 3072, pub.N.BitLen())

	got, err := c.GetImportJob(ctx, &kmspb.GetImportJobRequest{Name: job.GetName()})
	require.NoError(t, err)
	require.Equal(t, job.GetName(), got.GetName())
	require.Equal(t, job.GetPublicKey().GetPem(), got.GetPublicKey().GetPem())
	require.Equal(t, kmspb.ImportJob_ACTIVE, got.GetState())

	// A second job in the same ring, so the listing must select and order.
	second, err := c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
		Parent:      ringName,
		ImportJobId: "job-2",
		ImportJob:   &kmspb.ImportJob{ImportMethod: kmspb.ImportJob_RSA_OAEP_4096_SHA256_AES_256},
	})
	require.NoError(t, err)
	require.Equal(t, kmspb.ProtectionLevel_SOFTWARE, second.GetProtectionLevel(), "an unset protection level defaults to SOFTWARE")

	// PageSize 1 makes the client walk two pages with the listing's token.
	it := c.ListImportJobs(ctx, &kmspb.ListImportJobsRequest{Parent: ringName, PageSize: 1})
	var listed []string
	for {
		j, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		listed = append(listed, j.GetName())
	}
	require.Equal(t, []string{ringName + "/importJobs/job-1", ringName + "/importJobs/job-2"}, listed)

	_, err = c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
		Parent:      ringName,
		ImportJobId: "job-1",
		ImportJob:   &kmspb.ImportJob{ImportMethod: kmspb.ImportJob_RSA_OAEP_3072_SHA256},
	})
	requireGRPCCode(t, err, codes.AlreadyExists)

	_, err = c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
		Parent:      ringName,
		ImportJobId: "job-bad-method",
		ImportJob:   &kmspb.ImportJob{},
	})
	requireGRPCCode(t, err, codes.InvalidArgument)

	_, err = c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
		Parent:      "projects/kms-grpc-project/locations/global/keyRings/no-such-ring",
		ImportJobId: "job-1",
		ImportJob:   &kmspb.ImportJob{ImportMethod: kmspb.ImportJob_RSA_OAEP_3072_SHA256},
	})
	requireGRPCCode(t, err, codes.NotFound)

	_, err = c.GetImportJob(ctx, &kmspb.GetImportJobRequest{Name: ringName + "/importJobs/never"})
	requireGRPCCode(t, err, codes.NotFound)
}

// TestCloudKMS_GRPC_ImportCryptoKeyVersionKeepsTheCallersMaterial imports the
// same 32 bytes into two CryptoKeys; both versions must produce the HMAC
// computed locally from those bytes, which a fresh key cannot.
func TestCloudKMS_GRPC_ImportCryptoKeyVersionKeepsTheCallersMaterial(t *testing.T) {
	t.Parallel()
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-import-material")

	job, err := c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
		Parent:      ringName,
		ImportJobId: "material-job",
		ImportJob:   &kmspb.ImportJob{ImportMethod: kmspb.ImportJob_RSA_OAEP_3072_SHA256},
	})
	require.NoError(t, err)

	material := kmsRandomBytes(t, 32)
	wrapped := kmsWrapForImportJob(t, job.GetPublicKey().GetPem(), material)

	var versions []string
	for _, keyID := range []string{"mac-a", "mac-b"} {
		_, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
			Parent:                     ringName,
			CryptoKeyId:                keyID,
			SkipInitialVersionCreation: true,
			CryptoKey: &kmspb.CryptoKey{
				Purpose: kmspb.CryptoKey_MAC,
				VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
					Algorithm: kmspb.CryptoKeyVersion_HMAC_SHA256,
				},
			},
		})
		require.NoError(t, err)

		imported, err := c.ImportCryptoKeyVersion(ctx, &kmspb.ImportCryptoKeyVersionRequest{
			Parent:     ringName + "/cryptoKeys/" + keyID,
			ImportJob:  job.GetName(),
			Algorithm:  kmspb.CryptoKeyVersion_HMAC_SHA256,
			WrappedKey: wrapped,
		})
		require.NoError(t, err)
		require.Equal(t, ringName+"/cryptoKeys/"+keyID+"/cryptoKeyVersions/1", imported.GetName())
		require.Equal(t, kmspb.CryptoKeyVersion_ENABLED, imported.GetState())
		require.Equal(t, job.GetName(), imported.GetImportJob())
		require.NotNil(t, imported.GetImportTime())
		require.True(t, imported.GetReimportEligible())
		versions = append(versions, imported.GetName())
	}

	data := []byte("import round-trip")
	want := hmac.New(sha256.New, material)
	want.Write(data)
	expected := want.Sum(nil)

	for _, versionName := range versions {
		require.Equal(t, expected, kmsMacSign(t, c, versionName, data),
			"the imported version must MAC with the caller's key material, not a freshly generated key")
	}

	// The same material re-wrapped for a job in a different key ring is not
	// importable into this ring's keys.
	otherRing := kmsKeyRingForTest(t, c, "grpc-import-other-ring")
	otherJob, err := c.CreateImportJob(ctx, &kmspb.CreateImportJobRequest{
		Parent:      otherRing,
		ImportJobId: "material-job",
		ImportJob:   &kmspb.ImportJob{ImportMethod: kmspb.ImportJob_RSA_OAEP_3072_SHA256},
	})
	require.NoError(t, err)
	_, err = c.ImportCryptoKeyVersion(ctx, &kmspb.ImportCryptoKeyVersionRequest{
		Parent:     ringName + "/cryptoKeys/mac-a",
		ImportJob:  otherJob.GetName(),
		Algorithm:  kmspb.CryptoKeyVersion_HMAC_SHA256,
		WrappedKey: kmsWrapForImportJob(t, otherJob.GetPublicKey().GetPem(), material),
	})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	// A blob the job's wrapping key cannot unwrap is rejected, not stored.
	_, err = c.ImportCryptoKeyVersion(ctx, &kmspb.ImportCryptoKeyVersionRequest{
		Parent:     ringName + "/cryptoKeys/mac-a",
		ImportJob:  job.GetName(),
		Algorithm:  kmspb.CryptoKeyVersion_HMAC_SHA256,
		WrappedKey: kmsRandomBytes(t, 384),
	})
	requireGRPCCode(t, err, codes.InvalidArgument)

	// Material of the wrong length for the algorithm is rejected too.
	_, err = c.ImportCryptoKeyVersion(ctx, &kmspb.ImportCryptoKeyVersionRequest{
		Parent:     ringName + "/cryptoKeys/mac-a",
		ImportJob:  job.GetName(),
		Algorithm:  kmspb.CryptoKeyVersion_HMAC_SHA512,
		WrappedKey: wrapped,
	})
	requireGRPCCode(t, err, codes.InvalidArgument)

	_, err = c.ImportCryptoKeyVersion(ctx, &kmspb.ImportCryptoKeyVersionRequest{
		Parent:     ringName + "/cryptoKeys/mac-a",
		ImportJob:  ringName + "/importJobs/never",
		Algorithm:  kmspb.CryptoKeyVersion_HMAC_SHA256,
		WrappedKey: wrapped,
	})
	requireGRPCCode(t, err, codes.NotFound)
}

// TestCloudKMS_GRPC_TrustedKeyWrappedExportImport exports an exportable version
// under an HSM-trusted AES_256_KWP key and imports the blob into another
// CryptoKey; the imported version MACs identically only if the exact material
// moved.
func TestCloudKMS_GRPC_TrustedKeyWrappedExportImport(t *testing.T) {
	t.Parallel()
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-trusted-wrap")

	wrappingKey, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "wrapper",
		CryptoKey: &kmspb.CryptoKey{
			Purpose: kmspb.CryptoKey_AES_WRAPPING,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
				ProtectionLevel: kmspb.ProtectionLevel_HSM_SINGLE_TENANT,
				Algorithm:       kmspb.CryptoKeyVersion_AES_256_KWP,
			},
		},
	})
	require.NoError(t, err)
	wrapperVersion, err := c.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{
		Name: wrappingKey.GetName() + "/cryptoKeyVersions/1",
	})
	require.NoError(t, err)
	require.True(t, wrapperVersion.GetHsmTrusted(), "an HSM_SINGLE_TENANT AES_256_KWP version is an HSM-trusted wrapping key")
	require.Equal(t, kmspb.CryptoKeyVersion_AES_256_KWP, wrapperVersion.GetAlgorithm())

	sourceKey, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "source",
		CryptoKey: &kmspb.CryptoKey{
			Purpose:         kmspb.CryptoKey_MAC,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{Algorithm: kmspb.CryptoKeyVersion_HMAC_SHA256},
		},
	})
	require.NoError(t, err)

	// Version 1 is an ordinary version; version 2 is created exportable.
	exportable, err := c.CreateCryptoKeyVersion(ctx, &kmspb.CreateCryptoKeyVersionRequest{
		Parent:           sourceKey.GetName(),
		CryptoKeyVersion: &kmspb.CryptoKeyVersion{TrustedWrappingEnabled: true},
	})
	require.NoError(t, err)
	require.True(t, exportable.GetTrustedWrappingEnabled())

	// A version that was not created exportable cannot be exported.
	_, err = c.ExportTrustedKeyWrappedCryptoKeyVersion(ctx, &kmspb.ExportTrustedKeyWrappedCryptoKeyVersionRequest{
		Name:        sourceKey.GetName() + "/cryptoKeyVersions/1",
		WrappingKey: wrappingKey.GetName(),
	})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	// Nor can a key that is not an HSM-trusted AES_256_KWP key act as the
	// wrapping key.
	_, err = c.ExportTrustedKeyWrappedCryptoKeyVersion(ctx, &kmspb.ExportTrustedKeyWrappedCryptoKeyVersionRequest{
		Name:        exportable.GetName(),
		WrappingKey: sourceKey.GetName(),
	})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	export, err := c.ExportTrustedKeyWrappedCryptoKeyVersion(ctx, &kmspb.ExportTrustedKeyWrappedCryptoKeyVersionRequest{
		Name:        exportable.GetName(),
		WrappingKey: wrappingKey.GetName(),
	})
	require.NoError(t, err)
	require.NotEmpty(t, export.GetWrappedKey())
	require.Equal(t, kmsCRC32C(export.GetWrappedKey()), export.GetWrappedKeyCrc32C().GetValue())

	targetKey, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:                     ringName,
		CryptoKeyId:                "target",
		SkipInitialVersionCreation: true,
		CryptoKey: &kmspb.CryptoKey{
			Purpose:         kmspb.CryptoKey_MAC,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{Algorithm: kmspb.CryptoKeyVersion_HMAC_SHA256},
		},
	})
	require.NoError(t, err)

	imported, err := c.ImportTrustedKeyWrappedCryptoKeyVersion(ctx, &kmspb.ImportTrustedKeyWrappedCryptoKeyVersionRequest{
		Parent:       targetKey.GetName(),
		ImportingKey: wrappingKey.GetName(),
		WrappedKey:   export.GetWrappedKey(),
		Algorithm:    kmspb.CryptoKeyVersion_HMAC_SHA256,
	})
	require.NoError(t, err)
	require.Equal(t, targetKey.GetName()+"/cryptoKeyVersions/1", imported.GetName())
	require.True(t, imported.GetTrustedWrappingEnabled(), "material imported under a trusted key stays trusted-wrapping enabled")
	require.True(t, imported.GetReimportEligible())

	data := []byte("trusted wrapping round-trip")
	require.Equal(t, kmsMacSign(t, c, exportable.GetName(), data), kmsMacSign(t, c, imported.GetName(), data),
		"the imported version must hold the exported version's material")

	// A blob that was not wrapped with the importing key fails the RFC 5649
	// integrity check rather than importing garbage.
	_, err = c.ImportTrustedKeyWrappedCryptoKeyVersion(ctx, &kmspb.ImportTrustedKeyWrappedCryptoKeyVersionRequest{
		Parent:       targetKey.GetName(),
		ImportingKey: wrappingKey.GetName(),
		WrappedKey:   kmsRandomBytes(t, 40),
		Algorithm:    kmspb.CryptoKeyVersion_HMAC_SHA256,
	})
	requireGRPCCode(t, err, codes.InvalidArgument)
}

// TestCloudKMS_GRPC_DeleteCryptoKeyAndVersion: both deletes answer with an
// operation, a version is deletable only once DESTROYED, and a CryptoKey only
// once it has no versions. A one-second destroyScheduledDuration, the field an
// operator sets in the real service, lets the destroy complete in the test.
func TestCloudKMS_GRPC_DeleteCryptoKeyAndVersion(t *testing.T) {
	t.Parallel()
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-deletes")

	key, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "doomed",
		CryptoKey: &kmspb.CryptoKey{
			Purpose:                  kmspb.CryptoKey_ENCRYPT_DECRYPT,
			VersionTemplate:          &kmspb.CryptoKeyVersionTemplate{Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION},
			DestroyScheduledDuration: durationpb.New(time.Second),
		},
	})
	require.NoError(t, err)
	require.Equal(t, time.Second, key.GetDestroyScheduledDuration().AsDuration(),
		"the key carries the destroy schedule it was created with")
	second, err := c.CreateCryptoKeyVersion(ctx, &kmspb.CreateCryptoKeyVersionRequest{Parent: key.GetName()})
	require.NoError(t, err)

	// An ENABLED version holds live key material, so the delete is refused.
	_, err = c.DeleteCryptoKeyVersion(ctx, &kmspb.DeleteCryptoKeyVersionRequest{Name: second.GetName()})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	scheduled, err := c.DestroyCryptoKeyVersion(ctx, &kmspb.DestroyCryptoKeyVersionRequest{Name: second.GetName()})
	require.NoError(t, err)
	require.Equal(t, kmspb.CryptoKeyVersion_DESTROY_SCHEDULED, scheduled.GetState())
	// Still refused: the destroy has not come due yet.
	_, err = c.DeleteCryptoKeyVersion(ctx, &kmspb.DeleteCryptoKeyVersionRequest{Name: second.GetName()})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	destroyed := kmsAwaitVersionState(t, c, second.GetName(), kmspb.CryptoKeyVersion_DESTROYED)
	require.NotNil(t, destroyed.GetDestroyEventTime(), "a destroyed version records when the destroy happened")

	versionOp, err := c.DeleteCryptoKeyVersion(ctx, &kmspb.DeleteCryptoKeyVersionRequest{Name: second.GetName()})
	require.NoError(t, err)
	require.True(t, versionOp.Done(), "the deletion is complete when the RPC returns")
	require.NoError(t, versionOp.Wait(ctx))

	_, err = c.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: second.GetName()})
	requireGRPCCode(t, err, codes.NotFound)
	first := key.GetName() + "/cryptoKeyVersions/1"
	_, err = c.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: first})
	require.NoError(t, err, "deleting one version leaves its siblings alone")

	// Version 1 survives, so the key delete is refused rather than cascading.
	_, err = c.DeleteCryptoKey(ctx, &kmspb.DeleteCryptoKeyRequest{Name: key.GetName()})
	requireGRPCCode(t, err, codes.FailedPrecondition)

	_, err = c.DestroyCryptoKeyVersion(ctx, &kmspb.DestroyCryptoKeyVersionRequest{Name: first})
	require.NoError(t, err)
	kmsAwaitVersionState(t, c, first, kmspb.CryptoKeyVersion_DESTROYED)
	_, err = c.DeleteCryptoKeyVersion(ctx, &kmspb.DeleteCryptoKeyVersionRequest{Name: first})
	require.NoError(t, err)

	keyOp, err := c.DeleteCryptoKey(ctx, &kmspb.DeleteCryptoKeyRequest{Name: key.GetName()})
	require.NoError(t, err)
	require.True(t, keyOp.Done())
	require.NoError(t, keyOp.Wait(ctx))
	// The operation carries the DeleteCryptoKeyMetadata its RPC declares.
	keyMeta, err := keyOp.Metadata()
	require.NoError(t, err)
	require.NotNil(t, keyMeta, "DeleteCryptoKey's operation must carry DeleteCryptoKeyMetadata")

	// The server mounts one google.longrunning.Operations service, where every
	// operation name Cloud KMS hands out must resolve.
	conn, err := grpc.NewClient(grpcAddr, simGRPCDialOptions(simTokenSource())...)
	require.NoError(t, err)
	defer conn.Close()
	resolved, err := longrunningpb.NewOperationsClient(conn).GetOperation(ctx,
		&longrunningpb.GetOperationRequest{Name: keyOp.Name()})
	require.NoError(t, err, "a Cloud KMS operation must be findable through the Operations service")
	require.True(t, resolved.GetDone())

	_, err = c.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: key.GetName()})
	requireGRPCCode(t, err, codes.NotFound)

	it := c.ListCryptoKeys(ctx, &kmspb.ListCryptoKeysRequest{Parent: ringName})
	for {
		k, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		require.NotEqual(t, key.GetName(), k.GetName(), "a deleted CryptoKey must not be listed")
	}

	_, err = c.DeleteCryptoKey(ctx, &kmspb.DeleteCryptoKeyRequest{Name: key.GetName()})
	requireGRPCCode(t, err, codes.NotFound)
	_, err = c.DeleteCryptoKeyVersion(ctx, &kmspb.DeleteCryptoKeyVersionRequest{Name: second.GetName()})
	requireGRPCCode(t, err, codes.NotFound)
}

// kmsAwaitVersionState polls the version's state: Cloud KMS offers no wait on
// a scheduled destroy.
func kmsAwaitVersionState(t *testing.T, c *kms.KeyManagementClient, name string, want kmspb.CryptoKeyVersion_CryptoKeyVersionState) *kmspb.CryptoKeyVersion {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		v, err := c.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: name})
		require.NoError(t, err)
		if v.GetState() == want {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("CryptoKeyVersion %s is %s, want %s", name, v.GetState(), want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestCloudKMS_GRPC_RetiredResources: the simulator records no RetiredResource
// on deletion, so the reads report an empty location and a missing record, as
// the REST reads do.
func TestCloudKMS_GRPC_RetiredResources(t *testing.T) {
	t.Parallel()
	ensureProject(t, "kms-grpc-project")
	c := newKMSGRPCClient(t)
	location := "projects/kms-grpc-project/locations/global"

	it := c.ListRetiredResources(ctx, &kmspb.ListRetiredResourcesRequest{Parent: location})
	for {
		rr, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		require.NotEmpty(t, rr.GetName())
	}

	_, err := c.GetRetiredResource(ctx, &kmspb.GetRetiredResourceRequest{Name: location + "/retiredResources/never"})
	requireGRPCCode(t, err, codes.NotFound)
}

// TestCloudKMS_GRPC_DecapsulateIsRefusedWithItsReason: the simulator offers no
// post-quantum key encapsulation and refuses Decapsulate rather than invent a
// shared secret.
func TestCloudKMS_GRPC_DecapsulateIsRefusedWithItsReason(t *testing.T) {
	t.Parallel()
	c := newKMSGRPCClient(t)
	ringName := kmsKeyRingForTest(t, c, "grpc-decapsulate")

	key, err := c.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent:      ringName,
		CryptoKeyId: "kem",
		CryptoKey: &kmspb.CryptoKey{
			Purpose:         kmspb.CryptoKey_ENCRYPT_DECRYPT,
			VersionTemplate: &kmspb.CryptoKeyVersionTemplate{Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION},
		},
	})
	require.NoError(t, err)
	versionName := key.GetName() + "/cryptoKeyVersions/1"

	_, err = c.Decapsulate(ctx, &kmspb.DecapsulateRequest{
		Name:       versionName,
		Ciphertext: kmsRandomBytes(t, 32),
	})
	requireGRPCCode(t, err, codes.FailedPrecondition)
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Contains(t, st.Message(), "decapsulate is not supported")
	require.Contains(t, st.Message(), versionName)
}
