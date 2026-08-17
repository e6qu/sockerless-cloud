package azure_tf_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
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
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	baseURL     string
	simCmd      *exec.Cmd
	gatewayCmd  *exec.Cmd
	binaryPath  string
	simPort     int
	gatewayPort int
	caCertFile  string
)

func TestMain(m *testing.M) {
	noProxy := mergeNoProxy(os.Getenv("NO_PROXY"),
		"localhost",
		"127.0.0.1",
		"::1",
		"sockerless.localhost",
		".sockerless.localhost",
		"*.sockerless.localhost",
	)
	os.Setenv("NO_PROXY", noProxy)
	os.Setenv("no_proxy", noProxy)

	if runtime.GOOS == "darwin" && os.Getenv("SOCKERLESS_AZURE_TF_IN_DOCKER") != "1" {
		os.Exit(runTerraformTestsInDocker())
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		log.Fatalf("Failed to resolve repository root: %v", err)
	}
	caddyBin := os.Getenv("CADDY")
	if caddyBin == "" {
		caddyBin = "caddy"
	}
	caddyBin = requireExecutable(caddyBin, "Azure Terraform HTTPS gateway tests")

	binaryPath, _ = filepath.Abs("../simulator-azure")

	simDir, _ := filepath.Abs("..")
	build := exec.Command("go", "build", "-tags", "noui", "-o", binaryPath, ".")
	build.Dir = simDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		log.Fatalf("Failed to build simulator: %v\n%s", err, out)
	}

	// Pre-pull the multi-arch alpine image the test's azurerm_container_app
	// uses. The sim's ACA PUT handler starts the workload container on the
	// host's Docker daemon synchronously; if the image isn't cached the
	// pull blows the terraform-provider's request budget. ECR Public
	// Gallery serves linux/{amd64,arm64} variants without auth.
	//
	// ECR Public can return `toomanyrequests` on cold CI runners. Retry
	// with exponential backoff before giving up — never just one
	// attempt against a public registry.
	pullImage := "public.ecr.aws/docker/library/alpine:latest"
	backoff := 5 * time.Second
	var pullErr error
	for attempt := 1; attempt <= 4; attempt++ {
		pull := exec.Command("docker", "pull", pullImage)
		pull.Stdout = os.Stdout
		pull.Stderr = os.Stderr
		pullErr = pull.Run()
		if pullErr == nil {
			break
		}
		log.Printf("alpine pull attempt %d failed: %v — retrying in %s", attempt, pullErr, backoff)
		time.Sleep(backoff)
		backoff *= 2
	}
	if pullErr != nil {
		log.Fatalf("Failed to pre-pull alpine image after retries: %v", pullErr)
	}
	tag := exec.Command("docker", "tag", pullImage, "alpine:latest")
	if err := tag.Run(); err != nil {
		log.Fatalf("Failed to retag alpine: %v", err)
	}

	gatewayDir, err := os.MkdirTemp("", "azure-https-gateway-*")
	if err != nil {
		log.Fatalf("Failed to create gateway state dir: %v", err)
	}

	simPort = freeTCPPort()
	gatewayPort = freeTCPPort()
	gatewayAdminPort := freeTCPPort()
	caCertFile = filepath.Join(gatewayDir, "data", "caddy", "pki", "authorities", "local", "root.crt")

	simCmd = exec.Command(binaryPath)
	simCmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=127.0.0.1:%d", simPort),
		"SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON="+azureGatewayDataPlaneEndpoints(gatewayPort),
	)
	simCmd.Stdout = os.Stdout
	simCmd.Stderr = os.Stderr
	if err := simCmd.Start(); err != nil {
		log.Fatalf("Failed to start simulator: %v", err)
	}

	directURL := fmt.Sprintf("http://127.0.0.1:%d", simPort)
	if err := waitForHTTPHealth(directURL + "/health"); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Simulator did not become healthy: %v", err)
	}
	if err := verifyDirectMetadata(directURL, fmt.Sprintf("azure.sockerless.localhost:%d", gatewayPort)); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Simulator metadata did not become healthy: %v", err)
	}

	gatewayCmd = exec.Command(caddyBin, "run", "--config", filepath.Join(repoRoot, "make", "https-gateway", "Caddyfile"), "--adapter", "caddyfile")
	gatewayCmd.Env = append(os.Environ(),
		fmt.Sprintf("XDG_DATA_HOME=%s", filepath.Join(gatewayDir, "data")),
		fmt.Sprintf("XDG_CONFIG_HOME=%s", filepath.Join(gatewayDir, "config")),
		fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_PORT=%d", gatewayPort),
		fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_ADMIN_PORT=%d", gatewayAdminPort),
		"SOCKERLESS_AWS_SIM_PORT=1",
		"SOCKERLESS_GCP_SIM_PORT=1",
		fmt.Sprintf("SOCKERLESS_AZURE_SIM_PORT=%d", simPort),
	)
	gatewayCmd.Stdout = os.Stdout
	gatewayCmd.Stderr = os.Stderr
	if err := gatewayCmd.Start(); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Failed to start HTTPS gateway: %v", err)
	}

	baseURL = fmt.Sprintf("https://azure.sockerless.localhost:%d", gatewayPort)
	requireHTTPSURL(baseURL, "Azure Terraform endpoint")
	if err := waitForFile(caCertFile, 10*time.Second); err != nil {
		gatewayCmd.Process.Kill()
		simCmd.Process.Kill()
		log.Fatalf("HTTPS gateway did not publish its local CA: %v", err)
	}

	if err := waitForHealth(baseURL+"/health", caCertFile); err != nil {
		gatewayCmd.Process.Kill()
		simCmd.Process.Kill()
		log.Fatalf("HTTPS gateway did not become healthy: %v", err)
	}
	if err := verifyGatewayMetadata(baseURL, caCertFile); err != nil {
		gatewayCmd.Process.Kill()
		simCmd.Process.Kill()
		log.Fatalf("HTTPS gateway metadata did not become healthy: %v", err)
	}

	code := m.Run()
	gatewayCmd.Process.Kill()
	gatewayCmd.Wait()
	simCmd.Process.Kill()
	simCmd.Wait()
	os.RemoveAll(gatewayDir)
	os.Exit(code)
}

