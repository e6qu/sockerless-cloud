package aws_sdk_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func lambdaNodeDeploymentZip(t *testing.T, responseExpression string) []byte {
	t.Helper()
	if responseExpression == "" {
		responseExpression = "event"
	}
	return lambdaNodeSourceZip(t, fmt.Sprintf(
		"exports.handler = async (event) => { console.log(JSON.stringify(event)); return %s; };",
		responseExpression,
	))
}

func lambdaNodeSourceZip(t *testing.T, source string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	entry, err := zw.Create("index.js")
	if err != nil {
		t.Fatalf("create AWS Lambda deployment entry: %v", err)
	}
	if _, err := entry.Write([]byte(source)); err != nil {
		t.Fatalf("write AWS Lambda deployment entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close AWS Lambda deployment archive: %v", err)
	}
	return out.Bytes()
}

func lambdaPythonSourceZip(t *testing.T, source string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	entry, err := zw.Create("index.py")
	if err != nil {
		t.Fatalf("create AWS Lambda Python deployment entry: %v", err)
	}
	if _, err := entry.Write([]byte(source)); err != nil {
		t.Fatalf("write AWS Lambda Python deployment entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close AWS Lambda Python deployment archive: %v", err)
	}
	return out.Bytes()
}

func lambdaDeploymentZip(t *testing.T) []byte {
	t.Helper()
	return lambdaNodeDeploymentZip(t, "event")
}

// emptySHA256 is the hex SHA-256 of an empty body — the payload hash for a
// signed GET request with no body (Lambda's REST list/get operations).
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// signRawSigV4 signs a hand-built HTTP request with SigV4 using the seed
// credential every SDK/CLI/Terraform client already signs with (test/test,
// us-east-1). Raw requests that hit a SigV4-gated chokepoint (the awsJson /
// awsQuery control plane at POST /, the S3 REST data plane, and Lambda's REST
// control plane) must present a valid signature exactly as the real cloud
// front end requires; this is the same signature the aws-sdk-go-v2 client
// computes for those requests. Set every signed header on req before calling.
// payloadHash is the hex SHA-256 of the body (see signRawSigV4JSON) or a
// streaming sentinel such as "STREAMING-AWS4-HMAC-SHA256-PAYLOAD".
func signRawSigV4(t *testing.T, req *http.Request, service, payloadHash string) {
	t.Helper()
	signRawSigV4Creds(t, req, service, payloadHash, "test", "test")
}

// signRawSigV4Creds is signRawSigV4 with an explicit access key id and secret,
// for tests that need to sign with a real-but-wrong secret (proving the
// simulator rejects a well-formed signature that doesn't verify) rather than
// the seed admin credential.
func signRawSigV4Creds(t *testing.T, req *http.Request, service, payloadHash, akid, secret string) {
	t.Helper()
	creds := aws.Credentials{AccessKeyID: akid, SecretAccessKey: secret}
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, payloadHash, service, "us-east-1", time.Now()); err != nil {
		t.Fatalf("SigV4 sign (%s): %v", service, err)
	}
}

// signRawSigV4JSON signs a control-plane request whose payload hash is the
// SHA-256 of body — the shape awsJson/awsQuery clients sign.
func signRawSigV4JSON(t *testing.T, req *http.Request, service string, body []byte) {
	t.Helper()
	sum := sha256.Sum256(body)
	signRawSigV4(t, req, service, hex.EncodeToString(sum[:]))
}

// freeTCPUDPPort returns one numeric port that was simultaneously available
// on both protocols. Route 53 binds its configured DNS coordinate on TCP and
// UDP, so selecting a UDP-only ephemeral port can collide with another
// simulator's HTTP listener.
func freeTCPUDPPort() (int, error) {
	const attempts = 100
	for attempt := 0; attempt < attempts; attempt++ {
		tcpListener, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0, err
		}
		port := tcpListener.Addr().(*net.TCPAddr).Port
		udpListener, udpErr := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
		if udpErr != nil {
			_ = tcpListener.Close()
			continue
		}
		tcpCloseErr := tcpListener.Close()
		udpCloseErr := udpListener.Close()
		if tcpCloseErr != nil {
			return 0, tcpCloseErr
		}
		if udpCloseErr != nil {
			return 0, udpCloseErr
		}
		return port, nil
	}
	return 0, fmt.Errorf("could not find a port available on both TCP and UDP after %d attempts", attempts)
}

