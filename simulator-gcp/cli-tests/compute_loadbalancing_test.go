package gcp_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGcloudComputeGlobalHTTPLoadBalancer(t *testing.T) {
	hc := "cli-lb-hc"
	backend := "cli-lb-backend"
	urlMap := "cli-lb-url-map"
	proxy := "cli-lb-http-proxy"
	rule := "cli-lb-rule"

	out, err := gcloudCLI("compute", "health-checks", "create", "http", hc,
		"--check-interval=5s",
		"--timeout=5s",
		"--request-path=/healthz",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "health-checks create: %s", out)

	out, err = gcloudCLI("compute", "backend-services", "create", backend,
		"--global",
		"--protocol=HTTP",
		"--port-name=http",
		"--health-checks="+hc,
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "backend-services create: %s", out)

	out, err = gcloudCLI("compute", "url-maps", "create", urlMap,
		"--default-service="+backend,
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "url-maps create: %s", out)

	out, err = gcloudCLI("compute", "target-http-proxies", "create", proxy,
		"--url-map="+urlMap,
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "target-http-proxies create: %s", out)

	out, err = gcloudCLI("compute", "forwarding-rules", "create", rule,
		"--global",
		"--target-http-proxy="+proxy,
		"--ports=80",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "forwarding-rules create: %s", out)

	// The chain is only a load balancer if each link points at the previous
	// one. The forwarding rule has to carry the port the CLI was given and
	// address the proxy by name, and the URL map has to default to the backend
	// service — a chain built out of five resources that never reference each
	// other would create just as cleanly.
	out, err = gcloudCLI("compute", "forwarding-rules", "describe", rule,
		"--global",
		"--format=json").CombinedOutput()
	require.NoError(t, err, "forwarding-rules describe: %s", out)
	var forwardingRule struct {
		Name      string `json:"name"`
		PortRange string `json:"portRange"`
		Target    string `json:"target"`
	}
	parseJSONObject(t, string(out), &forwardingRule)
	require.Equal(t, rule, forwardingRule.Name)
	// gcloud renders --ports=80 as the single-port range "80"; a range whose
	// ends differ prints as "start-end".
	require.Equal(t, "80", forwardingRule.PortRange)
	require.True(t, strings.HasSuffix(forwardingRule.Target, "/targetHttpProxies/"+proxy),
		"the rule must target the proxy: %q", forwardingRule.Target)

	out, err = gcloudCLI("compute", "url-maps", "describe", urlMap, "--format=json").CombinedOutput()
	require.NoError(t, err, "url-maps describe: %s", out)
	var mapped struct {
		Name           string `json:"name"`
		DefaultService string `json:"defaultService"`
	}
	parseJSONObject(t, string(out), &mapped)
	require.Equal(t, urlMap, mapped.Name)
	require.True(t, strings.HasSuffix(mapped.DefaultService, "/backendServices/"+backend),
		"the URL map must default to the backend service: %q", mapped.DefaultService)

	out, err = gcloudCLI("compute", "forwarding-rules", "delete", rule, "--global", "--quiet").CombinedOutput()
	require.NoError(t, err, "forwarding-rules delete: %s", out)
	out, err = gcloudCLI("compute", "target-http-proxies", "delete", proxy, "--quiet").CombinedOutput()
	require.NoError(t, err, "target-http-proxies delete: %s", out)
	out, err = gcloudCLI("compute", "url-maps", "delete", urlMap, "--quiet").CombinedOutput()
	require.NoError(t, err, "url-maps delete: %s", out)
	out, err = gcloudCLI("compute", "backend-services", "delete", backend, "--global", "--quiet").CombinedOutput()
	require.NoError(t, err, "backend-services delete: %s", out)
	out, err = gcloudCLI("compute", "health-checks", "delete", hc, "--quiet").CombinedOutput()
	require.NoError(t, err, "health-checks delete: %s", out)
}