func runTerraformTestsInDocker() int {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		log.Printf("Failed to resolve repository root: %v", err)
		return 1
	}

	image := "sockerless-test-azure"
	dockerfile := filepath.Join(repoRoot, "Dockerfile.test")
	// A docker-container buildx driver retains a plain `docker build` result
	// only in its build cache. The nested test container must run the tagged
	// image, so load it into the daemon store whenever buildx is available.
	buildArgs := []string{"build"}
	if exec.Command("docker", "buildx", "version").Run() == nil {
		buildArgs = []string{"buildx", "build", "--load"}
	}
	buildArgs = append(buildArgs, "-t", image, "-f", dockerfile, repoRoot)
	build := exec.Command("docker", buildArgs...)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		log.Printf("Failed to build Azure simulator test image: %v", err)
		return 1
	}

	args := []string{
		"run", "--rm",
		// The Terraform stack drives Microsoft.Network and Microsoft.Compute,
		// whose handlers build real Linux network fabric — namespaces, bridges,
		// veth pairs, routes, nftables rules — and refuse to run without
		// CAP_NET_ADMIN and CAP_SYS_ADMIN (realexec.DetectNetworkCapabilities).
		// Two things are needed for the test process to hold them, and this is
		// the real-network harness the Makefile's docker-sdk-test target already
		// spells: --privileged, so the container is granted the capabilities at
		// all, and no SOCKERLESS_DOCKER_TEST_UIDGID, so the entrypoint does not
		// setpriv down to the host user — dropping to an unprivileged uid clears
		// the effective set again, privileged container or not. The image
		// already carries ip, nft and sysctl. Without both, the stack's
		// capability gate fires and the entire apply, assert and destroy round
		// trip never runs.
		"--privileged",
		"--security-opt", "label=disable",
		"--group-add", dockerSocketGroup(),
		"--group-add", "0",
		"-v", repoRoot + ":/src",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-w", "/src/simulator-azure/terraform-tests",
		"-e", "HOME=/tmp/sockerless-home",
		"-e", "USER=sockerless",
		"-e", "SOCKERLESS_AZURE_TF_IN_DOCKER=1",
		"-e", "GOWORK=off",
		"-e", "CGO_ENABLED=0",
		"-e", "TF_IN_AUTOMATION=1",
		"-e", "CLOUDSDK_CORE_DISABLE_PROMPTS=1",
		"-e", "AZURE_CORE_NO_COLOR=true",
		"-e", "NO_PROXY=localhost,127.0.0.1,::1,sockerless.localhost,.sockerless.localhost,*.sockerless.localhost",
		"-e", "no_proxy=localhost,127.0.0.1,::1,sockerless.localhost,.sockerless.localhost,*.sockerless.localhost",
	}
	if v := os.Getenv("TF_LOG"); v != "" {
		args = append(args, "-e", "TF_LOG="+v)
	}
	if v := os.Getenv("TF_LOG_PATH"); v != "" {
		args = append(args, "-e", "TF_LOG_PATH="+v)
	}
	// Apply+destroy of the full ~54-resource stack runs right at the edge of a
	// 5-minute budget on a loaded CI runner; honour the operator-configured
	// TERRAFORM_TEST_TIMEOUT (CI passes 600s) instead of a hardcoded 300s so
	// the destroy phase isn't killed near the deadline (apply_test.go runTimed).
	innerTimeout := os.Getenv("TERRAFORM_TEST_TIMEOUT")
	if innerTimeout == "" {
		innerTimeout = "600s"
	}
	args = append(args, "-e", "TERRAFORM_TEST_TIMEOUT="+innerTimeout)
	args = append(args, image, "go", "test", "-v", "-count=1", "-timeout", innerTimeout)
	// Carry the caller's selection into the container. Dropping it silently ran
	// the whole suite for a `-run`-scoped invocation, so a developer narrowing to
	// one test still launched every heavyweight terraform graph.
	if extra := strings.Fields(os.Getenv("TERRAFORM_TEST_ARGS")); len(extra) > 0 {
		args = append(args, extra...)
	}
	args = append(args, "./...")

	run := exec.Command("docker", args...)
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		log.Printf("Failed to run Azure Terraform tests in Docker: %v", err)
		return 1
	}
	return 0
}

