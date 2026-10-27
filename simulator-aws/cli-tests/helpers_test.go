package aws_cli_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/baseimage"
	"github.com/e6qu/sockerless-cloud/testutil/freeports"
	"github.com/e6qu/sockerless-cloud/testutil/scratchimage"
	"github.com/e6qu/sockerless-cloud/testutil/simready"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
)

// signRawSigV4 signs a hand-built request with SigV4 under the test/test
// us-east-1 credential every client signs with: the awsJson and awsQuery
// control plane at POST / requires a valid signature, as AWS does. The
// cli-tests module has no aws-sdk-go-v2 dependency, so this signs with the
// standard library. Set every signed header on req before calling.
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
	diagnosticsAddr        string
	awsCLIVersion          string
	provisionedToolDirs    []string
)

// requiredAWSCLIOperations are the subcommands this suite runs that only a
// recent botocore knows. installLatestAWSCLI provisions a CLI that lacks any
// of them, and one still lacking after provisioning fails the suite. Add a row
// whenever a test drives a newly launched operation.
var requiredAWSCLIOperations = [][2]string{
	{"kinesis", "update-stream-record-distribution-strategy"},
	{"cloudwatch", "create-resource-metrics-configuration"},
	{"cloudwatch", "get-resource-metrics-configuration"},
	{"cloudwatch", "update-resource-metrics-configuration"},
	{"cloudwatch", "delete-resource-metrics-configuration"},
	{"cloudwatch", "update-otel-enrichment"},
	{"ec2", "create-capacity-reservation-date-change-quote"},
	{"ec2", "describe-capacity-reservation-date-change-quotes"},
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
	{"rds", "create-db-capacity-reservation"},
	{"rds", "describe-db-capacity-reservations"},
	{"rds", "modify-db-capacity-reservation"},
	{"rds", "cancel-db-capacity-reservation"},
	{"elasticache", "prepare-migration"},
	{"elasticache", "stop-migration"},
}

