package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDNS_CrossJobResolution_CLI mirrors the SDK cross-job DNS test through
// the gcloud CLI: a VPC network and subnetwork, a private zone visible to the
// network, and two Cloud Run jobs with direct VPC egress onto the
// subnetwork. One job's task holds an address of the subnetwork, the zone's
// A record names it, and the other job resolves the record through the
// resolver its network hands it.
func TestDNS_CrossJobResolution_CLI(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-job DNS test (no fallback): %v", err)
	}

	// 1. The network the jobs egress into, with one subnetwork.
	runCLI(t, gcloudCLI("compute", "networks", "create", "cli-xjob-net", "--subnet-mode=custom"))
	runCLI(t, gcloudCLI("compute", "networks", "subnets", "create", "cli-xjob-net",
		"--network=cli-xjob-net", "--region="+location, "--range=10.85.0.0/24"))

	// 2. The private zone, visible to that network.
	runCLI(t, gcloudCLI("dns", "managed-zones", "create", "cli-xjob-zone",
		"--dns-name=cli-xjob.local.",
		"--description=CLI cross-job DNS test",
		"--visibility=private",
		"--networks=cli-xjob-net",
	))
	defer runCLI(t, gcloudCLI("dns", "managed-zones", "delete", "cli-xjob-zone"))

	out := runCLI(t, gcloudCLI("dns", "managed-zones", "describe", "cli-xjob-zone", "--format=json"))
	var zone struct {
		Id                      string `json:"id"`
		Visibility              string `json:"visibility"`
		PrivateVisibilityConfig struct {
			Networks []struct {
				NetworkUrl string `json:"networkUrl"`
			} `json:"networks"`
		} `json:"privateVisibilityConfig"`
	}
	parseJSON(t, out, &zone)
	require.Equal(t, "private", zone.Visibility)
	require.NotEmpty(t, zone.Id, "the zone must carry an id: %s", out)
	require.Len(t, zone.PrivateVisibilityConfig.Networks, 1, "the zone is visible to the network: %s", out)
	require.Contains(t, zone.PrivateVisibilityConfig.Networks[0].NetworkUrl, "/global/networks/cli-xjob-net")

	// 3. Two Cloud Run jobs with direct VPC egress, created and run over the
	// v2 REST surface (gcloud run jobs create against the simulator's v2
	// endpoint is not reliably supported; the SDK/REST path is the gcloud
	// back-door).
	createJob := func(name string, args []string) string {
		argsJSON, err := json.Marshal(args)
		require.NoError(t, err)
		body := `{
			"template":{"template":{
				"containers":[{"image":"` + commandImageName + `","args":` + string(argsJSON) + `}],
				"timeout":"60s",
				"vpcAccess":{"networkInterfaces":[{"network":"cli-xjob-net","subnetwork":"cli-xjob-net"}],"egress":"PRIVATE_RANGES_ONLY"}
			}}
		}`
		createURL := fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs?jobId=%s",
			baseURL, project, location, name)
		_ = httpDoJSON(t, "POST", createURL, body)

		// The workload keeps running while the test wires DNS around it, so
		// the execution comes from the RunJob operation's Execution metadata.
		return runJob(t, name).Execution
	}

	alphaExec := createJob("cli-alpha", []string{"resolve", "beta.cli-xjob.local", "30", "gcp-cli-cross-job-dns-ok", "10"})
	betaExec := createJob("cli-beta", []string{"sleep", "60"})

	alphaContainer := jobContainerName(alphaExec)
	betaContainer := jobContainerName(betaExec)

	require.Eventually(t, func() bool {
		for _, n := range []string{alphaContainer, betaContainer} {
			if err := exec.Command("docker", "inspect", n).Run(); err != nil {
				return false
			}
		}
		return true
	}, 30*time.Second, 300*time.Millisecond, "Docker containers should be running (alpha=%s, beta=%s)", alphaContainer, betaContainer)

	// The tasks' containers hold addresses of the subnetwork, which the test
	// reads off the engine: Cloud Run reports no address for a task.
	subnet := netip.MustParsePrefix("10.85.0.0/24")
	alphaIP := containerIP(t, alphaContainer)
	betaIP := containerIP(t, betaContainer)
	for _, ip := range []string{alphaIP, betaIP} {
		require.True(t, subnet.Contains(netip.MustParseAddr(ip)), "%s is not an address of the subnetwork the jobs egress into", ip)
	}

	// 4. The A record naming beta (direct REST — gcloud record-sets create
	// has inconsistent endpoint-override handling).
	rrURL := fmt.Sprintf("%s/dns/v1/projects/%s/managedZones/cli-xjob-zone/rrsets", baseURL, project)
	httpDoJSON(t, "POST", rrURL, fmt.Sprintf(
		`{"name":"beta.cli-xjob.local.","type":"A","ttl":60,"rrdatas":[%q]}`, betaIP))
	// Deferred after the zone's delete, so the record goes first: Cloud DNS
	// deletes a zone only once its own records are gone.
	defer runCLI(t, gcloudCLI("dns", "record-sets", "delete", "beta.cli-xjob.local.", "--zone=cli-xjob-zone", "--type=A"))

	// 5. alpha resolves beta's record through its network's resolver.
	var logs []byte
	require.Eventually(t, func() bool {
		var err error
		logs, err = exec.Command("docker", "logs", alphaContainer).CombinedOutput()
		return err == nil && strings.Contains(string(logs), "gcp-cli-cross-job-dns-ok")
	}, 30*time.Second, 500*time.Millisecond, "alpha should resolve beta.cli-xjob.local through the private zone: %s", logs)
	assert.Contains(t, string(logs), "gcp-cli-cross-job-dns-ok")
}

// jobContainerName + containerIP mirror the SDK-test helpers.
func jobContainerName(executionName string) string {
	last := executionName
	if idx := strings.LastIndex(executionName, "/"); idx >= 0 {
		last = executionName[idx+1:]
	}
	if len(last) > 12 {
		last = last[:12]
	}
	return "sockerless-sim-gcp-job-" + last + "-0-0"
}

func containerIP(t *testing.T, name string) string {
	t.Helper()
	for _, net := range inspectContainerNetworks(t, name) {
		if net.IPAddress != "" {
			return net.IPAddress
		}
	}
	return ""
}

type dockerContainerNetwork struct {
	IPAddress string `json:"IPAddress"`
}

func inspectContainerNetworks(t *testing.T, name string) map[string]dockerContainerNetwork {
	t.Helper()
	out, err := exec.Command("docker", "inspect", name).Output()
	require.NoError(t, err)
	var inspected []struct {
		NetworkSettings struct {
			Networks map[string]dockerContainerNetwork `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	require.NoError(t, json.Unmarshal(out, &inspected))
	require.NotEmpty(t, inspected)
	return inspected[0].NetworkSettings.Networks
}