func dockerSocketGroup() string {
	info, err := os.Stat("/var/run/docker.sock")
	if err != nil {
		return "0"
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "0"
	}
	return fmt.Sprintf("%d", stat.Gid)
}

func mergeNoProxy(existing string, entries ...string) string {
	seen := map[string]bool{}
	var merged []string
	for _, part := range strings.Split(existing, ",") {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		merged = append(merged, part)
	}
	for _, entry := range entries {
		if entry == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		merged = append(merged, entry)
	}
	return strings.Join(merged, ",")
}

func freeTCPPort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func requireExecutable(name, purpose string) string {
	if strings.ContainsRune(name, rune(os.PathSeparator)) {
		info, err := os.Stat(name)
		if err != nil {
			log.Fatalf("%s requires executable %q: %v", purpose, name, err)
		}
		if info.IsDir() {
			log.Fatalf("%s requires executable %q, but it is a directory", purpose, name)
		}
		if info.Mode()&0111 == 0 {
			log.Fatalf("%s requires executable %q, but it is not executable", purpose, name)
		}
		return name
	}
	path, err := exec.LookPath(name)
	if err != nil {
		log.Fatalf("%s requires %q in PATH or CADDY=/path/to/caddy; install Caddy before running these tests: %v", purpose, name, err)
	}
	return path
}

func requireHTTPSURL(raw, purpose string) {
	u, err := url.Parse(raw)
	if err != nil {
		log.Fatalf("%s must be a valid HTTPS URL, got %q: %v", purpose, raw, err)
	}
	if u.Scheme != "https" {
		log.Fatalf("%s must use HTTPS because AzureRM metadata discovery requires trusted HTTPS, got %q", purpose, raw)
	}
}

