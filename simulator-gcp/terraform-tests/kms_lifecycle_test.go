package gcp_tf_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformKMSKeyRingCryptoKey applies a google_kms_key_ring and a
// google_kms_crypto_key and destroys them. Cloud KMS deletes neither, so
// destroy drops them from state and schedules the key's versions for
// destruction.
func TestTerraformKMSKeyRingCryptoKey(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "kms-lifecycle")
	cleanTerraformFixture(t, fixtureDir)

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	// Destroy from a cleanup so a failed assertion still tears the fixture down.
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})

	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	require.Contains(t, outputs.must(t, "key_ring_id"), "projects/test-project/locations/global/keyRings/tf-kms-ring")
	require.Contains(t, outputs.must(t, "crypto_key_id"), "/cryptoKeys/tf-kms-key")
	require.Equal(t, "ENCRYPT_DECRYPT", outputs.must(t, "crypto_key_purpose"))
	// The provider re-reads rotation_period from GetCryptoKey, so a create that
	// dropped rotationPeriod leaves it empty.
	require.Equal(t, "604800s", outputs.must(t, "crypto_key_rotation_period"),
		"the crypto key's rotation schedule must survive create + read")
	require.Equal(t, "keys only", outputs.must(t, "crypto_key_iam_condition_title"),
		"the conditional binding reads back with the condition it was written with")

	// A second plan must be empty: whatever the create sent and the read dropped
	// surfaces as drift.
	out, err = runTimed(t, "terraform plan", terraformCmdInDir(fixtureDir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "Cloud KMS plan showed drift after apply (not idempotent):\n%s", out)
}
