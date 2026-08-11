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

	out, err = gcloudCLI("compute", "forwarding-rules", "describe", rule,
		"--global",
		"--format=value(name,portRange,target)").CombinedOutput()
	require.NoError(t, err, "forwarding-rules describe: %s", out)
	body := string(out)
	require.Contains(t, body, rule)
	require.Contains(t, strings.TrimSpace(body), "80")

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
