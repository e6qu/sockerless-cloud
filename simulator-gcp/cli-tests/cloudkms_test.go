package gcp_cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKMSCLILifecycleAndCrypto drives `gcloud kms` against the simulator.
// The encrypt/decrypt round-trip is the important part: gcloud computes and
// verifies the Cloud KMS CRC32C integrity fields and rejects responses that
// lack them ("The request sent to the server was corrupted in-transit"), so a
// clean round-trip proves the sim emits ciphertextCrc32c / plaintextCrc32c /
// verifiedPlaintextCrc32c correctly.
func TestKMSCLILifecycleAndCrypto(t *testing.T) {
	const location = "global"
	const ring = "cli-kms-ring"
	const key = "cli-kms-key"

	runCLI(t, gcloudCLI("kms", "keyrings", "create", ring, "--location="+location))

	out := runCLI(t, gcloudCLI("kms", "keyrings", "describe", ring, "--location="+location, "--format=json"))
	require.Contains(t, out, "/keyRings/"+ring)

	out = runCLI(t, gcloudCLI("kms", "keyrings", "list", "--location="+location, "--format=json"))
	require.Contains(t, out, "/keyRings/"+ring)

	runCLI(t, gcloudCLI("kms", "keys", "create", key,
		"--keyring="+ring, "--location="+location, "--purpose=encryption"))

	out = runCLI(t, gcloudCLI("kms", "keys", "describe", key,
		"--keyring="+ring, "--location="+location, "--format=json"))
	require.Contains(t, out, "/cryptoKeys/"+key)

	out = runCLI(t, gcloudCLI("kms", "keys", "list",
		"--keyring="+ring, "--location="+location, "--format=json"))
	require.Contains(t, out, "/cryptoKeys/"+key)

	dir := t.TempDir()
	ptFile := filepath.Join(dir, "plain.txt")
	ctFile := filepath.Join(dir, "cipher.bin")
	outFile := filepath.Join(dir, "decrypted.txt")
	secret := "gcloud-kms-roundtrip-secret"
	require.NoError(t, os.WriteFile(ptFile, []byte(secret), 0o600))

	runCLI(t, gcloudCLI("kms", "encrypt",
		"--key="+key, "--keyring="+ring, "--location="+location,
		"--plaintext-file="+ptFile, "--ciphertext-file="+ctFile))

	ct, err := os.ReadFile(ctFile)
	require.NoError(t, err)
	require.NotEmpty(t, ct)
	require.NotContains(t, string(ct), secret, "ciphertext must not contain the plaintext")

	runCLI(t, gcloudCLI("kms", "decrypt",
		"--key="+key, "--keyring="+ring, "--location="+location,
		"--ciphertext-file="+ctFile, "--plaintext-file="+outFile))

	got, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.Equal(t, secret, string(got))
}
