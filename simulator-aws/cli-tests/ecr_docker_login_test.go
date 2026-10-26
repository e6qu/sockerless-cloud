package aws_cli_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
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

// The Amazon ECR private-registry credential contract, driven by the two
// clients the documentation names: the AWS CLI for the credential and the
// Docker CLI for the registry.
//
//	aws ecr get-login-password --region <region> |
//	    docker login --username AWS --password-stdin <aws_account_id>.dkr.ecr.<region>.amazonaws.com
//	docker push <aws_account_id>.dkr.ecr.<region>.amazonaws.com/<repository>:<tag>
//
// (Amazon ECR User Guide, "Private registry authentication in Amazon ECR" and
// "Pushing a Docker image to an Amazon ECR private repository".) The container
// engine performs the login and the transfer: it probes /v2/, reads the
// registry's Basic challenge, replays `AWS:<password>` and pushes and pulls
// under it.
//
// The engine speaks TLS to a registry it was not told to treat as insecure, so
// the repository's Caddy HTTPS gateway (make/https-gateway/Caddyfile) serves the
// simulator under the Amazon ECR login-server name with a certificate from its
// local authority, which the test installs in the engine's per-registry
// certificate directory. The engine reads that directory for each request, so
// the next login applies it.
//
// The engine must also reach the listener. An engine in its own virtual
// machine (Podman on macOS, Docker Desktop) has no route into this host's
// network namespace, so the test is Linux-only; `make docker-test` runs the
// suite with `--network host` so engine, gateway, simulator and client share
// one loopback.

// ecrRegistryLoginServerDomain makes the login server
// `<aws_account_id>.dkr.ecr.aws.sockerless.localhost:<gateway port>`: the real
// `<aws_account_id>.dkr.ecr.<region>.amazonaws.com` with only the coordinate
// changed.
const ecrRegistryLoginServerDomain = "dkr.ecr.aws.sockerless.localhost"

// ecrRegistryLoginUsername is the user half of the decoded authorization
// token, which `docker login --username` takes.
const ecrRegistryLoginUsername = "AWS"

type ecrRegistryGateway struct {
	coordinate string
	// authority is the PEM of the CA that issued the gateway's certificate.
	authority []byte
}

