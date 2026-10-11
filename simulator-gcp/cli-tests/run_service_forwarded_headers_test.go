package gcp_cli_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A service deployed with `gcloud run deploy` receives the headers the Cloud
// Run front end adds: the client's IP appended to X-Forwarded-For, the
// client's protocol in X-Forwarded-Proto, and an X-Cloud-Trace-Context; an
// X-Google-* header the client sends never reaches it.
func TestCloudRun_CLI_ServiceReceivesForwardedRequestHeaders(t *testing.T) {
	t.Parallel()
	const service = "cli-svc-xff"
	runCLI(t, gcloudRegionalRunCLI("run", "deploy", service,
		"--region="+location,
		"--no-allow-unauthenticated",
		"--quiet",
		"--image="+httpProbeImageName,
		"--args=echo-headers",
		"--port=8080",
	))
	t.Cleanup(func() {
		runCLI(t, gcloudRegionalRunCLI("run", "services", "delete", service, "--region="+location, "--quiet"))
	})
	var stored struct {
		URI string `json:"uri"`
	}
	parseJSON(t, httpDoJSON(t, "GET", runServiceURL(service), ""), &stored)
	require.NotEmpty(t, stored.URI)
	invoker := cliInvokerEmail(t)
	addServiceInvoker(t, service, "serviceAccount:"+invoker)

	u, err := url.Parse(stored.URI)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+cliIDToken(t, invoker, stored.URI))
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("X-Google-Secret", "spoofed")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%q", body)
	var got http.Header
	require.NoError(t, json.Unmarshal(body, &got), string(body))

	forwardedFor := strings.Split(got.Get("X-Forwarded-For"), ",")
	require.Len(t, forwardedFor, 2, "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.Equal(t, "203.0.113.7", forwardedFor[0])
	client := net.ParseIP(forwardedFor[1])
	require.NotNil(t, client, "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	assert.True(t, client.IsLoopback(), "X-Forwarded-For %q", got.Get("X-Forwarded-For"))
	base, err := url.Parse(baseURL)
	require.NoError(t, err)
	assert.Equal(t, base.Scheme, got.Get("X-Forwarded-Proto"))
	assert.Regexp(t, `^[0-9a-f]{32}/[0-9]+;o=[01]$`, got.Get("X-Cloud-Trace-Context"))
	assert.Empty(t, got.Values("X-Google-Secret"))
}