// freeSimulatorPortPair reserves a simulator's HTTP coordinate and its Route 53
// DNS coordinate as a pair that cannot be the same port.
//
// Selecting them with two independent probes is not enough. Each probe binds an
// ephemeral port and releases it before returning the number, so the operating
// system is free to hand the second probe the port the first just released. The
// simulator then binds its Route 53 listener on the coordinate its HTTP server
// needs and dies with "listen tcp :<port>: bind: address already in use", which
// surfaces as a health-check timeout in whichever test launched it. Holding the
// HTTP reservation open across the DNS probe removes the overlap: the DNS probe
// binds TCP too, so it cannot be handed the held port.
func freeSimulatorPortPair() (httpPort int, dnsPort int, err error) {
	holder, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, 0, err
	}
	httpPort = holder.Addr().(*net.TCPAddr).Port
	dnsPort, dnsErr := freeTCPUDPPort()
	closeErr := holder.Close()
	if dnsErr != nil {
		return 0, 0, dnsErr
	}
	if closeErr != nil {
		return 0, 0, closeErr
	}
	if httpPort == dnsPort {
		return 0, 0, fmt.Errorf("http and Route 53 coordinates collided on port %d", httpPort)
	}
	return httpPort, dnsPort, nil
}

var (
	baseURL                string
	simPort                int
	dnsPort                int
	simCmd                 *exec.Cmd
	binaryPath             string
	evalImageName          string // Docker image containing eval-arithmetic binary
	lambdaHandlerImageName string // Docker image for Lambda Runtime API test handler
	containerCommandImage  string // Docker image containing container-command binary
	ctx                    = context.Background()
)

const (
	terraformECSBaseImage = "docker.io/hashicorp/terraform:1.15.8"
	terraformECSImage     = "sockerless-terraform-aws:test"
)

func sdkConfig() aws.Config {
	return aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:  simHTTPClient,
	}
}

// simEndpoint returns the endpoint coordinate for one AWS service against the
// simulator: the service's own hostname in the `.localhost` family on the
// simulator's port. It has the same shape as the endpoint a real client
// resolves (`servicediscovery.us-east-1.amazonaws.com`), so an operation that
// carries a modeled endpoint host prefix — Cloud Map's `data-` on
// DiscoverInstances/DiscoverInstancesRevision, Step Functions' `sync-` on
// StartSyncExecution/TestState — builds and signs the real prefixed host
// (`data-servicediscovery.localhost`) instead of a prefix glued onto an IP
// literal. Only the endpoint coordinate differs from a real-cloud client; the
// request the SDK builds and signs is byte-for-byte the one AWS receives.
func simEndpoint(service string) string {
	return fmt.Sprintf("http://%s.localhost:%d", service, simPort)
}

// simHTTPClient is the SDK transport every client in this suite uses. It keeps
// the SDK's own transport defaults and replaces only name resolution: a host in
// the `.localhost` family resolves to the loopback address, which is what RFC
// 6761 mandates and what glibc does on Linux but macOS's resolver does not.
// Resolution is a coordinate, not a request property — the Host header, the
// URL and the SigV4 signature the SDK produces are untouched, so a request to
// `data-servicediscovery.localhost` carries and signs exactly that host.
var simHTTPClient = awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
	tr.DialContext = simDialContext
	tr.Proxy = simProxy
})

var simDialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

func simDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if host, port, err := net.SplitHostPort(addr); err == nil && isLocalhostName(host) {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	return simDialer.DialContext(ctx, network, addr)
}

// simProxy keeps the SDK's environment-driven proxy behaviour for every host
// except the `.localhost` family, which resolves to loopback and must never be
// routed through a proxy Go would otherwise apply to a non-`localhost` name.
func simProxy(req *http.Request) (*url.URL, error) {
	if isLocalhostName(req.URL.Hostname()) {
		return nil, nil
	}
	return http.ProxyFromEnvironment(req)
}

func isLocalhostName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// capturedRequest holds the host and Authorization header of the request an
// SDK client actually put on the wire.
type capturedRequest struct {
	host          string
	authorization string
}

// signedHeaders returns the SignedHeaders list out of a SigV4 Authorization
// header, so a test can assert which headers the signature covers.
func (c capturedRequest) signedHeaders() []string {
	for _, part := range strings.Split(c.authorization, ",") {
		part = strings.TrimSpace(part)
		if rest, ok := strings.CutPrefix(part, "SignedHeaders="); ok {
			return strings.Split(rest, ";")
		}
	}
	return nil
}

