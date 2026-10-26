package gcp_cli_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/httpsgateway"
	"github.com/e6qu/sockerless-cloud/testutil/registrytrust"
	"github.com/e6qu/sockerless-cloud/testutil/scratchimage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Artifact Registry Docker credential contract, driven only by the clients
// its documentation names:
//
//   gcloud auth print-access-token |
//       docker login -u oauth2accesstoken --password-stdin https://LOCATION-docker.pkg.dev
//   docker push LOCATION-docker.pkg.dev/PROJECT/REPOSITORY/IMAGE:TAG
//
// The container engine, not this process, performs the login handshake and the
// transfer: it probes /v2/, follows the Bearer challenge to the token service,
// and pushes and pulls under the token it gets back.
//
// A container engine speaks TLS to a registry it was not told to treat as
// insecure, so the repository's Caddy HTTPS gateway (make/https-gateway/Caddyfile)
// serves the simulator under an Artifact Registry Docker endpoint name with a
// certificate from its local authority, which the test installs in the engine's
// per-registry certificate directory. An engine in its own virtual machine
// (Podman on macOS, Docker Desktop) has no route to this host's loopback, so the
// test runs on Linux only; `make docker-test` runs it there with
// `--network host`.

// arRegistryEndpointDomain replaces only the coordinate of the real
// `<location>-docker.pkg.dev`: a client names the registry
// `<location>-docker.pkg.gcp.sockerless.localhost:<gateway port>`.
const arRegistryEndpointDomain = "pkg.gcp.sockerless.localhost"

// arRegistryLoginUsername is the username Artifact Registry reserves for an
// OAuth 2.0 access token presented as an HTTP Basic password; `gcloud auth
// configure-docker`'s credential helper produces it too.
const arRegistryLoginUsername = "oauth2accesstoken"

// The IAM permissions Artifact Registry names when it denies a Docker Registry
// v2 request, as its audit-logging method table maps them.
const (
	arCLIPermDownload = "artifactregistry.repositories.downloadArtifacts"
	arCLIPermUpload   = "artifactregistry.repositories.uploadArtifacts"
)

type arRegistryGateway struct {
	// coordinate is the `host:port` a client names the registry by.
	coordinate string
	// authority is the PEM of the certificate authority that issued the
	// gateway's certificate.
	authority []byte
}

