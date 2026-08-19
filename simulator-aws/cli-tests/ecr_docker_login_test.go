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

	"github.com/e6qu/sockerless-cloud/testutil/registrytrust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Amazon ECR private-registry credential contract, driven by the two
// clients the documentation names and by nothing else: the AWS Command Line
// Interface for the credential and the Docker command line interface for the
// registry.
//
//	aws ecr get-login-password --region <region> |
//	    docker login --username AWS --password-stdin <aws_account_id>.dkr.ecr.<region>.amazonaws.com
//	docker push <aws_account_id>.dkr.ecr.<region>.amazonaws.com/<repository>:<tag>
//
// (Amazon ECR User Guide, "Private registry authentication in Amazon ECR" and
// "Pushing a Docker image to an Amazon ECR private repository".) The container
// engine — not this process — performs the login handshake and the transfer, so
// this is the exchange a user actually gets: the engine probes /v2/, reads the
// registry's Basic challenge, replays `AWS:<password>` as its credential, and
// pushes and pulls under it.
//
// Two things follow from the engine being the client.
//
// The registry is addressed by the host in the image reference, and that host
// needs a certificate, because a container engine speaks TLS to a registry it
// was not told to treat as insecure. The repository's Caddy HTTPS gateway
// (make/https-gateway/Caddyfile) serves the simulator under the Amazon ECR
// login-server name at its own coordinate and issues a certificate for it from
// its local authority, and that authority is installed in the engine's
// per-registry certificate directory the way an administrator installs a
// private registry's — the engine reads that directory while building the
// client for each request, so it applies to the very next login.
//
// And the engine has to be able to reach the listener. Where the engine runs in
// its own virtual machine — Podman on macOS, Docker Desktop — it has no route
// into this host's network namespace, which is why this test is gated to Linux
// and why the repository's Linux container path (`make docker-test`, which runs
// this suite with `--network host` against the engine's own socket) is where it
// runs on such a host: there, engine, gateway, simulator and client share one
// loopback.

// ecrRegistryLoginServerDomain is the domain the HTTPS gateway serves the
// Amazon ECR data plane under. The login server a client is pointed at is
// therefore `<aws_account_id>.dkr.ecr.aws.sockerless.localhost:<gateway port>`,
// which is the real `<aws_account_id>.dkr.ecr.<region>.amazonaws.com` shape
// with the coordinate — and only the coordinate — changed.
const ecrRegistryLoginServerDomain = "dkr.ecr.aws.sockerless.localhost"

// ecrRegistryLoginUsername is the user half of the decoded authorization
// token, which is the value `docker login --username` is documented to take
// and the user half of the Basic credential the engine replays from it.
const ecrRegistryLoginUsername = "AWS"

// ecrRegistryGateway is the TLS front the container engine reaches the
// simulator's Docker Registry HTTP API v2 data plane through.
type ecrRegistryGateway struct {
	// coordinate is the `host:port` a client names the registry by.
	coordinate string
	// authority is the PEM of the certificate authority that issued the
	// gateway's certificate.
	authority []byte
}

