package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCLI_ECR_CreatePullThroughCache drives the official AWS CLI
// against the simulator. Together with the SDK test, it exercises
// both real-world client paths for the pull-through cache rule API.
func TestCLI_ECR_CreatePullThroughCache(t *testing.T) {
	out := runCLI(t, awsCLI(
		"ecr", "create-pull-through-cache-rule",
		"--ecr-repository-prefix", "cli-docker-hub",
		"--upstream-registry-url", "registry-1.docker.io",
		"--upstream-registry", "docker-hub",
	))
	var resp struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		UpstreamRegistryUrl string `json:"upstreamRegistryUrl"`
	}
	parseJSON(t, out, &resp)
	assert.Equal(t, "cli-docker-hub", resp.EcrRepositoryPrefix)
	assert.Equal(t, "registry-1.docker.io", resp.UpstreamRegistryUrl)
}

// TestCLI_ECR_DescribePullThroughCache exercises the describe path
// and confirms listing-all returns the rule the test created.
func TestCLI_ECR_DescribePullThroughCache(t *testing.T) {
	runCLI(t, awsCLI(
		"ecr", "create-pull-through-cache-rule",
		"--ecr-repository-prefix", "cli-describe",
		"--upstream-registry-url", "registry-1.docker.io",
		"--upstream-registry", "docker-hub",
	))
	out := runCLI(t, awsCLI("ecr", "describe-pull-through-cache-rules"))
	var resp struct {
		PullThroughCacheRules []struct {
			EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
		} `json:"pullThroughCacheRules"`
	}
	parseJSON(t, out, &resp)
	require.NotEmpty(t, resp.PullThroughCacheRules)
	var found bool
	for _, r := range resp.PullThroughCacheRules {
		if r.EcrRepositoryPrefix == "cli-describe" {
			found = true
			break
		}
	}
	assert.True(t, found, "describe should list the cli-describe prefix")
}

// TestCLI_ECR_DeletePullThroughCache deletes a registered rule and
// confirms a second delete returns the not-found error.
func TestCLI_ECR_DeletePullThroughCache(t *testing.T) {
	runCLI(t, awsCLI(
		"ecr", "create-pull-through-cache-rule",
		"--ecr-repository-prefix", "cli-delete",
		"--upstream-registry-url", "registry-1.docker.io",
		"--upstream-registry", "docker-hub",
	))
	out := runCLI(t, awsCLI(
		"ecr", "delete-pull-through-cache-rule",
		"--ecr-repository-prefix", "cli-delete",
	))
	var resp struct {
		EcrRepositoryPrefix string `json:"ecrRepositoryPrefix"`
	}
	parseJSON(t, out, &resp)
	assert.Equal(t, "cli-delete", resp.EcrRepositoryPrefix)

	// Second delete: expect failure with PullThroughCacheRuleNotFound.
	cmd := awsCLI("ecr", "delete-pull-through-cache-rule", "--ecr-repository-prefix", "cli-delete")
	combined, err := cmd.CombinedOutput()
	require.Error(t, err, "expected CLI failure on second delete")
	assert.True(t, strings.Contains(string(combined), "PullThroughCacheRuleNotFound"),
		"expected PullThroughCacheRuleNotFound in CLI output, got: %s", string(combined))
}