func TestArtifactRegistryCLI_DockerLoginPushPull(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the container engine performs the registry handshake and transfer itself, and on a host whose engine runs inside its own virtual machine it has no route into this host's network namespace. Linux hosts — and the repository's Linux container path, `make docker-test` — share one loopback between engine, gateway, simulator and client.")
	}

	ctx := context.Background()

	const repoID = "cli-docker-login"
	created := runCLI(t, gcloudCLI("artifacts", "repositories", "create", repoID,
		"--location="+location,
		"--repository-format=docker",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("artifacts", "repositories", "delete", repoID,
			"--location="+location, "--quiet").Run()
	})
	var repository struct {
		Name        string `json:"name"`
		RegistryURI string `json:"registryUri"`
	}
	parseJSON(t, created, &repository)
	resource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)
	require.Equal(t, resource, repository.Name)
	require.Equal(t, location+"-docker.pkg.dev/"+project+"/"+repoID, repository.RegistryURI,
		"the control plane must name the repository under this location's Docker endpoint")

	gateway := startArtifactRegistryGateway(t)
	imagePath := project + "/" + repoID + "/app"
	reference := gateway.coordinate + "/" + imagePath + ":v1"

	// The credential the documentation pipes into `docker login`.
	token := strings.TrimSpace(runCLI(t, gcloudCLI("auth", "print-access-token")))
	require.NotEmpty(t, token)

	// Negative control for the authority installed below. An engine that
	// resolves this endpoint to loopback may treat the registry as insecure and
	// need no trust at all; elsewhere the login must fail on the certificate
	// first and succeed once the authority is installed.
	untrusted, untrustedErr := dockerCLI(t, token, "login", "--username", arRegistryLoginUsername, "--password-stdin", gateway.coordinate)
	trustIsFalsifiable := strings.Contains(untrusted, "certificate signed by unknown authority") ||
		strings.Contains(untrusted, "x509:")
	if trustIsFalsifiable {
		require.Error(t, untrustedErr, "an engine that refused the certificate must not report a successful login: %s", untrusted)
	} else {
		require.NoError(t, untrustedErr,
			"the engine neither refused the gateway's certificate nor completed the login: %s", untrusted)
		t.Logf("engine reaches %s without the gateway's authority installed, so installing it is not falsifiable here: %s",
			gateway.coordinate, strings.TrimSpace(untrusted))
	}
	_, _ = dockerCLI(t, "", "logout", gateway.coordinate)

	cleanupTrust, err := registrytrust.ConfigureTrustedRegistryCA(ctx, gateway.coordinate, gateway.authority)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })

	// Artifact Registry refuses a credential it cannot accept in two places with
	// two messages: the token service reports `authentication failed`, and the data
	// plane reports `No valid credential was supplied.` with no challenge. The
	// login below differs only in its credential and succeeds.
	const rejectedToken = "not-a-real-access-token"
	refusedToken := gateway.requireTokenServiceRefusal(t, rejectedToken)
	gateway.requireRejectedCredential(t, http.MethodGet, "/v2/", "Bearer "+rejectedToken)
	wrong, wrongErr := dockerCLI(t, rejectedToken, "login", "--username", arRegistryLoginUsername, "--password-stdin", gateway.coordinate)
	requireEngineRefused(t, refusedToken, wrong, wrongErr, "a login with a credential the registry rejects")

	// The access token the Google Cloud CLI prints logs in.
	loggedIn, err := dockerCLI(t, token, "login", "--username", arRegistryLoginUsername, "--password-stdin", gateway.coordinate)
	require.NoError(t, err, "login with the credential gcloud auth print-access-token issued: %s", loggedIn)
	assert.Contains(t, loggedIn, "Login Succeeded")
	t.Cleanup(func() { _, _ = dockerCLI(t, "", "logout", gateway.coordinate) })

	// Content unique to this run, so the pull has to come off the registry rather
	// than out of the engine's store.
	buildArtifactRegistryTestImage(t, reference)
	t.Cleanup(func() { _, _ = dockerCLI(t, "", "image", "rm", "-f", reference) })
	built := dockerImageID(t, reference)
	require.NotEmpty(t, built)

	pushed, err := dockerCLI(t, "", "push", reference)
	require.NoError(t, err, "push under the access token: %s", pushed)

	// The control plane sees what the data plane accepted.
	listed := runCLI(t, gcloudCLI("artifacts", "docker", "images", "list",
		location+"-docker.pkg.dev/"+project+"/"+repoID,
		"--include-tags",
		"--format=json",
	))
	var images []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Tags []string `json:"tags"`
	}
	parseJSON(t, listed, &images)
	require.Len(t, images, 1, "gcloud output: %s", listed)
	assert.Contains(t, images[0].Metadata.Name, resource+"/dockerImages/app@sha256:")
	assert.Equal(t, []string{"v1"}, images[0].Tags)

	// Dropping the local copy and pulling it back returns the same image.
	removed, err := dockerCLI(t, "", "image", "rm", "-f", reference)
	require.NoError(t, err, "remove the local copy before pulling it back: %s", removed)
	_, err = dockerCLI(t, "", "image", "inspect", reference)
	require.Error(t, err, "the local copy must be gone before the pull, or the pull proves nothing")

	pulled, err := dockerCLI(t, "", "pull", reference)
	require.NoError(t, err, "pull under the access token: %s", pulled)
	assert.Equal(t, built, dockerImageID(t, reference), "the pulled image must be the pushed one")

	// `docker logout` takes the credential away, and with it both directions.
	loggedOut, err := dockerCLI(t, "", "logout", gateway.coordinate)
	require.NoError(t, err, "logout: %s", loggedOut)

	// Artifact Registry refuses an anonymous upload in two steps: the challenge
	// sends the client to the token service, which refuses the scope.
	deniedUpload := gateway.requireAnonymousRefusal(t, http.MethodPost, "/v2/"+imagePath+"/blobs/uploads/",
		"repository:"+imagePath+":pull,push", arCLIPermUpload, resource)
	refusedPush, refusedPushErr := dockerCLI(t, "", "push", reference)
	requireEngineRefused(t, deniedUpload, refusedPush, refusedPushErr, "a push with no credential")

	// The read a pull starts with is refused the same way, and the engine
	// brings nothing back.
	deniedManifest := gateway.requireAnonymousRefusal(t, http.MethodGet, "/v2/"+imagePath+"/manifests/v1",
		"repository:"+imagePath+":pull", arCLIPermDownload, resource)
	removed, err = dockerCLI(t, "", "image", "rm", "-f", reference)
	require.NoError(t, err, "remove the local copy before the unauthenticated pull: %s", removed)
	refusedPull, refusedPullErr := dockerCLI(t, "", "pull", reference)
	requireEngineRefused(t, deniedManifest, refusedPull, refusedPullErr, "a pull with no credential")
	_, err = dockerCLI(t, "", "image", "inspect", reference)
	require.Error(t, err, "a pull with no credential must leave nothing behind")
}

