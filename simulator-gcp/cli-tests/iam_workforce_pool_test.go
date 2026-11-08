package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// `gcloud iam workforce-pools create` without --session-duration leaves the
// pool at IAM's one-hour default, which describe reads back.
func TestGcloudIAMWorkforcePoolDefaultSessionDuration(t *testing.T) {
	t.Parallel()
	runCLI(t, gcloudCLI("iam", "workforce-pools", "create", "cli-session-pool",
		"--organization="+cliOrganizationID, "--location=global", "--display-name=CLI pool"))
	t.Cleanup(func() {
		_, _ = gcloudCLI("iam", "workforce-pools", "delete", "cli-session-pool", "--location=global", "--quiet").CombinedOutput()
	})
	var pool struct {
		SessionDuration string `json:"sessionDuration"`
		State           string `json:"state"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("iam", "workforce-pools", "describe", "cli-session-pool",
		"--location=global", "--format=json")), &pool)
	assert.Equal(t, "ACTIVE", pool.State)
	assert.Equal(t, "3600s", pool.SessionDuration)
}
