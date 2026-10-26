package tfsim

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/httpsgateway"
	"github.com/e6qu/sockerless-cloud/testutil/simready"
)

type Env struct {
	BaseURL    string
	Endpoint   string
	State      string
	CACertFile string
	Client     *http.Client
	cmd        *exec.Cmd
	gatewayCmd *exec.Cmd
	dir        string
	extraEnv   []string
}

// RouteHostPrefixedRequests makes the simulator terraform's HTTP proxy and
// takes the fixture off the HTTPS gateway. An operation with a modeled endpoint
// host prefix (every s3control operation carries the account id) makes the
// provider sign a host like "123456789012.<endpoint host>", which resolves
// nowhere when the endpoint host is an IP literal. The provider still signs
// the host it would send AWS; only where the bytes land differs, as behind a
// corporate egress proxy. Call it before running terraform.
func (e *Env) RouteHostPrefixedRequests() {
	e.extraEnv = append(e.extraEnv,
		"HTTP_PROXY="+e.BaseURL, "http_proxy="+e.BaseURL,
		"NO_PROXY=", "no_proxy=")
}

// WithoutHTTPSGateway must precede Start for a fixture whose operations carry a
// modeled endpoint host prefix. The gateway's wildcard certificate covers one
// label of *.aws.sockerless.localhost and a prefix adds a second, which nothing
// resolves, so the provider would retry until the deadline. Such a fixture
// reaches the simulator over plain HTTP through the proxy coordinate instead.
func WithoutHTTPSGateway(t *testing.T) {
	t.Helper()
	t.Setenv("SOCKERLESS_TF_HTTPS_GATEWAY", "")
}

