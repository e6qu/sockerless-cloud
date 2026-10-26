package azure_sdk_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
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

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/e6qu/sockerless-cloud/testutil/simready"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The front end terminates TLS, so these tests run a TLS simulator with the
// full workload runtime and reach the site and the container app by hostname,
// presenting a client certificate or none.

func writeTLSCertForHosts(t *testing.T, dir string, hosts []string) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hosts[0]},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              hosts,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPath, keyPath := filepath.Join(dir, "server-cert.pem"), filepath.Join(dir, "server-key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
	return certPath, keyPath
}

func newClientCertificate(t *testing.T, commonName string) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func startTLSWorkloadSimulator(t *testing.T, certPath, keyPath string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(),
		"SIM_LISTEN_ADDR=127.0.0.1:"+port,
		"SIM_TLS_CERT="+certPath,
		"SIM_TLS_KEY="+keyPath,
	)
	cmd.Stdout = os.Stdout
	require.NoError(t, simready.Start(cmd, os.Stderr), "start the TLS simulator")
	t.Cleanup(func() {
		if err := simready.Stop(cmd, simShutdownGrace); err != nil {
			t.Errorf("stop the TLS simulator: %v", err)
		}
	})
	return port
}

// tlsSimCredential acquires tokens from the TLS simulator's own token endpoint
// through the client_credentials grant.
type tlsSimCredential struct {
	base   string
	client *http.Client
}

func (c *tlsSimCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	scope := strings.Join(opts.Scopes, " ")
	resp, err := c.client.PostForm(c.base+"/"+simTenantID+"/oauth2/v2.0/token", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {"test-client-id"},
		"client_secret": {"test-client-secret"}, "scope": {scope},
	})
	if err != nil {
		return azcore.AccessToken{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return azcore.AccessToken{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return azcore.AccessToken{}, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return azcore.AccessToken{}, err
	}
	return azcore.AccessToken{Token: out.AccessToken, ExpiresOn: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)}, nil
}

// trustedClient trusts the simulator's certificate, presents clientCert when
// given, and names serverName in the handshake as a client resolving that
// hostname does.
func trustedClient(t *testing.T, certPath, serverName string, clientCert *tls.Certificate) *http.Client {
	t.Helper()
	pemBytes, err := os.ReadFile(certPath)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pemBytes))
	cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, ServerName: serverName}
	if clientCert != nil {
		cfg.Certificates = []tls.Certificate{*clientCert}
	}
	return &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{TLSClientConfig: cfg}}
}

// frontEndOverTLS returns the status and, on 200, the headers the echoing
// workload received.
func frontEndOverTLS(t *testing.T, certPath, port, host, path string, clientCert *tls.Certificate) (int, http.Header, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:"+port+path, nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := trustedClient(t, certPath, host, clientCert).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, nil
	}
	var echoed http.Header
	require.NoError(t, json.Unmarshal(body, &echoed), string(body))
	return resp.StatusCode, echoed, nil
}

