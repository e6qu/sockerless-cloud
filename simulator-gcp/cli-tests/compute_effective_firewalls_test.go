package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// `gcloud compute instances network-interfaces get-effective-firewalls` and
// `gcloud compute networks get-effective-firewalls` read the VPC firewall rules
// and the firewall policies in force: the instance's read keeps only the rules
// that target it, the network's keeps every rule on the network.
func TestGcloudComputeEffectiveFirewalls(t *testing.T) {
	t.Parallel()
	const zone, region = "us-central1-a", "us-central1"
	const network, instance = "cli-eff-net", "cli-eff-vm"

	runCLI(t, gcloudCLI("compute", "networks", "create", network, "--subnet-mode=custom", "--format=value(name)"))
	runCLI(t, gcloudCLI("compute", "networks", "subnets", "create", network,
		"--network="+network, "--region="+region, "--range=10.86.0.0/24", "--format=value(name)"))
	for _, rule := range [][]string{
		{"cli-eff-all", "--allow=tcp:22"},
		{"cli-eff-web", "--allow=tcp:80", "--target-tags=web"},
		{"cli-eff-db", "--allow=tcp:5432", "--target-tags=db"},
	} {
		args := append([]string{"compute", "firewall-rules", "create", rule[0],
			"--network=" + network, "--source-ranges=10.0.0.0/8", "--format=value(name)"}, rule[1:]...)
		runCLI(t, gcloudCLI(args...))
	}

	runCLI(t, gcloudCLI("compute", "network-firewall-policies", "create", "cli-eff-policy", "--global", "--format=value(name)"))
	runCLI(t, gcloudCLI("compute", "network-firewall-policies", "rules", "create", "100",
		"--firewall-policy=cli-eff-policy", "--global-firewall-policy",
		"--action=allow", "--direction=INGRESS", "--src-ip-ranges=10.0.0.0/8", "--layer4-configs=tcp:443"))
	runCLI(t, gcloudCLI("compute", "network-firewall-policies", "associations", "create",
		"--firewall-policy=cli-eff-policy", "--global-firewall-policy",
		"--network="+network, "--name=cli-eff-assoc"))

	runCLI(t, gcloudCLI("compute", "instances", "create", instance,
		"--zone="+zone, "--machine-type=e2-micro",
		"--image-family="+debianFamily(), "--image-project=debian-cloud",
		"--subnet="+network, "--tags=web", "--no-address", "--format=value(name)"))
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "instances", "delete", instance, "--zone="+zone, "--quiet").CombinedOutput()
	})

	var onInstance struct {
		Firewalls []struct {
			Name string `json:"name"`
		} `json:"firewalls"`
		FirewallPolicys []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Rules []struct {
				Priority int `json:"priority"`
			} `json:"rules"`
		} `json:"firewallPolicys"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("compute", "instances", "network-interfaces", "get-effective-firewalls",
		instance, "--zone="+zone, "--network-interface=nic0", "--format=json")), &onInstance)
	names := []string{}
	for _, fw := range onInstance.Firewalls {
		names = append(names, fw.Name)
	}
	require.ElementsMatch(t, []string{"cli-eff-all", "cli-eff-web"}, names,
		"the instance carries the web tag and not the db one")
	require.Len(t, onInstance.FirewallPolicys, 1)
	require.Equal(t, "cli-eff-policy", onInstance.FirewallPolicys[0].Name)
	require.Equal(t, "NETWORK", onInstance.FirewallPolicys[0].Type)
	require.Len(t, onInstance.FirewallPolicys[0].Rules, 1)
	require.Equal(t, 100, onInstance.FirewallPolicys[0].Rules[0].Priority)

	var onNetwork struct {
		Firewalls []struct {
			Name string `json:"name"`
		} `json:"firewalls"`
		FirewallPolicys []struct {
			Name string `json:"name"`
		} `json:"firewallPolicys"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("compute", "networks", "get-effective-firewalls", network, "--format=json")), &onNetwork)
	names = []string{}
	for _, fw := range onNetwork.Firewalls {
		names = append(names, fw.Name)
	}
	require.ElementsMatch(t, []string{"cli-eff-all", "cli-eff-web", "cli-eff-db"}, names)
	require.Len(t, onNetwork.FirewallPolicys, 1)
	require.Equal(t, "cli-eff-policy", onNetwork.FirewallPolicys[0].Name)
}
