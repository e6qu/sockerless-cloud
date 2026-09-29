package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDNS_CrossJobResolution_CLI mirrors the SDK cross-job DNS test
// through the gcloud CLI. Private zone + two Cloud Run Jobs + A records
// pointing at the jobs' Docker IPs — one job resolves the other by
// short hostname via Docker's embedded DNS on the zone's backing
// network.
func TestDNS_CrossJobResolution_CLI(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-job DNS test (no fallback): %v", err)
	}

	// 1. Create the private zone via gcloud.
	runCLI(t, gcloudCLI("dns", "managed-zones", "create", "cli-xjob-zone",
		"--dns-name=cli-xjob.local.",
		"--description=CLI cross-job DNS test",
		"--visibility=private",
		"--networks=",
	))
	defer runCLI(t, gcloudCLI("dns", "managed-zones", "delete", "cli-xjob-zone"))

	// A private zone is backed by a real Docker user-defined network named for
	// the zone's id, and that network is what carries the resolution below —
	// so it is verified to exist rather than assumed.
	out := runCLI(t, gcloudCLI("dns", "managed-zones", "describe", "cli-xjob-zone", "--format=json"))
	var zone struct {
		Id         string `json:"id"`
		Visibility string `json:"visibility"`
	}
	parseJSON(t, out, &zone)
	require.Equal(t, "private", zone.Visibility)
	require.NotEmpty(t, zone.Id, "the zone must carry an id: %s", out)
	zoneNetwork := "sim-" + zone.Id
	require.NoError(t, exec.Command("docker", "network", "inspect", zoneNetwork).Run(),
		"the private zone must be backed by the Docker network %q", zoneNetwork)

	// 2. Create + run two Cloud Run Jobs via direct HTTP (gcloud run
	// jobs create against the simulator's v2 endpoint is not reliably
	// supported; the SDK/REST path is the gcloud back-door).
	createJob := func(name string, args []string) string {
		argsJSON, err := json.Marshal(args)
		require.NoError(t, err)
		body := `{
			"template":{"template":{
				"containers":[{"image":"` + commandImageName + `","args":` + string(argsJSON) + `}],
				"timeout":"60s"
			}}
		}`
		createURL := fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs?jobId=%s",
			baseURL, project, location, name)
		_ = httpDoJSON(t, "POST", createURL, body)

		runURL := fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs/%s:run",
			baseURL, project, location, name)
		runOut := httpDoJSON(t, "POST", runURL, "{}")
		// The response is an LRO with an embedded Execution whose
		// `name` is projects/.../executions/<execID>.
		var op struct {
			Response struct {
				Name string `json:"name"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(runOut), &op); err == nil && op.Response.Name != "" {
			return op.Response.Name
		}
		// Fallback: RunJob returns the Execution directly on success.
		var execResp struct {
			Name string `json:"name"`
		}
		parseJSON(t, runOut, &execResp)
		return execResp.Name
	}

	alphaExec := createJob("cli-alpha", []string{"resolve", "beta", "30", "gcp-cli-cross-job-dns-ok", "10"})
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

	alphaIP := containerIP(t, alphaContainer)
	betaIP := containerIP(t, betaContainer)
	require.NotEmpty(t, alphaIP)
	require.NotEmpty(t, betaIP)

	// 3. Create A records (direct REST — gcloud record-sets create has
	// inconsistent endpoint-override handling).
	rrURL := fmt.Sprintf("%s/dns/v1/projects/%s/managedZones/cli-xjob-zone/rrsets", baseURL, project)
	httpDoJSON(t, "POST", rrURL, fmt.Sprintf(
		`{"name":"alpha.cli-xjob.local.","type":"A","ttl":60,"rrdatas":[%q]}`, alphaIP))
	httpDoJSON(t, "POST", rrURL, fmt.Sprintf(
		`{"name":"beta.cli-xjob.local.","type":"A","ttl":60,"rrdatas":[%q]}`, betaIP))

	// The A records attach their containers to the zone's Docker network under
	// the record's short name — the mechanism the resolution below rides on.
	for _, name := range []string{alphaContainer, betaContainer} {
		require.Contains(t, containerNetworks(t, name), zoneNetwork,
			"%s must be attached to the private zone's Docker network", name)
	}

	// 4. Cross-job DNS: alpha resolves "beta" through its own resolver
	// once the private-zone A records have attached both containers to
	// the zone's backing Docker network.
	var logs []byte
	require.Eventually(t, func() bool {
		var err error
		logs, err = exec.Command("docker", "logs", alphaContainer).CombinedOutput()
		return err == nil && strings.Contains(string(logs), "gcp-cli-cross-job-dns-ok")
	}, 30*time.Second, 500*time.Millisecond, "alpha should resolve 'beta' via private zone: %s", logs)
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
	return "sockerless-sim-gcp-job-" + last
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

// containerNetworks returns the names of the Docker networks a container is
// attached to.
func containerNetworks(t *testing.T, name string) []string {
	t.Helper()
	var names []string
	for netName := range inspectContainerNetworks(t, name) {
		names = append(names, netName)
	}
	return names
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
