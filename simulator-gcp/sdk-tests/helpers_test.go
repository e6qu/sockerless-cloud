package gcp_sdk_test

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"cloud.google.com/go/iam/apiv1/iampb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/e6qu/sockerless-cloud/testutil/baseimage"
	"github.com/e6qu/sockerless-cloud/testutil/simready"

	"github.com/e6qu/sockerless-cloud/testutil/freeports"
	"github.com/e6qu/sockerless-cloud/testutil/gcpbootstrap"
	"github.com/e6qu/sockerless-cloud/testutil/loopbacktls"
	"github.com/e6qu/sockerless-cloud/testutil/scratchimage"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

var (
	baseURL            string
	grpcAddr           string // host:port for gRPC Cloud Logging
	dnsAddr            string // host:port of the Cloud DNS name server
	simCmd             *exec.Cmd
	binaryPath         string
	evalImageName      string // Docker image containing eval-arithmetic binary
	commandImageName   string // Docker image containing container-command binary
	httpProbeImageName string // Docker image containing localhost probe/server binary
	// metadataClientImageName is a Cloud Run workload that reads the metadata
	// server through the official Go clients.
	metadataClientImageName string
	ctx                     = context.Background()
)

func TestMain(m *testing.M) {
	// A simulator this process starts must not outlive it. The cleanup
	// below stops each one, and a killed `go test` never reaches it — so
	// the simulator watches this pid and exits when it goes. Set here
	// rather than at each start: every one of them inherits os.Environ().
	os.Setenv("SOCKERLESS_PARENT_PID", strconv.Itoa(os.Getpid()))
	// Each suite builds the simulator it runs into a path of its own. The
	// three suites share one working tree, so a single `../simulator-gcp`
	// would have one suite's `go build -o` overwrite the binary another is
	// executing the moment they run at the same time.
	binaryPath, _ = filepath.Abs("../.build/sdk-tests/simulator-gcp")
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

	workloadPlatform := scratchimage.NativePlatform()

	evalDir, _ := filepath.Abs("../../testdata/eval-arithmetic")
	evalImageName = "sockerless-eval-arithmetic:gcp-sdk"
	if err := scratchimage.Build(evalImageName, evalDir, "eval-arithmetic", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	commandDir, _ := filepath.Abs("../../testdata/container-command")
	commandImageName = "sockerless-container-command:gcp-sdk"
	if err := scratchimage.Build(commandImageName, commandDir, "container-command", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	// The Cloud SQL data plane boots real engines from these images at first
	// connection. Pulling them inside the timed test made a live registry a
	// flaky dependency of the engine lifecycle: a transient failure surfaced
	// as an engine that "did not become ready" rather than as what it was —
	// the network. Fetching them here, with retries, removes that race.
	if testRunSelects("TestCloudSQL_BackupCapturesDataAndRestoreReturnsToIt") {
		pullImageBeforeRun("mirror.gcr.io/library/postgres:16-alpine")
	}
	if testRunSelects("TestCloudSQL_MySQLBackupCapturesDataAndRestoreReturnsToIt") {
		pullImageBeforeRun("mirror.gcr.io/library/mysql:8.0")
	}
	// Every Memorystore instance and cluster a test creates starts a real
	// Redis engine: REDIS_7_0 is the instance default and clusters run 7.2.
	pullImageBeforeRun("mirror.gcr.io/library/redis:7.0-alpine")
	pullImageBeforeRun("mirror.gcr.io/library/redis:7.2-alpine")

	probeDir, _ := filepath.Abs("../../testdata/http-localhost-probe")
	httpProbeImageName = "sockerless-http-localhost-probe:gcp-sdk"
	if err := scratchimage.Build(httpProbeImageName, probeDir, "http-localhost-probe", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	metadataClientDir, _ := filepath.Abs("testdata/metadata-client")
	metadataClientImageName = "sockerless-metadata-client:gcp-sdk"
	if err := scratchimage.Build(metadataClientImageName, metadataClientDir, "metadata-client", workloadPlatform, ""); err != nil {
		log.Fatal(err)
	}

	// The Cloud Run job tests run this image as their workload, so it is
	// acquired here, with retries, and its absence fails the run: a pull that
	// only warned left the image to be fetched inside a timed test, where a
	// throttled registry surfaced as a flaky workload rather than a pull error.
	pullImageBeforeRun(simWorkloadImage)
	// Every Compute Engine instance test boots the Debian 12 image of the
	// public catalog, by the manifest of the engine's architecture.
	pullImageBeforeRun(debianCatalogImage)

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
		log.Fatalf("Failed to find free gRPC port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	grpcPort := ln2.Addr().(*net.TCPAddr).Port
	ln.Close()
	ln2.Close()

	dnsPort, err := freeports.TCPUDP()
	if err != nil {
		log.Fatalf("Failed to find a free DNS port: %v", err)
	}

	tlsDir, err := os.MkdirTemp("", "gcp-sdk-grpc-tls-*")
	if err != nil {
		log.Fatalf("Failed to create the gRPC certificate directory: %v", err)
	}
	if grpcTLS, err = loopbacktls.Write(tlsDir); err != nil {
		log.Fatalf("Failed to issue the gRPC port's certificate: %v", err)
	}
	if grpcRoots, err = grpcTLS.Pool(); err != nil {
		log.Fatalf("Failed to load the gRPC port's authority: %v", err)
	}

	simCmd = exec.Command(binaryPath)
	simCmd.Env = append(append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		fmt.Sprintf("SIM_GCP_GRPC_PORT=%d", grpcPort),
		fmt.Sprintf("SIM_DNS_PORT=%d", dnsPort),
	), simGRPCEnv()...)
	simCmd.Stdout = os.Stdout
	if err := simready.Start(simCmd, os.Stderr); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Simulator did not become healthy: %v", err)
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	grpcAddr = fmt.Sprintf("127.0.0.1:%d", grpcPort)
	dnsAddr = fmt.Sprintf("127.0.0.1:%d", dnsPort)

	// The Cloud Storage client reads its endpoint from this variable. Set it
	// once for the process, since every test talks to the same simulator and a
	// test that sets one cannot run in parallel. The Bigtable and Spanner
	// clients take the gRPC endpoint as an option instead: their emulator
	// variables make them send no credential.
	if err := os.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(baseURL, "http://")); err != nil {
		log.Fatalf("set STORAGE_EMULATOR_HOST: %v", err)
	}

	// The data plane now verifies an OAuth2 access token on every request, so
	// the tests that reach it through raw net/http calls (rather than an SDK
	// client, which carries its own token source) must present a real token.
	// Attach one — minted by the simulator's own token endpoint — to every
	// request the shared http.DefaultClient sends to the simulator, exactly as
	// a raw client authenticates against Google; this differs from the real
	// cloud only in the endpoint coordinate and the token source.
	http.DefaultClient.Transport = &simAuthTransport{
		base: http.DefaultTransport,
		host: fmt.Sprintf("127.0.0.1:%d", port),
		ts:   simTokenSource(),
	}

	number, err := createHarnessProject()
	if err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Failed to create project %s: %v", harnessProject, err)
	}
	harnessProjectNumber = number
	if err := grantHarnessTokenCreator(); err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Failed to grant the bootstrap account the Service Account Token Creator role: %v", err)
	}

	code := m.Run()
	simCmd.Process.Kill()
	simCmd.Wait()
	os.RemoveAll(tlsDir)
	os.Exit(code)
}