func azureGatewayDataPlaneEndpoints(port int) string {
	host := fmt.Sprintf("azure.sockerless.localhost:%d", port)
	cfg := struct {
		Storage    map[string]string `json:"storage,omitempty"`
		KeyVault   string            `json:"keyVault,omitempty"`
		ServiceBus string            `json:"serviceBus,omitempty"`
		EventGrid  string            `json:"eventGrid,omitempty"`
		ACR        string            `json:"acr,omitempty"`
	}{
		Storage: map[string]string{
			"blob":  fmt.Sprintf("https://{account}.blob.%s/", host),
			"file":  fmt.Sprintf("https://{account}.file.%s/", host),
			"queue": fmt.Sprintf("https://{account}.queue.%s/", host),
			"table": fmt.Sprintf("https://{account}.table.%s/", host),
			"web":   fmt.Sprintf("https://{account}.web.%s/", host),
			"dfs":   fmt.Sprintf("https://{account}.dfs.%s/", host),
		},
		KeyVault:   fmt.Sprintf("https://{vault}.vault.%s/", host),
		ServiceBus: fmt.Sprintf("https://{namespace}.servicebus.%s/", host),
		EventGrid:  fmt.Sprintf("https://{topic}.eventgrid.%s/api/events", host),
		ACR:        fmt.Sprintf("https://{name}.azurecr.%s/", host),
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		log.Fatalf("Failed to encode Azure gateway endpoint config: %v", err)
	}
	return string(data)
}

func waitForHTTPHealth(url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 50; i++ {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == 200 {
			var body struct {
				Status   string `json:"status"`
				Provider string `json:"provider"`
			}
			if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr == nil && body.Status == "ok" && body.Provider == "azure" {
				resp.Body.Close()
				return nil
			}
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

func verifyDirectMetadata(baseURL, host string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	u := baseURL + "/metadata/endpoints?api-version=2022-09-01"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", u, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read %s response: %w", u, readErr)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d body %q", u, resp.StatusCode, string(body))
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("GET %s: headers %#v decode JSON body %q: %w", u, resp.Header, string(body), err)
	}
	return nil
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}

func waitForHealth(url, caCert string) error {
	client, err := trustedHTTPClient(caCert)
	if err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

func verifyGatewayMetadata(baseURL, caCert string) error {
	client, err := trustedHTTPClient(caCert)
	if err != nil {
		return err
	}
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := verifyGatewayMetadataOnce(client, baseURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return lastErr
}

func verifyGatewayMetadataOnce(client *http.Client, baseURL string) error {
	for _, apiVersion := range []string{"2022-09-01", "2020-06-01"} {
		u := fmt.Sprintf("%s/metadata/endpoints?api-version=%s", baseURL, apiVersion)
		resp, err := client.Get(u)
		if err != nil {
			return fmt.Errorf("GET %s: %w", u, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read %s response: %w", u, readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: status %d body %q", u, resp.StatusCode, string(body))
		}
		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			return fmt.Errorf("GET %s: headers %#v decode JSON body %q: %w", u, resp.Header, string(body), err)
		}
	}
	return nil
}

func trustedHTTPClient(caCert string) (*http.Client, error) {
	caPEM, err := os.ReadFile(caCert)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse CA cert %s", caCert)
	}

	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}, nil
}

// tfStackDir is the shared-stack configuration (this package directory);
// tfSubscriptionDir is the standalone Microsoft.Subscription configuration and
// tfEntraDir the standalone Microsoft Entra ID configuration, each of which
// runs as its own CI shard (see TestTerraformSubscriptionApplyDestroy and
// TestTerraformEntraApplyDestroy).
func tfStackDir() string        { return filepath.Dir(mustAbs("main.tf")) }
func tfSubscriptionDir() string { return mustAbs("subscription") }
func tfEntraDir() string        { return mustAbs("entra") }

