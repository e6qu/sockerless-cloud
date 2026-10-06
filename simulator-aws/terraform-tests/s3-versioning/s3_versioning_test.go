package s3_versioning_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3VersioningTerraform writes an object twice into a versioning-enabled
// bucket through terraform-provider-aws: each write reports a version id of
// its own, the data source reads the earlier version back by its id, and
// destroy empties the bucket of every version before deleting it.
func TestS3VersioningTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve", "-var", "content=first")
	first := readOutputs(t, env)["version_id"]
	require.NotEmpty(t, first)
	assert.NotEqual(t, "null", first, "a write to a versioning-enabled bucket gets a version id of its own")

	env.Terraform(t, "apply", "-auto-approve", "-var", "content=second", "-var", "earlier_version="+first)
	outputs := readOutputs(t, env)
	assert.NotEqual(t, first, outputs["version_id"], "the second write is a new version")
	assert.Equal(t, "first", outputs["earlier_body"], "the earlier version is still readable by its id")

	env.Terraform(t, "destroy", "-auto-approve", "-var", "content=second", "-var", "earlier_version="+first)
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
