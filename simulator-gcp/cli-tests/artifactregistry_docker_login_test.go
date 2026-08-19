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

	"github.com/e6qu/sockerless-cloud/testutil/registrytrust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Google Artifact Registry Docker credential contract, driven by the two
// clients the documentation names and by nothing else: the Google Cloud CLI for
// the credential and the Docker command line interface for the registry.
//
//	gcloud auth print-access-token |
//	    docker login -u oauth2accesstoken --password-stdin https://LOCATION-docker.pkg.dev
//	docker push LOCATION-docker.pkg.dev/PROJECT/REPOSITORY/IMAGE:TAG
//
// ("Authenticate to Artifact Registry for Docker" and "Push and pull images",
// Artifact Registry documentation.) The container engine — not this process —
// performs the login handshake and the transfer, so this is the exchange a user
// actually gets: the engine probes /v2/, reads the Bearer challenge, exchanges
// its stored credential for a token at the realm the challenge names, and
// pushes and pulls under the token it gets back.
//
// Two things follow from the engine being the client.
//
// The registry is addressed by the host in the image reference, and that host
// needs a certificate, because a container engine speaks TLS to a registry it
// was not told to treat as insecure. The repository's Caddy HTTPS gateway
// (make/https-gateway/Caddyfile) serves the simulator under an Artifact
// Registry Docker endpoint name at its own coordinate and issues a certificate
// for it from its local authority, and that authority is installed in the
// engine's per-registry certificate directory the way an administrator installs
// a private registry's — the engine reads that directory while building the
// client for each request, so it applies to the very next login.
//
// And the engine has to be able to reach the listener. Where the engine runs in
// its own virtual machine — Podman on macOS, Docker Desktop — it has no route
// into this host's network namespace, which is why this test is gated to Linux
// and why the repository's Linux container path (`make docker-test`, which runs
// this suite with `--network host` against the engine's own socket) is where it
// runs on such a host: there, engine, gateway, simulator and client share one
// loopback.

// arRegistryEndpointDomain is the domain the HTTPS gateway serves the Artifact
// Registry Docker data plane under. The endpoint a client is pointed at is
// therefore `<location>-docker.pkg.gcp.sockerless.localhost:<gateway port>`,
// which is the real `<location>-docker.pkg.dev` shape with the coordinate — and
// only the coordinate — changed.
const arRegistryEndpointDomain = "pkg.gcp.sockerless.localhost"

// arRegistryLoginUsername is the username Artifact Registry reserves for an
// OAuth 2.0 access token presented as an HTTP Basic password, which is the
// value `docker login -u` is documented to take and what the `gcloud auth
// configure-docker` credential helper produces.
const arRegistryLoginUsername = "oauth2accesstoken"

// The IAM permissions Artifact Registry names when it denies a Docker Registry
// v2 request at the resource, as its audit-logging method table maps them: a
// manifest or blob read downloads, an upload uploads.
const (
	arCLIPermDownload = "artifactregistry.repositories.downloadArtifacts"
	arCLIPermUpload   = "artifactregistry.repositories.uploadArtifacts"
)

// arRegistryGateway is the TLS front the container engine reaches the
// simulator's Docker Registry HTTP API v2 data plane through.
type arRegistryGateway struct {
	// coordinate is the `host:port` a client names the registry by.
	coordinate string
	// authority is the PEM of the certificate authority that issued the
	// gateway's certificate.
	authority []byte
}