// grantHarnessTokenCreator grants the bootstrap account the Service Account
// Token Creator role on the test organization, which the tests that
// impersonate a service account need: no basic role, Owner included, carries
// iam.serviceAccounts.getAccessToken.
func grantHarnessTokenCreator() error {
	c, err := resourcemanager.NewOrganizationsRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	if err != nil {
		return err
	}
	defer c.Close()
	policy, err := c.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: crmTestOrg})
	if err != nil {
		return err
	}
	policy.Bindings = append(policy.Bindings, &iampb.Binding{
		Role:    "roles/iam.serviceAccountTokenCreator",
		Members: []string{"serviceAccount:" + gcpbootstrap.Email},
	})
	_, err = c.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: crmTestOrg, Policy: policy})
	return err
}

// harnessProject is the project the tests address. TestMain creates it the
// way a user creates a project before working in it.
const harnessProject = "test-project"

// harnessProjectNumber is the project number Cloud Resource Manager assigned
// harnessProject.
var harnessProjectNumber string

// createHarnessProject creates harnessProject through Cloud Resource Manager
// projects.create, waits on the operation, and returns the project number.
func createHarnessProject() (string, error) {
	c, err := resourcemanager.NewProjectsRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	if err != nil {
		return "", err
	}
	defer c.Close()
	op, err := c.CreateProject(ctx, &resourcemanagerpb.CreateProjectRequest{
		Project: &resourcemanagerpb.Project{ProjectId: harnessProject, DisplayName: harnessProject, Parent: crmTestOrg},
	})
	if err != nil {
		return "", err
	}
	project, err := op.Wait(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(project.GetName(), "projects/"), nil
}

// simTokenSource returns an oauth2.TokenSource for the simulator's bootstrap
// service account, minted through the service-account JWT-bearer flow a client
// runs against Google, differing only in the token endpoint coordinate. The
// token is cached and refreshed by oauth2.ReuseTokenSource until it nears
// expiry.
func simTokenSource() oauth2.TokenSource {
	return simTokenSourceFor(baseURL)
}

// simTokenSourceFor returns a token source that mints from a specific
// simulator's token endpoint. Each simulator process signs with its own key, so
// a client talking to an isolated simulator instance (e.g. the quota tests'
// dedicated process) must present a token minted by that same instance.
func simTokenSourceFor(base string) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, simTokenFetcher{base: base})
}

// simAuthTransport attaches a simulator-minted OAuth2 access token to every
// request bound for the simulator host that does not already carry one, so raw
// net/http calls authenticate against the data plane the way an SDK client
// does. Requests to any other host (an external OpenID Connect issuer, a public
// registry) and the token-minting endpoints themselves are passed through
// untouched — the latter is how the token is obtained in the first place.
type simAuthTransport struct {
	base http.RoundTripper
	host string
	ts   oauth2.TokenSource
}

