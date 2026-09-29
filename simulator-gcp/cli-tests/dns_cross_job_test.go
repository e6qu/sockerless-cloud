package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDNS_CrossJobResolution_CLI mirrors the SDK cross-job DNS test
// through the gcloud CLI. Private zone + two Cloud Run Jobs + A records
// pointing at the addresses the jobs log — one job resolves the other by
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
	createJob := func(name, script string) string {
		argsJSON, err := json.Marshal([]string{script})
		require.NoError(t, err)
		body := `{
			"template":{"template":{
				"containers":[{"image":"` + cliWorkloadImage + `","command":["sh","-c"],"args":` + string(argsJSON) + `}],
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

	// Each job reports its own address to Cloud Logging, where the test reads
	// it the way any client of the service would.
	const reportAddress = `echo "address=$(hostname -i | cut -d' ' -f1)"; `
	alphaExec := createJob("cli-alpha", reportAddress+
		`for i in $(seq 1 300); do `+
		`if nslookup beta >/dev/null 2>&1; then echo gcp-cli-cross-job-dns-ok; exit 0; fi; `+
		`sleep 0.1; done; echo "beta never resolved" >&2; exit 1`)
	betaExec := createJob("cli-beta", reportAddress+`sleep 60`)

	alphaContainer := jobContainerName(alphaExec)
	betaContainer := jobContainerName(betaExec)

	alphaIP := cliJobLogLine(t, "cli-alpha", "address=")
	betaIP := cliJobLogLine(t, "cli-beta", "address=")
	require.NotNil(t, net.ParseIP(alphaIP), "cli-alpha reported address %q", alphaIP)
	require.NotNil(t, net.ParseIP(betaIP), "cli-beta reported address %q", betaIP)

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
	cliJobLogLine(t, "cli-alpha", "gcp-cli-cross-job-dns-ok")
}

// cliJobLogLine reads the Cloud Run job's Cloud Logging entries with
// `gcloud logging read` until one starts with prefix, and returns the rest of
// that line. gcloud offers no stream to wait on, so the read repeats.
func cliJobLogLine(t *testing.T, job, prefix string) string {
	t.Helper()
	var rest string
	require.Eventually(t, func() bool {
		out := runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_job" AND resource.labels.job_name="`+job+`"`,
			"--format=value(textPayload)"))
		for _, line := range strings.Split(out, "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
				rest = value
				return true
			}
		}
		return false
	}, 60*time.Second, 250*time.Millisecond, "job %s never logged a line starting %q", job, prefix)
	return rest
}

// jobContainerName derives the container the simulator runs an execution in.
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
