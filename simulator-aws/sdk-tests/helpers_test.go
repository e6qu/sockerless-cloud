package aws_sdk_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/e6qu/sockerless-cloud/testutil/baseimage"
	"github.com/e6qu/sockerless-cloud/testutil/simready"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/e6qu/sockerless-cloud/testutil/freeports"
	"github.com/e6qu/sockerless-cloud/testutil/scratchimage"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
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

// waiterMinDelay and waiterMaxDelay bound the gap an SDK waiter leaves between
// describes. The published defaults (5 to 60 seconds, up to 120) suit
// resources that take minutes; these settle in seconds, and default waiters
// would sleep past the transition.
const (
	waiterMinDelay = 250 * time.Millisecond
	waiterMaxDelay = 2 * time.Second
)

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// signRawSigV4 signs a hand-built request with SigV4 under the test/test
// us-east-1 credential every client signs with, as aws-sdk-go-v2 would: the
// awsJson and awsQuery control plane at POST /, the S3 REST data plane and
// Lambda's REST control plane require a valid signature. Set every signed
// header on req before calling. payloadHash is the body's hex SHA-256 or a
// streaming sentinel such as "STREAMING-AWS4-HMAC-SHA256-PAYLOAD".
func signRawSigV4(t *testing.T, req *http.Request, service, payloadHash string) {
	t.Helper()
	signRawSigV4Creds(t, req, service, payloadHash, "test", "test")
}

// signRawSigV4Creds is signRawSigV4 with an explicit key, for signing a
// well-formed request with a wrong secret.
func signRawSigV4Creds(t *testing.T, req *http.Request, service, payloadHash, akid, secret string) {
	t.Helper()
	creds := aws.Credentials{AccessKeyID: akid, SecretAccessKey: secret}
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, payloadHash, service, "us-east-1", time.Now()); err != nil {
		t.Fatalf("SigV4 sign (%s): %v", service, err)
	}
}

// signRawSigV4JSON signs with the body's SHA-256 as payload hash, as awsJson and
// awsQuery clients do.
func signRawSigV4JSON(t *testing.T, req *http.Request, service string, body []byte) {
	t.Helper()
	sum := sha256.Sum256(body)
	signRawSigV4(t, req, service, hex.EncodeToString(sum[:]))
}

// freeSimulatorPortPair reserves a simulator's HTTP port and its Route 53 DNS
// port as two different ports. Two independent probes can get the same port,
// since each releases its port before returning, and the simulator would then
// fail to bind its DNS listener. Holding the HTTP reservation open across the
// DNS probe, which binds TCP too, rules that out.
func freeSimulatorPortPair() (httpPort int, dnsPort int, err error) {
	holder, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, 0, err
	}
	httpPort = holder.Addr().(*net.TCPAddr).Port
	dnsPort, dnsErr := freeports.TCPUDP()
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

// terraformECSBaseImage comes from Google's Docker Hub mirror at the index
// digest Docker Hub serves: Docker Hub limits anonymous pulls per address.
const (
	terraformECSBaseImage = "mirror.gcr.io/hashicorp/terraform:1.17.0@sha256:4d1b496a256f2989bccdb04b9104a4ede97234b10fba90259e173e905ec4fb58"
	terraformECSImage     = "sockerless-terraform-aws:aws-sdk"
)

func sdkConfig() aws.Config {
	return aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:  sdkHTTPClient,
	}
}

// simEndpoint returns one service's endpoint against the simulator: the
// service's own hostname in the `.localhost` family on the simulator's port,
// shaped like the endpoint a real client resolves
// (`servicediscovery.us-east-1.amazonaws.com`). An operation with a modeled
// endpoint host prefix (Cloud Map's `data-`, Step Functions' `sync-`) then
// signs a real prefixed host such as `data-servicediscovery.localhost`; only
// the coordinate differs from what AWS receives.
func simEndpoint(service string) string {
	return fmt.Sprintf("http://%s.localhost:%d", service, simPort)
}