func TestArtifactRegistryCLI_DockerLoginPushPull(t *testing.T) {
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

	// The credential, from the command the documentation pipes into
	// `docker login`.
	token := strings.TrimSpace(runCLI(t, gcloudCLI("auth", "print-access-token")))
	require.NotEmpty(t, token)

	// Negative control for the certificate authority installed below. Engines
	// differ on whether they need it: one that resolves this endpoint to a
	// loopback address may treat the registry as insecure and reach it with no
	// trust configuration at all, and where that happens the installation
	// cannot be falsified here. Where it can, the login must fail on the
	// certificate first and succeed once the authority is installed, which is
	// what makes the installation more than a no-op.
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
	// Whatever the probe stored, the arms below have to start from no
	// credential.
	_, _ = dockerCLI(t, "", "logout", gateway.coordinate)

	cleanupTrust, err := registrytrust.ConfigureTrustedRegistryCA(ctx, gateway.coordinate, gateway.authority)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })

	// A login with a credential the registry cannot accept is refused, and the
	// engine cannot log in with it. Artifact Registry refuses it in two places,
	// with two different messages, and the engine meets both: the token service
	// the challenge sends it to reports `authentication failed`, and the data
	// plane itself reports `No valid credential was supplied.` with no challenge
	// at all. The login below runs the same command against the same endpoint
	// through the same engine and succeeds, so the credential is the only thing
	// that separates the refusal from the success.
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

	// An image whose content exists nowhere else, so the pull below has to come
	// off the registry rather than out of the engine's store.
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

	// The registry refuses the upload a push starts with when no credential
	// comes with it, and the engine, holding none, cannot push. Artifact
	// Registry's refusal is a two-step: the challenge sends the client to the
	// token service, and the token service refuses the scope it was told to ask
	// for.
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
// plane over TLS under an Artifact Registry Docker endpoint name for this
// suite's location, using the repository's HTTPS gateway configuration, and
// returns the coordinate a client names it by together with the authority that
// issued its certificate.
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
	gatewayProcess.Stderr = os.Stderr
	require.NoError(t, gatewayProcess.Start(), "start the HTTPS gateway")
	t.Cleanup(func() {
		_ = gatewayProcess.Process.Kill()
		_ = gatewayProcess.Wait()
	})

	authorityFile := filepath.Join(gatewayDir, "data", "caddy", "pki", "authorities", "local", "root.crt")
	authority, err := readWhenNonEmpty(authorityFile, 30*time.Second)
	require.NoError(t, err, "the HTTPS gateway must publish the authority it issues from")

	gateway := &arRegistryGateway{
		coordinate: fmt.Sprintf("%s-docker.%s:%d", location, arRegistryEndpointDomain, gatewayPort),
		authority:  authority,
	}
	gateway.waitForRegistryChallenge(t)
	return gateway
}

// client reaches the registry the way the container engine does — over TLS,
// under the Docker endpoint name, trusting the authority that issued the
// gateway's certificate, which is the same PEM the engine's per-registry
// certificate directory carries.
func (g *arRegistryGateway) client(t *testing.T) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(g.authority), "parse the gateway's authority")
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
}

// do drives one request at the gateway with the Authorization header named, or
// none at all, and returns the response together with its body.
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

// waitForRegistryChallenge waits until the gateway serves Artifact Registry's
// own answer to an unauthenticated /v2/ probe under the Docker endpoint name —
// the first request a container engine makes, so a gateway that satisfies it is
// ready for the client in exactly the way the client needs. The shapes asserted
// are the ones the live service sends a request that carries no credential at
// all: the Bearer challenge naming the token service, and the shorter of the
// two `not authenticated` messages, because this request addresses the registry
// rather than a repository.
func (g *arRegistryGateway) waitForRegistryChallenge(t *testing.T) {
	t.Helper()
	client := g.client(t)
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp, err := client.Get("https://" + g.coordinate + "/v2/")
		if err != nil {
			last = err.Error()
			time.Sleep(200 * time.Millisecond)
			continue
		}
		challenge := resp.Header.Get("Www-Authenticate")
		status := resp.StatusCode
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr == nil && status == http.StatusUnauthorized && strings.HasPrefix(challenge, "Bearer ") {
			assert.Equal(t, `Bearer realm="https://`+g.coordinate+`/v2/token"`, challenge,
				"the challenge realm must name the token service at the endpoint the client reached")
			assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
			code, message := arCLIRegistryError(t, string(body))
			assert.Equal(t, "UNAUTHORIZED", code)
			assert.Equal(t, "not authenticated: No credential was supplied.", message)
			return
		}
		last = fmt.Sprintf("status %d, challenge %q", status, challenge)
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the HTTPS gateway did not serve the Artifact Registry challenge at %s: %s", g.coordinate, last)
}

// requireTokenServiceRefusal drives the Docker token service at the realm every
// challenge names with a credential the registry cannot accept — the exchange a
// `docker login` performs the moment it has read the challenge — and requires
// Artifact Registry's own refusal of it, which the token service words
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

// requireRejectedCredential drives one Docker Registry HTTP API v2 request with
// a credential the registry cannot accept and requires Artifact Registry's own
// refusal: 401, no challenge at all — the client already knows where the token
// service is, and repeating the challenge would send it back to mint the same
// rejected credential — and the service's flat message.
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

// requireAnonymousRefusal walks the whole exchange a container engine holding
// no credential performs against Artifact Registry, and requires the service's
// own answer at each step: the request draws a Bearer challenge naming the
// token service, the service and the scope; and the token service refuses that
// scope with HTTP 403, the `DENIED` code, and the IAM permission the method
// needs. The engine reports a `denied:` because the mint denied it, which is
// where the live service denies it too.
//
// This is the registry half of the refusals the engine meets, asserted where
// the coordinate cannot distort it (requireEngineRefused explains what the
// coordinate does to the engine's own report). The request is the one the
// engine makes at that point in the exchange — the blob upload a push begins
// with, the manifest read a pull begins with — sent over the same TLS
// coordinate, in the same credential state, by a client trusting the same
// authority.
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

	// The engine asks the token service for the scope the challenge named, and
	// Artifact Registry refuses that scope to an uncredentialled caller. This is
	// where the exchange ends and where the `denied:` the user reads comes from.
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

	// A token the mint does issue carries an identity rather than a permission,
	// so the data plane evaluates access per request against the repository
	// addressed — and refuses the anonymous identity there too. `registry:catalog`
	// names no repository, so it is minted without a credential.
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

