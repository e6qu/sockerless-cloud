package azure_sdk_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
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

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// simTenantID is the tenant the simulator presents; the token endpoint accepts
// any tenant in the path, and this value matches the identifier the Terraform
// and CLI harnesses use so all three clients acquire tokens from the same
// /{tenant}/oauth2/v2.0/token route.
const simTenantID = "11111111-1111-1111-1111-111111111111"

// simARMBearer is a real, simulator-minted Azure Resource Manager access token
// in "Bearer <jwt>" form, acquired once in TestMain through the OAuth2
// client_credentials grant. Direct HTTP tests — those that build requests by
// hand rather than through an SDK client's credential — send it as their
// Authorization header so the simulator's ARM bearer verification accepts them.
var simARMBearer string

var (
	baseURL            string
	simCmd             *exec.Cmd
	binaryPath         string
	evalImageName      string // Docker image containing eval-arithmetic binary
	httpProbeImageName string // Docker image containing localhost probe/server binary
	commandImageName   string // Docker image containing container-command binary
	sbAMQPEndpoint     string
	ctx                = context.Background()
	subscriptionID     = "00000000-0000-0000-0000-000000000001"

	// simAzureDNSAddr is the simulator's DNS front (SIM_AZURE_DNS_LISTEN_ADDR).
	// Tests that dial a resource by its advertised hostname — a PostgreSQL
	// flexible server's fullyQualifiedDomainName — resolve it here, the way a
	// deployment points its resolver at the simulator.
	simAzureDNSAddr string

	// azureFilesDataDir is where the simulator materializes every Azure Files
	// share: <dir>/<account>/<share>. It is the directory a Container Apps
	// workload's Volume{StorageType: AzureFile} bind-mounts, so it is where a
	// file written through the Files data plane has to land.
	azureFilesDataDir string
)

type fakeCredential struct{}

// GetToken acquires a real access token from the simulator's token endpoint via
// the client_credentials grant, honoring the resource the SDK asks for. The
// returned token is a genuine RS256 JWT the simulator signed and can verify, so
// SDK-driven requests exercise the real acquire-then-authorize path against the
// ARM plane rather than a static placeholder string.
func (f *fakeCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	scope := strings.Join(opts.Scopes, " ")
	if scope == "" {
		scope = "https://management.azure.com/.default"
	}
	token, expiry, err := fetchSimAccessToken(scope)
	if err != nil {
		return azcore.AccessToken{}, err
	}
	return azcore.AccessToken{Token: token, ExpiresOn: expiry}, nil
}

// fetchSimAccessToken performs an OAuth2 client_credentials grant against the
// simulator's token endpoint — the same request a real Azure AD service
// principal makes — and returns the minted access token and its expiry.
func fetchSimAccessToken(scope string) (string, time.Time, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {scope},
	}
	resp, err := http.PostForm(baseURL+"/"+simTenantID+"/oauth2/v2.0/token", form)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("acquire simulator access token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read simulator token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("simulator token endpoint returned %d: %s", resp.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("parse simulator token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("simulator token response carried no access_token: %s", body)
	}
	return out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

func clientOpts() *arm.ClientOptions {
	return &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Cloud: cloud.Configuration{
				Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
					cloud.ResourceManager: {
						Endpoint: baseURL,
						Audience: "https://management.azure.com/",
					},
				},
			},
			InsecureAllowCredentialWithHTTP: true,
		},
	}
}