func TestECRCLI_DockerLoginPushPull(t *testing.T) {
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

	// The credential, from the command the documentation pipes into
	// `docker login`.
	password := strings.TrimSpace(runCLI(t, awsCLI("ecr", "get-login-password")))
	require.NotEmpty(t, password)

	// Negative control for the certificate authority installed below. Engines
	// differ on whether they need it: one that resolves this login server to a
	// loopback address may treat the registry as insecure and reach it with no
	// trust configuration at all, and where that happens the installation
	// cannot be falsified here. Where it can, the login must fail on the
	// certificate first and succeed once the authority is installed, which is
	// what makes the installation more than a no-op.
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
	// Whatever the probe stored, the arms below have to start from no
	// credential.
	_, _ = dockerCLI(t, "", "logout", gateway.coordinate)

	cleanupTrust, err := registrytrust.ConfigureTrustedRegistryCA(ctx, gateway.coordinate, gateway.authority)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })

	// A login with the wrong password is refused by the registry, and the
	// engine cannot log in with it. The login below runs the same command
	// against the same login server through the same engine and succeeds, so
	// the credential is the only thing that separates the refusal from the
	// success.
	const wrongPassword = "not-the-password"
	refusedLogin := gateway.requireRefusal(t, http.MethodGet, "/v2/", wrongPassword)
	wrong, wrongErr := dockerCLI(t, wrongPassword, "login", "--username", ecrRegistryLoginUsername, "--password-stdin", gateway.coordinate)
	requireEngineRefused(t, refusedLogin, wrong, wrongErr, "a login with the wrong password")

	// The authorization token's password logs in.
	loggedIn, err := dockerCLI(t, password, "login", "--username", ecrRegistryLoginUsername, "--password-stdin", gateway.coordinate)
	require.NoError(t, err, "login with the credential GetAuthorizationToken issued: %s", loggedIn)
	assert.Contains(t, loggedIn, "Login Succeeded")
	t.Cleanup(func() { _, _ = dockerCLI(t, "", "logout", gateway.coordinate) })

	// An image whose content exists nowhere else, so the pull below has to come
	// off the registry rather than out of the engine's store.
	buildECRTestImage(t, reference)
	t.Cleanup(func() { _, _ = dockerCLI(t, "", "image", "rm", "-f", reference) })
	built := dockerImageID(t, reference)
	require.NotEmpty(t, built)

	pushed, err := dockerCLI(t, "", "push", reference)
	require.NoError(t, err, "push under the authorization token: %s", pushed)

	// The control plane sees what the data plane accepted.
	assert.Contains(t, runCLI(t, awsCLI("ecr", "list-images", "--repository-name", repo)), "v1")

	// Dropping the local copy and pulling it back returns the same image.
	removed, err := dockerCLI(t, "", "image", "rm", "-f", reference)
	require.NoError(t, err, "remove the local copy before pulling it back: %s", removed)
	_, err = dockerCLI(t, "", "image", "inspect", reference)
	require.Error(t, err, "the local copy must be gone before the pull, or the pull proves nothing")

	pulled, err := dockerCLI(t, "", "pull", reference)
	require.NoError(t, err, "pull under the authorization token: %s", pulled)
	assert.Equal(t, built, dockerImageID(t, reference), "the pulled image must be the pushed one")

	// `docker logout` takes the credential away, and with it both directions.
	loggedOut, err := dockerCLI(t, "", "logout", gateway.coordinate)
	require.NoError(t, err, "logout: %s", loggedOut)

	// The registry refuses the upload a push starts with when no credential
	// comes with it, and the engine, holding none, cannot push.
	refusedUpload := gateway.requireRefusal(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "")
	refusedPush, refusedPushErr := dockerCLI(t, "", "push", reference)
	requireEngineRefused(t, refusedUpload, refusedPush, refusedPushErr, "a push with no credential")

	// The read a pull starts with is refused the same way, and the engine
	// brings nothing back.
	refusedManifest := gateway.requireRefusal(t, http.MethodGet, "/v2/"+repo+"/manifests/v1", "")
	removed, err = dockerCLI(t, "", "image", "rm", "-f", reference)
	require.NoError(t, err, "remove the local copy before the unauthenticated pull: %s", removed)
	refusedPull, refusedPullErr := dockerCLI(t, "", "pull", reference)
	requireEngineRefused(t, refusedManifest, refusedPull, refusedPullErr, "a pull with no credential")
	_, err = dockerCLI(t, "", "image", "inspect", reference)
	require.Error(t, err, "a pull with no credential must leave nothing behind")
}

// startECRRegistryGateway serves the running simulator's registry data plane
// over TLS under this registry's Amazon ECR login-server name, using the
// repository's HTTPS gateway configuration, and returns the coordinate a client
// names it by together with the authority that issued its certificate.
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
	gatewayProcess.Stderr = os.Stderr
	require.NoError(t, gatewayProcess.Start(), "start the HTTPS gateway")
	t.Cleanup(func() {
		_ = gatewayProcess.Process.Kill()
		_ = gatewayProcess.Wait()
	})

	authorityFile := filepath.Join(gatewayDir, "data", "caddy", "pki", "authorities", "local", "root.crt")
	authority, err := readWhenNonEmpty(authorityFile, 30*time.Second)
	require.NoError(t, err, "the HTTPS gateway must publish the authority it issues from")

	gateway := &ecrRegistryGateway{
		coordinate: fmt.Sprintf("%s.%s:%d", registryID, ecrRegistryLoginServerDomain, gatewayPort),
		authority:  authority,
	}
	gateway.waitForRegistryChallenge(t)
	return gateway
}

