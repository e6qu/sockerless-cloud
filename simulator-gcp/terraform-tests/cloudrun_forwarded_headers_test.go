package gcp_tf_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2ServiceForwardedHeaders deploys a service with
// google_cloud_run_v2_service whose container echoes its request headers: the
// Cloud Run front end appends the client's IP to X-Forwarded-For, names the
// client's protocol in X-Forwarded-Proto, adds an X-Cloud-Trace-Context, and
// removes the X-Google-* headers the client sent.
func TestTerraformCloudRunV2ServiceForwardedHeaders(t *testing.T) {
	image := buildProbeImage(t)
	fixtureDir := filepath.Join("fixtures", "cloudrun-forwarded-headers")
	cleanTerraformFixture(t, fixtureDir)

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	apply := terraformCmdInDir(fixtureDir, "apply", "-auto-approve")
	apply.Env = append(apply.Env, "TF_VAR_image="+image)
	t.Cleanup(func() {
		destroy := terraformCmdInDir(fixtureDir, "destroy", "-auto-approve")
		destroy.Env = append(destroy.Env, "TF_VAR_image="+image)
		out, err := runTimed(t, "terraform destroy", destroy)
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply", apply)
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	u, err := url.Parse(readOutputsInDir(t, fixtureDir).must(t, "uri"))
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	req.Host = u.Host
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