func TestMain(m *testing.M) {
	// Each suite builds the simulator it runs into a path of its own. The
	// three suites share one working tree, so a single `../simulator-azure`
	// would have one suite's `go build -o` overwrite the binary another is
	// executing the moment they run at the same time.
	binaryPath, _ = filepath.Abs("../.build/sdk-tests/simulator-azure")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		log.Fatalf("Failed to create simulator build dir: %v", err)
	}

	simDir, _ := filepath.Abs("..")
	build := exec.Command("go", "build", "-tags", "noui", "-o", binaryPath, ".")
	build.Dir = simDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		log.Fatalf("Failed to build simulator: %v\n%s", err, out)
	}

	workloadPlatform := nativeDockerPlatform()

	// The workload images are tagged for this suite alone. A container image
	// tag is a single global name in the one engine the machine runs, and every
	// cloud's suite builds workloads from the same testdata; sharing one tag
	// makes two suites running at once clobber each other's image mid-run, and
	// the loser's container start fails with "No such image" for a workload its
	// own TestMain built.
	evalDir, _ := filepath.Abs("../../testdata/eval-arithmetic")
	evalImageName = "sockerless-eval-arithmetic:azure-sdk"
	buildGoScratchImage(evalImageName, evalDir, "eval-arithmetic", workloadPlatform)

	probeDir, _ := filepath.Abs("../../testdata/http-localhost-probe")
	httpProbeImageName = "sockerless-http-localhost-probe:azure-sdk"
	buildGoScratchImage(httpProbeImageName, probeDir, "http-localhost-probe", workloadPlatform)

	commandDir, _ := filepath.Abs("../../testdata/container-command")
	commandImageName = "sockerless-container-command:azure-sdk"
	buildGoScratchImage(commandImageName, commandDir, "container-command", workloadPlatform)

	// The flexible-server data plane boots a real engine from this image at
	// first connection. Pulling it inside the timed test made a live registry
	// a flaky dependency of the engine lifecycle; fetching it here, with
	// retries, removes that race.
	if testRunSelects("TestAzurePGFlexibleServer_BackupCapturesDataAndRestoreReturnsToIt") {
		pullImageBeforeRun("public.ecr.aws/docker/library/postgres:16-alpine")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	installAzureSDKTestResolver(port)

	amqpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free Service Bus AMQP port: %v", err)
	}
	amqpPort := amqpLn.Addr().(*net.TCPAddr).Port
	amqpLn.Close()
	sbAMQPEndpoint = fmt.Sprintf("127.0.0.1:%d", amqpPort)

	dnsLn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to find free DNS port: %v", err)
	}
	dnsPort := dnsLn.LocalAddr().(*net.UDPAddr).Port
	dnsLn.Close()
	simAzureDNSAddr = fmt.Sprintf("127.0.0.1:%d", dnsPort)

	certDir, err := os.MkdirTemp("", "sockerless-servicebus-amqp-tls-*")
	if err != nil {
		log.Fatalf("Failed to create Service Bus AMQP cert dir: %v", err)
	}
	certPath, keyPath := writeServiceBusAMQPCert(certDir)

	simCmd = exec.Command(binaryPath)
	advertisedEndpoints := fmt.Sprintf(
		`{"storage":{"blob":"http://{account}.blob.shim.localhost:%d/","file":"http://{account}.file.shim.localhost:%d/","queue":"http://{account}.queue.shim.localhost:%d/","table":"http://{account}.table.shim.localhost:%d/","web":"http://{account}.web.shim.localhost:%d/","dfs":"http://{account}.dfs.shim.localhost:%d/"},"keyVault":"https://{vault}.vault.shim.localhost:%d/","serviceBus":"https://{namespace}.servicebus.shim.localhost:%d/","acr":"http://{name}.azurecr.shim.localhost:%d/"}`,
		port, port, port, port, port, port, port, port, port)
	azureFilesDataDir, err = os.MkdirTemp("", "sockerless-sim-azure-files-*")
	if err != nil {
		log.Fatalf("Failed to create Azure Files data dir: %v", err)
	}

	simCmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		fmt.Sprintf("SIM_AZURE_FILES_DATA_DIR=%s", azureFilesDataDir),
		fmt.Sprintf("SIM_SERVICEBUS_AMQP_LISTEN_ADDR=:%d", amqpPort),
		fmt.Sprintf("SIM_SERVICEBUS_AMQP_TLS_CERT=%s", certPath),
		fmt.Sprintf("SIM_SERVICEBUS_AMQP_TLS_KEY=%s", keyPath),
		"SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON="+advertisedEndpoints,
		"SIM_AZURE_DNS_LISTEN_ADDR="+simAzureDNSAddr,
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

	token, _, err := fetchSimAccessToken("https://management.azure.com/.default")
	if err != nil {
		simCmd.Process.Kill()
		log.Fatalf("Failed to acquire simulator ARM bearer token: %v", err)
	}
	simARMBearer = "Bearer " + token

	// The Cosmos emulator takes minutes to initialise and only two tests need
	// it, so it is started here and initialises alongside everything else
	// rather than inside the first test that asks — which is where its
	// readiness budget kept expiring on a loaded runner.
	//
	// A run that cannot reach those tests does not pay for it: warming is
	// skipped when -run names a filter no Cosmos test matches. The tests call
	// startCosmosEmulator either way, and it boots the emulator itself if this
	// did not, so the filter decides when the cost is paid and never whether
	// the oracle is available.
	if cosmosSuiteMayRun() {
		go warmCosmosEmulator()
		defer stopCosmosEmulator()
	}

	code := m.Run()
	simCmd.Process.Kill()
	simCmd.Wait()
	// os.Exit skips deferred cleanup, so the directories this run created are
	// removed here.
	os.RemoveAll(azureFilesDataDir)
	os.RemoveAll(certDir)
	os.Exit(code)
}

