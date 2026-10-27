package lambda_concurrency_test

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLambdaConcurrencyTerraform configures a function's reserved
// concurrency, an alias's provisioned concurrency and a Kinesis event source
// mapping. The provider waits for the provisioned concurrency configuration
// to read READY, after which the alias's first invocation runs on an
// environment it initialised.
func TestLambdaConcurrencyTerraform(t *testing.T) {
	handler, err := filepath.Abs("../../../testdata/lambda-runtime-handler")
	require.NoError(t, err)
	tfsim.BuildGoScratchImage(t, "sockerless-lambda-runtime-handler:aws-terraform", handler, "lambda-runtime-handler")
	architecture := "x86_64"
	if runtime.GOARCH == "arm64" {
		architecture = "arm64"
	}

	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve", "-var", "lambda_architecture="+architecture)
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	assert.JSONEq(t, `{"invocations":1,"initializationType":"provisioned-concurrency"}`, outputs["warm_invocation"].Value,
		"the alias's first invocation runs on its provisioned environment")
	assert.Equal(t, "2", outputs["reserved_concurrent_executions"].Value)
	assert.Equal(t, "Enabled", outputs["mapping_state"].Value)
	assert.Equal(t, "-1", outputs["mapping_maximum_retry_attempts"].Value,
		"a Kinesis mapping retries without limit unless told otherwise")

	env.Terraform(t, "destroy", "-auto-approve", "-var", "lambda_architecture="+architecture)
}
