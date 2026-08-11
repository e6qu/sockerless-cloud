package aws_tf_test

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
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
	tfState     string
	tfEndpoint  string
	simDataDir  string
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

	stateDir, err := os.MkdirTemp("", "sockerless-aws-tf-state-*")
	if err != nil {
		log.Fatalf("Failed to create terraform state dir: %v", err)
	}
	tfState = filepath.Join(stateDir, "terraform.tfstate")
	simDataDir = filepath.Join(stateDir, "simulator-data")
	binaryPath = filepath.Join(stateDir, "simulator-aws")
	gatewayDir := ""

	if configured := os.Getenv("SOCKERLESS_AWS_SIMULATOR_BINARY"); configured != "" {
		binaryPath = requireExecutable(configured, "AWS Terraform tests")
	} else {
		simDir, _ := filepath.Abs("..")
		build := exec.Command("go", "build", "-tags", "noui", "-o", binaryPath, ".")
		build.Dir = simDir
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			log.Fatalf("Failed to build simulator: %v\n%s", err, out)
		}
	}

	lambdaHandlerDir, _ := filepath.Abs("../../testdata/lambda-runtime-handler")
	buildGoScratchImage("sockerless-lambda-runtime-handler:test", lambdaHandlerDir, "lambda-runtime-handler", nativeDockerPlatform())
	containerCommandDir, _ := filepath.Abs("../../testdata/container-command")
	buildGoScratchImage("sockerless-container-command:test", containerCommandDir, "container-command", nativeDockerPlatform())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free port: %v", err)
	}
	simPort = ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if err := startTerraformSimulator(); err != nil {
		log.Fatalf("Failed to start simulator: %v", err)
	}
	reapSubprocessesOnSignal()

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", simPort)
	tfEndpoint = baseURL

	if err := waitForHealth(baseURL + "/health"); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Simulator did not become healthy: %v", err)
	}

	if os.Getenv("SOCKERLESS_TF_HTTPS_GATEWAY") == "1" {
		gatewayDir, err = os.MkdirTemp("", "aws-https-gateway-*")
		if err != nil {
			simCmd.Process.Kill()
			log.Fatalf("Failed to create HTTPS gateway state dir: %v", err)
		}

		repoRoot, err := filepath.Abs("../..")
		if err != nil {
			simCmd.Process.Kill()
			log.Fatalf("Failed to resolve repository root: %v", err)
		}
		caddyBin := os.Getenv("CADDY")
		if caddyBin == "" {
			caddyBin = "caddy"
		}
		caddyBin = requireExecutable(caddyBin, "AWS Terraform HTTPS gateway tests")

		gatewayPorts := reserveTCPPorts(2)
		gatewayPort = gatewayPorts[0]
		gatewayAdminPort := gatewayPorts[1]
		caCertFile = filepath.Join(gatewayDir, "data", "caddy", "pki", "authorities", "local", "root.crt")
		gatewayCmd = exec.Command(caddyBin, "run", "--config", filepath.Join(repoRoot, "make", "https-gateway", "Caddyfile"), "--adapter", "caddyfile")
		gatewayCmd.Env = append(os.Environ(),
			fmt.Sprintf("XDG_DATA_HOME=%s", filepath.Join(gatewayDir, "data")),
			fmt.Sprintf("XDG_CONFIG_HOME=%s", filepath.Join(gatewayDir, "config")),
			fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_PORT=%d", gatewayPort),
			fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_ADMIN_PORT=%d", gatewayAdminPort),
			fmt.Sprintf("SOCKERLESS_AWS_SIM_PORT=%d", simPort),
			"SOCKERLESS_GCP_SIM_PORT=1",
			"SOCKERLESS_AZURE_SIM_PORT=1",
			fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_DEFAULT_SIM_PORT=%d", simPort),
		)
		gatewayCmd.Stdout = os.Stdout
		gatewayCmd.Stderr = os.Stderr
		// Own process group, reaped the same way as the simulator.
		gatewayCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := gatewayCmd.Start(); err != nil {
			simCmd.Process.Kill()
			log.Fatalf("Failed to start HTTPS gateway: %v", err)
		}

		tfEndpoint = fmt.Sprintf("https://localhost:%d", gatewayPort)
		requireHTTPSURL(tfEndpoint, "AWS Terraform HTTPS gateway endpoint")
		if err := waitForFile(caCertFile, 10*time.Second); err != nil {
			gatewayCmd.Process.Kill()
			simCmd.Process.Kill()
			log.Fatalf("HTTPS gateway did not publish its local CA: %v", err)
		}
		if err := waitForHTTPSHealth(tfEndpoint+"/health", caCertFile); err != nil {
			gatewayCmd.Process.Kill()
			simCmd.Process.Kill()
			log.Fatalf("HTTPS gateway did not become healthy: %v", err)
		}
	}

	code := m.Run()
	reapGroup(gatewayCmd)
	reapGroup(simCmd)
	if err := cleanupTerraformSimulatorState(simDataDir); err != nil {
		log.Printf("Failed to remove Terraform simulator workloads: %v", err)
		code = 1
	}
	if gatewayDir != "" {
		if err := os.RemoveAll(gatewayDir); err != nil {
			log.Printf("Failed to remove HTTPS gateway state: %v", err)
			code = 1
		}
	}
	if err := os.RemoveAll(stateDir); err != nil {
		log.Printf("Failed to remove Terraform state: %v", err)
		code = 1
	}
	os.Exit(code)
}