// tfWorkspaceFrom copies a standalone configuration into a working directory
// private to this run.
//
// Terraform keeps its working state — terraform.tfstate, the state lock, the
// .terraform plugin directory, errored.tfstate — next to the configuration it
// runs, so running in the checked-in configuration directory makes that
// directory shared mutable state between every terraform process on the
// machine. Two overlapping runs of the same round trip (the CI shard alongside
// a local run, or two local runs) then corrupt each other: the second run
// wipes the working directory while the first is mid-apply, and the first
// run's subsequent plan reads an empty state and reports every resource as a
// fresh create, which reads as a simulator idempotency failure. A per-run
// directory keeps the round trip hermetic, and takes the state files out of
// the checked-in tree entirely.
func tfWorkspaceFrom(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	require.NoError(t, err, "read the configuration at %s", src)
	copied := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".tf" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		require.NoError(t, err, "read %s", entry.Name())
		require.NoError(t, os.WriteFile(filepath.Join(dst, entry.Name()), data, 0o600), "write %s", entry.Name())
		copied++
	}
	require.NotZero(t, copied, "no .tf files found in the configuration at %s", src)
	return dst
}

// terraformCmd builds a terraform command for the configuration in dir; the
// endpoint, trust anchor, and service-principal coordinates are identical for
// every configuration this harness drives.
func terraformCmd(dir string, args ...string) *exec.Cmd {
	requireHTTPSURL(baseURL, "Azure Terraform endpoint")
	cmd := exec.Command("terraform", args...)
	cmd.Dir = dir
	// Own process group so runTimed can reap terraform + its provider-plugin
	// grandchildren with one kill(-pgid); otherwise a timed-out command leaves
	// orphaned, spinning plugins that starve later runs into cascading timeouts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("TF_VAR_endpoint=%s", baseURL),
		fmt.Sprintf("SSL_CERT_FILE=%s", caCertFile),
		"ARM_CLIENT_ID=test-client-id",
		"ARM_CLIENT_SECRET=test-client-secret",
		"ARM_TENANT_ID=11111111-1111-1111-1111-111111111111",
		"ARM_SUBSCRIPTION_ID=00000000-0000-0000-0000-000000000001",
	)
	if v := os.Getenv("TF_LOG"); v != "" {
		cmd.Env = append(cmd.Env, "TF_LOG="+v)
	}
	if v := os.Getenv("TF_LOG_PATH"); v != "" {
		cmd.Env = append(cmd.Env, "TF_LOG_PATH="+v)
	}
	return cmd
}

// runTimed runs a terraform command, capturing combined output, with a
// watchdog that fires just before the test's own deadline. terraform spawns
// provider-plugin grandchildren; if go test's hard timeout (SIGQUIT) fired
// while a plain CombinedOutput() was in flight it would orphan that whole tree
// (t.Cleanup does NOT run on the test-binary timeout), leaving spinning
// provider processes that starve later runs into cascading timeouts. The
// watchdog instead kills the process group (Setpgid'd in terraformCmd) and
// returns a clean error with captured output.
func runTimed(t *testing.T, label string, cmd *exec.Cmd) ([]byte, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	start := time.Now()
	require.NoError(t, cmd.Start(), "%s: failed to start", label)

	killGroup := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	t.Cleanup(killGroup)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var watchdog <-chan time.Time
	if dl, ok := t.Deadline(); ok {
		if d := time.Until(dl) - 20*time.Second; d > 0 {
			timer := time.NewTimer(d)
			defer timer.Stop()
			watchdog = timer.C
		}
	}

	select {
	case err := <-done:
		t.Logf("%s duration=%s", label, time.Since(start).Round(time.Millisecond))
		return buf.Bytes(), err
	case <-watchdog:
		killGroup()
		<-done
		t.Logf("%s TIMED OUT after %s (process group killed to avoid orphans)",
			label, time.Since(start).Round(time.Millisecond))
		return buf.Bytes(), fmt.Errorf("%s timed out near the test deadline", label)
	}
}

func mustAbs(name string) string {
	p, err := filepath.Abs(name)
	if err != nil {
		log.Fatal(err)
	}
	return p
}
