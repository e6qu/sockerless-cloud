package gcp_cli_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud run deploy --depends-on --startup-probe` carries the container
// dependencies in the revision template's Knative container-dependencies
// annotation. The ingress container checks once, as it starts, that the
// sidecar listens and exits if not, and the sidecar opens its port three
// seconds after it starts, so the service answers only when its instance
// starts the ingress after the sidecar passed its startup probe.
func TestCloudRun_CLI_ServiceContainerDependsOnStartupProbe(t *testing.T) {
	const service = "cli-svc-depends-on"
	runCLI(t, gcloudRegionalRunCLI("run", "deploy", service,
		"--region="+location,
		"--no-allow-unauthenticated",
		"--quiet",
		"--container=ingress",
		"--image="+httpProbeImageName,
		"--args=after-sidecar,cli-sidecar-started-first",
		"--port=8080",
		"--depends-on=sidecar",
		"--container=sidecar",
		"--image="+commandImageName,
		"--args=http,9090,ready,3",
		"--startup-probe=tcpSocket.port=9090,periodSeconds=1,timeoutSeconds=1,failureThreshold=30",
	))
	t.Cleanup(func() {
		runCLI(t, gcloudRegionalRunCLI("run", "services", "delete", service, "--region="+location, "--quiet"))
	})

	var stored struct {
		URI      string `json:"uri"`
		Template struct {
			Containers []struct {
				Name         string   `json:"name"`
				DependsOn    []string `json:"dependsOn"`
				StartupProbe *struct {
					TCPSocket struct {
						Port int `json:"port"`
					} `json:"tcpSocket"`
				} `json:"startupProbe"`
			} `json:"containers"`
		} `json:"template"`
	}
	parseJSON(t, httpDoJSON(t, "GET", runServiceURL(service), ""), &stored)
	require.Len(t, stored.Template.Containers, 2)
	assert.Equal(t, []string{"sidecar"}, stored.Template.Containers[0].DependsOn)
	require.NotNil(t, stored.Template.Containers[1].StartupProbe)
	assert.Equal(t, 9090, stored.Template.Containers[1].StartupProbe.TCPSocket.Port)

	described := runCLI(t, gcloudRegionalRunCLI("run", "services", "describe", service, "--region="+location, "--format=json"))
	assert.Contains(t, described, "run.googleapis.com/container-dependencies")

	invoker := cliInvokerEmail(t)
	addServiceInvoker(t, service, "serviceAccount:"+invoker)
	status, body := requestService(t, stored.URI, "/", cliIDToken(t, invoker, stored.URI))
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "cli-sidecar-started-first", strings.TrimSpace(body))
}