// App Service with client certificates enabled asks for one at the TLS
// handshake and forwards it base64-encoded as X-ARR-ClientCert. Required
// answers 403 without one outside the exclusion paths, which never ask;
// Optional lets a request without one through.
func TestSDK_WebApps_ClientCertificates(t *testing.T) {
	t.Parallel()
	siteName := uniquename.New("sdk-mtls-app")
	host := siteName + ".azurewebsites.net"
	dir := t.TempDir()
	certPath, keyPath := writeTLSCertForHosts(t, dir, []string{"localhost", host})
	port := startTLSWorkloadSimulator(t, certPath, keyPath)
	armBase := "https://localhost:" + port
	cred := &tlsSimCredential{base: armBase, client: trustedClient(t, certPath, "localhost", nil)}
	opts := &arm.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: trustedClient(t, certPath, "localhost", nil),
		Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
			cloud.ResourceManager: {Endpoint: armBase, Audience: "https://management.azure.com/"},
		}},
	}}
	rg := "sdk-mtls-rg"
	ensureRGOver(t, opts.Transport.(*http.Client), armBase, rg)
	plans, err := armappservice.NewPlansClient(subscriptionID, cred, opts)
	require.NoError(t, err)
	planPoller, err := plans.BeginCreateOrUpdate(ctx, rg, siteName+"-plan", armappservice.Plan{
		Location:   to.Ptr("eastus"),
		Kind:       to.Ptr("linux"),
		SKU:        &armappservice.SKUDescription{Name: to.Ptr("B1")},
		Properties: &armappservice.PlanProperties{Reserved: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	plan, err := planPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	sites, err := armappservice.NewWebAppsClient(subscriptionID, cred, opts)
	require.NoError(t, err)
	poller, err := sites.BeginCreateOrUpdate(ctx, rg, siteName, armappservice.Site{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID:             plan.ID,
			ClientCertEnabled:        to.Ptr(true),
			ClientCertMode:           to.Ptr(armappservice.ClientCertModeRequired),
			ClientCertExclusionPaths: to.Ptr("/public"),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + httpProbeImageName),
				AppCommandLine: to.Ptr("echo-headers"),
				AppSettings:    []*armappservice.NameValuePair{{Name: to.Ptr("WEBSITES_PORT"), Value: to.Ptr("8080")}},
			},
		},
	}, nil)
	require.NoError(t, err)
	site, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sites.Delete(ctx, rg, siteName, nil) })
	assert.True(t, ptrVal(site.Properties.ClientCertEnabled))
	assert.Equal(t, armappservice.ClientCertModeRequired, ptrVal(site.Properties.ClientCertMode))
	assert.Equal(t, "/public", ptrVal(site.Properties.ClientCertExclusionPaths))

	clientCert := newClientCertificate(t, "mtls-client")
	status, echoed, err := frontEndOverTLS(t, certPath, port, host, "/", &clientCert)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, base64.StdEncoding.EncodeToString(clientCert.Leaf.Raw), echoed.Get("X-ARR-ClientCert"),
		"the app receives the client certificate base64-encoded in X-ARR-ClientCert")
	assert.Equal(t, "https", echoed.Get("X-Forwarded-Proto"))

	status, _, err = frontEndOverTLS(t, certPath, port, host, "/", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, status, "Required refuses a request without a certificate")

	status, echoed, err = frontEndOverTLS(t, certPath, port, host, "/public/index.html", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "an excluded path needs no certificate")
	assert.Empty(t, echoed.Get("X-ARR-ClientCert"))
	status, echoed, err = frontEndOverTLS(t, certPath, port, host, "/Public/also", &clientCert)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, echoed.Get("X-ARR-ClientCert"), "an excluded path is not asked for a certificate, whatever its case")

	_, err = sites.Update(ctx, rg, siteName, armappservice.SitePatchResource{
		Properties: &armappservice.SitePatchResourceProperties{ClientCertMode: to.Ptr(armappservice.ClientCertModeOptional)},
	}, nil)
	require.NoError(t, err)
	got, err := sites.Get(ctx, rg, siteName, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ClientCertModeOptional, ptrVal(got.Properties.ClientCertMode))
	assert.True(t, ptrVal(got.Properties.ClientCertEnabled), "a PATCH of the mode keeps the rest")
	status, echoed, err = frontEndOverTLS(t, certPath, port, host, "/", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "Optional lets a request without a certificate through")
	assert.Empty(t, echoed.Get("X-ARR-ClientCert"))
	status, echoed, err = frontEndOverTLS(t, certPath, port, host, "/", &clientCert)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, base64.StdEncoding.EncodeToString(clientCert.Leaf.Raw), echoed.Get("X-ARR-ClientCert"))
}

func ensureRGOver(t *testing.T, client *http.Client, armBase, rg string) {
	t.Helper()
	cred := &tlsSimCredential{base: armBase, client: client}
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://management.azure.com/.default"}})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		armBase+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01", strings.NewReader(`{"location":"eastus"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.Token)
	resp, err := client.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode, "%s", body)
}

