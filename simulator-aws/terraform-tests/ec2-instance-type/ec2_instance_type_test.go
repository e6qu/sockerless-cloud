package ec2instancetype_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2InstanceTypeTerraform selects instance types through the
// aws_ec2_instance_types data source by the vCPUs, memory and architecture
// AWS publishes for them.
func TestEC2InstanceTypeTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	assert.Equal(t, "t4g.nano", outputs["graviton_2_vcpus_512_mib"].Value)
	assert.Equal(t, "m5.24xlarge,m5.metal", outputs["m5_96_vcpus"].Value)

	env.Terraform(t, "destroy", "-auto-approve")
}