// startArtifactRegistryGateway serves the running simulator's registry data
// plane over TLS under this suite's Artifact Registry Docker endpoint name.
func startArtifactRegistryGateway(t *testing.T) *arRegistryGateway {
	t.Helper()

	caddyBinary := os.Getenv("CADDY")
	if caddyBinary == "" {
		caddyBinary = "caddy"
	}
	resolved, err := exec.LookPath(caddyBinary)
	require.NoErrorf(t, err,
		"the Artifact Registry Docker data plane is served over TLS by the repository's HTTPS gateway, which is Caddy; %q must be executable",
		caddyBinary)

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	_, simPort, err := net.SplitHostPort(strings.TrimPrefix(baseURL, "http://"))
	require.NoError(t, err, "read the simulator's port out of %s", baseURL)

	gatewayDir := t.TempDir()
	ports := reserveGatewayPorts(t, 2)
	gatewayPort, adminPort := ports[0], ports[1]

	gatewayProcess := exec.Command(resolved, "run",
		"--config", filepath.Join(repoRoot, "make", "https-gateway", "Caddyfile"),
		"--adapter", "caddyfile")
	gatewayProcess.Env = append(os.Environ(),
		"XDG_DATA_HOME="+filepath.Join(gatewayDir, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(gatewayDir, "config"),
		fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_PORT=%d", gatewayPort),
		fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_ADMIN_PORT=%d", adminPort),
		"SOCKERLESS_AWS_SIM_PORT=1",
		"SOCKERLESS_GCP_SIM_PORT="+simPort,
		"SOCKERLESS_AZURE_SIM_PORT=1",
		"SOCKERLESS_HTTPS_GATEWAY_DEFAULT_SIM_PORT="+simPort,
	)
	gatewayProcess.Stdout = os.Stdout
	startErr := httpsgateway.Start(gatewayProcess, os.Stderr)
	if gatewayProcess.Process != nil {
		t.Cleanup(func() {
			_ = gatewayProcess.Process.Kill()
			_ = gatewayProcess.Wait()
		})
	}
	require.NoError(t, startErr, "start the HTTPS gateway")

	authorityFile := filepath.Join(gatewayDir, "data", "caddy", "pki", "authorities", "local", "root.crt")
	authority, err := os.ReadFile(authorityFile)
	require.NoError(t, err, "the HTTPS gateway must publish the authority it issues from")

	gateway := &arRegistryGateway{
		coordinate: fmt.Sprintf("%s-docker.%s:%d", location, arRegistryEndpointDomain, gatewayPort),
		authority:  authority,
	}
	gateway.requireRegistryChallenge(t)
	return gateway
}

// client trusts the same authority PEM the engine's per-registry certificate
// directory carries.
func (g *arRegistryGateway) client(t *testing.T) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(g.authority), "parse the gateway's authority")
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
}

func (g *arRegistryGateway) do(t *testing.T, method, path, authorization string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, "https://"+g.coordinate+path, http.NoBody)
	require.NoError(t, err)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := g.client(t).Do(req)
	require.NoErrorf(t, err, "%s %s at the Docker endpoint", method, path)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp, string(body)
}

// requireRegistryChallenge requires Artifact Registry's answer to the
// credential-less /v2/ probe a container engine opens with: the Bearer
// challenge naming the token service, and the shorter `not authenticated`
// message, because the request names the registry rather than a repository.
func (g *arRegistryGateway) requireRegistryChallenge(t *testing.T) {
	t.Helper()
	resp, err := g.client(t).Get("https://" + g.coordinate + "/v2/")
	require.NoError(t, err, "probe Artifact Registry through the HTTPS gateway")
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, `Bearer realm="https://`+g.coordinate+`/v2/token"`, resp.Header.Get("Www-Authenticate"),
		"the challenge realm must name the token service at the endpoint the client reached")
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
	code, message := arCLIRegistryError(t, string(body))
	assert.Equal(t, "UNAUTHORIZED", code)
	assert.Equal(t, "not authenticated: No credential was supplied.", message)
}