// Container Apps ingress with clientCertificateMode accept forwards the
// client certificate as X-Forwarded-Client-Cert — Hash=<SHA-256>;Cert="<PEM>"
// — and lets a client without one through; require refuses the client at the
// handshake; ignore drops the certificate.
func TestSDK_ContainerAppsIngress_ClientCertificates(t *testing.T) {
	t.Parallel()
	appName := uniquename.New("acamtls")
	dir := t.TempDir()
	certPath, keyPath := writeTLSCertForHosts(t, dir, []string{"localhost", "*.internal.sim-env.localhost"})
	port := startTLSWorkloadSimulator(t, certPath, keyPath)
	armBase := "https://localhost:" + port
	cred := &tlsSimCredential{base: armBase, client: trustedClient(t, certPath, "localhost", nil)}
	opts := &arm.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: trustedClient(t, certPath, "localhost", nil),
		Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
			cloud.ResourceManager: {Endpoint: armBase, Audience: "https://management.azure.com/"},
		}},
	}}
	rg := "sdk-aca-mtls-rg"
	ensureRGOver(t, opts.Transport.(*http.Client), armBase, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, opts)
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, appName, armappcontainers.ContainerApp{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.ContainerAppProperties{
			EnvironmentID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.App/managedEnvironments/sim-env"),
			Configuration: &armappcontainers.Configuration{Ingress: &armappcontainers.Ingress{
				External:              to.Ptr(true),
				TargetPort:            to.Ptr[int32](8080),
				ClientCertificateMode: to.Ptr(armappcontainers.IngressClientCertificateModeAccept),
			}},
			Template: &armappcontainers.Template{
				Containers: []*armappcontainers.Container{{Name: to.Ptr("main"), Image: to.Ptr(httpProbeImageName), Args: []*string{to.Ptr("echo-headers")}}},
				Scale:      &armappcontainers.Scale{MinReplicas: to.Ptr[int32](1), MaxReplicas: to.Ptr[int32](1)},
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, appName, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	fqdn := ptrVal(created.Properties.Configuration.Ingress.Fqdn)
	require.Equal(t, appName+".internal.sim-env.localhost", fqdn)
	assert.Equal(t, armappcontainers.IngressClientCertificateModeAccept, ptrVal(created.Properties.Configuration.Ingress.ClientCertificateMode))

	clientCert := newClientCertificate(t, "aca-mtls-client")
	status, echoed, err := frontEndOverTLS(t, certPath, port, fqdn, "/", &clientCert)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	forwarded := echoed.Get("X-Forwarded-Client-Cert")
	thumbprint := sha256.Sum256(clientCert.Leaf.Raw)
	require.True(t, strings.HasPrefix(forwarded, "Hash="+hex.EncodeToString(thumbprint[:])+`;Cert="`), "X-Forwarded-Client-Cert %q", forwarded)
	encoded := strings.TrimSuffix(strings.TrimPrefix(forwarded, "Hash="+hex.EncodeToString(thumbprint[:])+`;Cert="`), `"`)
	decoded, err := url.PathUnescape(encoded)
	require.NoError(t, err)
	assert.Equal(t, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCert.Leaf.Raw})), decoded,
		"Cert carries the client certificate in PEM")
	assert.NotContains(t, forwarded, "Chain=", "a self-signed certificate brings no intermediates")

	status, echoed, err = frontEndOverTLS(t, certPath, port, fqdn, "/", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "accept lets a client without a certificate through")
	assert.Empty(t, echoed.Get("X-Forwarded-Client-Cert"))

	update := func(mode armappcontainers.IngressClientCertificateMode) {
		t.Helper()
		patch, err := client.BeginUpdate(ctx, rg, appName, armappcontainers.ContainerApp{
			Properties: &armappcontainers.ContainerAppProperties{Configuration: &armappcontainers.Configuration{
				Ingress: &armappcontainers.Ingress{ClientCertificateMode: to.Ptr(mode)},
			}},
		}, nil)
		require.NoError(t, err)
		_, err = patch.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		got, err := client.Get(ctx, rg, appName, nil)
		require.NoError(t, err)
		assert.Equal(t, mode, ptrVal(got.Properties.Configuration.Ingress.ClientCertificateMode))
	}
	update(armappcontainers.IngressClientCertificateModeRequire)
	_, _, err = frontEndOverTLS(t, certPath, port, fqdn, "/", nil)
	require.Error(t, err, "require refuses a client that presents no certificate")
	status, echoed, err = frontEndOverTLS(t, certPath, port, fqdn, "/", &clientCert)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, echoed.Get("X-Forwarded-Client-Cert"), "Hash="+hex.EncodeToString(thumbprint[:]))

	update(armappcontainers.IngressClientCertificateModeIgnore)
	status, echoed, err = frontEndOverTLS(t, certPath, port, fqdn, "/", &clientCert)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, echoed.Get("X-Forwarded-Client-Cert"), "ignore drops the certificate")
}