// simHTTPClient keeps the SDK's transport defaults and replaces only name
// resolution: the `.localhost` family resolves to loopback, as RFC 6761
// mandates and glibc does but macOS's resolver does not. The Host header, URL
// and signature stay as the SDK made them.
var simHTTPClient = awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
	tr.DialContext = simDialContext
	tr.Proxy = simProxy
})

// sdkHTTPClient hands the transport each request body without its io.WriterTo.
// smithy-go closes a body once the response arrives, and the closed body's
// WriteTo answers io.EOF, which net/http, still probing for bytes past
// Content-Length, takes for a failed write: it closes the connection the
// response is read from, ending an event stream with "use of closed network
// connection". A closed body's Read answers io.EOF, which net/http takes for
// the end of the body.
var sdkHTTPClient aws.HTTPClient = readOnlyBodyClient{simHTTPClient}

type readOnlyBodyClient struct{ next aws.HTTPClient }

func (c readOnlyBodyClient) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = readOnlyBody{req.Body}
	}
	return c.next.Do(req)
}

type readOnlyBody struct{ io.ReadCloser }

var simDialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

func simDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	switch {
	case isLocalhostName(host):
		addr = net.JoinHostPort("127.0.0.1", port)
	case isS3ExpressName(host), isS3AccessPointName(host), isLambdaFunctionURLName(host):
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(simPort))
	case host == "127.0.0.1" || host == "::1":
	default:
		// A host that is not the simulator means a wrong endpoint coordinate, and
		// dialing it would send a request signed with this suite's credentials to
		// the real cloud.
		return nil, fmt.Errorf("refusing to dial %q: this suite addresses the simulator, and that host is not it", host)
	}
	return simDialer.DialContext(ctx, network, addr)
}

// isS3AccessPointName reports whether a hostname addresses an S3 access point,
// `<name>-<account>.s3-accesspoint.<region>.amazonaws.com`, or an Object Lambda
// access point's host. The SDK derives the host from the access point ARN the
// caller passes as the bucket, so name resolution alone points it at the
// simulator.
func isS3AccessPointName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.Contains(h, ".s3-accesspoint.") || strings.Contains(h, ".s3-object-lambda.")
}

// isLambdaFunctionURLName reports whether a hostname is an AWS Lambda function
// URL, `<url-id>.lambda-url.<region>.on.aws`, which a client reaches by the URL
// GetFunctionUrlConfig reports.
func isLambdaFunctionURLName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.Contains(h, ".lambda-url.") && strings.HasSuffix(h, ".on.aws")
}

// isS3ExpressName reports whether a hostname belongs to S3 Express One Zone: the
// regional control endpoint `s3express-control.<region>.amazonaws.com` or a
// directory bucket's zonal endpoint
// `<bucket>.s3express-<zone-id>.<region>.amazonaws.com`. Neither can be a
// BaseEndpoint: the SDK picks between them by bucket name, and an override
// collapses both onto one host and takes the S3 Express auth branch even for
// CreateBucket. So the client resolves exactly what it resolves against AWS,
// and name resolution points it at the simulator.
func isS3ExpressName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.HasPrefix(h, "s3express-control.") || strings.Contains(h, ".s3express-")
}

// simProxy keeps the SDK's environment-driven proxy for every host but the
// `.localhost` family, which resolves to loopback and must not go through a
// proxy Go would otherwise apply to a name other than `localhost`.
func simProxy(req *http.Request) (*url.URL, error) {
	if host := req.URL.Hostname(); isLocalhostName(host) || isS3ExpressName(host) || isS3AccessPointName(host) ||
		isLambdaFunctionURLName(host) {
		return nil, nil
	}
	return http.ProxyFromEnvironment(req)
}

