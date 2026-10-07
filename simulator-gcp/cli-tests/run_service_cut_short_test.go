package gcp_cli_test

import (
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A service deployed with `gcloud run deploy` whose container closes its
// connection in the middle of a chunked body: Cloud Run has already relayed
// the container's status and headers, so it ends the caller's response where
// the container stopped rather than appending an error page.
func TestCloudRun_CLI_ServiceAbortsAResponseTheContainerCutsShort(t *testing.T) {
	const service = "cli-svc-cut-short"
	runCLI(t, gcloudRegionalRunCLI("run", "deploy", service,
		"--region="+location,
		"--no-allow-unauthenticated",
		"--quiet",
		"--image="+httpProbeImageName,
		"--args=cut-short",
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
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the container's status reached the caller")
	body, err := io.ReadAll(resp.Body)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the response ends where the container stopped")
	assert.Equal(t, "partial", string(body), "nothing follows what the container sent")
}
