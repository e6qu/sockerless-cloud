package gcp_cli_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcloudRegionalCLI is gcloudCLI for a `gcloud run services` command that
// resolves the regional Cloud Run host. gcloud prefixes the region onto the
// configured endpoint's host, `us-central1-<host>`, as it does for the real
// `us-central1-run.googleapis.com`; gcloud's own HTTP proxy setting delivers
// that request to the simulator.
func gcloudRegionalCLI(args ...string) *exec.Cmd {
	cmd := gcloudCLI(args...)
	cmd.Env = append(cmd.Env, "HTTP_PROXY="+baseURL, "NO_PROXY=")
	return cmd
}

// cliInvokerAccountID names the service account the tests grant
// roles/run.invoker to and invoke services as.
const cliInvokerAccountID = "cli-run-invoker"

var (
	cliInvokerOnce sync.Once
	cliInvokerOut  []byte
	cliInvokerErr  error
)

func cliInvokerEmail(t *testing.T) string {
	t.Helper()
	cliInvokerOnce.Do(func() {
		cliInvokerOut, cliInvokerErr = gcloudCLI("iam", "service-accounts", "create", cliInvokerAccountID,
			"--project="+project, "--format=json").CombinedOutput()
	})
	require.NoError(t, cliInvokerErr, "create the invoker service account: %s", cliInvokerOut)
	return fmt.Sprintf("%s@%s.iam.gserviceaccount.com", cliInvokerAccountID, project)
}

// cliIDToken is the ID token `gcloud auth print-identity-token` mints for a
// service account it impersonates, for the service URL it is presented to.
func cliIDToken(t *testing.T, email, audience string) string {
	t.Helper()
	token := lastLine(runCLI(t, gcloudCLI("auth", "print-identity-token",
		"--impersonate-service-account="+email, "--audiences="+audience, "--include-email")))
	require.NotEmpty(t, token, "gcloud printed no identity token")
	return token
}

func addServiceInvoker(t *testing.T, serviceID, member string) {
	t.Helper()
	runCLI(t, gcloudRegionalCLI("run", "services", "add-iam-policy-binding", serviceID,
		"--region="+location, "--member="+member, "--role=roles/run.invoker", "--format=json"))
}

func removeServiceInvoker(t *testing.T, serviceID, member string) {
	t.Helper()
	runCLI(t, gcloudRegionalCLI("run", "services", "remove-iam-policy-binding", serviceID,
		"--region="+location, "--member="+member, "--role=roles/run.invoker", "--format=json"))
}

// createEchoService creates a service that answers with the request's method
// and path, deletes it when the test ends, and returns its URL.
func createEchoService(t *testing.T, id, containerExtra string) string {
	t.Helper()
	body := fmt.Sprintf(`{"template":{"containers":[{"image":%q,"args":["echo-request"]%s}]}}`, httpProbeImageName, containerExtra)
	out := httpDoJSON(t, "POST", servicesBaseURL()+"?serviceId="+id, body)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", runServiceURL(id), "")
		if err == nil {
			resp.Body.Close()
		}
	})
	var lro struct {
		Response struct {
			URI string `json:"uri"`
		} `json:"response"`
	}
	parseJSON(t, out, &lro)
	require.NotEmpty(t, lro.Response.URI)
	return lro.Response.URI
}

// requestService sends a GET to a service's URL presenting an ID token, the
// way `curl -H "Authorization: Bearer $(gcloud auth print-identity-token)"`
// does.
func requestService(t *testing.T, uri, path, idToken string) (int, string) {
	t.Helper()
	u, err := url.Parse(uri)
	require.NoError(t, err)
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

// TestCloudRunServices_CLI_InvokerBindingGatesTheServiceURL grants and revokes
// roles/run.invoker with `gcloud run services add-iam-policy-binding` and
// `remove-iam-policy-binding`, and invokes the service with the ID token
// `gcloud auth print-identity-token` mints: the service refuses the principal
// until the binding exists and again once it is removed.
func TestCloudRunServices_CLI_InvokerBindingGatesTheServiceURL(t *testing.T) {
	const id = "cli-svc-invoker"
	uri := createEchoService(t, id, "")
	email := cliInvokerEmail(t)
	member := "serviceAccount:" + email
	token := cliIDToken(t, email, uri)

	status, body := requestService(t, uri, "/", token)
	assert.Equal(t, http.StatusForbidden, status, "body=%q", body)
	assert.Contains(t, body, "Your client does not have permission to get URL <code>/</code> from this server.")

	addServiceInvoker(t, id, member)
	policy := runCLI(t, gcloudRegionalCLI("run", "services", "get-iam-policy", id,
		"--region="+location, "--format=json"))
	assert.True(t, strings.Contains(policy, `"roles/run.invoker"`) && strings.Contains(policy, member),
		"get-iam-policy must show the binding: %s", policy)

	status, body = requestService(t, uri, "/granted", token)
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "GET /granted", body)

	removeServiceInvoker(t, id, member)
	status, body = requestService(t, uri, "/", token)
	assert.Equal(t, http.StatusForbidden, status, "body=%q", body)
}
