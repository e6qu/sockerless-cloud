package gcp_cli_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hpke"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func kmsCLIPublicKey(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	block, _ := pem.Decode(data)
	require.NotNil(t, block, "gcloud wrote no PEM public key: %s", data)
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	return pub
}

// gcloud kms asymmetric-sign hashes nothing itself: --digest-algorithm names
// the digest field it sends, and Cloud KMS signs with the digest the key's
// algorithm names.
func TestKMSCLIAsymmetricSignWithTheKeysDigest(t *testing.T) {
	t.Parallel()
	const ring = "cli-kms-sign-ring"
	const key = "cli-p384"
	runCLI(t, gcloudCLI("kms", "keyrings", "create", ring, "--location=global"))
	runCLI(t, gcloudCLI("kms", "keys", "create", key, "--keyring="+ring, "--location=global",
		"--purpose=asymmetric-signing", "--default-algorithm=ec-sign-p384-sha384"))

	dir := t.TempDir()
	input := filepath.Join(dir, "message.txt")
	sigFile := filepath.Join(dir, "message.sig")
	pubFile := filepath.Join(dir, "public.pem")
	message := []byte("signed through gcloud with SHA-384")
	require.NoError(t, os.WriteFile(input, message, 0o600))

	runCLI(t, gcloudCLI("kms", "keys", "versions", "get-public-key", "1", "--key="+key, "--keyring="+ring,
		"--location=global", "--output-file="+pubFile))
	pub := kmsCLIPublicKey(t, pubFile).(*ecdsa.PublicKey)
	require.Equal(t, elliptic.P384(), pub.Curve)

	runCLI(t, gcloudCLI("kms", "asymmetric-sign", "--version=1", "--key="+key, "--keyring="+ring,
		"--location=global", "--digest-algorithm=sha384", "--input-file="+input, "--signature-file="+sigFile))
	sig, err := os.ReadFile(sigFile)
	require.NoError(t, err)
	sum := sha512.Sum384(message)
	require.True(t, ecdsa.VerifyASN1(pub, sum[:], sig), "the signature verifies over the SHA-384 digest")

	out, err := gcloudCLI("kms", "asymmetric-sign", "--version=1", "--key="+key, "--keyring="+ring,
		"--location=global", "--digest-algorithm=sha256", "--input-file="+input, "--signature-file="+sigFile).CombinedOutput()
	require.Error(t, err, "a SHA-256 digest does not match an EC_SIGN_P384_SHA384 key: %s", out)
	require.Contains(t, string(out), "sha384")
}

func TestKMSCLIDecapsulateMLKEM(t *testing.T) {
	t.Parallel()
	const ring = "cli-kms-kem-ring"
	const key = "cli-ml-kem-768"
	runCLI(t, gcloudCLI("kms", "keyrings", "create", ring, "--location=global"))
	runCLI(t, gcloudCLI("kms", "keys", "create", key, "--keyring="+ring, "--location=global",
		"--purpose=key-encapsulation", "--default-algorithm=ml-kem-768"))

	dir := t.TempDir()
	pubFile := filepath.Join(dir, "public.raw")
	runCLI(t, gcloudCLI("kms", "keys", "versions", "get-public-key", "1", "--key="+key, "--keyring="+ring,
		"--location=global", "--public-key-format=nist-pqc", "--output-file="+pubFile))
	raw, err := os.ReadFile(pubFile)
	require.NoError(t, err)
	ek, err := mlkem.NewEncapsulationKey768(raw)
	require.NoError(t, err)
	secret, ciphertext := ek.Encapsulate()

	ctFile := filepath.Join(dir, "ciphertext.bin")
	secretFile := filepath.Join(dir, "secret.bin")
	require.NoError(t, os.WriteFile(ctFile, ciphertext, 0o600))
	runCLI(t, gcloudCLI("kms", "decapsulate", "--version=1", "--key="+key, "--keyring="+ring,
		"--location=global", "--ciphertext-file="+ctFile, "--shared-secret-file="+secretFile))
	got, err := os.ReadFile(secretFile)
	require.NoError(t, err)
	require.Equal(t, secret, got)
}

