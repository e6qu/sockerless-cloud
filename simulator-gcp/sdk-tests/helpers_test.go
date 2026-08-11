package gcp_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

	"golang.org/x/oauth2"
)

var (
	baseURL            string
	grpcAddr           string // host:port for gRPC Cloud Logging
	simCmd             *exec.Cmd
	binaryPath         string
	evalImageName      string // Docker image containing eval-arithmetic binary
	commandImageName   string // Docker image containing container-command binary
	httpProbeImageName string // Docker image containing localhost probe/server binary
	ctx                = context.Background()
)

func TestMain(m *testing.M) {
	binaryPath, _ = filepath.Abs("../simulator-gcp")

	simDir, _ := filepath.Abs("..")
	build := exec.Command("go", "build", "-tags", "noui", "-o", binaryPath, ".")
	build.Dir = simDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		log.Fatalf("Failed to build simulator: %v\n%s", err, out)
	}

	workloadPlatform := nativeDockerPlatform()

	evalDir, _ := filepath.Abs("../../testdata/eval-arithmetic")
	evalImageName = "sockerless-eval-arithmetic:test"
	buildGoScratchImage(evalImageName, evalDir, "eval-arithmetic", workloadPlatform)

	commandDir, _ := filepath.Abs("../../testdata/container-command")
	commandImageName = "sockerless-container-command:test"
	buildGoScratchImage(commandImageName, commandDir, "container-command", workloadPlatform)

	probeDir, _ := filepath.Abs("../../testdata/http-localhost-probe")
	httpProbeImageName = "sockerless-http-localhost-probe:test"
	buildGoScratchImage(httpProbeImageName, probeDir, "http-localhost-probe", workloadPlatform)

	if _, err := exec.LookPath("docker"); err == nil {
		pullCtx, pullCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := exec.CommandContext(pullCtx, "docker", "pull", "alpine:latest").Run(); err != nil {
			log.Printf("Warning: docker pull alpine:latest failed (Cloud Build tests may flake): %v", err)
		}
		pullCancel()
	}

	// Allocate both ports while both listeners are open. Closing the first
	// before allocating the second lets the OS re-assign the just-freed
	// port to the second listener, causing the sim's HTTP and gRPC servers
	// to collide on the same port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free port: %v", err)
	}
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free gRPC port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	grpcPort := ln2.Addr().(*net.TCPAddr).Port
	ln.Close()
	ln2.Close()

	simCmd = exec.Command(binaryPath)
	simCmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		fmt.Sprintf("SIM_GCP_GRPC_PORT=%d", grpcPort),
	)
	simCmd.Stdout = os.Stdout
	simCmd.Stderr = os.Stderr
	if err := simCmd.Start(); err != nil {
		log.Fatalf("Failed to start simulator: %v", err)
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	grpcAddr = fmt.Sprintf("127.0.0.1:%d", grpcPort)

	if err := waitForHealth(baseURL + "/health"); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Simulator did not become healthy: %v", err)
	}

	// The data plane now verifies an OAuth2 access token on every request, so
	// the tests that reach it through raw net/http calls (rather than an SDK
	// client, which carries its own token source) must present a real token.
	// Attach one — minted by the simulator's own token endpoint — to every
	// request the shared http.DefaultClient sends to the simulator, exactly as
	// a raw client authenticates against Google; this differs from the real
	// cloud only in the endpoint coordinate and the token source.
	http.DefaultClient.Transport = &simAuthTransport{
		base: http.DefaultTransport,
		host: fmt.Sprintf("127.0.0.1:%d", port),
		ts:   simTokenSource(),
	}

	code := m.Run()
	simCmd.Process.Kill()
	simCmd.Wait()
	os.Exit(code)
}

// simTokenSource returns an oauth2.TokenSource that fetches a real access token
// from the simulator's OAuth2 token endpoint — the same endpoint a
// service-account JWT-bearer flow hits against Google. The simulator signs the
// token with its access-token key and the data-plane bearer middleware verifies
// it, so this is the genuine credential path a client uses, differing from the
// real cloud only in the endpoint coordinate. The token is cached and refreshed
// by oauth2.ReuseTokenSource until it nears expiry.
func simTokenSource() oauth2.TokenSource {
	return simTokenSourceFor(baseURL)
}

// simTokenSourceFor returns a token source that mints from a specific
// simulator's token endpoint. Each simulator process signs with its own key, so
// a client talking to an isolated simulator instance (e.g. the quota tests'
// dedicated process) must present a token minted by that same instance.
func simTokenSourceFor(base string) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, simTokenFetcher{base: base})
}

// simAuthTransport attaches a simulator-minted OAuth2 access token to every
// request bound for the simulator host that does not already carry one, so raw
// net/http calls authenticate against the data plane the way an SDK client
// does. Requests to any other host (an external OpenID Connect issuer, a public
// registry) and the token-minting endpoints themselves are passed through
// untouched — the latter is how the token is obtained in the first place.
type simAuthTransport struct {
	base http.RoundTripper
	host string
	ts   oauth2.TokenSource
}

