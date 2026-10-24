package azure_tf_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformFrontEndForwardedHeaders provisions a Linux web app and a
// container app whose containers echo their request headers, and sends each a
// request carrying an X-Forwarded-For of its own. The App Service front end
// appends the client's address and port and names the client's protocol in
// X-Forwarded-Proto and X-AppService-Proto; Container Apps ingress appends the
// client's IP and overwrites X-Forwarded-Proto.
func TestTerraformFrontEndForwardedHeaders(t *testing.T) {
	dir := tfWorkspaceFrom(t, mustAbs("forwardedheaders"))
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmd(dir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	outputs := readOutputs(t, dir)

	site := frontEndEchoedHeaders(t, outputs.must(t, "app_hostname"))
	forwardedFor := strings.Split(site.Get("X-Forwarded-For"), ", ")
	require.Len(t, forwardedFor, 2, "X-Forwarded-For %q", site.Get("X-Forwarded-For"))
	assert.Equal(t, "203.0.113.7", forwardedFor[0])
	clientIP, clientPort, err := net.SplitHostPort(forwardedFor[1])
	require.NoError(t, err, "the client's address carries its port: %q", forwardedFor[1])
	assert.True(t, net.ParseIP(clientIP).IsLoopback(), "X-Forwarded-For %q", site.Get("X-Forwarded-For"))
	assert.NotEmpty(t, clientPort)
	assert.Equal(t, "http", site.Get("X-Forwarded-Proto"))
	assert.Equal(t, "http", site.Get("X-AppService-Proto"))

	app := frontEndEchoedHeaders(t, outputs.must(t, "container_app_fqdn"))
	forwardedFor = strings.Split(app.Get("X-Forwarded-For"), ", ")
	require.Len(t, forwardedFor, 2, "X-Forwarded-For %q", app.Get("X-Forwarded-For"))
	assert.Equal(t, "203.0.113.7", forwardedFor[0])
	client := net.ParseIP(forwardedFor[1])
	require.NotNil(t, client, "X-Forwarded-For %q", app.Get("X-Forwarded-For"))
	assert.True(t, client.IsLoopback(), "X-Forwarded-For %q", app.Get("X-Forwarded-For"))
	assert.Equal(t, "http", app.Get("X-Forwarded-Proto"))

	// The client certificate settings read back as written, and a request
	// over plain HTTP carries no certificate for either front end to forward.
	assert.Equal(t, "Optional", outputs.must(t, "app_client_certificate_mode"))
	assert.Equal(t, "accept", outputs.must(t, "container_app_client_certificate_mode"))
	assert.Empty(t, site.Get("X-ARR-ClientCert"))
	assert.Empty(t, app.Get("X-Forwarded-Client-Cert"))
}

// frontEndEchoedHeaders sends a request carrying host straight to the
// simulator's front end and parses the header lines the workload echoed.
func frontEndEchoedHeaders(t *testing.T, host string) http.Header {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", simPort), nil)
	require.NoError(t, err)
	req.Host = host
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("X-Forwarded-Proto", "spoofed")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s answered: %s", host, body)
	header, err := textproto.NewReader(bufio.NewReader(strings.NewReader(string(body) + "\r\n"))).ReadMIMEHeader()
	require.NoError(t, err, "parse the echoed headers: %q", body)
	return http.Header(header)
}