// requireTokenServiceRefusal sends the token service a credential the registry
// cannot accept, as `docker login` does once it has read the challenge, and
// requires Artifact Registry's refusal, which the token service words
// differently from the data plane:
//
//	$ curl -i -u 'oauth2accesstoken:bogus' \
//	    'https://us-central1-docker.pkg.dev/v2/token?service=…&scope=…'
//	HTTP/2 401
//	{"errors":[{"code":"UNAUTHORIZED","message":"authentication failed"}]}
func (g *arRegistryGateway) requireTokenServiceRefusal(t *testing.T, password string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		"https://"+g.coordinate+"/v2/token?service="+url.QueryEscape(g.coordinate), http.NoBody)
	require.NoError(t, err)
	req.SetBasicAuth(arRegistryLoginUsername, password)
	resp, err := g.client(t).Do(req)
	require.NoError(t, err, "the Docker token service at the endpoint's realm")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equalf(t, http.StatusUnauthorized, resp.StatusCode,
		"the token service must refuse this credential: %s", body)
	code, message := arCLIRegistryError(t, string(body))
	assert.Equal(t, "UNAUTHORIZED", code)
	assert.Equal(t, "authentication failed", message)
	return resp.StatusCode
}

// requireRejectedCredential requires Artifact Registry's refusal of a request
// carrying a credential it cannot accept: 401 with the flat message and no
// challenge, since repeating it would only send the client to mint the same
// rejected credential.
func (g *arRegistryGateway) requireRejectedCredential(t *testing.T, method, path, authorization string) {
	t.Helper()
	resp, body := g.do(t, method, path, authorization)
	require.Equalf(t, http.StatusUnauthorized, resp.StatusCode,
		"the registry must refuse %s %s with this credential: %s", method, path, body)
	assert.Empty(t, resp.Header.Get("Www-Authenticate"),
		"a rejected credential draws no challenge from Artifact Registry")
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
	code, message := arCLIRegistryError(t, body)
	assert.Equal(t, "UNAUTHORIZED", code)
	assert.Equal(t, "No valid credential was supplied.", message)
}

// requireAnonymousRefusal walks the exchange a credential-less container engine
// performs: the request draws a Bearer challenge naming the token service, the
// service and the scope, and the token service refuses that scope with 403,
// `DENIED` and the IAM permission the method needs. It is the registry half of
// the refusal, asserted where the coordinate cannot distort it; see
// requireEngineRefused.
func (g *arRegistryGateway) requireAnonymousRefusal(t *testing.T, method, path, wantScope, permission, resource string) int {
	t.Helper()

	resp, body := g.do(t, method, path, "")
	require.Equalf(t, http.StatusUnauthorized, resp.StatusCode,
		"the registry must challenge %s %s when no credential comes with it: %s", method, path, body)
	assert.Equal(t,
		`Bearer realm="https://`+g.coordinate+`/v2/token",service="`+g.coordinate+`",scope="`+wantScope+`"`,
		resp.Header.Get("Www-Authenticate"),
		"the challenge must name the token service, the endpoint the request reached, and the scope to ask for")
	code, message := arCLIRegistryError(t, body)
	assert.Equal(t, "UNAUTHORIZED", code)
	assert.Equal(t, "not authenticated: No valid credential was supplied.", message)

	// The token service's refusal of the scope is where the `denied:` the user
	// reads comes from.
	resp, body = g.do(t, http.MethodGet,
		"/v2/token?service="+url.QueryEscape(g.coordinate)+"&scope="+url.QueryEscape(wantScope), "")
	require.Equalf(t, http.StatusForbidden, resp.StatusCode,
		"the token service must refuse the scope %s to an uncredentialled caller: %s", wantScope, body)
	code, message = arCLIRegistryError(t, body)
	assert.Equal(t, "DENIED", code)
	assert.Equal(t,
		fmt.Sprintf("Unauthenticated request. Unauthenticated requests do not have permission %q on resource %q (or it may not exist)",
			permission, resource),
		message)

	// A minted token carries an identity rather than a permission, so the data
	// plane checks access per repository. `registry:catalog` names no repository
	// and is minted without a credential.
	resp, body = g.do(t, http.MethodGet,
		"/v2/token?service="+url.QueryEscape(g.coordinate)+"&scope="+url.QueryEscape("registry:catalog:*"), "")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var issued struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &issued))
	require.NotEmpty(t, issued.Token, "the token service must issue a token for a scope naming no repository")
	assert.Equal(t, 43200, issued.ExpiresIn)

	resp, body = g.do(t, method, path, "Bearer "+issued.Token)
	require.Equalf(t, http.StatusForbidden, resp.StatusCode,
		"the anonymous token must be denied at the resource for %s %s: %s", method, path, body)
	code, message = arCLIRegistryError(t, body)
	assert.Equal(t, "DENIED", code)
	assert.Equal(t,
		fmt.Sprintf("Unauthenticated request. Unauthenticated requests do not have permission %q on resource %q (or it may not exist)",
			permission, resource),
		message)
	// The status the engine's own request finally meets: a denial, whether the
	// mint refused the scope or the resource refused the identity.
	return resp.StatusCode
}