func Start(t *testing.T, configDir string) *Env {
	t.Helper()

	stateDir, err := os.MkdirTemp("", "sockerless-aws-tf-state-*")
	if err != nil {
		t.Fatalf("create terraform state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(stateDir) })

	binaryPath := filepath.Join(stateDir, "simulator-aws")
	simDir, err := filepath.Abs(filepath.Join(configDir, "..", ".."))
	if err != nil {
		t.Fatalf("resolve simulator dir: %v", err)
	}
	if configured := os.Getenv("SOCKERLESS_AWS_SIMULATOR_BINARY"); configured != "" {
		binaryPath = requireExecutable(t, configured, "AWS Terraform tests")
	} else {
		build := exec.Command("go", "build", "-o", binaryPath, ".")
		build.Dir = simDir
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build simulator: %v\n%s", err, out)
		}
	}

	port := reservePorts(t, 1)[0]

	cmd := exec.Command(binaryPath)
	cmd.Env = append(
		os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		"SIM_DNS_PORT=0",
	)
	cmd.Stdout = os.Stdout
	// Own process group, so t.Cleanup, the deadline watchdog and the signal
	// reaper each reap the simulator subtree with one kill(-pgid).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	startErr := simready.Start(cmd, os.Stderr)
	if cmd.Process == nil {
		t.Fatalf("start simulator: %v", startErr)
	}
	trackForReaping(cmd)
	t.Cleanup(func() {
		reapProcessGroup(cmd)
		untrackForReaping(cmd)
	})

	env := &Env{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		State:   filepath.Join(stateDir, "terraform.tfstate"),
		Client:  http.DefaultClient,
		cmd:     cmd,
		dir:     configDir,
	}
	env.Endpoint = env.BaseURL
	if startErr != nil {
		t.Fatalf("simulator did not start listening: %v", startErr)
	}
	if os.Getenv("SOCKERLESS_TF_HTTPS_GATEWAY") == "1" {
		startHTTPSGateway(t, env, stateDir, simDir, port)
	}
	env.armDeadlineReaper(t)
	return env
}

// armDeadlineReaper reaps the simulator and HTTPS gateway process groups a few
// seconds before the test deadline: go test's hard timeout skips t.Cleanup.
func (e *Env) armDeadlineReaper(t *testing.T) {
	dl, ok := t.Deadline()
	if !ok {
		return
	}
	d := time.Until(dl) - 15*time.Second
	if d <= 0 {
		return
	}
	timer := time.AfterFunc(d, func() {
		reapProcessGroup(e.gatewayCmd)
		reapProcessGroup(e.cmd)
	})
	t.Cleanup(func() { timer.Stop() })
}

func (e *Env) Terraform(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := e.run(t, args...)
	if err != nil {
		t.Fatalf("terraform %v failed: %v\n%s", args, err, out)
	}
	return out
}

// TerraformFails runs a terraform command the simulator must make fail and
// returns its combined output.
func (e *Env) TerraformFails(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := e.run(t, args...)
	if err == nil {
		t.Fatalf("terraform %v unexpectedly succeeded\n%s", args, out)
	}
	return out
}

func (e *Env) run(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	if len(args) > 0 {
		switch args[0] {
		case "init":
			// The lock beside the configuration is untracked local state; init
			// re-resolves the exactly pinned providers instead of failing on a stale one.
			args = append([]string{"init", "-upgrade"}, args[1:]...)
		case "apply", "destroy", "output", "plan", "refresh":
			out := make([]string, 0, len(args)+1)
			out = append(out, args[0], "-state="+e.State)
			out = append(out, args[1:]...)
			args = out
		}
	}
	cmd := exec.Command("terraform", args...)
	cmd.Dir = e.dir
	// Own process group, so one kill(-pgid) reaps a stuck terraform and its
	// provider plugins.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(), "TF_VAR_endpoint="+e.Endpoint)
	cmd.Env = append(cmd.Env, e.extraEnv...)
	if e.CACertFile != "" {
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+e.CACertFile)
	}
	if v := os.Getenv("TF_LOG"); v != "" {
		cmd.Env = append(cmd.Env, "TF_LOG="+v)
	}
	if v := os.Getenv("TF_LOG_PATH"); v != "" {
		cmd.Env = append(cmd.Env, "TF_LOG_PATH="+v)
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("terraform %v: failed to start: %v", args, err)
	}
	killGroup := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	// The watchdog below covers the hard timeout, which skips t.Cleanup.
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
		t.Logf("terraform %v duration=%s", args, time.Since(start).Round(time.Millisecond))
		return buf.Bytes(), err
	case <-watchdog:
		killGroup()
		t.Logf("terraform %v killed at the watchdog: %v", args, <-done)
		t.Fatalf("terraform %v timed out near the test deadline (process group killed to avoid orphans)\n%s",
			args, buf.Bytes())
		return nil, nil
	}
}

// reapProcessGroup SIGKILLs and reaps cmd's process group. It accepts a nil or
// unstarted command and repeated calls.
func reapProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

var (
	reaperMu   sync.Mutex
	reaperCmds []*exec.Cmd
	reaperOnce sync.Once
)

// trackForReaping lets the signal reaper kill cmd's process group on Ctrl-C or
// the hard-timeout SIGQUIT, neither of which runs t.Cleanup.
func trackForReaping(cmd *exec.Cmd) {
	reaperMu.Lock()
	reaperCmds = append(reaperCmds, cmd)
	reaperMu.Unlock()
	reaperOnce.Do(installSignalReaper)
}

func untrackForReaping(cmd *exec.Cmd) {
	reaperMu.Lock()
	for i, c := range reaperCmds {
		if c == cmd {
			reaperCmds = append(reaperCmds[:i], reaperCmds[i+1:]...)
			break
		}
	}
	reaperMu.Unlock()
}

func installSignalReaper() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-ch
		reaperMu.Lock()
		for _, c := range reaperCmds {
			if c != nil && c.Process != nil {
				_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			}
		}
		reaperMu.Unlock()
		// Re-deliver with the default disposition so the exit status names the
		// signal.
		if s, ok := sig.(syscall.Signal); ok {
			signal.Reset(s)
			_ = syscall.Kill(syscall.Getpid(), s)
		}
	}()
}