func isLocalhostName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

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
// after every Finalize middleware, host-prefix mutation and SigV4 signing
// included, proving an operation with a modeled host prefix used the prefixed
// host.
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
	// A killed `go test` never reaches the cleanup that stops the simulators, so
	// each one watches this pid and exits with it. Every simulator inherits
	// os.Environ().
	os.Setenv("SOCKERLESS_PARENT_PID", strconv.Itoa(os.Getpid()))
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
		// Each suite builds its simulator into its own path: the suites share one
		// working tree, so one suite's `go build -o` would overwrite the binary
		// another runs.
		binaryPath, _ = filepath.Abs("../.build/sdk-tests/simulator-aws")
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
	evalImageName = "sockerless-eval-arithmetic:aws-sdk"
	if err := scratchimage.Build(evalImageName, evalDir, "eval-arithmetic", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	lambdaHandlerDir, _ := filepath.Abs("../../testdata/lambda-runtime-handler")
	lambdaHandlerImageName = "sockerless-lambda-runtime-handler:aws-sdk"
	if err := scratchimage.Build(lambdaHandlerImageName, lambdaHandlerDir, "lambda-runtime-handler", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	containerCommandDir, _ := filepath.Abs("../../testdata/container-command")
	containerCommandImage = "sockerless-container-command:aws-sdk"
	if err := scratchimage.Build(containerCommandImage, containerCommandDir, "container-command", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	// Pull busybox, the workload image of many ECS tests, once with retries: a
	// registry throttle at RunTask time would read as a container failing to start
	// (ExitCode -1) rather than a pull error.
	pullImageWithRetry("mirror.gcr.io/library/busybox:latest")
	if testRunSelects("TestSFN_AmazonECSAndCodeBuildIntegrations_SDK") ||
		testRunSelects("TestECS_TaskRoleCredentialsAuthorizeWorkloadAWSCLI") ||
		testRunSelects("TestCodeBuild_BuildHoldsItsServiceRoleSession_SDK") ||
		testRunSelects("TestBatch_JobHoldsItsJobRoleSession_SDK") {
		// The integration's timeout measures the Step Functions execution, not a cold
		// host's registry transfer, so pull the public images the Amazon ECS task
		// definition and AWS CodeBuild project name before m.Run.
		pullImageWithRetry("mirror.gcr.io/library/alpine:3.21")
		pullImageWithRetry("mirror.gcr.io/amazon/aws-cli:2.27.49")
	}
	// An AWS Amplify job's wait measures the job, not the transfer of the
	// several-hundred-megabyte managed build image.
	for _, name := range []string{"TestAmplifyJobLifecycle", "TestAmplifyRealBuildE2E", "TestAmplifyPrivateRepositoryPythonBuildE2E"} {
		if testRunSelects(name) {
			pullImageWithRetry("mirror.gcr.io/library/node:22-bookworm")
			break
		}
	}
	// An ElastiCache resource reports available once its engine answers, which on
	// a cold host waits on the engine image's transfer.
	elastiCacheEngines := false
	for _, name := range []string{
		"TestElastiCache_ClusterLifecycle", "TestElastiCache_ReplicationGroupAndReboot", "TestElastiCache_EngineVersionDefault",
		"TestElastiCache_RedisClusterServesItsEndpoint", "TestElastiCache_MemcachedClusterServesItsNodes",
		"TestElastiCache_ReplicationGroupReplicates", "TestElastiCache_ClusterModeReplicationGroupShards",
		"TestElastiCache_ServerlessCacheServesItsEndpoint", "TestElastiCache_Serverless", "TestElastiCache_ServerlessMigration",
		"TestElastiCache_ReplicaAndShardConfig",
	} {
		elastiCacheEngines = elastiCacheEngines || testRunSelects(name)
	}
	if elastiCacheEngines {
		for _, image := range []string{
			"mirror.gcr.io/library/redis:7.0-alpine", "mirror.gcr.io/library/redis:6.2-alpine",
			"mirror.gcr.io/valkey/valkey:8.0-alpine", "mirror.gcr.io/library/memcached:1.6.22-alpine",
		} {
			pullImageWithRetry(image)
		}
	}
	if testRunSelects("TestSFN_AmazonECSRunsTerraformAgainstSimulator_SDK") {
		buildTerraformAWSImage(workloadPlatform)
	}

	// Route 53 serves the DNS coordinate over UDP and TCP, on a port other than
	// the HTTP server's.
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
	if err := simready.Start(simCmd, os.Stderr); err != nil {
		shutdownSimulator(simCmd)
		log.Fatalf("Simulator did not become healthy: %v", err)
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

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

// pullImageWithRetry pulls a public image with bounded exponential backoff so
// a transient registry throttle does not flake a test, and fails the suite
// once the retries run out.
func pullImageWithRetry(image string) {
	if err := baseimage.Ensure(image); err != nil {
		log.Fatalf("Failed to pull %s: %v", image, err)
	}
}

// hostEgressArgs hands the host's outbound proxy, and the CA bundle its
// certificates chain to, to a container on the host network, so the registry
// fetch takes the host's egress path.
func hostEgressArgs() []string {
	var args []string
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if v, ok := os.LookupEnv(key); ok {
			args = append(args, "-e", key+"="+v)
		}
	}
	if bundle, ok := os.LookupEnv("SSL_CERT_FILE"); ok {
		args = append(args, "-v", bundle+":/etc/ssl/certs/ca-certificates.crt:ro")
	}
	return args
}

// buildTerraformAWSImage builds the Terraform workload of the Step Functions to
// Amazon ECS provider test. A Fargate task in a private subnet has no route to
// registry.terraform.io, so the image carries the provider as a filesystem
// mirror and the task needs no internet egress.
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
		args := []string{
			"run", "--rm",
			"--network", "host",
			"--platform", platform,
			"-v", buildDir + ":/workspace",
			"-w", "/workspace",
		}
		args = append(args, hostEgressArgs()...)
		args = append(args,
			terraformECSBaseImage,
			"providers", "mirror",
			"-platform="+platformName,
			"/workspace/provider-mirror",
		)
		cmd := exec.Command("docker", args...)
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
	defer exec.Command("docker", "rm", "-f", containerID).Run()

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

// testRunSelects reports whether the -test.run pattern, or the SHARD_RUN
// expression CI exports, selects the named top-level test, which scopes
// provisioning before m.Run to the shard that runs the workload.
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

// gluePythonShellImage is the image the simulator starts an AWS Glue Python
// shell job run in.
const gluePythonShellImage = "mirror.gcr.io/library/python:3.9"

// ensureGluePythonShellImage pulls the interpreter before a test times a Glue
// job run, so a slow registry fails the test with the reason rather than as a
// four-minute timeout. It stays out of TestMain, which runs for every shard, so
// a registry refusal fails one test rather than every shard.
func ensureGluePythonShellImage(t *testing.T) {
	t.Helper()
	if err := baseimage.Ensure(gluePythonShellImage); err != nil {
		t.Fatalf("could not pull %s, which every AWS Glue Python shell job run executes in: %v",
			gluePythonShellImage, err)
	}
}

// nativeLambdaArchitectures is the host's architecture, which the built images
// carry. AWS Lambda defaults a function to x86_64, so on an arm64 host the
// function says so, as an arm64 image on Lambda must.
func nativeLambdaArchitectures() []lambdatypes.Architecture {
	if runtime.GOARCH == "arm64" {
		return []lambdatypes.Architecture{lambdatypes.ArchitectureArm64}
	}
	return []lambdatypes.Architecture{lambdatypes.ArchitectureX8664}
}

func serviceRole(t *testing.T, principal string) string {
	t.Helper()
	return createWorkloadRole(t, uniquename.New(strings.SplitN(principal, ".", 2)[0]), principal)
}

// statesRole creates a uniquely named role AWS Step Functions may assume,
// allowed actions on every resource, deleted when the test ends.
func statesRole(t *testing.T, actions ...string) string {
	t.Helper()
	return createServiceRole(t, uniquename.New("states"), "states.amazonaws.com", actions...)
}

// lambdaRole creates a uniquely named execution role AWS Lambda may assume,
// allowed actions on every resource, deleted when the test ends.
func lambdaRole(t *testing.T, actions ...string) string {
	t.Helper()
	return createServiceRole(t, uniquename.New("lambda"), "lambda.amazonaws.com", actions...)
}