func cleanupTerraformSimulatorState(stateDir string) error {
	stateID := fmt.Sprintf("%x", sha256.Sum256([]byte("aws\x00"+stateDir)))
	filter := "label=sockerless-sim-state=" + stateID
	containerIDs, err := dockerObjectIDs("ps", "-aq", "--filter", filter)
	if err != nil {
		return fmt.Errorf("list state-owned workload containers: %w", err)
	}
	for _, id := range containerIDs {
		if out, err := exec.Command("docker", "rm", "--force", "--volumes", id).CombinedOutput(); err != nil {
			return fmt.Errorf("remove state-owned workload container %s: %w: %s", id, err, out)
		}
	}
	networkIDs, err := dockerObjectIDs("network", "ls", "-q", "--filter", filter)
	if err != nil {
		return fmt.Errorf("list state-owned workload networks: %w", err)
	}
	for _, id := range networkIDs {
		if out, err := exec.Command("docker", "network", "rm", id).CombinedOutput(); err != nil {
			return fmt.Errorf("remove state-owned workload network %s: %w: %s", id, err, out)
		}
	}
	return nil
}

func dockerObjectIDs(args ...string) ([]string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return strings.Fields(string(out)), nil
}

func startTerraformSimulator() error {
	simCmd = exec.Command(binaryPath)
	simCmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", simPort),
		"SIM_DNS_PORT=0",
		"SIM_PERSIST=true",
		"SIM_DATA_DIR="+simDataDir,
	)
	simCmd.Stdout = os.Stdout
	simCmd.Stderr = os.Stderr
	// Own process group so the whole simulator subtree is reaped with one
	// kill(-pgid) — including on Ctrl-C / the go-test hard-timeout SIGQUIT,
	// which aborts the binary without running the post-m.Run() cleanup below.
	simCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := simCmd.Start(); err != nil {
		return err
	}
	return waitForHealth(fmt.Sprintf("http://127.0.0.1:%d/health", simPort))
}

func restartTerraformSimulator(t *testing.T) {
	t.Helper()
	reapGroup(simCmd)
	require.NoError(t, startTerraformSimulator())
}

// reapGroup gives a Setpgid'd subprocess group time to shut down cleanly before
// forcing it down. The simulator's SIGTERM path removes workload containers
// and networks, so normal test completion must not bypass it.
func reapGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

// reapSubprocessesOnSignal kills the simulator and HTTPS gateway process groups
// when the test binary is aborted by Ctrl-C or the go-test hard-timeout
// SIGQUIT. The normal-exit cleanup after m.Run() does not run on those paths,
// so without this the subprocesses would orphan.
func reapSubprocessesOnSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-ch
		if gatewayCmd != nil && gatewayCmd.Process != nil {
			_ = syscall.Kill(-gatewayCmd.Process.Pid, syscall.SIGKILL)
		}
		if simCmd != nil && simCmd.Process != nil {
			_ = syscall.Kill(-simCmd.Process.Pid, syscall.SIGKILL)
		}
		// Restore default disposition and re-deliver so the exit status
		// reflects the terminating signal.
		signal.Reset(sig.(syscall.Signal))
		_ = syscall.Kill(syscall.Getpid(), sig.(syscall.Signal))
	}()
}