func startHTTPSGateway(t *testing.T, env *Env, stateDir, simDir string, simPort int) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join(simDir, ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	caddyBin := os.Getenv("CADDY")
	if caddyBin == "" {
		caddyBin = "caddy"
	}
	caddyBin = requireExecutable(t, caddyBin, "AWS Terraform HTTPS gateway tests")

	gatewayDir := filepath.Join(stateDir, "https-gateway")
	if err := os.MkdirAll(gatewayDir, 0o755); err != nil {
		t.Fatalf("create HTTPS gateway state dir: %v", err)
	}
	gatewayPorts := reservePorts(t, 2)
	gatewayPort, gatewayAdminPort := gatewayPorts[0], gatewayPorts[1]
	env.CACertFile = filepath.Join(gatewayDir, "data", "caddy", "pki", "authorities", "local", "root.crt")
	env.gatewayCmd = exec.Command(caddyBin, "run", "--config", filepath.Join(repoRoot, "make", "https-gateway", "Caddyfile"), "--adapter", "caddyfile")
	env.gatewayCmd.Env = append(os.Environ(),
		"XDG_DATA_HOME="+filepath.Join(gatewayDir, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(gatewayDir, "config"),
		fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_PORT=%d", gatewayPort),
		fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_ADMIN_PORT=%d", gatewayAdminPort),
		fmt.Sprintf("SOCKERLESS_AWS_SIM_PORT=%d", simPort),
		"SOCKERLESS_GCP_SIM_PORT=1",
		"SOCKERLESS_AZURE_SIM_PORT=1",
		fmt.Sprintf("SOCKERLESS_HTTPS_GATEWAY_DEFAULT_SIM_PORT=%d", simPort),
	)
	env.gatewayCmd.Stdout = os.Stdout
	env.gatewayCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	startErr := httpsgateway.Start(env.gatewayCmd, os.Stderr)
	if env.gatewayCmd.Process != nil {
		trackForReaping(env.gatewayCmd)
		t.Cleanup(func() {
			reapProcessGroup(env.gatewayCmd)
			untrackForReaping(env.gatewayCmd)
		})
	}
	if startErr != nil {
		t.Fatalf("start HTTPS gateway: %v", startErr)
	}

	env.Endpoint = fmt.Sprintf("https://localhost:%d", gatewayPort)
	client, err := trustedHTTPClient(env.CACertFile)
	if err != nil {
		t.Fatalf("HTTPS gateway trust: %v", err)
	}
	env.Client = client
	if err := checkHTTPSHealth(env.Endpoint+"/health", client); err != nil {
		t.Fatalf("HTTPS gateway health: %v", err)
	}
}

func checkHTTPSHealth(raw string, client *http.Client) error {
	bounded := *client
	bounded.Timeout = 2 * time.Second
	resp, err := bounded.Get(raw)
	if err != nil {
		return fmt.Errorf("GET %s: %w", raw, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", raw, resp.StatusCode)
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
	// No overall timeout, as in the SDKs' default client: an SDK retries a
	// request its client gave up on, which turns a slow create into two.
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}, nil
}

// reservePorts returns n distinct ports free for the children's wildcard
// ":<port>" bind: it probes with the same wildcard bind (a port free on
// loopback may not be), and holds every listener open until all n are chosen
// so the kernel cannot hand one port out twice. It releases them together,
// just before the children bind.
func reservePorts(t *testing.T, n int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)
	defer func() {
		for _, ln := range listeners {
			if err := ln.Close(); err != nil {
				t.Fatalf("close port reservation: %v", err)
			}
		}
	}()
	for range n {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatalf("reserve port: %v", err)
		}
		listeners = append(listeners, ln)
		tcpAddr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			t.Fatalf("port reservation address is not TCP: %T", ln.Addr())
		}
		ports = append(ports, tcpAddr.Port)
	}
	return ports
}

func requireExecutable(t *testing.T, name, purpose string) string {
	t.Helper()
	if filepath.Base(name) != name {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("%s requires executable %q: %v", purpose, name, err)
		}
		if info.IsDir() {
			t.Fatalf("%s requires executable %q, but it is a directory", purpose, name)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatalf("%s requires executable %q, but it is not executable", purpose, name)
		}
		return name
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("%s requires %q in PATH: %v", purpose, name, err)
	}
	return path
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