func TestECRCLI_DockerLoginPushPull(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the container engine performs the registry handshake and transfer itself, and on a host whose engine runs inside its own virtual machine it has no route into this host's network namespace. Linux hosts — and the repository's Linux container path, `make docker-test` — share one loopback between engine, gateway, simulator and client.")
	}

	ctx := context.Background()

	const repo = "ecr-docker-login/app"
	created := runCLI(t, awsCLI("ecr", "create-repository", "--repository-name", repo))
	t.Cleanup(func() {
		runCLI(t, awsCLI("ecr", "delete-repository", "--repository-name", repo, "--force"))
	})
	var repository struct {
		Repository struct {
			RegistryId    string `json:"registryId"`
			RepositoryUri string `json:"repositoryUri"`
		} `json:"repository"`
	}
	parseJSON(t, created, &repository)
	registryID := repository.Repository.RegistryId
	require.NotEmpty(t, registryID)
	require.True(t,
		strings.HasPrefix(repository.Repository.RepositoryUri, registryID+".dkr.ecr."),
		"the control plane must name the repository under this registry's login server: %s",
		repository.Repository.RepositoryUri)

	gateway := startECRRegistryGateway(t, registryID)
	reference := gateway.coordinate + "/" + repo + ":v1"

	password := strings.TrimSpace(runCLI(t, awsCLI("ecr", "get-login-password")))
	require.NotEmpty(t, password)

	// Negative control for the CA installed below. An engine that resolves the
	// login server to loopback may treat the registry as insecure and need no
	// trust configuration, which leaves nothing to falsify; elsewhere the login
	// must fail on the certificate first and succeed once the CA is installed.
	untrusted, untrustedErr := dockerCLI(t, password, "login", "--username", ecrRegistryLoginUsername, "--password-stdin", gateway.coordinate)
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
	// Whatever the probe stored, the arms below start from no credential.
	_, _ = dockerCLI(t, "", "logout", gateway.coordinate)

	cleanupTrust, err := registrytrust.ConfigureTrustedRegistryCA(ctx, gateway.coordinate, gateway.authority)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })

	// The registry refuses a login with the wrong password. The same command
	// against the same login server succeeds below, so the credential is the only
	// difference.
	const wrongPassword = "not-the-password"
	refusedLogin := gateway.requireRefusal(t, http.MethodGet, "/v2/", wrongPassword)
	wrong, wrongErr := dockerCLI(t, wrongPassword, "login", "--username", ecrRegistryLoginUsername, "--password-stdin", gateway.coordinate)
	requireEngineRefused(t, refusedLogin, wrong, wrongErr, "a login with the wrong password")

	loggedIn, err := dockerCLI(t, password, "login", "--username", ecrRegistryLoginUsername, "--password-stdin", gateway.coordinate)
	require.NoError(t, err, "login with the credential GetAuthorizationToken issued: %s", loggedIn)
	assert.Contains(t, loggedIn, "Login Succeeded")
	t.Cleanup(func() { _, _ = dockerCLI(t, "", "logout", gateway.coordinate) })

	// The image's content exists nowhere else, so the pull below has to come off
	// the registry rather than out of the engine's store.
	buildECRTestImage(t, reference)
	t.Cleanup(func() { _, _ = dockerCLI(t, "", "image", "rm", "-f", reference) })
	built := dockerImageID(t, reference)
	require.NotEmpty(t, built)

	pushed, err := dockerCLI(t, "", "push", reference)
	require.NoError(t, err, "push under the authorization token: %s", pushed)

	assert.Contains(t, runCLI(t, awsCLI("ecr", "list-images", "--repository-name", repo)), "v1")

	removed, err := dockerCLI(t, "", "image", "rm", "-f", reference)
	require.NoError(t, err, "remove the local copy before pulling it back: %s", removed)
	_, err = dockerCLI(t, "", "image", "inspect", reference)
	require.Error(t, err, "the local copy must be gone before the pull, or the pull proves nothing")

	pulled, err := dockerCLI(t, "", "pull", reference)
	require.NoError(t, err, "pull under the authorization token: %s", pulled)
	assert.Equal(t, built, dockerImageID(t, reference), "the pulled image must be the pushed one")

	loggedOut, err := dockerCLI(t, "", "logout", gateway.coordinate)
	require.NoError(t, err, "logout: %s", loggedOut)

	// Without a credential the registry refuses the upload a push starts with.
	refusedUpload := gateway.requireRefusal(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "")
	refusedPush, refusedPushErr := dockerCLI(t, "", "push", reference)
	requireEngineRefused(t, refusedUpload, refusedPush, refusedPushErr, "a push with no credential")

	// It refuses the read a pull starts with the same way.
	refusedManifest := gateway.requireRefusal(t, http.MethodGet, "/v2/"+repo+"/manifests/v1", "")
	removed, err = dockerCLI(t, "", "image", "rm", "-f", reference)
	require.NoError(t, err, "remove the local copy before the unauthenticated pull: %s", removed)
	refusedPull, refusedPullErr := dockerCLI(t, "", "pull", reference)
	requireEngineRefused(t, refusedManifest, refusedPull, refusedPullErr, "a pull with no credential")
	_, err = dockerCLI(t, "", "image", "inspect", reference)
	require.Error(t, err, "a pull with no credential must leave nothing behind")
}

// startECRRegistryGateway serves the simulator's registry data plane over TLS
// under this registry's Amazon ECR login-server name, and returns the
// coordinate and the CA that issued its certificate.
func startECRRegistryGateway(t *testing.T, registryID string) *ecrRegistryGateway {
	t.Helper()

	caddyBinary := os.Getenv("CADDY")
	if caddyBinary == "" {
		caddyBinary = "caddy"
	}
	resolved, err := exec.LookPath(caddyBinary)
	require.NoErrorf(t, err,
		"the Amazon ECR data plane is served over TLS by the repository's HTTPS gateway, which is Caddy; %q must be executable",
		caddyBinary)

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

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
		fmt.Sprintf("SOCKERLESS_AWS_SIM_PORT=%d", simulatorPort),
		"SOCKERLESS_GCP_SIM_PORT=1",
		"SOCKERLESS_AZURE_SIM_PORT=1",
		fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_DEFAULT_SIM_PORT=%d", simulatorPort),
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

	gateway := &ecrRegistryGateway{
		coordinate: fmt.Sprintf("%s.%s:%d", registryID, ecrRegistryLoginServerDomain, gatewayPort),
		authority:  authority,
	}
	gateway.requireRegistryChallenge(t)
	return gateway
}

// client reaches the registry as the container engine does: over TLS, under
// the login-server name, trusting the CA the engine's per-registry certificate
// directory carries.
func (g *ecrRegistryGateway) client(t *testing.T) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(g.authority), "parse the gateway's authority")
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
}