// client reaches the registry the way the container engine does — over TLS,
// under the login-server name, trusting the authority that issued the
// gateway's certificate, which is the same PEM the engine's per-registry
// certificate directory carries.
func (g *ecrRegistryGateway) client(t *testing.T) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(g.authority), "parse the gateway's authority")
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
}

// waitForRegistryChallenge waits until the gateway serves the registry's own
// answer to an unauthenticated /v2/ probe under the login-server name — the
// first request a container engine makes, so a gateway that satisfies it is
// ready for the client in exactly the way the client needs.
func (g *ecrRegistryGateway) waitForRegistryChallenge(t *testing.T) {
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
		_ = resp.Body.Close()
		if status == http.StatusUnauthorized && strings.HasPrefix(challenge, "Basic ") {
			require.Contains(t, challenge, "https://"+g.coordinate+"/",
				"the registry's challenge realm must name the login server the client reached")
			return
		}
		last = fmt.Sprintf("status %d, challenge %q", status, challenge)
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the HTTPS gateway did not serve the Amazon ECR registry challenge at %s: %s", g.coordinate, last)
}

// requireRefusal drives one Docker Registry HTTP API v2 request at the login
// server with the credential named — the token's password, or none at all —
// and requires Amazon ECR's own refusal of it: 401, the Basic challenge naming
// the login server the request reached, and the plain-text body the service
// answers with.
//
// This is the registry half of every refusal the engine meets below, asserted
// where the coordinate cannot distort it (requireEngineRefused explains what
// the coordinate does to the engine's own report). The request is the one the
// engine makes at that point in the exchange — the `/v2/` probe a login
// begins with, the blob upload a push begins with, the manifest read a pull
// begins with — sent over the same TLS coordinate, in the same credential
// state, by a client trusting the same authority.
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

// buildECRTestImage builds a single-layer image whose only content is unique to
// this run, so nothing already in the engine's store can stand in for it.
func buildECRTestImage(t *testing.T, reference string) {
	t.Helper()
	buildDir := t.TempDir()
	payload := fmt.Sprintf("ecr-docker-login %s\n", time.Now().UTC().Format(time.RFC3339Nano))
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
// credential state with Amazon ECR's 401 and its Basic challenge — is asserted
// against the registry itself by requireRefusal, immediately before the
// command runs. What makes the pair a refusal rather than a stuck engine is
// that this very command, with the same arguments against the same login
// server, succeeds elsewhere in this test under the credential
// GetAuthorizationToken issued: the login, the push and the pull all go
// through, and the credential is the only thing that changes here.
//
// What the engine prints is deliberately not matched, because the wording is
// the engine's and, on one of them, the coordinate's:
//
//   - dockerd carries `127.0.0.0/8` and `::1/128` in its built-in
//     insecure-registry list and resolves a login server's name to classify it
//     (registry/config.go, isSecureIndex), so a login server that resolves to
//     loopback — which every `*.localhost` name does — is insecure to it and
//     gets a second endpoint, plain HTTP at the same host and port
//     (registry/service_v2.go, lookupV2Endpoints). `docker login` moves on to
//     that endpoint unless the first one failed with an Unauthorized error,
//     and the registry's 401 is not one, because loginV2 turns every non-200
//     into a plain error (registry/auth.go). So the refused login is retried
//     in plain HTTP against the gateway's TLS listener, which answers
//     `400 Bad Request`, and the daemon reports that last error instead of the
//     registry's. Amazon ECR's own login server resolves to a public address,
//     where there is no second endpoint and the same daemon reports
//     `401 Unauthorized` — the divergence is in the coordinate, the one thing
//     a simulator test differs in.
//   - Podman renders a pull it holds no credential for as a prompt to log in
//     first, which names neither the registry nor a status.
//
// Asserting the registry's refusal at the registry is what keeps both of those
// renderings out of the claim while still proving the credential is what was
// missing.
// The registry's status is a parameter rather than a convention, so the engine
// half of the claim cannot be written without the registry half that identifies
// it: a bare "the command failed" is exactly what this pair exists not to be.
func requireEngineRefused(t *testing.T, registryStatus int, output string, err error, what string) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, registryStatus,
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
