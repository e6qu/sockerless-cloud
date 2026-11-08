package gcp_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud compute network-firewall-policies clone-rules` replaces a policy's
// rules with another policy's.
func TestGcloudComputeNetworkFirewallPolicyCloneRules(t *testing.T) {
	t.Parallel()
	runCLI(t, gcloudCLI("compute", "network-firewall-policies", "create", "cli-clone-source", "--global", "--format=value(name)"))
	runCLI(t, gcloudCLI("compute", "network-firewall-policies", "rules", "create", "150",
		"--firewall-policy=cli-clone-source", "--global-firewall-policy",
		"--action=deny", "--direction=INGRESS", "--src-ip-ranges=10.0.0.0/8", "--layer4-configs=tcp:22"))
	runCLI(t, gcloudCLI("compute", "network-firewall-policies", "create", "cli-clone-target", "--global", "--format=value(name)"))

	runCLI(t, gcloudCLI("compute", "network-firewall-policies", "clone-rules", "cli-clone-target",
		"--source-firewall-policy=cli-clone-source", "--global"))

	// gcloud describes a network firewall policy as a one-element list.
	var described []struct {
		Rules []struct {
			Priority int    `json:"priority"`
			Action   string `json:"action"`
		} `json:"rules"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("compute", "network-firewall-policies", "describe", "cli-clone-target",
		"--global", "--format=json")), &described)
	require.Len(t, described, 1)
	target := described[0]
	require.Len(t, target.Rules, 1)
	assert.Equal(t, 150, target.Rules[0].Priority)
	assert.Equal(t, "deny", target.Rules[0].Action)
}

// `gcloud compute firewall-policies create` names an organization firewall
// policy by its short name; Compute Engine allocates the numeric name and
// records the folder as the policy's parent.
func TestGcloudComputeOrganizationFirewallPolicyCreate(t *testing.T) {
	t.Parallel()
	const folder = "771177"
	runCLI(t, gcloudCLI("compute", "firewall-policies", "create", "--short-name=cli-org-policy",
		"--folder="+folder, "--description=edge"))

	var listed []struct {
		Name      string `json:"name"`
		ShortName string `json:"shortName"`
		Parent    string `json:"parent"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("compute", "firewall-policies", "list", "--folder="+folder, "--format=json")), &listed)
	require.Len(t, listed, 1)
	assert.Equal(t, "cli-org-policy", listed[0].ShortName)
	assert.Equal(t, "folders/"+folder, listed[0].Parent)
	require.NotEmpty(t, listed[0].Name)
	assert.Empty(t, strings.Trim(listed[0].Name, "0123456789"), "the policy's name is a numeric ID")
}
