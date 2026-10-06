package s3_object_lock_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3ObjectLockTerraform creates an Object Lock bucket through
// terraform-provider-aws with a default GOVERNANCE retention, an object that
// takes it, one under explicit GOVERNANCE retention and one under a legal
// hold. Destroying the GOVERNANCE-retained object without bypassing
// governance retention is refused; with force_destroy, which bypasses it and
// lifts legal holds, everything is destroyed.
func TestS3ObjectLockTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	outputs := readOutputs(t, env)
	version := outputs["defaulted_version_id"]
	assert.NotEmpty(t, version)
	assert.NotEqual(t, "null", version, "Object Lock turns versioning on, so the write gets a version id of its own")
	assert.Equal(t, "GOVERNANCE", outputs["default_mode"], "the object takes the bucket's default retention")
	until, err := time.Parse(time.RFC3339, outputs["default_retain_until"])
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 1), until, 10*time.Minute)
	assert.Equal(t, "GOVERNANCE", outputs["governed_mode"])
	assert.Equal(t, "ON", outputs["held_status"])

	out := env.TerraformFails(t, "destroy", "-auto-approve", "-target=aws_s3_object.governed")
	assert.Contains(t, string(out), "AccessDenied", "GOVERNANCE retention refuses a delete that does not bypass it")

	env.Terraform(t, "apply", "-auto-approve", "-var", "force_destroy=true")
	env.Terraform(t, "destroy", "-auto-approve", "-var", "force_destroy=true")
}

func readOutputs(t *testing.T, env *tfsim.Env) map[string]string {
	t.Helper()
	var raw map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &raw))
	out := map[string]string{}
	for key, value := range raw {
		out[key] = value.Value
	}
	return out
}