func (t *simAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.host && req.Header.Get("Authorization") == "" && !isTokenMintPath(req.URL.Path) {
		tok, err := t.ts.Token()
		if err != nil {
			return nil, err
		}
		// RoundTripper must not mutate the caller's request.
		req = req.Clone(req.Context())
		tok.SetAuthHeader(req)
	}
	return t.base.RoundTrip(req)
}

// isTokenMintPath reports whether a path is one of the simulator's OAuth2 /
// Security Token Service token-minting endpoints, which are reached without a
// bearer (they issue the bearer).
func isTokenMintPath(p string) bool {
	return p == "/token" || p == "/oauth2/v4/token" || p == "/v1/token"
}

// simBearerHeader returns an "Authorization: Bearer <token>" value carrying a
// real simulator-minted access token, for tests that issue raw HTTP requests to
// the data plane rather than going through an SDK client.
func simBearerHeader(t *testing.T) string {
	t.Helper()
	tok, err := simTokenFetcher{base: baseURL}.Token()
	if err != nil {
		t.Fatalf("mint simulator access token: %v", err)
	}
	return "Bearer " + tok.AccessToken
}

// simAuthHTTPClient returns an *http.Client that attaches a simulator-minted
// access token to every request bound for the simulator, for SDK clients that
// accept an explicit HTTP client (e.g. the Cloud Storage client in emulator
// mode, which otherwise sends no credentials) rather than a token-source
// option.
func simAuthHTTPClient() *http.Client {
	return &http.Client{Transport: &simAuthTransport{
		base: http.DefaultTransport,
		host: strings.TrimPrefix(baseURL, "http://"),
		ts:   simTokenSource(),
	}}
}

// serviceHostOptions points a client at a Google API's own host, the
// coordinate whose Host header names the service, and delivers its requests to
// the simulator through an HTTP proxy. Eventarc and Cloud Build both publish
// `/v1/projects/{p}/locations/{l}/triggers`, and Google tells them apart by
// host.
func serviceHostOptions(host string) []option.ClientOption {
	return serviceHostOptionsVia(baseURL, simTokenSource(), host)
}

// serviceHostOptionsVia reaches the service host through the simulator at
// endpoint, with credentials from src.
func serviceHostOptionsVia(endpoint string, src oauth2.TokenSource, host string) []option.ClientOption {
	proxy, err := url.Parse(endpoint)
	if err != nil {
		log.Fatalf("parse simulator URL: %v", err)
	}
	return []option.ClientOption{
		option.WithEndpoint("http://" + host),
		option.WithHTTPClient(&http.Client{Transport: &oauth2.Transport{
			Source: src,
			Base:   &http.Transport{Proxy: http.ProxyURL(proxy)},
		}}),
	}
}

const (
	eventarcHost   = "eventarc.googleapis.com"
	cloudBuildHost = "cloudbuild.googleapis.com"
	// Cloud SQL Admin publishes Spanner's /v1/projects/{p}/instances paths.
	spannerHost = "spanner.googleapis.com"
)

// simTokenFetcher mints a fresh access token for the simulator's bootstrap
// service account on every call: the client library reads the account's
// credential file, signs a JWT-bearer assertion with its key and exchanges it
// at the file's token_uri, the simulator's token endpoint.
type simTokenFetcher struct{ base string }

func (f simTokenFetcher) Token() (*oauth2.Token, error) {
	config, err := google.JWTConfigFromJSON(gcpbootstrap.CredentialJSON(f.base+"/token"),
		"https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("read the bootstrap credential: %w", err)
	}
	return config.TokenSource(context.Background()).Token()
}

// simWorkloadImage is the base image these suites run as a container workload
// and build from. It is served from mirror.gcr.io, which is a registry CI
// warms from a cached tarball (scripts/base-images-for.sh derives that set by
// scanning these sources for its references); Docker Hub is not warmed, and
// its anonymous pull cap has flaked these runs.
const simWorkloadImage = "mirror.gcr.io/library/alpine:3.20"

// pullImageBeforeRun fetches a public image before m.Run so a transient
// registry failure surfaces here as a clear pull error, not as a workload
// that "failed to start" inside a timed test.
func pullImageBeforeRun(image string) {
	if err := baseimage.Ensure(image); err != nil {
		log.Fatalf("Failed to pull %s: %v", image, err)
	}
}

// testRunSelects reports whether the -test.run filter (or SHARD_RUN) selects
// the named test, so expensive image acquisition runs only when the tests
// that need it will.
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

// debianCatalogImage is the OCI image the simulator's public catalog boots a
// Debian 12 instance from, pinned as the catalog pins it; the engine picks the
// platform's manifest out of the index.
const debianCatalogImage = "mirror.gcr.io/library/debian:12@sha256:d74df7598108a173a621eda4d58a2adbf850b2d9a9d81bc2cca1a6c21174a880"
