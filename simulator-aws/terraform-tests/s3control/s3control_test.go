package s3control_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3ControlTerraform drives the S3 control plane through
// terraform-provider-aws: a standard access point over a bucket, the Object
// Lambda access point that serves reads through a transformation function, a
// Storage Lens dashboard, and the Access Grants instance with a location and a
// grant inside it. Apply creates each, the outputs are read back from the
// simulator's own answers, and destroy removes them in dependency order —
// which is where an instance that refuses to delete while it still holds
// grants would show up.
func TestS3ControlTerraform(t *testing.T) {
	tfsim.WithoutHTTPSGateway(t)
	env := tfsim.Start(t, ".")
	env.RouteHostPrefixedRequests()
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	assert.Contains(t, outputs.must(t, "access_point_arn"), ":accesspoint/tf-s3control-ap")
	assert.Contains(t, outputs.must(t, "object_lambda_access_point_arn"), "arn:aws:s3-object-lambda:")
	assert.Contains(t, outputs.must(t, "storage_lens_arn"), "storage-lens/tf-s3control-lens")
	assert.Contains(t, outputs.must(t, "access_grants_instance_arn"), "access-grants/default")
	assert.Equal(t, "s3://tf-s3control-source/data/*", outputs.must(t, "access_grant_scope"),
		"a grant with no sub-prefix covers its location's whole scope")

	env.Terraform(t, "destroy", "-auto-approve")
}

type tfOutputs map[string]struct {
	Value any `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, isString := v.Value.(string)
	require.Truef(t, isString, "output %q is %T, not a string", key, v.Value)
	require.NotEmptyf(t, s, "output %q is empty", key)
	return s
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	raw := env.Terraform(t, "output", "-json")
	var out tfOutputs
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}