// gcloud imports a pre-wrapped key for the HPKE methods: the caller seals the
// key material to the import job's raw public key with HPKE. The installed
// gcloud's `import-jobs create` offers only the RSA methods, so the job comes
// from the REST method the quantum-safe import guide calls.
func TestKMSCLIHPKEImport(t *testing.T) {
	t.Parallel()
	const ring = "cli-kms-hpke-ring"
	const job = "cli-hpke-xwing"
	const key = "cli-hpke-imported"
	runCLI(t, gcloudCLI("kms", "keyrings", "create", ring, "--location=global"))
	httpDoJSON(t, "POST", baseURL+"/v1/projects/"+project+"/locations/global/keyRings/"+ring+"/importJobs?importJobId="+job,
		`{"importMethod":"HPKE_KEM_XWING_HKDF_SHA256_AES_256_GCM","protectionLevel":"SOFTWARE"}`)
	out := runCLI(t, gcloudCLI("kms", "import-jobs", "describe", job, "--keyring="+ring, "--location=global",
		"--format=value(publicKey.data)"))
	// gcloud prints a bytes field in base64, URL-safe in some releases and
	// standard in others.
	printed := strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimSpace(out))
	pubBytes, err := base64.StdEncoding.DecodeString(printed)
	require.NoError(t, err, "gcloud printed %q", out)
	recipient, err := hpke.MLKEM768X25519().NewPublicKey(pubBytes)
	require.NoError(t, err)

	target, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(target)
	require.NoError(t, err)
	wrapped, err := hpke.Seal(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), nil, pkcs8)
	require.NoError(t, err)
	dir := t.TempDir()
	wrappedFile := filepath.Join(dir, "wrapped_key.bin")
	require.NoError(t, os.WriteFile(wrappedFile, wrapped, 0o600))

	runCLI(t, gcloudCLI("kms", "keys", "create", key, "--keyring="+ring, "--location=global",
		"--purpose=asymmetric-signing", "--default-algorithm=ec-sign-p256-sha256",
		"--protection-level=software", "--skip-initial-version-creation"))
	runCLI(t, gcloudCLI("kms", "keys", "versions", "import", "--key="+key, "--keyring="+ring,
		"--location=global", "--import-job="+job, "--algorithm=ec-sign-p256-sha256", "--wrapped-key-file="+wrappedFile))

	input := filepath.Join(dir, "message.txt")
	sigFile := filepath.Join(dir, "message.sig")
	message := []byte("signed by the key gcloud imported")
	require.NoError(t, os.WriteFile(input, message, 0o600))
	runCLI(t, gcloudCLI("kms", "asymmetric-sign", "--version=1", "--key="+key, "--keyring="+ring,
		"--location=global", "--digest-algorithm=sha256", "--input-file="+input, "--signature-file="+sigFile))
	sig, err := os.ReadFile(sigFile)
	require.NoError(t, err)
	sum := sha256.Sum256(message)
	require.True(t, ecdsa.VerifyASN1(&target.PublicKey, sum[:], sig), "the imported version signs with the sealed key")
}

func TestKMSCLIDeletedKeyIsRetired(t *testing.T) {
	t.Parallel()
	const ring = "cli-kms-retire-ring"
	const key = "cli-retired"
	runCLI(t, gcloudCLI("kms", "keyrings", "create", ring, "--location=global"))
	runCLI(t, gcloudCLI("kms", "keys", "create", key, "--keyring="+ring, "--location=global",
		"--purpose=encryption", "--skip-initial-version-creation"))
	runCLI(t, gcloudCLI("kms", "keys", "delete", key, "--keyring="+ring, "--location=global", "--quiet"))

	keyName := "projects/" + project + "/locations/global/keyRings/" + ring + "/cryptoKeys/" + key
	var retired []struct {
		Name             string `json:"name"`
		OriginalResource string `json:"originalResource"`
		ResourceType     string `json:"resourceType"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("kms", "retired-resources", "list", "--location=global", "--format=json")), &retired)
	var name string
	for _, rr := range retired {
		if rr.OriginalResource == keyName {
			name = rr.Name
			require.Equal(t, "cloudkms.googleapis.com/CryptoKey", rr.ResourceType)
		}
	}
	require.NotEmpty(t, name, "deleting %s records a RetiredResource: %+v", keyName, retired)

	out := runCLI(t, gcloudCLI("kms", "retired-resources", "describe", name[strings.LastIndex(name, "/")+1:],
		"--location=global", "--format=value(originalResource)"))
	require.Equal(t, keyName, strings.TrimSpace(out))

	createOut, err := gcloudCLI("kms", "keys", "create", key, "--keyring="+ring, "--location=global",
		"--purpose=encryption").CombinedOutput()
	require.Error(t, err, "a retired CryptoKey name cannot be reused: %s", createOut)
	require.Contains(t, string(createOut), "ALREADY_EXISTS")
}

func TestKMSCLIKeyHandleProvisionsAKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := filepath.Join(dir, "autokey.yaml")
	require.NoError(t, os.WriteFile(config, []byte("name: projects/"+project+"/autokeyConfig\nkeyProjectResolutionMode: RESOURCE_PROJECT\n"), 0o600))
	runCLI(t, gcloudCLI("kms", "autokey-config", "update", config))

	var handle struct {
		Name                 string `json:"name"`
		KmsKey               string `json:"kmsKey"`
		ResourceTypeSelector string `json:"resourceTypeSelector"`
	}
	parseDescribedResource(t, runCLI(t, gcloudCLI("kms", "key-handles", "create", "--location=us-central1",
		"--resource-type=bigquery.googleapis.com/Dataset", "--key-handle-id=cli-dataset", "--format=json")), &handle)
	require.Equal(t, "projects/"+project+"/locations/us-central1/keyHandles/cli-dataset", handle.Name)
	require.True(t, strings.HasPrefix(handle.KmsKey, "projects/"+project+"/locations/us-central1/keyRings/autokey/cryptoKeys/"), handle.KmsKey)

	keyID := handle.KmsKey[strings.LastIndex(handle.KmsKey, "/")+1:]
	var key struct {
		Purpose         string `json:"purpose"`
		RotationPeriod  string `json:"rotationPeriod"`
		VersionTemplate struct {
			ProtectionLevel string `json:"protectionLevel"`
		} `json:"versionTemplate"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("kms", "keys", "describe", keyID, "--keyring=autokey",
		"--location=us-central1", "--format=json")), &key)
	require.Equal(t, "ENCRYPT_DECRYPT", key.Purpose)
	require.Equal(t, "HSM", key.VersionTemplate.ProtectionLevel)
	require.Equal(t, "31536000s", key.RotationPeriod)
}
