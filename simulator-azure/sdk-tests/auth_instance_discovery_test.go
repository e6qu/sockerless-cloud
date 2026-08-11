package azure_sdk_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
)

// Microsoft Entra ID instance discovery, exercised through the client contract
// that consumes it.
//
// `GET /common/discovery/instance` is how an OpenID Connect client decides
// whether an authority host belongs to the identity service and where that
// authority's tenant metadata lives. The response field names asserted here
// are the ones MSAL Go unmarshals (its InstanceDiscoveryResponse and
// InstanceDiscoveryMetadata types live in an internal package, so the contract
// is pinned by name rather than by importing the type), and the second test
// drives the real MSAL Go confidential client — the library azidentity itself
// wraps — through the discovery-to-token chain against the simulator.

// instanceDiscoveryResponse mirrors the wire contract MSAL Go decodes from the
// instance discovery endpoint. Field names, not Go types, are the contract.
type instanceDiscoveryResponse struct {
	TenantDiscoveryEndpoint string `json:"tenant_discovery_endpoint"`
	APIVersion              string `json:"api-version"`
	Metadata                []struct {
		PreferredNetwork string   `json:"preferred_network"`
		PreferredCache   string   `json:"preferred_cache"`
		Aliases          []string `json:"aliases"`
	} `json:"metadata"`
}

type instanceDiscoveryError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorCodes       []int  `json:"error_codes"`
	TraceID          string `json:"trace_id"`
	CorrelationID    string `json:"correlation_id"`
	Timestamp        string `json:"timestamp"`
}

func instanceDiscoveryURL(query url.Values) string {
	return baseURL + "/common/discovery/instance?" + query.Encode()
}

// simAuthorityHost is the host:port the simulator answers as, which is the
// authority it vouches for through instance discovery.
func simAuthorityHost(t *testing.T) string {
	t.Helper()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse simulator base URL %q: %v", baseURL, err)
	}
	return parsed.Host
}

// TestInstanceDiscovery_ServesTheContractMSALDecodes asserts the success body
// carries every field MSAL reads: the tenant discovery endpoint it fetches
// next, and the alias metadata it caches an authority under.
func TestInstanceDiscovery_ServesTheContractMSALDecodes(t *testing.T) {
	host := simAuthorityHost(t)
	resp, err := http.Get(instanceDiscoveryURL(url.Values{
		"api-version": {"1.1"},
		"authorization_endpoint": {fmt.Sprintf("https://%s/%s/oauth2/v2.0/authorize",
			host, simTenantID)},
	}))
	if err != nil {
		t.Fatalf("instance discovery request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("instance discovery status = %d, want 200", resp.StatusCode)
	}

	var body instanceDiscoveryResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode instance discovery response: %v", err)
	}
	wantEndpoint := fmt.Sprintf("%s/%s/v2.0/.well-known/openid-configuration", baseURL, simTenantID)
	if body.TenantDiscoveryEndpoint != wantEndpoint {
		t.Errorf("tenant_discovery_endpoint = %q, want %q", body.TenantDiscoveryEndpoint, wantEndpoint)
	}
	if body.APIVersion != "1.1" {
		t.Errorf("api-version = %q, want \"1.1\"", body.APIVersion)
	}
	if len(body.Metadata) != 1 {
		t.Fatalf("metadata has %d entries, want 1", len(body.Metadata))
	}
	entry := body.Metadata[0]
	if entry.PreferredNetwork != host || entry.PreferredCache != host {
		t.Errorf("preferred_network/preferred_cache = %q/%q, want %q for both",
			entry.PreferredNetwork, entry.PreferredCache, host)
	}
	if len(entry.Aliases) != 1 || entry.Aliases[0] != host {
		t.Errorf("aliases = %v, want [%q]", entry.Aliases, host)
	}

	// The advertised tenant document must resolve — a discovery response
	// pointing at a URL the simulator does not serve would strand every client
	// one hop later.
	docResp, err := http.Get(body.TenantDiscoveryEndpoint)
	if err != nil {
		t.Fatalf("fetch advertised tenant_discovery_endpoint: %v", err)
	}
	defer docResp.Body.Close()
	if docResp.StatusCode != http.StatusOK {
		t.Fatalf("advertised tenant_discovery_endpoint %s returned %d, want 200",
			body.TenantDiscoveryEndpoint, docResp.StatusCode)
	}
	var doc struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(docResp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode advertised tenant metadata: %v", err)
	}
	if doc.Issuer == "" || doc.TokenEndpoint == "" {
		t.Fatalf("advertised tenant metadata missing issuer/token_endpoint")
	}
}

