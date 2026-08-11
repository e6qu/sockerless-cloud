package gcp_cli_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The `gcloud` CLI has two official non-interactive credential paths: a
// service-account key file (covered by TestIAMServiceAccountKeysActivateCLI)
// and the GCE metadata server, which is how it authenticates when it runs on
// an instance. This file drives the second one against the simulator with the
// real CLI, carrying no pre-minted access token: the credential gcloud uses is
// the one it reads from the simulator's metadata server, and every command
// below fails without it.

// simMetadataHost is the host:port coordinate a client points GCE_METADATA_HOST
// at. It is the only thing that differs from an on-instance gcloud, which finds
// the metadata server at metadata.google.internal.
func simMetadataHost(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	return u.Host
}

// gcloudMetadataCLI builds a gcloud command whose only credential is the
// simulator's metadata server. The config dir is isolated and empty, and — in
// deliberate contrast to gcloudCLI — no access token is placed in the
// environment, so a command that succeeds proves gcloud obtained a usable
// credential from the metadata server on its own.
func gcloudMetadataCLI(t *testing.T, configDir string, args ...string) *exec.Cmd {
	t.Helper()
	host := simMetadataHost(t)
	cmd := exec.Command("gcloud", args...)
	// No credential file and no pre-minted token may stand in for the metadata
	// server, so both are removed from the inherited environment rather than
	// blanked — gcloud reads an empty GOOGLE_APPLICATION_CREDENTIALS as a
	// credential file named "".
	var env []string
	for _, entry := range os.Environ() {
		switch strings.SplitN(entry, "=", 2)[0] {
		case "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_ACCESS_TOKEN":
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env,
		"CLOUDSDK_CONFIG="+configDir,
		"CLOUDSDK_CORE_DISABLE_PROMPTS=1",
		"CLOUDSDK_CORE_PROJECT="+project,
		// The metadata-server coordinate, in the three spellings the Google
		// client libraries and gcloud read it from.
		"GCE_METADATA_HOST="+host,
		"GCE_METADATA_IP="+host,
		"GCE_METADATA_ROOT="+host,
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_IAM="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_RUN="+baseURL+"/",
	)
	return cmd
}

// TestGCEMetadataServerGcloudCredentials drives the official on-instance gcloud
// credential path end to end. gcloud discovers the instance's identity by
// walking the metadata server's directory listings, reads that identity
// recursively, mints an access token from it and authenticates a native
// command with it — all against the simulator, differing from real Google only
// in the GCE_METADATA_HOST coordinate.
func TestGCEMetadataServerGcloudCredentials(t *testing.T) {
	const accountID = "metadata-adc-sa"
	email := accountID + "@" + project + ".iam.gserviceaccount.com"
	runCLI(t, gcloudCLI("iam", "service-accounts", "create", accountID,
		"--display-name=Metadata ADC SA", "--format=json"))

	configDir := filepath.Join(tmpDir, "gcloud-metadata-config")
	require.NoError(t, os.RemoveAll(configDir))

	// gcloud reports the identity it read from the metadata server as its
	// active account. Without the metadata server it has no account at all.
	accounts := runCLI(t, gcloudMetadataCLI(t, configDir, "auth", "list", "--format=value(account)"))
	assert.Contains(t, accounts, "iam.gserviceaccount.com",
		"gcloud must adopt the metadata server's identity as its account, got %q", accounts)

	// A native data-plane command authenticated only by the metadata-server
	// credential. This is the call the issue reported as impossible.
	listed := runCLI(t, gcloudMetadataCLI(t, configDir,
		"iam", "service-accounts", "list", "--format=value(email)"))
	assert.Contains(t, listed, email,
		"a native gcloud command must authenticate with the metadata-server credential, got %q", listed)

	// Application Default Credentials resolve through the same path, and the
	// token they yield is accepted by the simulator's data plane.
	adcToken := strings.TrimSpace(runCLI(t, gcloudMetadataCLI(t, configDir,
		"auth", "application-default", "print-access-token")))
	require.NotEmpty(t, adcToken, "ADC must resolve to a token through the metadata server")
	assertTokenAuthenticates(t, adcToken)
}

// assertTokenAuthenticates proves a token is one the simulator's data plane
// accepts, by presenting it on a real API read the way any client would.
func assertTokenAuthenticates(t *testing.T, token string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/projects/"+project+"/serviceAccounts", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"the data plane must accept the token: %s", string(body))
}

// TestGCloudRegionPrefixedEndpointHost proves the simulator answers the host
// form gcloud builds for a regional service. Given an endpoint override,
// gcloud prefixes the region onto the host — `us-central1-<host>` — exactly as
// it does for the real `us-central1-run.googleapis.com`. The coordinate that
// carries that request to the simulator is gcloud's own HTTP proxy setting, so
// the CLI, the URL it builds and the credential it presents are all unmodified;
// only where the bytes are delivered differs.
func TestGCloudRegionPrefixedEndpointHost(t *testing.T) {
	const serviceName = "region-prefixed-host-svc"
	httpDoJSON(t, http.MethodPost,
		fmt.Sprintf("%s/apis/serving.knative.dev/v1/namespaces/%s/services", baseURL, project),
		fmt.Sprintf(`{
			"apiVersion": "serving.knative.dev/v1",
			"kind": "Service",
			"metadata": {"name": %q, "namespace": %q},
			"spec": {"template": {"spec": {"containers": [{"image": "gcr.io/test-project/hello"}]}}}
		}`, serviceName, project))

	configDir := filepath.Join(tmpDir, "gcloud-region-prefix-config")
	require.NoError(t, os.RemoveAll(configDir))

	cmd := gcloudMetadataCLI(t, configDir, "run", "services", "list",
		"--region="+location, "--format=value(metadata.name)")
	cmd.Env = append(cmd.Env, "HTTP_PROXY="+baseURL, "NO_PROXY=")
	listed := runCLI(t, cmd)

	assert.Contains(t, listed, serviceName,
		"the simulator must answer the %s-prefixed host gcloud builds for a regional service, got %q",
		location, listed)
}