// buildArtifactRegistryTestImage builds a single-layer image unique to this
// run, so nothing already in the engine's store can stand in for it.
func buildArtifactRegistryTestImage(t *testing.T, reference string) {
	t.Helper()
	buildDir := t.TempDir()
	payload := fmt.Sprintf("artifactregistry-docker-login %s\n", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, os.WriteFile(filepath.Join(buildDir, "payload"), []byte(payload), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(buildDir, "Dockerfile"),
		[]byte("FROM scratch\nCOPY payload /payload\n"), 0o644))

	args := []string{"build"}
	if exec.Command("docker", "buildx", "version").Run() == nil {
		// A docker-container buildx driver keeps a plain `docker build` result only in
		// its build cache; the push needs the tag in the engine's image store.
		args = []string{"buildx", "build", "--load"}
	}
	args = append(args, "--platform", scratchimage.NativePlatform(), "-t", reference, buildDir)
	out, err := dockerCLI(t, "", args...)
	require.NoError(t, err, "build the image to push: %s", out)
}

// dockerImageID is the same before a push and after the pull that brings the
// image back.
func dockerImageID(t *testing.T, reference string) string {
	t.Helper()
	out, err := dockerCLI(t, "", "image", "inspect", "--format", "{{.Id}}", reference)
	require.NoError(t, err, "inspect %s: %s", reference, out)
	return strings.TrimSpace(out)
}

// dockerCLI bounds each call so a wedged engine fails this test rather than
// the whole suite's deadline.
func dockerCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	t.Logf("docker %s\n%s", strings.Join(args, " "), out)
	return string(out), err
}

// requireEngineRefused asserts the engine half of a refusal; the caller asserts
// the registry half against the registry just before, and the same command
// succeeds elsewhere in the test under a valid credential.
//
// It does not match the engine's wording, which differs between engines: a
// refused login is `unauthorized: authentication failed` on dockerd and
// `invalid username/password` on Podman. The coordinate distorts it further:
// dockerd treats a registry that resolves to loopback, as every `*.localhost`
// name does, as insecure (registry/config.go, isSecureIndex) and retries in
// plain HTTP against the gateway's TLS listener (registry/service_v2.go,
// lookupV2Endpoints). registryStatus is a parameter so no caller can assert the
// engine half without the registry half.
func requireEngineRefused(t *testing.T, registryStatus int, output string, err error, what string) {
	t.Helper()
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, registryStatus,
		"%s: the registry must have refused the request the engine makes here", what)
	require.Error(t, err, "%s must be refused: %s", what, output)
}

// reserveGatewayPorts holds every listener open until all n are chosen, so the
// operating system cannot hand out the same port twice.
func reserveGatewayPorts(t *testing.T, n int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)
	defer func() {
		for _, listener := range listeners {
			require.NoError(t, listener.Close())
		}
	}()
	for range n {
		listener, err := net.Listen("tcp", ":0")
		require.NoError(t, err, "reserve a port for the HTTPS gateway")
		listeners = append(listeners, listener)
		address, ok := listener.Addr().(*net.TCPAddr)
		require.True(t, ok, "port reservation is not TCP: %T", listener.Addr())
		ports = append(ports, address.Port)
	}
	return ports
}
