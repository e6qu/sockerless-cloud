package azure_sdk_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
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
	binaryPath, _ = filepath.Abs("../simulator-azure")

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

	probeDir, _ := filepath.Abs("../../testdata/http-localhost-probe")
	httpProbeImageName = "sockerless-http-localhost-probe:test"
	buildGoScratchImage(httpProbeImageName, probeDir, "http-localhost-probe", workloadPlatform)

	commandDir, _ := filepath.Abs("../../testdata/container-command")
	commandImageName = "sockerless-container-command:test"
	buildGoScratchImage(commandImageName, commandDir, "container-command", workloadPlatform)

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
		if err == nil && port == fmt.Sprint(simPort) && (host == "eventgrid.localhost" || strings.HasSuffix(host, ".eventgrid.localhost")) {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
	http.DefaultClient.Transport = transport
	os.Setenv("NO_PROXY", mergeNoProxy(os.Getenv("NO_PROXY"), "localhost", "127.0.0.1", "::1", ".localhost", "*.localhost"))
	os.Setenv("no_proxy", mergeNoProxy(os.Getenv("no_proxy"), "localhost", "127.0.0.1", "::1", ".localhost", "*.localhost"))
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
