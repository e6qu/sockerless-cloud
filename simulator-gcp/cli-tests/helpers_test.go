package gcp_cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
)

var (
	baseURL          string
	grpcAddr         string // host:port of the sim's gRPC server (Bigtable data/admin + Cloud Logging)
	cbtPath          string // absolute path to an installed cbt binary (Bigtable data-plane CLI)
	simCmd           *exec.Cmd
	binaryPath       string
	evalImageName    string
	commandImageName string
	tmpDir           string

	project  = "test-project"
	location = "us-central1"

	// accessToken is a real simulator-minted OAuth2 access token the CLIs
	// present to the data plane; set once in TestMain.
	accessToken string
)

// mintSimAccessToken fetches an access token from the simulator's OAuth2 token
// endpoint — the same JWT-bearer grant a real gcloud invocation uses — so CLI
// commands authenticate against the data plane exactly as they would against
// Google, differing only in the endpoint coordinate.
func mintSimAccessToken(base string) (string, error) {
	resp, err := http.PostForm(base+"/token", url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("token endpoint returned an empty access token")
	}
	return body.AccessToken, nil
}

func TestMain(m *testing.M) {
	// Use the host gcloud when present; otherwise install a pinned Google Cloud
	// CLI release into a temp dir so the suite runs unconditionally instead of
	// skipping. Mirrors the AWS cli-tests install-or-fail approach — a required
	// tool is installed by the harness, never skipped when absent.
	gcloudPath, err := exec.LookPath("gcloud")
	if err != nil {
		gcloudPath = installGcloudCLI()
	}
	if out, err := exec.Command(gcloudPath, "version").CombinedOutput(); err != nil {
		log.Fatalf("gcloud CLI is not runnable after setup: %v\n%s", err, out)
	}

	// Build simulator
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

	// Find free ports for HTTP and gRPC
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free gRPC port: %v", err)
	}
	grpcPort := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	// Start simulator
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

	// Mint a real access token from the simulator's OAuth2 token endpoint. The
	// data plane verifies the bearer on every request, so the gcloud CLI must
	// present a token the simulator signed — the same credential a real gcloud
	// invocation carries, obtained from the configured endpoint coordinate.
	var tokenErr error
	accessToken, tokenErr = mintSimAccessToken(baseURL)
	if tokenErr != nil {
		simCmd.Process.Kill()
		log.Fatalf("Failed to mint simulator access token: %v", tokenErr)
	}

	// Create tmp dir
	tmpDir, _ = filepath.Abs("tmp")
	os.MkdirAll(tmpDir, 0755)

	// Install the Bigtable data-plane CLI (cbt) into the tmp dir. cbt is the
	// canonical Bigtable data-plane adaptor (gcloud bigtable is admin-only),
	// required by TestBigtableCLI_DataPlane_cbt. Installing it here (rather
	// than skip-if-absent) keeps the test deterministic: every CLI test run
	// exercises the gRPC data plane through a second real adaptor. The
	// `pkg@version` form of go install does not modify this module's go.mod.
	cbtDir := filepath.Join(tmpDir, "bin")
	os.MkdirAll(cbtDir, 0755)
	installCBT(cbtDir)

	code := m.Run()

	simCmd.Process.Kill()
	simCmd.Wait()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