func (t *simAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.host && req.Header.Get("Authorization") == "" && !isTokenMintPath(req.URL.Path) {
		tok, err := t.ts.Token()
		if err != nil {
			return nil, err
		}
		// RoundTripper must not mutate the caller's request.
		req = req.Clone(req.Context())
		tok.SetAuthHeader(req)
	}
	return t.base.RoundTrip(req)
}

// isTokenMintPath reports whether a path is one of the simulator's OAuth2 /
// Security Token Service token-minting endpoints, which are reached without a
// bearer (they issue the bearer).
func isTokenMintPath(p string) bool {
	return p == "/token" || p == "/oauth2/v4/token" || p == "/v1/token"
}

// simBearerHeader returns an "Authorization: Bearer <token>" value carrying a
// real simulator-minted access token, for tests that issue raw HTTP requests to
// the data plane rather than going through an SDK client.
func simBearerHeader(t *testing.T) string {
	t.Helper()
	tok, err := simTokenFetcher{base: baseURL}.Token()
	if err != nil {
		t.Fatalf("mint simulator access token: %v", err)
	}
	return "Bearer " + tok.AccessToken
}

// simAuthHTTPClient returns an *http.Client that attaches a simulator-minted
// access token to every request bound for the simulator, for SDK clients that
// accept an explicit HTTP client (e.g. the Cloud Storage client in emulator
// mode, which otherwise sends no credentials) rather than a token-source
// option.
func simAuthHTTPClient() *http.Client {
	return &http.Client{Transport: &simAuthTransport{
		base: http.DefaultTransport,
		host: strings.TrimPrefix(baseURL, "http://"),
		ts:   simTokenSource(),
	}}
}

type simTokenFetcher struct{ base string }

func (f simTokenFetcher) Token() (*oauth2.Token, error) {
	resp, err := http.PostForm(f.base+"/token", url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
	})
	if err != nil {
		return nil, fmt.Errorf("fetch simulator access token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("simulator token endpoint returned %d", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode simulator token response: %w", err)
	}
	if body.AccessToken == "" {
		return nil, fmt.Errorf("simulator token endpoint returned an empty access token")
	}
	return &oauth2.Token{
		AccessToken: body.AccessToken,
		TokenType:   body.TokenType,
		Expiry:      time.Now().Add(time.Duration(body.ExpiresIn) * time.Second),
	}, nil
}

func nativeDockerPlatform() string {
	return "linux/" + runtime.GOARCH
}

func buildGoScratchImage(imageName, sourceDir, binaryName, platform string) {
	buildDir, err := os.MkdirTemp("", "sockerless-gcp-image-*")
	if err != nil {
		log.Fatalf("Failed to create image build dir: %v", err)
	}
	defer os.RemoveAll(buildDir)

	binaryPath := filepath.Join(buildDir, binaryName)
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = sourceDir
	build.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOWORK=off",
		"GOOS=linux",
		"GOARCH="+runtime.GOARCH,
	)
	if out, err := build.CombinedOutput(); err != nil {
		log.Fatalf("Failed to build %s workload binary: %v\n%s", binaryName, err, out)
	}

	dockerfile := fmt.Sprintf(`FROM scratch
COPY %s /usr/local/bin/%s
ENTRYPOINT ["/usr/local/bin/%s"]
`, binaryName, binaryName, binaryName)
	// On a docker-container buildx driver (the default on many dev machines),
	// `docker build -t` leaves the image in the build cache only — never the
	// daemon store — so the sim's container start can't find it. `docker buildx
	// build --load` materializes it into the daemon store. The legacy builder
	// builds into the store natively and rejects the buildx-only `--load`, so
	// omit it there.
	var args []string
	if exec.Command("docker", "buildx", "version").Run() == nil {
		args = []string{"buildx", "build", "--load"}
	} else {
		args = []string{"build"}
	}
	args = append(args, "--platform", platform, "-t", imageName, "-f", "-", buildDir)
	dockerBuild := exec.Command("docker", args...)
	dockerBuild.Stdin = strings.NewReader(dockerfile)
	if out, err := dockerBuild.CombinedOutput(); err != nil {
		log.Fatalf("Failed to build %s Docker image: %v\n%s", imageName, err, out)
	}
}

func waitForHealth(url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	// Registration creates every persistent store table before the listener
	// binds; with synchronous=FULL SQLite on a loaded hosted disk that DDL
	// phase alone has measured ~25 seconds, so the budget covers it with
	// headroom while still failing loudly.
	deadline := time.Now().Add(120 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			resp.Body.Close()
		} else if err != nil {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s: %v", url, lastErr)
}
