package gcp_tf_test

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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	baseURL      string
	simCmd       *exec.Cmd
	gatewayCmd   *exec.Cmd
	binaryPath   string
	simPort      int
	gatewayPort  int
	grpcEndpoint string
	caCertFile   string
	tfEndpoint   string
	// accessToken is a real simulator-minted OAuth2 access token the terraform
	// google provider presents to the data plane; set once in TestMain.
	accessToken string
)

// mintSimAccessToken fetches an access token from the simulator's OAuth2 token
// endpoint — the same JWT-bearer grant a real terraform google provider obtains
// — so provider requests authenticate against the data plane exactly as they
// would against Google, differing only in the endpoint coordinate.
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

	binaryPath, _ = filepath.Abs("../simulator-gcp")

	simDir, _ := filepath.Abs("..")
	build := exec.Command("go", "build", "-tags", "noui", "-o", binaryPath, ".")
	build.Dir = simDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		log.Fatalf("Failed to build simulator: %v\n%s", err, out)
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
		ln.Close()
		log.Fatalf("Failed to find free gRPC port: %v", err)
	}
	simPort = ln.Addr().(*net.TCPAddr).Port
	grpcPort := ln2.Addr().(*net.TCPAddr).Port
	grpcEndpoint = fmt.Sprintf("127.0.0.1:%d", grpcPort)
	ln.Close()
	ln2.Close()

	simCmd = exec.Command(binaryPath)
	simCmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", simPort),
		fmt.Sprintf("SIM_GCP_GRPC_PORT=%d", grpcPort),
	)
	simCmd.Stdout = os.Stdout
	simCmd.Stderr = os.Stderr
	if err := simCmd.Start(); err != nil {
		log.Fatalf("Failed to start simulator: %v", err)
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", simPort)
	tfEndpoint = baseURL

	if err := waitForHealth(baseURL + "/health"); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Simulator did not become healthy: %v", err)
	}

	// The data plane verifies the bearer on every request, so the terraform
	// google provider must present a token the simulator signed. Mint one from
	// the simulator's own token endpoint (reachable directly over plain HTTP
	// even when the provider talks through the HTTPS gateway).
	var tokenErr error
	accessToken, tokenErr = mintSimAccessToken(baseURL)
	if tokenErr != nil {
		simCmd.Process.Kill()
		log.Fatalf("Failed to mint simulator access token: %v", tokenErr)
	}

	if os.Getenv("SOCKERLESS_TF_HTTPS_GATEWAY") == "1" {
		gatewayDir, err := os.MkdirTemp("", "gcp-https-gateway-*")
		if err != nil {
			simCmd.Process.Kill()
			log.Fatalf("Failed to create HTTPS gateway state dir: %v", err)
		}
		defer os.RemoveAll(gatewayDir)

		repoRoot, err := filepath.Abs("../..")
		if err != nil {
			simCmd.Process.Kill()
			log.Fatalf("Failed to resolve repository root: %v", err)
		}
		caddyBin := os.Getenv("CADDY")
		if caddyBin == "" {
			caddyBin = "caddy"
		}
		caddyBin = requireExecutable(caddyBin, "GCP Terraform HTTPS gateway tests")

		gatewayPort = freeTCPPort()
		gatewayAdminPort := freeTCPPort()
		caCertFile = filepath.Join(gatewayDir, "data", "caddy", "pki", "authorities", "local", "root.crt")
		gatewayCmd = exec.Command(caddyBin, "run", "--config", filepath.Join(repoRoot, "make", "https-gateway", "Caddyfile"), "--adapter", "caddyfile")
		gatewayCmd.Env = append(os.Environ(),
			fmt.Sprintf("XDG_DATA_HOME=%s", filepath.Join(gatewayDir, "data")),
			fmt.Sprintf("XDG_CONFIG_HOME=%s", filepath.Join(gatewayDir, "config")),
			fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_PORT=%d", gatewayPort),
			fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_ADMIN_PORT=%d", gatewayAdminPort),
			"SOCKERLESS_AWS_SIM_PORT=1",
			fmt.Sprintf("SOCKERLESS_GCP_SIM_PORT=%d", simPort),
			"SOCKERLESS_AZURE_SIM_PORT=1",
			fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_DEFAULT_SIM_PORT=%d", simPort),
		)
		gatewayCmd.Stdout = os.Stdout
		gatewayCmd.Stderr = os.Stderr
		if err := gatewayCmd.Start(); err != nil {
			simCmd.Process.Kill()
			log.Fatalf("Failed to start HTTPS gateway: %v", err)
		}

		tfEndpoint = fmt.Sprintf("https://localhost:%d", gatewayPort)
		requireHTTPSURL(tfEndpoint, "GCP Terraform HTTPS gateway endpoint")
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
	if gatewayCmd != nil {
		gatewayCmd.Process.Kill()
		gatewayCmd.Wait()
	}
	simCmd.Process.Kill()
	simCmd.Wait()
	os.Exit(code)
}

func waitForHealth(url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
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

func terraformCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("terraform", args...)
	cmd.Dir = filepath.Dir(mustAbs("main.tf"))
	// Own process group so runTimed can reap terraform + its provider-plugin
	// grandchildren with one kill(-pgid); otherwise a timed-out command leaves
	// orphaned, spinning plugins that starve later runs into cascading timeouts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BIGTABLE_EMULATOR_HOST=%s", grpcEndpoint),
		fmt.Sprintf("TF_VAR_endpoint=%s", tfEndpoint),
		fmt.Sprintf("TF_VAR_access_token=%s", accessToken),
	)
	if caCertFile != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SSL_CERT_FILE=%s", caCertFile))
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
