package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAMConditionsCLI_KMSKeyBinding adds a conditional binding to a crypto
// key with gcloud kms keys add-iam-policy-binding and reads it back with
// get-iam-policy: the condition survives the policy's write and read.
func TestIAMConditionsCLI_KMSKeyBinding(t *testing.T) {
	t.Parallel()
	const (
		ring   = "cli-cond-ring"
		key    = "cli-cond-key"
		member = "serviceAccount:cli-cond@test-project.iam.gserviceaccount.com"
	)
	runCLI(t, gcloudCLI("kms", "keyrings", "create", ring, "--location="+location))
	runCLI(t, gcloudCLI("kms", "keys", "create", key, "--keyring="+ring, "--location="+location, "--purpose=encryption"))

	runCLI(t, gcloudCLI("kms", "keys", "add-iam-policy-binding", key,
		"--keyring="+ring, "--location="+location,
		"--member="+member,
		"--role=roles/cloudkms.viewer",
		`--condition=^:^expression=resource.type == "cloudkms.googleapis.com/CryptoKey":title=keys only`,
		"--format=json"))

	var policy struct {
		Version  int `json:"version"`
		Bindings []struct {
			Role      string   `json:"role"`
			Members   []string `json:"members"`
			Condition *struct {
				Title      string `json:"title"`
				Expression string `json:"expression"`
			} `json:"condition"`
		} `json:"bindings"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("kms", "keys", "get-iam-policy", key,
		"--keyring="+ring, "--location="+location, "--format=json")), &policy)
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, 3, policy.Version, "a policy with a conditional binding is version 3")
	assert.Equal(t, "roles/cloudkms.viewer", policy.Bindings[0].Role)
	assert.Equal(t, []string{member}, policy.Bindings[0].Members)
	require.NotNil(t, policy.Bindings[0].Condition, "get-iam-policy returns the binding's condition")
	assert.Equal(t, "keys only", policy.Bindings[0].Condition.Title)
	assert.Equal(t, `resource.type == "cloudkms.googleapis.com/CryptoKey"`, policy.Bindings[0].Condition.Expression)
}
