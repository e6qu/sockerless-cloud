package ec2instance_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2InstanceTerraform launches an instance from the published Amazon
// Linux 2023 AMI the aws_ami data source selects, captures it with
// aws_ami_from_instance and boots a second instance from the capture.
func TestEC2InstanceTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	assert.Regexp(t, `^al2023-ami-2023\..*-kernel-6\.1-x86_64$`, outputs["ami_name"].Value)
	assert.Equal(t, "amazon", outputs["ami_owner_alias"].Value)
	assert.Equal(t, "x86_64", outputs["golden_architecture"].Value)
	assert.Equal(t, "running", outputs["golden_state"].Value)
	assert.Equal(t, "x86_64", outputs["captured_architecture"].Value)
	assert.Equal(t, "running", outputs["clone_state"].Value)
	assert.Equal(t, outputs["captured_ami"].Value, outputs["clone_ami"].Value)

	env.Terraform(t, "destroy", "-auto-approve")
}
