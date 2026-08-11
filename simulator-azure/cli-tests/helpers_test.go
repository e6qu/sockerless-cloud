package azure_cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
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
	simCmd           *exec.Cmd
	binaryPath       string
	evalImageName    string
	commandImageName string
	tmpDir           string

	// azureFilesDataDir is where the simulator materializes every Azure Files
	// share: <dir>/<account>/<share>. It is the directory a Container Apps
	// workload's Volume{StorageType: AzureFile} bind-mounts, so it is where a
	// file uploaded with `az storage file upload` has to land.
	azureFilesDataDir string

	subscriptionID = "00000000-0000-0000-0000-000000000001"
	resourceGroup  = "cli-test-rg"

	// armBearer is a real, simulator-minted Azure Resource Manager access token,
	// acquired once in TestMain via the OAuth2 client_credentials grant. The az
	// CLI does not auto-attach a bearer to plain http endpoints, so azRest sends
	// this token through az rest's --headers flag on every ARM control-plane
	// call and the simulator's bearer verification accepts it.
	armBearer string

	// simTenantID matches the tenant the SDK and Terraform harnesses use so all
	// three acquire tokens from the same /{tenant}/oauth2/v2.0/token route.
	simTenantID = "11111111-1111-1111-1111-111111111111"
)

func TestMain(m *testing.M) {
	// The Azure CLI (az) is a large Python application with no clean, self-
	// contained, cross-platform tarball we can drop into PATH from TestMain the
	// way the AWS CLI bundle or the Google Cloud CLI tarball allow — a faithful
	// install needs a platform package manager (brew/apt/dnf) or a Python
	// environment (pip install azure-cli), none of which is a deterministic
	// TestMain-time install across both linux and darwin. AGENTS.md § "No skip-
	// if-absent tests" sanctions failing loud as the fallback when a clean
	// install is not feasible, so require az to be present and fail with an
	// actionable message instead of silently skipping. CI installs the Azure
	// CLI, so CI stays green.
	if _, err := exec.LookPath("az"); err != nil {
		log.Fatalf("az CLI is required for the Azure CLI tests; install the Azure CLI (https://aka.ms/azure-cli): %v", err)
	}

	// Build simulator
	binaryPath, _ = filepath.Abs("../simulator-azure")
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

	// Find free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Start simulator
	simCmd = exec.Command(binaryPath)
	advertisedEndpoints := fmt.Sprintf(
		`{"storage":{"blob":"http://{account}.blob.cli-shim.localhost:%d/","file":"http://{account}.file.cli-shim.localhost:%d/","queue":"http://{account}.queue.cli-shim.localhost:%d/","table":"http://{account}.table.cli-shim.localhost:%d/"},"keyVault":"https://{vault}.vault.cli-shim.localhost:%d/","serviceBus":"https://{namespace}.servicebus.cli-shim.localhost:%d/","acr":"http://{name}.azurecr.cli-shim.localhost:%d/"}`,
		port, port, port, port, port, port, port)
	azureFilesDataDir, err = os.MkdirTemp("", "sockerless-sim-azure-files-cli-*")
	if err != nil {
		log.Fatalf("Failed to create Azure Files data dir: %v", err)
	}
	simCmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		fmt.Sprintf("SIM_AZURE_FILES_DATA_DIR=%s", azureFilesDataDir),
		"SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON="+advertisedEndpoints,
	)
	simCmd.Stdout = os.Stdout
	simCmd.Stderr = os.Stderr
	if err := simCmd.Start(); err != nil {
		log.Fatalf("Failed to start simulator: %v", err)
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	if err := waitForHealth(baseURL + "/health"); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Simulator did not become healthy: %v", err)
	}

	token, err := fetchSimARMBearer()
	if err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Failed to acquire simulator ARM bearer token: %v", err)
	}
	armBearer = token

	// Create tmp dir
	tmpDir, _ = filepath.Abs("tmp")
	os.MkdirAll(tmpDir, 0755)

	// Create resource group (needed by most tests)
	rgURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=2021-04-01",
		baseURL, subscriptionID, resourceGroup)
	cmd := azRest("PUT", rgURL, `{"location":"eastus"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("Failed to create resource group: %v\n%s", err, out)
	}

	// Pull the workload images the Container Apps suites start, before any
	// test's own deadline is running. A cold runner's registry transfer would
	// otherwise sit inside the window a test allows for the container to reach
	// RUNNING, and surface as "container never started" rather than as the
	// image acquisition it actually is.
	for _, image := range []string{
		"public.ecr.aws/docker/library/alpine:latest",
		"public.ecr.aws/docker/library/alpine:3.20",
	} {
		pullWorkloadImage(image)
	}

	code := m.Run()

	simCmd.Process.Kill()
	simCmd.Wait()
	os.RemoveAll(tmpDir)
	os.RemoveAll(azureFilesDataDir)
	os.Exit(code)
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

// fetchSimARMBearer performs an OAuth2 client_credentials grant against the
// simulator's token endpoint — the same request a real Azure AD service
// principal makes — and returns the minted ARM access token.
func fetchSimARMBearer() (string, error) {
	form := neturl.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {"https://management.azure.com/.default"},
	}
	resp, err := http.PostForm(baseURL+"/"+simTenantID+"/oauth2/v2.0/token", form)
	if err != nil {
		return "", fmt.Errorf("acquire simulator access token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read simulator token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("simulator token endpoint returned %d: %s", resp.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parse simulator token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("simulator token response carried no access_token: %s", body)
	}
	return out.AccessToken, nil
}

// azRest creates an "az rest" command with config isolation.
// Uses az rest to bypass cloud registration issues with HTTP endpoints.
// Extra args are appended verbatim — used by callers that need --headers.
func azRest(method, url, body string, extra ...string) *exec.Cmd {
	args := []string{"rest", "--method", method, "--url", url, "--output", "json"}
	if body != "" {
		args = append(args, "--body", body)
	}
	// Attach the ARM bearer token unless the caller manages its own request
	// headers. Callers that pass --headers are Host-routed data-plane requests
	// (Event Grid topic publish, Function invoke) that do not authenticate with
	// an ARM bearer; ARM control-plane calls pass no extra headers and receive
	// the token here so the simulator's bearer verification accepts them.
	if !hasHeadersFlag(extra) {
		args = append(args, "--headers", "Authorization=Bearer "+armBearer)
	}
	args = append(args, extra...)
	cmd := exec.Command("az", args...)
	cmd.Env = append(os.Environ(),
		"AZURE_CONFIG_DIR="+filepath.Join(tmpDir, "azure-config"),
		"AZURE_CORE_NO_COLOR=1",
	)
	return cmd
}

// hasHeadersFlag reports whether the caller already supplies its own az rest
// --headers set (a Host-routed data-plane call), so azRest leaves header
// management to the caller instead of injecting the ARM bearer.
func hasHeadersFlag(extra []string) bool {
	for _, a := range extra {
		if a == "--headers" {
			return true
		}
	}
	return false
}

// armURL constructs the full ARM resource URL
func armURL(provider, resourcePath, apiVersion string) string {
	return fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/%s/%s?api-version=%s",
		baseURL, subscriptionID, resourceGroup, provider, resourcePath, apiVersion)
}

func runCLI(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	const perCmdTimeout = 60 * time.Second
	// az is a Python program; recent versions compile a module that raises a
	// Python SyntaxWarning ("invalid decimal literal") to stderr, which merges
	// into the captured stdout below and corrupts the JSON az prints. The
	// warning is interpreter noise, not part of az's data contract, so silence
	// Python's own warnings for the subprocess. Set at the interpreter's startup
	// via the environment so it also covers warnings raised during import.
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "PYTHONWARNINGS=ignore")
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Start(); err != nil {
		t.Fatalf("CLI command failed to start: %v\nCommand: %s", err, strings.Join(cmd.Args, " "))
	}
	// Kill a hung CLI call so it can't consume the whole suite timeout and mask
	// the real failure in the error message.
	timer := time.AfterFunc(perCmdTimeout, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("CLI command failed: %v\nCommand: %s\nOutput: %s", err, strings.Join(cmd.Args, " "), combined.String())
	}
	return combined.String()
}

func parseJSON(t *testing.T, data string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(data), target); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nData: %s", err, data)
	}
}

func nativeDockerPlatform() string {
	return "linux/" + runtime.GOARCH
}

func buildGoScratchImage(imageName, sourceDir, binaryName, platform string) {
	buildDir, err := os.MkdirTemp("", "sockerless-azure-image-*")
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

func waitForCLIJSON(t *testing.T, url string, ready func(string) bool) string {
	t.Helper()
	// Generous deadline: each `az rest` poll pays Python startup cost, so a tight
	// window allows only a few attempts and races on a loaded CI runner.
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for {
		out, err := azRest("GET", url, "").CombinedOutput()
		last = string(out)
		if err == nil && ready(last) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for CLI resource state at %s; last output: %s", url, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// pullWorkloadImage fetches an image with bounded exponential backoff, so a
// transient registry throttle does not fail the whole suite before it starts.
func pullWorkloadImage(image string) {
	delay := time.Second
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
			if delay < 8*time.Second {
				delay *= 2
			}
		}
		out, err := exec.Command("docker", "pull", image).CombinedOutput()
		if err == nil {
			return
		}
		lastErr = fmt.Errorf("%v: %s", err, out)
	}
	log.Fatalf("pull %s after retries: %v", image, lastErr)
}