// captureSignedRequest is an SDK API option that records the final request
// after every Finalize middleware has run — the modeled endpoint host-prefix
// mutation and the SigV4 signer included. A test uses it to assert the host the
// SDK addressed and signed, which is the only way to prove an operation with a
// modeled host prefix really exercised the prefixed host.
func captureSignedRequest(rec *capturedRequest) func(*middleware.Stack) error {
	return func(stack *middleware.Stack) error {
		return stack.Finalize.Add(middleware.FinalizeMiddlewareFunc("captureSignedRequest",
			func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
				if req, ok := in.Request.(*smithyhttp.Request); ok {
					rec.host = req.URL.Host
					rec.authorization = req.Header.Get("Authorization")
				}
				return next.HandleFinalize(ctx, in)
			}), middleware.After)
	}
}

func TestMain(m *testing.M) {
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
		binaryPath, _ = filepath.Abs("../simulator-aws")
		simDir, _ := filepath.Abs("..")
		build := exec.Command("go", "build", "-tags", "noui", "-o", binaryPath, ".")
		build.Dir = simDir
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
		if out, err := build.CombinedOutput(); err != nil {
			log.Fatalf("Failed to build simulator: %v\n%s", err, out)
		}
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

	// Pre-pull busybox up front (with retry) — it backs the awsvpc netns
	// pause container AND is the workload image for many ECS tests. Pulling
	// it lazily at RunTask time made the ECR-gallery fetch a flaky dependency
	// of the task lifecycle: a transient throttle there surfaced as the task
	// container "failing to start" (ExitCode -1) rather than a clear pull
	// error. Fetching it once here removes that race from every test.
	pullImageWithRetry("public.ecr.aws/docker/library/busybox:latest")
	if testRunSelects("TestSFN_AmazonECSAndCodeBuildIntegrations_SDK") ||
		testRunSelects("TestECS_TaskRoleCredentialsAuthorizeWorkloadAWSCLI") {
		// The integration's timeout measures the Step Functions execution,
		// not registry transfer on a newly provisioned test host. These are the
		// exact public images configured on the Amazon ECS task definition and
		// AWS CodeBuild project; pulling them before m.Run still executes both
		// real containers while keeping image acquisition outside the per-test
		// lifecycle deadline.
		pullImageWithRetry("public.ecr.aws/docker/library/alpine:3.21")
		pullImageWithRetry("public.ecr.aws/aws-cli/aws-cli:2.27.49")
	}
	if testRunSelects("TestSFN_AmazonECSRunsTerraformAgainstSimulator_SDK") {
		buildTerraformAWSImage(workloadPlatform)
	}

	// Route 53 serves the same DNS coordinate over UDP and TCP, and it must not
	// land on the port the HTTP server is about to take.
	port, dnsPortValue, err := freeSimulatorPortPair()
	if err != nil {
		log.Fatalf("Failed to reserve simulator ports: %v", err)
	}
	simPort = port
	dnsPort = dnsPortValue

	simCmd = exec.Command(binaryPath)
	simCmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		fmt.Sprintf("SIM_DNS_PORT=%d", dnsPort),
		"SIM_LOG_LEVEL=warn",
	)
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

	code := m.Run()
	shutdownSimulator(simCmd)
	os.Exit(code)
}

func shutdownSimulator(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal simulator shutdown: %w", err)
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("wait for simulator shutdown: %w", err)
		}
		return nil
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return errors.New("simulator did not stop within 15 seconds")
	}
}

func nativeDockerPlatform() string {
	return "linux/" + runtime.GOARCH
}

// pullImageWithRetry pulls a public image up front with bounded exponential
// backoff so a transient registry throttle doesn't flake a test that runs the
// image. Mirrors the azure sdk-tests pattern. Fails the suite only after
// exhausting retries — a genuinely unreachable image must fail loud.
func pullImageWithRetry(image string) {
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		cmd := exec.Command("docker", "pull", image)
		if out, err := cmd.CombinedOutput(); err == nil {
			return
		} else {
			lastErr = fmt.Errorf("%w\n%s", err, out)
		}
		time.Sleep(time.Duration(attempt*attempt) * time.Second)
	}
	log.Fatalf("Failed to pull %s after retries: %v", image, lastErr)
}

