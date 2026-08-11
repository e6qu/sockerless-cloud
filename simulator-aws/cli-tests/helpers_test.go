package aws_cli_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// signRawSigV4 signs a hand-built HTTP request with SigV4 using the seed
// credential every CLI/SDK/Terraform client already signs with (test/test,
// us-east-1). Raw requests that reach a SigV4-gated chokepoint — the awsJson /
// awsQuery control plane at POST / — must carry a valid signature exactly as
// the real AWS front end requires. The cli-tests module intentionally has no
// aws-sdk-go-v2 dependency, so this signs with the standard library the same
// way a real client (and the simulator's own verifier) does. Set every signed
// header on req before calling.
func signRawSigV4(t *testing.T, req *http.Request, service string, body []byte) {
	t.Helper()
	const (
		akid   = "test"
		secret = "test"
		region = "us-east-1"
	)
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(body)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if req.Header.Get("X-Amz-Target") != "" {
		signed = append(signed, "x-amz-target")
	}
	sort.Strings(signed)

	headerValue := func(name string) string {
		switch name {
		case "host":
			return host
		case "x-amz-content-sha256":
			return payloadHash
		case "x-amz-date":
			return amzDate
		case "x-amz-target":
			return req.Header.Get("X-Amz-Target")
		default:
			return req.Header.Get(name)
		}
	}
	var canonHeaders strings.Builder
	for _, h := range signed {
		canonHeaders.WriteString(h + ":" + headerValue(h) + "\n")
	}
	signedHeaders := strings.Join(signed, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonicalRequest := strings.Join([]string{
		req.Method,
		path,
		req.URL.RawQuery,
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		akid, scope, signedHeaders, signature))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var (
	baseURL                string
	simCmd                 *exec.Cmd
	binaryPath             string
	evalImageName          string
	lambdaHandlerImageName string
	containerCommandImage  string
	tmpDir                 string
	persistenceDir         string
	simulatorPort          int
	awsCLIVersion          string
	provisionedToolDirs    []string
)

// requiredAWSCLIOperations are the AWS Command Line Interface subcommands this
// suite invokes that only a recent botocore knows. They are a *requirement*, not
// a capability probe: every one of them is exercised unconditionally, so a CLI
// that lacks any of them is provisioned (installLatestAWSCLI) before the suite
// runs, and a CLI that still lacks one after provisioning is a hard failure.
// Keep a row here whenever a test drives a newly-launched operation, so the
// requirement is declared in one place instead of degrading into a per-test skip.
var requiredAWSCLIOperations = [][2]string{
	{"ec2", "describe-account-vpc-encryption-control"},
	{"ec2", "modify-account-vpc-encryption-control"},
	{"glue", "create-glossary"},
	{"glue", "get-glossary"},
	{"glue", "update-glossary"},
	{"glue", "delete-glossary"},
	{"glue", "list-glossaries"},
	{"glue", "create-glossary-term"},
	{"glue", "get-glossary-term"},
	{"glue", "update-glossary-term"},
	{"glue", "delete-glossary-term"},
	{"glue", "list-glossary-terms"},
	{"glue", "associate-glossary-terms"},
	{"glue", "disassociate-glossary-terms"},
	{"glue", "put-form-type"},
	{"glue", "get-form-type"},
	{"glue", "delete-form-type"},
	{"glue", "list-form-types"},
	{"glue", "put-asset-type"},
	{"glue", "get-asset-type"},
	{"glue", "delete-asset-type"},
	{"glue", "list-asset-types"},
	{"glue", "put-asset"},
	{"glue", "get-asset"},
	{"glue", "update-asset"},
	{"glue", "delete-asset"},
	{"glue", "search-assets"},
	{"glue", "put-attachment"},
	{"glue", "delete-attachment"},
	{"glue", "list-iterable-forms"},
	{"glue", "batch-get-iterable-forms"},
	{"glue", "batch-get-data-quality-ruleset-evaluation-run"},
	{"cloudfront", "list-connection-groups"},
	{"cloudfront", "create-connection-function"},
	{"cloudwatch", "put-alarm-mute-rule"},
	{"cloudwatch", "put-managed-insight-rules"},
	{"cloudwatch", "get-insight-rule-report"},
	{"cloudwatch", "get-metric-widget-image"},
	{"cloudwatch", "put-log-alarm"},
	{"logs", "put-storage-tier-policy"},
	{"logs", "get-storage-tier-policy"},
	{"logs", "put-syslog-configuration"},
	{"logs", "list-syslog-configurations"},
	{"logs", "delete-syslog-configuration"},
	{"acm", "tag-resource"},
	{"ssm", "create-cloud-connector"},
	{"s3api", "put-object-annotation"},
	{"s3api", "update-bucket-metadata-annotation-table-configuration"},
	{"lambda", "checkpoint-durable-execution"},
	{"lambda", "send-durable-execution-callback-heartbeat"},
	{"lambda", "send-durable-execution-callback-success"},
}

func TestMain(m *testing.M) {
	// Some CI / host images ship an AWS Command Line Interface that predates
	// simulator-tested surfaces. Use the host binary only when it knows every
	// operation the suite drives and speaks the current Amazon CloudWatch
	// protocol; otherwise install the current v2 release into a temporary
	// directory so the suite always runs against a capable client.
	awsPath, err := exec.LookPath("aws")
	if err != nil || len(missingAWSCLIOperations(awsPath)) > 0 || !cloudWatchCLIUsesAwsJSON(awsPath) {
		awsPath = installLatestAWSCLI()
	}

	// Prepend the chosen CLI's directory to PATH so awsCLI() picks it up.
	// installLatestAWSCLI already does this, but ensure consistency for the
	// host-CLI case too.
	if dir := filepath.Dir(awsPath); dir != "" {
		os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}

	out, err := exec.Command(awsPath, "--version").CombinedOutput()
	if err != nil {
		log.Fatalf("aws CLI at %s does not run: %v\n%s", awsPath, err, out)
	}
	awsCLIVersion = strings.TrimSpace(string(out))

	// The provisioned CLI must satisfy every requirement. Failing here is the
	// point: a suite that silently skipped these surfaces would report green
	// while testing nothing.
	if missing := missingAWSCLIOperations(awsPath); len(missing) > 0 {
		log.Fatalf("%s (%s) lacks required operation(s) %v — the cli-tests drive them unconditionally. Install a newer AWS CLI v2.",
			awsCLIVersion, awsPath, missing)
	}
	if !cloudWatchCLIUsesAwsJSON(awsPath) {
		log.Fatalf("%s (%s) still sends Amazon CloudWatch over the legacy awsQuery protocol; the simulator serves the awsJson1.0 surface the current API uses. Install a newer AWS CLI v2.",
			awsCLIVersion, awsPath)
	}
	ensureSessionManagerPlugin()

	// Build simulator
	binaryPath, _ = filepath.Abs("../simulator-aws")
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

	lambdaHandlerDir, _ := filepath.Abs("../../testdata/lambda-runtime-handler")
	lambdaHandlerImageName = "sockerless-lambda-runtime-handler:test"
	buildGoScratchImage(lambdaHandlerImageName, lambdaHandlerDir, "lambda-runtime-handler", workloadPlatform)

	containerCommandDir, _ := filepath.Abs("../../testdata/container-command")
	containerCommandImage = "sockerless-container-command:test"
	buildGoScratchImage(containerCommandImage, containerCommandDir, "container-command", workloadPlatform)

	// Find free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	simulatorPort = port
	persistenceDir, err = os.MkdirTemp("", "sockerless-aws-cli-state-")
	if err != nil {
		log.Fatalf("Failed to create simulator persistence directory: %v", err)
	}

	// Start simulator
	simCmd = newCLISimulatorCommand()
	simCmd.Stdout = os.Stdout
	simCmd.Stderr = os.Stderr
	if err := simCmd.Start(); err != nil {
		log.Fatalf("Failed to start simulator: %v", err)
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	if err := waitForHealth(baseURL + "/health"); err != nil {
		shutdownSimulator(simCmd)
		log.Fatalf("Simulator did not become healthy: %v", err)
	}

	// Create tmp dir for test files
	tmpDir, _ = filepath.Abs("tmp")
	os.MkdirAll(tmpDir, 0755)

	code := m.Run()

	shutdownSimulator(simCmd)
	os.RemoveAll(tmpDir)
	for _, dir := range provisionedToolDirs {
		os.RemoveAll(dir)
	}
	os.RemoveAll(persistenceDir)
	os.Exit(code)
}

func newCLISimulatorCommand() *exec.Cmd {
	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", simulatorPort),
		"SIM_DNS_PORT=0",
		"SIM_PERSIST=true",
		"SIM_DATA_DIR="+persistenceDir,
	)
	return cmd
}

func restartCLISimulator(t *testing.T) {
	t.Helper()
	shutdownSimulator(simCmd)
	simCmd = newCLISimulatorCommand()
	simCmd.Stdout = os.Stdout
	simCmd.Stderr = os.Stderr
	if err := simCmd.Start(); err != nil {
		t.Fatalf("Failed to restart simulator: %v", err)
	}
	if err := waitForHealth(baseURL + "/health"); err != nil {
		shutdownSimulator(simCmd)
		t.Fatalf("Restarted simulator did not become healthy: %v", err)
	}
}

func shutdownSimulator(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
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

func awsCLI(args ...string) *exec.Cmd {
	cmd := exec.Command("aws", args...)
	cmd.Env = append(os.Environ(),
		"AWS_ENDPOINT_URL="+baseURL,
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_DEFAULT_REGION=us-east-1",
		"AWS_PAGER=",
	)
	return cmd
}

// awsCLIHostPrefixed is awsCLI for an operation that carries a modeled endpoint
// host prefix — Cloud Map's `data-`, Step Functions' `sync-`. botocore builds
// and signs the prefixed host from the endpoint URL, and the prefixed name has
// to resolve to the simulator's loopback listener. `*.localhost` resolves to
// 127.0.0.1 on Linux but not on macOS, and the CLI has no resolver hook, so the
// loopback listener is reached through the proxy coordinate every HTTP client
// honours. The request botocore emits is unchanged — same Host header, same
// SigV4 signature over it — only where the bytes land differs, exactly as for
// an operator whose AWS traffic egresses through a corporate proxy.
func awsCLIHostPrefixed(args ...string) *exec.Cmd {
	cmd := awsCLI(args...)
	cmd.Env = append(cmd.Env,
		"HTTP_PROXY="+baseURL,
		"http_proxy="+baseURL,
		"NO_PROXY=",
		"no_proxy=",
	)
	return cmd
}

func runCLI(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	const perCmdTimeout = 60 * time.Second
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

func runCLIExpectError(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	const perCmdTimeout = 60 * time.Second
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Start(); err != nil {
		t.Fatalf("CLI command failed to start: %v\nCommand: %s", err, strings.Join(cmd.Args, " "))
	}
	timer := time.AfterFunc(perCmdTimeout, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	if err := cmd.Wait(); err == nil {
		t.Fatalf("CLI command unexpectedly succeeded\nCommand: %s\nOutput: %s", strings.Join(cmd.Args, " "), combined.String())
	}
	return combined.String()
}

func awsCLIHasOperation(awsPath, service, operation string) bool {
	if awsPath == "" {
		return false
	}
	cmd := exec.Command(awsPath, service, operation, "--generate-cli-skeleton", "input")
	cmd.Env = append(os.Environ(), "AWS_PAGER=")
	return cmd.Run() == nil
}

// missingAWSCLIOperations returns the requiredAWSCLIOperations rows the given
// aws binary does not know, as "service operation" strings.
func missingAWSCLIOperations(awsPath string) []string {
	var missing []string
	for _, op := range requiredAWSCLIOperations {
		if !awsCLIHasOperation(awsPath, op[0], op[1]) {
			missing = append(missing, op[0]+" "+op[1])
		}
	}
	return missing
}

// cloudWatchCLIUsesAwsJSON reports whether the given aws binary sends Amazon
// CloudWatch operations over awsJson1.0 (X-Amz-Target
// GraniteServiceVersion20100801.<Op>), the protocol the service uses today and
// the one the simulator's awsJson router serves. A botocore old enough to still
// emit the legacy awsQuery form would reach a router these operations are not
// registered on; TestMain provisions a current CLI rather than tolerating it.
//
// list-metrics needs no required arguments, so botocore builds and logs the
// request against an unreachable port and we read only the --debug log of what
// it constructed. All CloudWatch operations share one protocol, so list-metrics
// is a faithful probe for the whole service.
func cloudWatchCLIUsesAwsJSON(awsPath string) bool {
	if awsPath == "" {
		return false
	}
	cmd := exec.Command(awsPath, "cloudwatch", "list-metrics",
		"--endpoint-url", "http://127.0.0.1:1", "--region", "us-east-1", "--debug")
	cmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID=test", "AWS_SECRET_ACCESS_KEY=test", "AWS_PAGER=")
	out, _ := cmd.CombinedOutput()
	return strings.Contains(string(out), "GraniteServiceVersion20100801.")
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

func cleanupCLIECSTask(t *testing.T, clusterName, taskArn string) {
	t.Helper()
	t.Cleanup(func() {
		runCLI(t, awsCLI("ecs", "stop-task",
			"--cluster", clusterName,
			"--task", taskArn,
			"--reason", "test cleanup",
		))
	})
}

func installLatestAWSCLI() string {
	binDir, err := os.MkdirTemp("", "sockerless-aws-cli-*")
	if err != nil {
		log.Fatalf("Failed to create aws CLI install dir: %v", err)
	}
	provisionedToolDirs = append(provisionedToolDirs, binDir)
	installDir := filepath.Join(binDir, "aws-cli")

	switch runtime.GOOS {
	case "darwin":
		pkg := filepath.Join(binDir, "AWSCLIV2.pkg")
		if out, err := exec.Command("curl", "-fsSL", "-o", pkg, "https://awscli.amazonaws.com/AWSCLIV2.pkg").CombinedOutput(); err != nil {
			log.Fatalf("Failed to download aws CLI pkg: %v\n%s", err, out)
		}
		expanded := filepath.Join(binDir, "expanded")
		if out, err := exec.Command("pkgutil", "--expand", pkg, expanded).CombinedOutput(); err != nil {
			log.Fatalf("Failed to expand aws CLI pkg: %v\n%s", err, out)
		}
		payload := filepath.Join(expanded, "aws-cli.pkg", "Payload")
		if out, err := exec.Command("tar", "-xf", payload, "-C", binDir).CombinedOutput(); err != nil {
			log.Fatalf("Failed to extract aws CLI payload: %v\n%s", err, out)
		}
		// tar extracts to aws-cli/aws relative to binDir.
	case "linux":
		zip := filepath.Join(binDir, "awscliv2.zip")
		if out, err := exec.Command("curl", "-fsSL", "-o", zip, "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip").CombinedOutput(); err != nil {
			log.Fatalf("Failed to download aws CLI zip: %v\n%s", err, out)
		}
		if out, err := exec.Command("unzip", "-q", zip, "-d", binDir).CombinedOutput(); err != nil {
			log.Fatalf("Failed to unzip aws CLI: %v\n%s", err, out)
		}
		// The upstream archive extracts to aws/aws, not aws-cli/aws. Rename it
		// so installDir always points at the directory containing the aws binary.
		extracted := filepath.Join(binDir, "aws")
		if _, err := os.Stat(extracted); err == nil {
			if err := os.Rename(extracted, installDir); err != nil {
				log.Fatalf("Failed to rename aws CLI install dir: %v", err)
			}
		}
	default:
		log.Fatalf("Unsupported OS for automatic aws CLI install: %s", runtime.GOOS)
	}

	awsBin := filepath.Join(installDir, "aws")
	if _, err := os.Stat(awsBin); err != nil {
		log.Fatalf("aws CLI binary not found after install at %s: %v", awsBin, err)
	}

	// Prepend to PATH so awsCLI() picks it up.
	os.Setenv("PATH", installDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return awsBin
}

// ensureSessionManagerPlugin provisions the real AWS Systems Manager Session
// Manager plugin when the host does not already carry it. The AWS CLI launches
// this executable before it sends ecs execute-command, so merely testing the
// control-plane rejection still requires the vendor plugin to be present.
func ensureSessionManagerPlugin() {
	if pluginPath, err := exec.LookPath("session-manager-plugin"); err == nil {
		if out, err := exec.Command(pluginPath, "--version").CombinedOutput(); err != nil {
			log.Fatalf("Session Manager plugin at %s does not run: %v\n%s", pluginPath, err, out)
		}
		return
	}

	binDir, err := os.MkdirTemp("", "sockerless-session-manager-plugin-*")
	if err != nil {
		log.Fatalf("Failed to create Session Manager plugin install dir: %v", err)
	}
	provisionedToolDirs = append(provisionedToolDirs, binDir)
	extractDir := filepath.Join(binDir, "extract")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		log.Fatalf("Failed to create Session Manager plugin extraction dir: %v", err)
	}

	switch runtime.GOOS {
	case "darwin":
		archPath := "mac"
		if runtime.GOARCH == "arm64" {
			archPath = "mac_arm64"
		}
		pkg := filepath.Join(binDir, "session-manager-plugin.pkg")
		download := "https://s3.amazonaws.com/session-manager-downloads/plugin/latest/" + archPath + "/session-manager-plugin.pkg"
		if out, err := exec.Command("curl", "-fsSL", "-o", pkg, download).CombinedOutput(); err != nil {
			log.Fatalf("Failed to download the Session Manager plugin package: %v\n%s", err, out)
		}
		expanded := filepath.Join(binDir, "expanded")
		if out, err := exec.Command("pkgutil", "--expand", pkg, expanded).CombinedOutput(); err != nil {
			log.Fatalf("Failed to expand the Session Manager plugin package: %v\n%s", err, out)
		}
		payloads, err := findFilesNamed(expanded, "Payload")
		if err != nil {
			log.Fatalf("Failed to inspect the Session Manager plugin package: %v", err)
		}
		if len(payloads) == 0 {
			log.Fatalf("Session Manager plugin package contained no Payload archive")
		}
		for _, payload := range payloads {
			if out, err := exec.Command("tar", "-xf", payload, "-C", extractDir).CombinedOutput(); err != nil {
				log.Fatalf("Failed to extract the Session Manager plugin payload: %v\n%s", err, out)
			}
		}
	case "linux":
		archPath := "ubuntu_64bit"
		if runtime.GOARCH == "arm64" {
			archPath = "ubuntu_arm64"
		}
		deb := filepath.Join(binDir, "session-manager-plugin.deb")
		download := "https://s3.amazonaws.com/session-manager-downloads/plugin/latest/" + archPath + "/session-manager-plugin.deb"
		if out, err := exec.Command("curl", "-fsSL", "-o", deb, download).CombinedOutput(); err != nil {
			log.Fatalf("Failed to download the Session Manager plugin package: %v\n%s", err, out)
		}
		if out, err := exec.Command("dpkg-deb", "-x", deb, extractDir).CombinedOutput(); err != nil {
			log.Fatalf("Failed to extract the Session Manager plugin package: %v\n%s", err, out)
		}
	default:
		log.Fatalf("Unsupported OS for automatic Session Manager plugin install: %s", runtime.GOOS)
	}

	plugins, err := findFilesNamed(extractDir, "session-manager-plugin")
	if err != nil {
		log.Fatalf("Failed to inspect the Session Manager plugin installation: %v", err)
	}
	if len(plugins) != 1 {
		log.Fatalf("Session Manager plugin package contained %d plugin binaries, want 1: %v", len(plugins), plugins)
	}
	pluginLink := filepath.Join(binDir, "session-manager-plugin")
	if err := os.Symlink(plugins[0], pluginLink); err != nil {
		log.Fatalf("Failed to expose the Session Manager plugin on PATH: %v", err)
	}
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := exec.Command(pluginLink, "--version").CombinedOutput(); err != nil {
		log.Fatalf("Provisioned Session Manager plugin does not run: %v\n%s", err, out)
	}
}

func findFilesNamed(root, name string) ([]string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == name {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, err
}