func waitForHealth(url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
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

func nativeDockerPlatform() string {
	return "linux/" + runtime.GOARCH
}

func buildGoScratchImage(imageName, sourceDir, binaryName, platform string) {
	buildDir, err := os.MkdirTemp("", "sockerless-aws-image-*")
	if err != nil {
		log.Fatalf("Failed to create image build dir: %v", err)
	}
	defer os.RemoveAll(buildDir)

	binaryPath := filepath.Join(buildDir, binaryName)
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Dir = sourceDir
	build.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH="+runtime.GOARCH,
	)
	if out, err := build.CombinedOutput(); err != nil {
		log.Fatalf("Failed to build %s binary: %v\n%s", binaryName, err, out)
	}

	dockerfile := fmt.Sprintf(`FROM scratch
COPY %s /usr/local/bin/%s
ENTRYPOINT ["/usr/local/bin/%s"]
`, binaryName, binaryName, binaryName)
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
		log.Fatalf("Failed to build %s Docker image: %v\n%s", binaryName, err, out)
	}
}

func terraformCmd(args ...string) *exec.Cmd {
	args = terraformArgs(args...)
	cmd := exec.Command("terraform", args...)
	cmd.Dir = filepath.Dir(mustAbs("main.tf"))
	// Put terraform and the provider-plugin subprocesses it spawns into their
	// own process group so runTimed can reap the whole tree with one
	// kill(-pgid). Without this, a timed-out command leaves orphaned, spinning
	// provider plugins (reparented to init) that starve later runs into
	// cascading timeouts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("TF_VAR_endpoint=%s", tfEndpoint),
	)
	if caCertFile != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SSL_CERT_FILE=%s", caCertFile))
	}
	// Pass through TF_LOG / TF_LOG_PATH when the operator wants
	// terraform-provider-aws's request/response trace (debugging sim
	// handler gaps surfaced by the provider's call sequence).
	if v := os.Getenv("TF_LOG"); v != "" {
		cmd.Env = append(cmd.Env, "TF_LOG="+v)
	}
	if v := os.Getenv("TF_LOG_PATH"); v != "" {
		cmd.Env = append(cmd.Env, "TF_LOG_PATH="+v)
	}
	return cmd
}

func terraformArgs(args ...string) []string {
	if len(args) == 0 || tfState == "" {
		return args
	}
	if args[0] == "init" {
		out := make([]string, 0, len(args)+2)
		out = append(out, args[0], "-backend-config=path="+tfState, "-reconfigure")
		out = append(out, args[1:]...)
		return out
	}
	return args
}

func mustAbs(name string) string {
	p, err := filepath.Abs(name)
	if err != nil {
		log.Fatal(err)
	}
	return p
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

// reserveTCPPorts returns n distinct ports free for the WILDCARD bind the
// gateway performs. Probing one at a time on 127.0.0.1 got both halves wrong:
// a port free on loopback is not necessarily free for a ":port" bind, and each
// probe released its port before returning it, so the next probe could be
// handed the one just released. Holding every listener open until all n are
// chosen makes them distinct by construction.
func reserveTCPPorts(n int) []int {
	listeners := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)
	for range n {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			log.Fatalf("Failed to reserve port: %v", err)
		}
		listeners = append(listeners, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range listeners {
		if err := ln.Close(); err != nil {
			log.Fatalf("Failed to close port reservation: %v", err)
		}
	}
	return ports
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
		log.Fatalf("%s must use HTTPS when SOCKERLESS_TF_HTTPS_GATEWAY=1, got %q", purpose, raw)
	}
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

func waitForHTTPSHealth(raw, caCert string) error {
	client, err := trustedHTTPClient(caCert)
	if err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		resp, err := client.Get(raw)
		if err == nil && resp.StatusCode == 200 {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", raw)
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