// buildTerraformAWSImage prepares the exact Terraform workload used by the
// Step Functions → Amazon ECS provider proof. A Fargate task in a private
// subnet has no contractually implied route to registry.terraform.io, so the
// provider is fetched before the task starts and committed into the workload
// image as a filesystem mirror. The task itself therefore proves the AWS API
// path without depending on undeclared internet egress.
func buildTerraformAWSImage(platform string) {
	pullImageWithRetry(terraformECSBaseImage)

	buildDir, err := os.MkdirTemp("", "sockerless-terraform-aws-*")
	if err != nil {
		log.Fatalf("Failed to create Terraform AWS image directory: %v", err)
	}
	defer os.RemoveAll(buildDir)

	configuration := `terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.50.0"
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(buildDir, "main.tf"), []byte(configuration), 0600); err != nil {
		log.Fatalf("Failed to write Terraform AWS provider mirror configuration: %v", err)
	}
	cliConfig := `provider_installation {
  filesystem_mirror {
    path    = "/providers"
    include = ["registry.terraform.io/hashicorp/aws"]
  }
}
`
	cliConfigPath := filepath.Join(buildDir, "terraformrc")
	if err := os.WriteFile(cliConfigPath, []byte(cliConfig), 0600); err != nil {
		log.Fatalf("Failed to write Terraform AWS provider installation configuration: %v", err)
	}

	mirrorPath := filepath.Join(buildDir, "provider-mirror")
	platformName := strings.Replace(platform, "/", "_", 1)
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		if err := os.RemoveAll(mirrorPath); err != nil {
			log.Fatalf("Failed to reset Terraform AWS provider mirror: %v", err)
		}
		if err := os.MkdirAll(mirrorPath, 0755); err != nil {
			log.Fatalf("Failed to create Terraform AWS provider mirror: %v", err)
		}
		cmd := exec.Command(
			"docker", "run", "--rm",
			"--network", "host",
			"--platform", platform,
			"-v", buildDir+":/workspace",
			"-w", "/workspace",
			terraformECSBaseImage,
			"providers", "mirror",
			"-platform="+platformName,
			"/workspace/provider-mirror",
		)
		if out, runErr := cmd.CombinedOutput(); runErr == nil {
			lastErr = nil
			break
		} else {
			lastErr = fmt.Errorf("%w\n%s", runErr, out)
		}
		time.Sleep(time.Duration(attempt*attempt) * time.Second)
	}
	if lastErr != nil {
		log.Fatalf("Failed to mirror the Terraform AWS provider after retries: %v", lastErr)
	}

	create := exec.Command("docker", "create", "--platform", platform, terraformECSBaseImage)
	out, err := create.CombinedOutput()
	if err != nil {
		log.Fatalf("Failed to create Terraform AWS image staging container: %v\n%s", err, out)
	}
	containerID := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", "-f", containerID).Run() //nolint:errcheck

	for _, copySpec := range [][2]string{
		{mirrorPath, containerID + ":/providers"},
		{cliConfigPath, containerID + ":/terraformrc"},
	} {
		copyCmd := exec.Command("docker", "cp", copySpec[0], copySpec[1])
		if copyOut, copyErr := copyCmd.CombinedOutput(); copyErr != nil {
			log.Fatalf("Failed to stage Terraform AWS image content: %v\n%s", copyErr, copyOut)
		}
	}
	commit := exec.Command("docker", "commit", containerID, terraformECSImage)
	if commitOut, commitErr := commit.CombinedOutput(); commitErr != nil {
		log.Fatalf("Failed to commit Terraform AWS workload image: %v\n%s", commitErr, commitOut)
	}
}

// testRunSelects reports whether Go's configured -test.run pattern selects the
// named top-level test. CI also exports the same shard expression as SHARD_RUN;
// accepting either source keeps pre-M.Run provisioning scoped to the shard
// that actually executes the workload.
func testRunSelects(name string) bool {
	pattern := os.Getenv("SHARD_RUN")
	if pattern == "" {
		for i, arg := range os.Args {
			switch {
			case strings.HasPrefix(arg, "-test.run="):
				pattern = strings.TrimPrefix(arg, "-test.run=")
			case arg == "-test.run" && i+1 < len(os.Args):
				pattern = os.Args[i+1]
			}
			if pattern != "" {
				break
			}
		}
	}
	if pattern == "" {
		return true
	}
	selected, err := regexp.MatchString(pattern, name)
	if err != nil {
		log.Fatalf("Invalid -test.run expression %q: %v", pattern, err)
	}
	return selected
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
		log.Fatalf("Failed to build %s Docker image: %v\n%s", binaryName, err, out)
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
