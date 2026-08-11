package gcp_tf_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformKMSKeyRingCryptoKey applies a google_kms_key_ring +
// google_kms_crypto_key against the simulator and tears them down. Key rings
// and crypto keys are not deletable in real GCP, so terraform destroy drops
// them from state (the crypto key's versions are scheduled for destruction);
// the sim must support that round-trip without error.
func TestTerraformKMSKeyRingCryptoKey(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "kms-lifecycle")

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	require.Contains(t, outputs.must(t, "key_ring_id"), "projects/test-project/locations/global/keyRings/tf-kms-ring")
	require.Contains(t, outputs.must(t, "crypto_key_id"), "/cryptoKeys/tf-kms-key")
	require.Equal(t, "ENCRYPT_DECRYPT", outputs.must(t, "crypto_key_purpose"))

	out, err = runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
}