// installCBT builds the Bigtable data-plane CLI (cbt) into binDir and sets
// cbtPath to the resulting executable. cbt is the canonical Bigtable data-plane
// adaptor (gcloud bigtable is admin-only) — installing it here keeps the
// data-plane CLI test unconditional. Fatals on failure (no skip, no fallback):
// the install is a hard requirement, not best-effort.
func installCBT(binDir string) {
	binExe := "cbt"
	if runtime.GOOS == "windows" {
		binExe = "cbt.exe"
	}
	cbtPath = filepath.Join(binDir, binExe)
	// Already installed in a prior run that reused tmp/.
	if _, err := os.Stat(cbtPath); err == nil {
		return
	}
	// v1.13.0 is the last release of cloud.google.com/go/bigtable that still
	// ships cmd/cbt; later versions moved it out of the module.
	install := exec.Command("go", "install", "cloud.google.com/go/bigtable/cmd/cbt@v1.13.0")
	install.Env = append(os.Environ(), "GOBIN="+binDir, "GOWORK=off")
	var buf bytes.Buffer
	install.Stdout = &buf
	install.Stderr = &buf
	if err := install.Run(); err != nil {
		log.Fatalf("go install cbt failed (required for Bigtable data-plane CLI test): %v\n%s", err, buf.String())
	}
	if _, err := os.Stat(cbtPath); err != nil {
		log.Fatalf("go install cbt reported success but %s is missing: %v\n%s", cbtPath, err, buf.String())
	}
}