func installAzureSDKTestResolver(simPort int) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err == nil && port == fmt.Sprint(simPort) && simAdvertisedDataPlaneHost(host) {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
	http.DefaultClient.Transport = transport
	os.Setenv("NO_PROXY", mergeNoProxy(os.Getenv("NO_PROXY"), "localhost", "127.0.0.1", "::1", ".localhost", "*.localhost"))
	os.Setenv("no_proxy", mergeNoProxy(os.Getenv("no_proxy"), "localhost", "127.0.0.1", "::1", ".localhost", "*.localhost"))
}

// simAdvertisedDataPlaneHost reports whether a host is one of the per-resource
// data-plane hosts the simulator advertises through
// SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON. Those hosts are the coordinates
// a client dials — an Event Grid topic endpoint, a container registry's login
// server — and only their resolution differs from the real cloud: `.localhost`
// names do not resolve on every platform, so the test dialer sends them to the
// loopback listener the simulator is on. The request itself, host header
// included, is the one a client sends to the real service.
func simAdvertisedDataPlaneHost(host string) bool {
	for _, suffix := range []string{"eventgrid.localhost", "azurecr.shim.localhost"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	// A Cosmos DB account's document endpoint is its own host — the coordinate
	// ARM advertises and a client dials, and the one that tells the service
	// whose keys sign the request.
	return strings.Contains(host, ".documents.")
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

func writeServiceBusAMQPCert(dir string) (string, string) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("generate Service Bus AMQP TLS key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		log.Fatalf("generate Service Bus AMQP TLS serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "servicebus.localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"servicebus.localhost", "*.servicebus.localhost", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatalf("create Service Bus AMQP TLS cert: %v", err)
	}
	certPath := filepath.Join(dir, "servicebus-amqp-cert.pem")
	keyPath := filepath.Join(dir, "servicebus-amqp-key.pem")
	certFile, err := os.Create(certPath)
	if err != nil {
		log.Fatalf("create Service Bus AMQP TLS cert file: %v", err)
	}
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		certFile.Close()
		log.Fatalf("write Service Bus AMQP TLS cert: %v", err)
	}
	if err := certFile.Close(); err != nil {
		log.Fatalf("close Service Bus AMQP TLS cert: %v", err)
	}
	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("create Service Bus AMQP TLS key file: %v", err)
	}
	if err := pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		keyFile.Close()
		log.Fatalf("write Service Bus AMQP TLS key: %v", err)
	}
	if err := keyFile.Close(); err != nil {
		log.Fatalf("close Service Bus AMQP TLS key: %v", err)
	}
	return certPath, keyPath
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
		log.Fatalf("Failed to build %s Docker image: %v\n%s", imageName, err, out)
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

// cosmosSuiteMayRun reports whether this run can reach a test that needs the
// Cosmos emulator, so TestMain knows whether warming one is worth it. An
// unfiltered run can; a filtered one can only if its pattern matches a name
// the Cosmos tests carry.
func cosmosSuiteMayRun() bool {
	filter := flag.Lookup("test.run")
	if filter == nil || filter.Value.String() == "" {
		return true
	}
	matched, err := regexp.MatchString(filter.Value.String(), "TestCosmos_DifferentialVsEmulator")
	if err != nil {
		return true // an unparsable filter is go test's to reject, not ours
	}
	if matched {
		return true
	}
	matched, err = regexp.MatchString(filter.Value.String(), "TestCosmosScripts_DifferentialVsEmulator")
	return err != nil || matched
}

// pullImageBeforeRun fetches a public image before m.Run so a transient
// registry failure surfaces here as a clear pull error, not as a workload
// that "failed to start" inside a timed test.
func pullImageBeforeRun(image string) {
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
