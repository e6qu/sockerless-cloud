package gcp_tf_test

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformKMSPostQuantumKeysAndKeyHandle applies an ML-KEM-768 key, an
// Ed25519 key whose public key the data source reads, and an Autokey
// KeyHandle, and holds the second plan empty.
func TestTerraformKMSPostQuantumKeysAndKeyHandle(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "kms-pqc")
	cleanTerraformFixture(t, fixtureDir)

	// google_kms_autokey_config configures folders only, so the project's
	// same-project key storage comes from the API the provider would call.
	req, err := http.NewRequest(http.MethodPatch,
		baseURL+"/v1/projects/test-project/autokeyConfig?updateMask=keyProjectResolutionMode",
		strings.NewReader(`{"keyProjectResolutionMode":"RESOURCE_PROJECT"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "enable Autokey: %s", body)

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})

	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	require.Equal(t, "ML_KEM_768", outputs.must(t, "ml_kem_algorithm"))
	require.Equal(t, "KEY_ENCAPSULATION", outputs.must(t, "ml_kem_purpose"))
	require.Equal(t, "EC_SIGN_ED25519", outputs.must(t, "ed25519_public_key_algorithm"))
	block, _ := pem.Decode([]byte(outputs.must(t, "ed25519_public_key_pem")))
	require.NotNil(t, block)
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	_, isEd25519 := pub.(ed25519.PublicKey)
	require.True(t, isEd25519, "an EC_SIGN_ED25519 key's public key is Ed25519, got %T", pub)
	require.Regexp(t, `^projects/test-project/locations/us-central1/keyRings/autokey/cryptoKeys/[0-9]+-storage-Bucket-[0-9a-f]+$`,
		outputs.must(t, "key_handle_kms_key"))

	out, err = runTimed(t, "terraform plan", terraformCmdInDir(fixtureDir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "Cloud KMS plan showed drift after apply (not idempotent):\n%s", out)
}
