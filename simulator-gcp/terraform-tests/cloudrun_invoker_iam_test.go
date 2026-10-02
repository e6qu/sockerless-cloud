package gcp_tf_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2ServiceInvokerIAM grants roles/run.invoker on a
// private service with google_cloud_run_v2_service_iam_member and invokes the
// service with the ID tokens google_service_account_id_token mints: the
// granted service account reaches the container, one holding no role is
// refused, and removing the member refuses the granted one too.
func TestTerraformCloudRunV2ServiceInvokerIAM(t *testing.T) {
	image := buildProbeImage(t)
	fixtureDir := filepath.Join("fixtures", "cloudrun-invoker-iam")
	cleanTerraformFixture(t, fixtureDir)

	withVars := func(cmd *exec.Cmd, grant bool) *exec.Cmd {
		cmd.Env = append(cmd.Env, "TF_VAR_image="+image)
		if !grant {
			cmd.Env = append(cmd.Env, "TF_VAR_grant_invoker=false")
		}
		return cmd
	}
	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", withVars(terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"), true))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply", withVars(terraformCmdInDir(fixtureDir, "apply", "-auto-approve"), true))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	uri := outputs.must(t, "uri")
	serviceID := outputs.must(t, "service_name")
	member := outputs.must(t, "invoker_member")
	assert.Contains(t, serviceInvokers(t, serviceID), member, "the service policy must carry the member terraform granted")

	status, body := invokeWithIDToken(t, uri, "/granted", outputs.must(t, "invoker_id_token"))
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "GET /granted", body)

	status, body = invokeWithIDToken(t, uri, "/", outputs.must(t, "bystander_id_token"))
	assert.Equal(t, http.StatusForbidden, status, "body=%q", body)
	assert.Contains(t, body, "Your client does not have permission to get URL <code>/</code> from this server.")

	out, err = runTimed(t, "terraform apply", withVars(terraformCmdInDir(fixtureDir, "apply", "-auto-approve"), false))
	require.NoError(t, err, "terraform apply without the member failed:\n%s", out)
	outputs = readOutputsInDir(t, fixtureDir)
	assert.NotContains(t, serviceInvokers(t, serviceID), member, "removing the iam_member must remove the binding")
	status, body = invokeWithIDToken(t, uri, "/", outputs.must(t, "invoker_id_token"))
	assert.Equal(t, http.StatusForbidden, status, "body=%q", body)
}

// serviceInvokers reads the members bound to roles/run.invoker on the service
// through the Cloud Run Admin API v2 getIamPolicy method.
func serviceInvokers(t *testing.T, serviceID string) []string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		baseURL+"/v2/projects/test-project/locations/us-central1/services/"+serviceID+":getIamPolicy", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var policy struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&policy))
	var members []string
	for _, binding := range policy.Bindings {
		if binding.Role == "roles/run.invoker" {
			members = append(members, binding.Members...)
		}
	}
	return slices.Compact(members)
}

// invokeWithIDToken sends a GET to the service's URL presenting an ID token.
func invokeWithIDToken(t *testing.T, uri, path, idToken string) (int, string) {
	t.Helper()
	u, err := url.Parse(uri)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(u.Host, ".a.run.app"), "the service is served on run.app: %s", uri)
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	require.NoError(t, err)
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+idToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}