// TestInstanceDiscovery_RejectsForeignAuthority asserts the rejection MSAL Go
// turns into its "invalid_instance: the authority host is not valid" error: a
// 400 whose body names invalid_instance. The simulator is a single authority
// instance and vouches for its own host alone.
func TestInstanceDiscovery_RejectsForeignAuthority(t *testing.T) {
	resp, err := http.Get(instanceDiscoveryURL(url.Values{
		"api-version":            {"1.1"},
		"authorization_endpoint": {"https://not-this-authority.test/" + simTenantID + "/oauth2/v2.0/authorize"},
	}))
	if err != nil {
		t.Fatalf("instance discovery request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body instanceDiscoveryError
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode instance discovery error: %v", err)
	}
	if body.Error != "invalid_instance" {
		t.Errorf("error = %q, want \"invalid_instance\"", body.Error)
	}
	if len(body.ErrorCodes) != 1 || body.ErrorCodes[0] != 50049 {
		t.Errorf("error_codes = %v, want [50049]", body.ErrorCodes)
	}
	if !strings.Contains(body.ErrorDescription, "AADSTS50049") {
		t.Errorf("error_description = %q, want it to carry AADSTS50049", body.ErrorDescription)
	}
	if body.TraceID == "" || body.CorrelationID == "" || body.Timestamp == "" {
		t.Errorf("error body missing trace_id/correlation_id/timestamp: %+v", body)
	}
}

// TestEntraAuthority_MSALGoAcquiresTokenThroughTenantDiscovery drives the real
// MSAL Go confidential client — the library the Azure SDK's azidentity wraps —
// against the simulator with the production authority string
// https://login.microsoftonline.com/{tenant}, unmodified. The only things that
// differ from a run against Azure are coordinates: the client's dialer resolves
// the authority host to the simulator's listener, and its TLS trust root is the
// certificate the simulator serves. No MSAL option is changed, and instance
// discovery is left enabled.
//
// MSAL requires an https authority, so this test runs its own TLS-terminated
// simulator rather than the plain-HTTP one the rest of the suite shares.
func TestEntraAuthority_MSALGoAcquiresTokenThroughTenantDiscovery(t *testing.T) {
	const authorityHost = "login.microsoftonline.com"

	dir := t.TempDir()
	certPath, keyPath := writeAuthorityTLSCert(t, dir, authorityHost)

	addr := startTLSSimulator(t, certPath, keyPath)
	httpClient := authorityRoutedHTTPClient(t, certPath, authorityHost, addr)

	cred, err := confidential.NewCredFromSecret("test-client-secret")
	if err != nil {
		t.Fatalf("build client secret credential: %v", err)
	}
	app, err := confidential.New(
		"https://"+authorityHost+"/"+simTenantID,
		"test-client-id",
		cred,
		confidential.WithHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatalf("build MSAL confidential client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := app.AcquireTokenByCredential(ctx, []string{"https://management.azure.com/.default"})
	if err != nil {
		t.Fatalf("MSAL client_credentials against the simulator authority: %v", err)
	}
	if result.AccessToken == "" {
		t.Fatal("MSAL returned an empty access token")
	}
	if result.ExpiresOn.Before(time.Now()) {
		t.Errorf("MSAL token already expired at %s", result.ExpiresOn)
	}

	// The token MSAL acquired must be one the simulator's Azure Resource
	// Manager plane accepts — proving the authority the client discovered and
	// the resource plane it then calls are the same identity service.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/subscriptions?api-version=2022-12-01", nil)
	if err != nil {
		t.Fatalf("build ARM request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+result.AccessToken)
	armResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ARM request with the MSAL-acquired token: %v", err)
	}
	defer armResp.Body.Close()
	if armResp.StatusCode != http.StatusOK {
		t.Fatalf("ARM returned %d for the MSAL-acquired token, want 200", armResp.StatusCode)
	}
}

// writeAuthorityTLSCert issues a self-signed certificate for the authority host
// and returns the certificate and key paths. It stands in for the certificate
// authority an operator trusts when pointing a client at a private deployment.
func writeAuthorityTLSCert(t *testing.T, dir, host string) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate authority TLS key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate authority TLS serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create authority TLS certificate: %v", err)
	}
	certPath := filepath.Join(dir, "authority-cert.pem")
	keyPath := filepath.Join(dir, "authority-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write authority TLS certificate: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write authority TLS key: %v", err)
	}
	return certPath, keyPath
}

// startTLSSimulator runs a second simulator process with TLS terminated, and
// returns its listen address. MSAL refuses a plain-http authority, so the
// authority coordinate has to be an https one.
func startTLSSimulator(t *testing.T, certPath, keyPath string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port for the TLS simulator: %v", err)
	}
	addr := listener.Addr().String()
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

	probe := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // health probe only, before the trusted-root client is built
		},
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := probe.Get("https://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return addr
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS simulator at %s did not become healthy", addr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// authorityRoutedHTTPClient returns an http.Client that resolves the authority
// host to the simulator's listener and trusts the certificate it serves — the
// two coordinates that point an otherwise unmodified client at a deployment
// other than Azure's public one.
func authorityRoutedHTTPClient(t *testing.T, certPath, authorityHost, addr string) *http.Client {
	t.Helper()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read authority certificate: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemBytes) {
		t.Fatal("authority certificate could not be added to the trust roots")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if host, _, err := net.SplitHostPort(address); err == nil && host == authorityHost {
					address = addr
				}
				return dialer.DialContext(ctx, network, address)
			},
		},
	}
}
