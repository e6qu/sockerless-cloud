package azure_cli_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real Azure CLI, logged in to the simulator through Azure's own
// custom-cloud flow.
//
// `az cloud register` + `az cloud set` + `az login` is how an operator points
// the CLI at a deployment other than Azure's public one. The CLI's identity
// layer is MSAL, which requires an https authority and refuses to talk to an
// authority whose host it does not recognise: for any host outside its own
// hard-coded table it sends instance discovery to login.microsoftonline.com —
// never to the configured authority — and fails with invalid_instance when the
// public service does not vouch for the host. The one authority shape MSAL
// resolves against the configured host, with no client-side switch, is the AD
// FS shape `az cloud register --endpoint-active-directory https://<host>/adfs`
// that Azure Stack Hub deployments use: MSAL then fetches the authority's own
// OpenID metadata document and takes the token endpoint from it.
//
// So the two coordinates below are all that separate this run from one against
// Azure: the endpoint URLs registered on the cloud, and the certificate
// authority the CLI trusts (REQUESTS_CA_BUNDLE, the same variable an operator
// sets for a private CA). Every command, credential and identifier is what a
// real client uses.

const azLoginTenantID = "11111111-1111-1111-1111-111111111111"

// azLoginEnv isolates this test's CLI state. `az cloud set` and `az login`
// mutate the configuration directory, so the login test must not share the one
// the rest of the suite uses for `az rest`.
type azLoginEnv struct {
	baseURL   string
	configDir string
	caBundle  string
}

func (e azLoginEnv) command(args ...string) *exec.Cmd {
	cmd := exec.Command("az", args...)
	cmd.Env = append(os.Environ(),
		"AZURE_CONFIG_DIR="+e.configDir,
		"AZURE_CORE_NO_COLOR=1",
		"AZURE_CORE_COLLECT_TELEMETRY=0",
		"REQUESTS_CA_BUNDLE="+e.caBundle,
	)
	return cmd
}

