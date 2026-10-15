package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlueIntegrationConfigCLI creates a zero-ETL integration into an Amazon
// S3 Tables table bucket, reads its defaulted TargetNamespace, changes its
// RefreshInterval, and is refused a namespace change.
func TestGlueIntegrationConfigCLI(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("glue", "create-integration",
		"--integration-name", "glue-cli-integration-config",
		"--source-arn", "arn:aws:rds:us-east-1:123456789012:db:source",
		"--target-arn", "arn:aws:s3tables:us-east-1:123456789012:bucket/cli-zero-etl",
		"--integration-config", `{"RefreshInterval":"60"}`))
	var created struct {
		IntegrationArn    string `json:"IntegrationArn"`
		IntegrationConfig struct {
			RefreshInterval string `json:"RefreshInterval"`
			TargetNamespace string `json:"TargetNamespace"`
		} `json:"IntegrationConfig"`
	}
	parseJSON(t, out, &created)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-integration", "--integration-identifier", created.IntegrationArn))
	})
	id := created.IntegrationArn[strings.LastIndex(created.IntegrationArn, ":")+1:]
	assert.Equal(t, "zetl_"+strings.ReplaceAll(id, "-", "_"), created.IntegrationConfig.TargetNamespace)

	out = runCLI(t, awsCLI("glue", "modify-integration",
		"--integration-identifier", created.IntegrationArn,
		"--integration-config", `{"RefreshInterval":"120"}`))
	var modified struct {
		IntegrationConfig struct {
			RefreshInterval string `json:"RefreshInterval"`
			TargetNamespace string `json:"TargetNamespace"`
		} `json:"IntegrationConfig"`
	}
	parseJSON(t, out, &modified)
	require.Equal(t, "120", modified.IntegrationConfig.RefreshInterval)
	assert.Equal(t, created.IntegrationConfig.TargetNamespace, modified.IntegrationConfig.TargetNamespace)

	assert.Contains(t, runCLIExpectError(t, awsCLI("glue", "modify-integration",
		"--integration-identifier", created.IntegrationArn,
		"--integration-config", `{"TargetNamespace":"renamed"}`)), "InvalidInputException")
}