func TestMain(m *testing.M) {
	// A killed `go test` never reaches the cleanup that stops the simulators, so
	// each one watches this pid and exits with it. Every simulator inherits
	// os.Environ().
	os.Setenv("SOCKERLESS_PARENT_PID", strconv.Itoa(os.Getpid()))
	// Use the host's aws CLI only when it knows every operation the suite drives
	// and speaks the current Amazon CloudWatch protocol; otherwise install the
	// current v2 release into a temporary directory.
	awsPath, err := exec.LookPath("aws")
	if err != nil || len(missingAWSCLIOperations(awsPath)) > 0 || !cloudWatchCLIUsesAwsJSON(awsPath) {
		awsPath = installLatestAWSCLI()
	}

	// Put the chosen CLI's directory first on PATH for awsCLI(); the host-CLI case
	// needs it too.
	if dir := filepath.Dir(awsPath); dir != "" {
		os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}

	out, err := exec.Command(awsPath, "--version").CombinedOutput()
	if err != nil {
		log.Fatalf("aws CLI at %s does not run: %v\n%s", awsPath, err, out)
	}
	awsCLIVersion = strings.TrimSpace(string(out))

	// A suite that skipped these surfaces would report green while testing
	// nothing.
	if missing := missingAWSCLIOperations(awsPath); len(missing) > 0 {
		log.Fatalf("%s (%s) lacks required operation(s) %v — the cli-tests drive them unconditionally. Install a newer AWS CLI v2.",
			awsCLIVersion, awsPath, missing)
	}
	if !cloudWatchCLIUsesAwsJSON(awsPath) {
		log.Fatalf("%s (%s) still sends Amazon CloudWatch over the legacy awsQuery protocol; the simulator serves the awsJson1.0 surface the current API uses. Install a newer AWS CLI v2.",
			awsCLIVersion, awsPath)
	}
	ensureSessionManagerPlugin()

	// Each suite builds its simulator into its own path: the suites share one
	// working tree, so one suite's `go build -o` would overwrite the binary
	// another runs.
	if configuredBinary := os.Getenv("SOCKERLESS_AWS_SIMULATOR_BINARY"); configuredBinary != "" {
		var err error
		binaryPath, err = filepath.Abs(configuredBinary)
		if err != nil {
			log.Fatalf("Failed to resolve SOCKERLESS_AWS_SIMULATOR_BINARY: %v", err)
		}
		if info, err := os.Stat(binaryPath); err != nil {
			log.Fatalf("SOCKERLESS_AWS_SIMULATOR_BINARY is not readable: %v", err)
		} else if info.IsDir() || info.Mode()&0111 == 0 {
			log.Fatalf("SOCKERLESS_AWS_SIMULATOR_BINARY is not an executable file: %s", binaryPath)
		}
	} else {
		binaryPath, _ = filepath.Abs("../.build/cli-tests/simulator-aws")
		if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
			log.Fatalf("Failed to create simulator build dir: %v", err)
		}
		simDir, _ := filepath.Abs("..")
		build := exec.Command("go", "build", "-o", binaryPath, ".")
		build.Dir = simDir
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			log.Fatalf("Failed to build simulator: %v\n%s", err, out)
		}
	}

	workloadPlatform := scratchimage.NativePlatform()

	evalDir, _ := filepath.Abs("../../testdata/eval-arithmetic")
	evalImageName = "sockerless-eval-arithmetic:aws-cli"
	if err := scratchimage.Build(evalImageName, evalDir, "eval-arithmetic", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	lambdaHandlerDir, _ := filepath.Abs("../../testdata/lambda-runtime-handler")
	lambdaHandlerImageName = "sockerless-lambda-runtime-handler:aws-cli"
	if err := scratchimage.Build(lambdaHandlerImageName, lambdaHandlerDir, "lambda-runtime-handler", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	containerCommandDir, _ := filepath.Abs("../../testdata/container-command")
	containerCommandImage = "sockerless-container-command:aws-cli"
	if err := scratchimage.Build(containerCommandImage, containerCommandDir, "container-command", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	ports, err := freeports.Pick(2)
	if err != nil {
		log.Fatalf("Failed to find free API and diagnostics ports: %v", err)
	}
	port := ports[0]
	simulatorPort = port
	diagnosticsAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(ports[1]))
	persistenceDir, err = os.MkdirTemp("", "sockerless-aws-cli-state-")
	if err != nil {
		log.Fatalf("Failed to create simulator persistence directory: %v", err)
	}

	simCmd = newCLISimulatorCommand()
	simCmd.Stdout = os.Stdout
	if err := simready.Start(simCmd, os.Stderr); err != nil {
		shutdownSimulator(simCmd)
		log.Fatalf("Simulator did not become healthy: %v", err)
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

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
		"SIM_DIAGNOSTICS_ADDR="+diagnosticsAddr,
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
	if err := simready.Start(simCmd, os.Stderr); err != nil {
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

// awsCLIHostPrefixed is awsCLI for an operation with a modeled endpoint host
// prefix (Cloud Map's `data-`, Step Functions' `sync-`). botocore signs the
// prefixed host, which has to reach the simulator: `*.localhost` resolves to
// 127.0.0.1 on Linux but not on macOS, and the CLI has no resolver hook, so
// the request goes through the proxy coordinate every HTTP client honours.
// botocore's request and signature stay unchanged; only where the bytes land
// differs, as behind a corporate egress proxy.
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

// runCLI runs a command that must succeed and returns its stdout. The CLI
// writes warnings and notices to stderr, which would corrupt a JSON or value
// comparison; stderr is reported only when the command fails.
func runCLI(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	// The CLI writes its --debug log to stderr, so a call that never returns
	// still says which request it sent and what it last read.
	cmd.Args = append(cmd.Args, "--debug")
	var stdout, stderr lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	switch err := runCLIBounded(t, cmd, cliBound(t, cmd)); {
	case errors.Is(err, errCLIBoundExceeded):
		t.Fatalf("CLI command did not finish within its bound\nCommand: %s\nSimulator in-flight requests:\n%s\nSimulator goroutines:\n%s\nCLI --debug output (last %d bytes):\n%s",
			strings.Join(cmd.Args, " "), simulatorDiagnostics("/debug/inflight"),
			simulatorDiagnostics("/debug/pprof/goroutine?debug=1"), cliDebugTail, tail(stderr.String(), cliDebugTail))
	case err != nil:
		t.Fatalf("CLI command failed: %v\nCommand: %s\nStdout: %s\nStderr (last %d bytes):\n%s",
			err, strings.Join(cmd.Args, " "), stdout.String(), cliDebugTail, tail(stderr.String(), cliDebugTail))
	}
	return stdout.String()
}

// cliCommandBound caps one CLI call. The test binary's -timeout panics with
// goroutine stacks and nothing from the CLI or the simulator, so the bound
// fires early enough to collect both.
const cliCommandBound = 60 * time.Second

const cliDiagnosticsMargin = 30 * time.Second

// cliDebugTail is how much of the CLI's --debug log a failure carries; the
// log's end holds the request in flight and the error.
const cliDebugTail = 16 << 10

var errCLIBoundExceeded = errors.New("CLI command exceeded its bound")

const cmdWaitDelay = 5 * time.Second

// cliBound is cliCommandBound or the test's deadline less
// cliDiagnosticsMargin, whichever comes first. An `aws <service> wait` call
// polls on its waiter's delay (30 seconds for Amazon RDS) and fails on its own
// once the attempts run out, so only the deadline bounds it: cliCommandBound
// would cut a waiter off at its second poll.
func cliBound(t *testing.T, cmd *exec.Cmd) time.Duration {
	bound := cliCommandBound
	if len(cmd.Args) > 2 && cmd.Args[2] == "wait" {
		bound = time.Duration(math.MaxInt64)
	}
	if deadline, ok := t.Deadline(); ok {
		if remaining := time.Until(deadline) - cliDiagnosticsMargin; remaining < bound {
			bound = max(remaining, time.Second)
		}
	}
	return bound
}

// runCLIBounded runs cmd in its own process group for at most bound. Past the
// bound it logs the simulator's diagnostics while they still describe the
// stalled call, kills the group and reports errCLIBoundExceeded.
func runCLIBounded(t *testing.T, cmd *exec.Cmd, bound time.Duration) error {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A descendant holding the output pipes keeps Wait reading after the CLI
	// exits; WaitDelay ends that.
	cmd.WaitDelay = cmdWaitDelay
	if err := cmd.Start(); err != nil {
		t.Fatalf("CLI command failed to start: %v\nCommand: %s", err, strings.Join(cmd.Args, " "))
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case err := <-exited:
		return err
	case <-timer.C:
	}
	inflight := simulatorDiagnostics("/debug/inflight")
	goroutines := simulatorDiagnostics("/debug/pprof/goroutine?debug=1")
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-exited
	t.Logf("CLI command exceeded %s; simulator in-flight requests at that moment:\n%s\nSimulator goroutines:\n%s", bound, inflight, goroutines)
	return errCLIBoundExceeded
}

func simulatorDiagnostics(path string) string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + diagnosticsAddr + path)
	if err != nil {
		return fmt.Sprintf("(read %s: %v)", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("(read %s: %v)", path, err)
	}
	return string(body)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// lockedBuffer is shared by the command's copy goroutines and a test that gives
// up on the command.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runCLIExpectError runs a command that must fail and returns both streams: the
// CLI reports the service's error on stderr.
func runCLIExpectError(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	var combined lockedBuffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	switch err := runCLIBounded(t, cmd, cliBound(t, cmd)); {
	case errors.Is(err, errCLIBoundExceeded):
		t.Fatalf("CLI command did not finish within its bound\nCommand: %s\nOutput: %s", strings.Join(cmd.Args, " "), combined.String())
	case err == nil:
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

func missingAWSCLIOperations(awsPath string) []string {
	var missing []string
	for _, op := range requiredAWSCLIOperations {
		if !awsCLIHasOperation(awsPath, op[0], op[1]) {
			missing = append(missing, op[0]+" "+op[1])
		}
	}
	return missing
}

// cloudWatchCLIUsesAwsJSON reports whether awsPath sends Amazon CloudWatch
// operations over awsJson1.0 (X-Amz-Target GraniteServiceVersion20100801.<Op>),
// the protocol the service and the simulator's router use; a botocore that
// still emits awsQuery would miss them. list-metrics takes no required
// arguments, so botocore builds and logs the request against an unreachable
// port and the probe reads the --debug log; every CloudWatch operation shares
// the protocol.
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

func cliServiceRole(t *testing.T, principal string) string {
	t.Helper()
	name := uniquename.New(strings.SplitN(principal, ".", 2)[0])
	var created struct {
		Role struct {
			Arn string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-role", "--role-name", name, "--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"`+principal+`"},"Action":"sts:AssumeRole"}]}`)), &created)
	t.Cleanup(func() { _ = awsCLI("iam", "delete-role", "--role-name", name).Run() })
	return created.Role.Arn
}

// cliStatesRole creates a uniquely named role AWS Step Functions may assume,
// allowed actions on every resource, deleted when the test ends.
func cliStatesRole(t *testing.T, actions ...string) string {
	t.Helper()
	return cliRoleAllowing(t, "states.amazonaws.com", actions...)
}

// cliRoleAllowing creates a uniquely named role the service principal may
// assume, allowed actions on every resource, deleted when the test ends.
func cliRoleAllowing(t *testing.T, principal string, actions ...string) string {
	t.Helper()
	role := cliServiceRole(t, principal)
	name := role[strings.LastIndex(role, "/")+1:]
	allowed, err := json.Marshal(actions)
	if err != nil {
		t.Fatal(err)
	}
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", name, "--policy-name", "actions", "--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":`+string(allowed)+`,"Resource":"*"}]}`))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-role-policy", "--role-name", name, "--policy-name", "actions").Run() })
	return role
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
		// The upstream archive holds the binary at aws/dist/aws, beside the libraries
		// it loads.
		installDir = filepath.Join(binDir, "aws", "dist")
	default:
		log.Fatalf("Unsupported OS for automatic aws CLI install: %s", runtime.GOOS)
	}

	awsBin := filepath.Join(installDir, "aws")
	if _, err := os.Stat(awsBin); err != nil {
		log.Fatalf("aws CLI binary not found after install at %s: %v", awsBin, err)
	}

	os.Setenv("PATH", installDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return awsBin
}

// ensureSessionManagerPlugin installs the AWS Systems Manager Session Manager
// plugin when the host lacks it: the AWS CLI launches the plugin before it
// sends ecs execute-command, even for a call the control plane rejects.
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

// gluePythonShellImage is the image the simulator starts an AWS Glue Python
// shell job run in.
const gluePythonShellImage = "mirror.gcr.io/library/python:3.9"

// ensureGluePythonShellImage pulls the interpreter before a test times a Glue
// job run: a slow registry inside the bounded wait would read as a job that
// never succeeded. It stays out of TestMain, which runs for every shard, so a
// registry refusal fails one test rather than every shard.
func ensureGluePythonShellImage(t *testing.T) {
	t.Helper()
	if err := baseimage.Ensure(gluePythonShellImage); err != nil {
		t.Fatalf("could not pull %s, which every AWS Glue Python shell job run executes in: %v",
			gluePythonShellImage, err)
	}
}

// nativeLambdaArchitecture is the AWS Lambda architecture of the images
// TestMain builds for the host.
func nativeLambdaArchitecture() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "x86_64"
}