// TestAzLogin_ServicePrincipalAgainstSimulatorCloud runs the whole operator
// flow end to end: register the simulator as a cloud, select it, log in with a
// service principal, and then drive native `az` commands whose bearer token the
// CLI acquired and attached itself.
func TestAzLogin_ServicePrincipalAgainstSimulatorCloud(t *testing.T) {
	env := startAzLoginSimulator(t)

	// Register the simulator's coordinates as an Azure CLI cloud.
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-sim",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-sim"))

	// The login the issue reports as impossible.
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id",
		"-p", "test-client-secret",
		"--tenant", azLoginTenantID,
		"--allow-no-subscriptions"))

	// The CLI now holds a simulator-minted token and reports the identity it
	// logged in as.
	var account struct {
		EnvironmentName string `json:"environmentName"`
		ID              string `json:"id"`
		TenantID        string `json:"tenantId"`
		User            struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"user"`
	}
	parseJSON(t, runCLI(t, env.command("account", "show", "-o", "json")), &account)
	if account.EnvironmentName != "sockerless-sim" {
		t.Errorf("environmentName = %q, want \"sockerless-sim\"", account.EnvironmentName)
	}
	if account.User.Name != "test-client-id" || account.User.Type != "servicePrincipal" {
		t.Errorf("logged in as %q (%q), want test-client-id (servicePrincipal)",
			account.User.Name, account.User.Type)
	}
	if account.TenantID != azLoginTenantID {
		t.Errorf("tenantId = %q, want %q", account.TenantID, azLoginTenantID)
	}
	if account.ID != subscriptionID {
		t.Errorf("subscription id = %q, want %q", account.ID, subscriptionID)
	}

	// Native `az` commands — not `az rest` with a hand-fetched bearer — now work
	// against the simulator, which is what the issue asks for. Create a resource
	// group and read it back through the CLI's own resource commands.
	groupName := "az-login-rg"
	var created struct {
		Name     string `json:"name"`
		Location string `json:"location"`
	}
	parseJSON(t, runCLI(t, env.command("group", "create",
		"-n", groupName, "-l", "eastus", "-o", "json")), &created)
	if created.Name != groupName {
		t.Errorf("created group name = %q, want %q", created.Name, groupName)
	}

	var listed []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command("group", "list", "-o", "json")), &listed)
	found := false
	for _, g := range listed {
		if g.Name == groupName {
			found = true
		}
	}
	if !found {
		t.Errorf("az group list did not return %q: %+v", groupName, listed)
	}

	runCLI(t, env.command("logout"))
}

// TestAzLogin_InstanceDiscoveryThroughCLI drives the Entra instance discovery
// endpoint with the CLI's own HTTP client. The endpoint is unauthenticated on
// Entra and on the simulator alike, and answers which authority hosts this
// identity service vouches for.
func TestAzLogin_InstanceDiscoveryThroughCLI(t *testing.T) {
	env := startAzLoginSimulator(t)
	host := strings.TrimPrefix(env.baseURL, "https://")

	var discovered struct {
		TenantDiscoveryEndpoint string `json:"tenant_discovery_endpoint"`
		APIVersion              string `json:"api-version"`
		Metadata                []struct {
			PreferredNetwork string   `json:"preferred_network"`
			PreferredCache   string   `json:"preferred_cache"`
			Aliases          []string `json:"aliases"`
		} `json:"metadata"`
	}
	out := runCLI(t, env.command("rest", "--method", "GET", "--skip-authorization-header",
		"--url", fmt.Sprintf("%s/common/discovery/instance?api-version=1.1&authorization_endpoint=https%%3A%%2F%%2F%s%%2F%s%%2Foauth2%%2Fv2.0%%2Fauthorize",
			env.baseURL, host, azLoginTenantID),
		"-o", "json"))
	parseJSON(t, out, &discovered)

	wantEndpoint := fmt.Sprintf("%s/%s/v2.0/.well-known/openid-configuration", env.baseURL, azLoginTenantID)
	if discovered.TenantDiscoveryEndpoint != wantEndpoint {
		t.Errorf("tenant_discovery_endpoint = %q, want %q", discovered.TenantDiscoveryEndpoint, wantEndpoint)
	}
	if discovered.APIVersion != "1.1" {
		t.Errorf("api-version = %q, want \"1.1\"", discovered.APIVersion)
	}
	if len(discovered.Metadata) != 1 || discovered.Metadata[0].PreferredNetwork != host {
		t.Errorf("metadata = %+v, want a single entry preferring %q", discovered.Metadata, host)
	}

	// An authority the simulator does not serve is rejected, which is the
	// answer MSAL turns into its invalid_instance error.
	rejected := env.command("rest", "--method", "GET", "--skip-authorization-header",
		"--url", fmt.Sprintf("%s/common/discovery/instance?api-version=1.1&authorization_endpoint=https%%3A%%2F%%2Fsomewhere-else.test%%2F%s%%2Foauth2%%2Fv2.0%%2Fauthorize",
			env.baseURL, azLoginTenantID))
	rejected.Env = append(rejected.Env, "PYTHONWARNINGS=ignore")
	rejectedOut, err := rejected.CombinedOutput()
	if err == nil {
		t.Fatalf("az rest against a foreign authority succeeded, want a 400: %s", rejectedOut)
	}
	if !strings.Contains(string(rejectedOut), "invalid_instance") ||
		!strings.Contains(string(rejectedOut), "AADSTS50049") {
		t.Errorf("rejection did not carry invalid_instance/AADSTS50049: %s", rejectedOut)
	}
}

// startAzLoginSimulator runs a TLS-terminated simulator and returns the
// coordinates the CLI needs to reach it. MSAL refuses a plain-http authority,
// so this test cannot share the suite's http listener.
func startAzLoginSimulator(t *testing.T) azLoginEnv {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := writeAzLoginTLSCert(t, dir)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port for the TLS simulator: %v", err)
	}
	addr := listener.Addr().String()
	port := addr[strings.LastIndex(addr, ":"):]
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}

	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(),
		"SIM_LISTEN_ADDR="+addr,
		"SIM_TLS_CERT="+certPath,
		"SIM_TLS_KEY="+keyPath,
		"SIM_RUNTIME=process",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the TLS simulator: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// The certificate names localhost, so address the simulator by that name —
	// the CLI verifies the hostname against the trusted bundle.
	base := "https://localhost" + port
	probe := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // health probe only; the CLI itself verifies against the bundle
		},
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := probe.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS simulator at %s did not become healthy", base)
		}
		time.Sleep(100 * time.Millisecond)
	}

	return azLoginEnv{
		baseURL:   base,
		configDir: filepath.Join(dir, "azure-config"),
		caBundle:  certPath,
	}
}

// writeAzLoginTLSCert issues the certificate the simulator serves and the CLI
// trusts — the stand-in for the private certificate authority an operator
// points REQUESTS_CA_BUNDLE at.
func writeAzLoginTLSCert(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate simulator TLS key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate simulator TLS serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create simulator TLS certificate: %v", err)
	}
	certPath := filepath.Join(dir, "sim-cert.pem")
	keyPath := filepath.Join(dir, "sim-key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0o600); err != nil {
		t.Fatalf("write simulator TLS certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatalf("write simulator TLS key: %v", err)
	}
	return certPath, keyPath
}