// requireRegistryChallenge checks the registry's answer to an unauthenticated
// /v2/ probe, the first request a container engine makes.
func (g *ecrRegistryGateway) requireRegistryChallenge(t *testing.T) {
	t.Helper()
	resp, err := g.client(t).Get("https://" + g.coordinate + "/v2/")
	require.NoError(t, err, "probe the Amazon ECR registry through the HTTPS gateway")
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	challenge := resp.Header.Get("Www-Authenticate")
	require.Truef(t, strings.HasPrefix(challenge, "Basic "), "challenge %q", challenge)
	require.Contains(t, challenge, "https://"+g.coordinate+"/",
		"the registry's challenge realm must name the login server the client reached")
}

// requireRefusal sends the request the engine makes at that point (the `/v2/`
// probe a login begins with, the blob upload a push begins with, the manifest
// read a pull begins with) with the given credential, over the same TLS
// coordinate, and requires Amazon ECR's refusal: 401, the Basic challenge
// naming the login server, and the plain-text body. It is the registry half of
// each refusal; requireEngineRefused explains why the engine half cannot carry
// it.
func (g *ecrRegistryGateway) requireRefusal(t *testing.T, method, path, password string) int {
	t.Helper()
	req, err := http.NewRequest(method, "https://"+g.coordinate+path, http.NoBody)
	require.NoError(t, err)
	if password != "" {
		req.SetBasicAuth(ecrRegistryLoginUsername, password)
	}
	resp, err := g.client(t).Do(req)
	require.NoErrorf(t, err, "%s %s at the login server", method, path)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Equalf(t, http.StatusUnauthorized, resp.StatusCode,
		"the registry must refuse %s %s with this credential: %s", method, path, body)
	assert.Equal(t, `Basic realm="https://`+g.coordinate+`/",service="ecr.amazonaws.com"`,
		resp.Header.Get("Www-Authenticate"),
		"the refusal must carry Amazon ECR's Basic challenge, naming the login server the request reached")
	assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
	assert.Equal(t, "Not Authorized", strings.TrimSpace(string(body)))
	return resp.StatusCode
}

// buildECRTestImage builds a single-layer image whose content is unique to this
// run, so nothing already in the engine's store can stand in for it.
func buildECRTestImage(t *testing.T, reference string) {
	t.Helper()
	buildDir := t.TempDir()
	payload := fmt.Sprintf("ecr-docker-login %s\n", time.Now().UTC().Format(time.RFC3339Nano))
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

// dockerImageID reads the identifier the engine holds an image under, the same
// before a push and after the pull that brings it back.
func dockerImageID(t *testing.T, reference string) string {
	t.Helper()
	out, err := dockerCLI(t, "", "image", "inspect", "--format", "{{.Id}}", reference)
	require.NoError(t, err, "inspect %s: %s", reference, out)
	return strings.TrimSpace(out)
}

// dockerCLI feeds stdin to a subcommand that reads a secret from it, and bounds
// the call so a wedged engine fails this test rather than the suite's deadline.
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

// requireEngineRefused asserts the container engine could not carry out an
// operation for want of an accepted credential. requireRefusal asserts the
// registry half right before; the same command succeeds elsewhere in the test
// under the issued credential, so the credential is the only thing that
// changes. The registry's status is a parameter so the engine half cannot be
// written without the registry half.
//
// It does not match the engine's output, whose wording depends on the engine
// and, on dockerd, on the coordinate:
//
//   - dockerd lists `127.0.0.0/8` and `::1/128` as insecure registries and
//     classifies a login server by resolving it (registry/config.go,
//     isSecureIndex), so every `*.localhost` login server gets a second,
//     plain-HTTP endpoint (registry/service_v2.go, lookupV2Endpoints).
//     `docker login` moves on to it unless the first failed Unauthorized, and
//     loginV2 turns every non-200 into a plain error (registry/auth.go), so the
//     daemon retries in plain HTTP against the TLS listener and reports its
//     `400 Bad Request`. Amazon ECR's login server resolves to a public
//     address, where the same daemon reports `401 Unauthorized`.
//   - Podman renders a pull without a credential as a prompt to log in first,
//     naming neither the registry nor a status.
func requireEngineRefused(t *testing.T, registryStatus int, output string, err error, what string) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, registryStatus,
		"%s: the registry must have refused the request the engine makes here", what)
	require.Error(t, err, "%s must be refused: %s", what, output)
}

// reserveGatewayPorts returns n distinct ports free for the gateway's wildcard
// bind, holding every listener open until all n are chosen so no port is
// handed out twice.
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