// installGcloudCLI downloads a pinned Google Cloud CLI release, extracts it into
// a temp dir, prepends its bin directory to PATH, and returns the gcloud binary
// path. Called from TestMain when gcloud is absent (a fresh checkout, a CI image
// without it) so the CLI suite runs instead of skipping — the same install-or-
// fail contract the AWS cli-tests use for the aws CLI. Fatals loudly if the
// install itself fails; never a skip, never a fallback.
func installGcloudCLI() string {
	// Pinned Google Cloud CLI release. The versioned archive keeps the install
	// deterministic across runs; bump this constant to move to a newer release.
	const version = "500.0.0"

	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm"
	default:
		log.Fatalf("Unsupported architecture for automatic gcloud install: %s", runtime.GOARCH)
	}

	var osName string
	switch runtime.GOOS {
	case "linux", "darwin":
		osName = runtime.GOOS
	default:
		log.Fatalf("Unsupported OS for automatic gcloud install: %s", runtime.GOOS)
	}

	binDir, err := os.MkdirTemp("", "sockerless-gcloud-cli-*")
	if err != nil {
		log.Fatalf("Failed to create gcloud CLI install dir: %v", err)
	}

	archive := fmt.Sprintf("google-cloud-cli-%s-%s-%s.tar.gz", version, osName, arch)
	dlURL := "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/" + archive
	tarball := filepath.Join(binDir, archive)
	if out, err := exec.Command("curl", "-fsSL", "-o", tarball, dlURL).CombinedOutput(); err != nil {
		log.Fatalf("Failed to download gcloud CLI (%s): %v\n%s", dlURL, err, out)
	}
	if out, err := exec.Command("tar", "-xzf", tarball, "-C", binDir).CombinedOutput(); err != nil {
		log.Fatalf("Failed to extract gcloud CLI: %v\n%s", err, out)
	}

	// The archive extracts to google-cloud-sdk/ with the launcher at bin/gcloud;
	// the bundled Python interpreter makes bin/gcloud runnable without running
	// install.sh.
	gcloudBin := filepath.Join(binDir, "google-cloud-sdk", "bin", "gcloud")
	if _, err := os.Stat(gcloudBin); err != nil {
		log.Fatalf("gcloud binary not found after install at %s: %v", gcloudBin, err)
	}

	// Prepend to PATH so gcloudCLI() picks it up.
	os.Setenv("PATH", filepath.Dir(gcloudBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return gcloudBin
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

// gcloudCLI creates a gcloud command with config isolation and endpoint overrides.
func gcloudCLI(args ...string) *exec.Cmd {
	cmd := exec.Command("gcloud", args...)
	cmd.Env = append(os.Environ(),
		"CLOUDSDK_CONFIG="+filepath.Join(tmpDir, "gcloud-config"),
		"CLOUDSDK_AUTH_ACCESS_TOKEN="+accessToken,
		"CLOUDSDK_CORE_PROJECT="+project,
		"CLOUDSDK_CORE_DISABLE_PROMPTS=1",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_DNS="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_APIGATEWAY="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_API_GATEWAY="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDBUILD="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDRESOURCEMANAGER="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_IAM="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_PUBSUB="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_LOGGING="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDFUNCTIONS="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_RUN="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_SERVICEUSAGE="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_SECRETMANAGER="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDKMS="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_VPCACCESS="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_COMPUTE="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_ARTIFACTREGISTRY="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_EVENTARC="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_STORAGE="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_BIGQUERY="+baseURL+"/bigquery/v2/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_FIRESTORE="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_REDIS="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_SQL="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_SPANNER="+baseURL+"/spanner/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_DATAFLOW="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_BIGTABLE="+baseURL+"/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_BIGTABLEADMIN="+baseURL+"/",
	)
	return cmd
}

// gcloudSAActivatedCLI creates a gcloud command bound to an isolated config
// dir and carrying no pre-minted access token, so the credentials stored in
// that config — the service account activated from a minted key file — are
// what authenticate. The simulator coordinates come from the config's own
// properties, which the test sets with the same `gcloud config set` commands
// the GCP console's post-mint CLI panel prints (api_endpoint_overrides/iam
// and auth/token_host) — proving that printed usage verbatim.
func gcloudSAActivatedCLI(configDir string, args ...string) *exec.Cmd {
	cmd := exec.Command("gcloud", args...)
	cmd.Env = append(os.Environ(),
		"CLOUDSDK_CONFIG="+configDir,
		"CLOUDSDK_CORE_PROJECT="+project,
		"CLOUDSDK_CORE_DISABLE_PROMPTS=1",
	)
	return cmd
}

// httpDo performs a direct HTTP request to the simulator REST API.
// Used when gcloud commands don't support endpoint overrides well.
func httpDo(method, url string, body string) (*http.Response, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// The data plane verifies an OAuth2 access token on every request, so these
	// raw HTTP calls must present the same real, simulator-minted token the
	// gcloud CLI carries via CLOUDSDK_AUTH_ACCESS_TOKEN — differing from a real
	// Google request only in the endpoint coordinate and the token source.
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return http.DefaultClient.Do(req)
}

func httpDoJSON(t *testing.T, method, url, body string) string {
	t.Helper()
	resp, err := httpDo(method, url, body)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	return string(data)
}

func runCLI(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()

	const perCmdTimeout = 30 * time.Second

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Start(); err != nil {
		t.Fatalf("CLI command failed to start: %v\nCommand: %s", err, strings.Join(cmd.Args, " "))
	}

	// Kill the process if it has not exited within the per-command budget.
	// This prevents a single hanging gcloud call from consuming the whole
	// suite timeout and masking the actual failure in the error message.
	timer := time.AfterFunc(perCmdTimeout, func() {
		_ = cmd.Process.Kill()
	})
	defer timer.Stop()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("CLI command failed: %v\nCommand: %s\nOutput: %s", err, strings.Join(cmd.Args, " "), combined.String())
	}
	return combined.String()
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
	dockerBuild := exec.Command("docker", "build",
		"--platform", platform,
		"-t", imageName,
		"-f", "-", buildDir)
	dockerBuild.Stdin = strings.NewReader(dockerfile)
	if out, err := dockerBuild.CombinedOutput(); err != nil {
		log.Fatalf("Failed to build %s Docker image: %v\n%s", imageName, err, out)
	}
}

func parseJSON(t *testing.T, data string, target any) {
	t.Helper()
	// gcloud may prefix JSON with status text (e.g. "Created [URL].\n").
	// Try each plausible JSON delimiter until the remaining output decodes.
	for i, r := range data {
		if r != '[' && r != '{' {
			continue
		}
		if err := json.Unmarshal([]byte(data[i:]), target); err == nil {
			return
		}
	}
	if err := json.Unmarshal([]byte(data), target); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nData: %s", err, data)
	}
}