// buildArtifactRegistryTestImage builds a single-layer image whose only content
// is unique to this run, so nothing already in the engine's store can stand in
// for it.
func buildArtifactRegistryTestImage(t *testing.T, reference string) {
	t.Helper()
	buildDir := t.TempDir()
	payload := fmt.Sprintf("artifactregistry-docker-login %s\n", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, os.WriteFile(filepath.Join(buildDir, "payload"), []byte(payload), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(buildDir, "Dockerfile"),
		[]byte("FROM scratch\nCOPY payload /payload\n"), 0o644))

	args := []string{"build"}
	if exec.Command("docker", "buildx", "version").Run() == nil {
		// A docker-container buildx driver keeps a plain `docker build` result
		// only in its build cache; the push needs the tag in the engine's
		// image store.
		args = []string{"buildx", "build", "--load"}
	}
	args = append(args, "--platform", nativeDockerPlatform(), "-t", reference, buildDir)
	out, err := dockerCLI(t, "", args...)
	require.NoError(t, err, "build the image to push: %s", out)
}

// dockerImageID reads the identifier the engine holds an image under, which is
// the same before a push and after the pull that brings it back.
func dockerImageID(t *testing.T, reference string) string {
	t.Helper()
	out, err := dockerCLI(t, "", "image", "inspect", "--format", "{{.Id}}", reference)
	require.NoError(t, err, "inspect %s: %s", reference, out)
	return strings.TrimSpace(out)
}

// dockerCLI runs the Docker command line interface, feeding stdin when the
// subcommand reads a secret from it, and bounds the call so a wedged engine
// fails this test rather than the whole suite's deadline.
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

// requireEngineRefused asserts the container engine could not carry out one
// operation for want of a credential the registry accepts. It is the engine
// half of a refusal; the registry half — that the registry answers this exact
// credential state with Artifact Registry's own status, code and message — is
// asserted against the registry itself immediately before the command runs.
// What makes the pair a refusal rather than a stuck engine is that this very
// command, with the same arguments against the same endpoint, succeeds
// elsewhere in this test under the credential `gcloud auth print-access-token`
// issued: the login, the push and the pull all go through, and the credential
// is the only thing that changes here.
//
// What the engine prints is deliberately not matched, because the wording is
// the engine's, not the registry's, and the two engines word the same refusal
// differently. A refused login is `unauthorized: authentication failed` on
// dockerd and `invalid username/password` on Podman; a refused push is the
// service's own `denied: …` line on dockerd and `403 (Forbidden)` from the
// blob copy on Podman. Matching either wording would assert the engine's
// rendering rather than Artifact Registry's contract.
//
// The coordinate can distort the engine's report further. dockerd carries
// `127.0.0.0/8` and `::1/128` in its built-in insecure-registry list and
// resolves a registry's name to classify it (registry/config.go,
// isSecureIndex), so an endpoint that resolves to loopback — which every
// `*.localhost` name does — is insecure to it and gets a second endpoint,
// plain HTTP at the same host and port (registry/service_v2.go,
// lookupV2Endpoints). Where dockerd does not stop at the first endpoint's
// refusal it retries in plain HTTP against the gateway's TLS listener, and the
// transport's answer to that is the error the caller finally sees. Artifact
// Registry's own endpoint resolves to a public address, where there is no
// second endpoint — the divergence is in the coordinate, the one thing a
// simulator test differs in.
//
// Asserting the registry's refusal at the registry is what keeps all of that
// out of the claim while still proving the credential is what was missing.
// The registry's status is a parameter rather than a convention, so the engine
// half of the claim cannot be written without the registry half that identifies
// it: a bare "the command failed" is exactly what this pair exists not to be.
func requireEngineRefused(t *testing.T, registryStatus int, output string, err error, what string) {
	t.Helper()
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, registryStatus,
		"%s: the registry must have refused the request the engine makes here", what)
	require.Error(t, err, "%s must be refused: %s", what, output)
}

// readWhenNonEmpty reads a file the moment it has content, which is how a
// process that writes it asynchronously — the gateway publishing the authority
// it just generated — is waited on without guessing how long it takes.
func readWhenNonEmpty(path string, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil && len(content) > 0 {
			return content, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for %s", path)
}

// reserveGatewayPorts returns n distinct ports free for the wildcard bind the
// gateway performs. Every listener is held open until all n are chosen, so the
// operating system cannot hand the same port out twice.
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
