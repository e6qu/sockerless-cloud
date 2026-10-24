package azure_sdk_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An App Service app on a simulator that terminates TLS still reaches its
// managed identity endpoint: IDENTITY_ENDPOINT is a plain-HTTP URL at the
// platform host, and the simulator serves it plain on the callback listener
// beside the TLS one, so the app's token request needs no certificate
// authority of the operator's.
//
//	PUT  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{name}
//	PUT  /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}
//	GET {IDENTITY_ENDPOINT}?api-version=2019-08-01&resource={resource}

// tlsSimulator is a TLS-terminated simulator a test started, with the client
// that trusts its certificate and the ARM options that address it.
type tlsSimulator struct {
	baseURL string
	client  *http.Client
}

func startTLSSiteSimulator(t *testing.T) tlsSimulator {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := writeAuthorityTLSCert(t, dir, "localhost")
	addr := startTLSSimulator(t, certPath, keyPath)
	pemBytes, err := os.ReadFile(certPath)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(pemBytes))
	return tlsSimulator{
		baseURL: "https://" + addr,
		client: &http.Client{
			Timeout:   60 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		},
	}
}

func (s tlsSimulator) clientOpts() *arm.ClientOptions {
	return &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Cloud: cloud.Configuration{
				Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
					cloud.ResourceManager: {Endpoint: s.baseURL, Audience: "https://management.azure.com/"},
				},
			},
			Transport: s.client,
		},
	}
}

// GetToken acquires a token from this simulator's own token endpoint: each
// process signs with a key of its own, so the suite's shared bearer does not
// authenticate here.
func (s tlsSimulator) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	scope := strings.Join(opts.Scopes, " ")
	if scope == "" {
		scope = "https://management.azure.com/.default"
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/"+simTenantID+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return azcore.AccessToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
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

// siteGet sends one GET to a site's hostname on this simulator.
func (s tlsSimulator) siteGet(t *testing.T, host, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	require.NoError(t, err)
	req.Host = host + ".azurewebsites.net"
	resp, err := s.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func TestSDK_WebApps_IdentityEndpointReachableUnderTLS(t *testing.T) {
	t.Parallel()
	sim := startTLSSiteSimulator(t)
	const rg, name = "sdk-tls-identity-rg", "sdk-tls-identity-app"

	token, err := sim.GetToken(ctx, policy.TokenRequestOptions{})
	require.NoError(t, err)
	rgReq, err := http.NewRequestWithContext(ctx, http.MethodPut,
		sim.baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		strings.NewReader(`{"location":"eastus"}`))
	require.NoError(t, err)
	rgReq.Header.Set("Content-Type", "application/json")
	rgReq.Header.Set("Authorization", "Bearer "+token.Token)
	rgResp, err := sim.client.Do(rgReq)
	require.NoError(t, err)
	rgResp.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, rgResp.StatusCode)

	plans, err := armappservice.NewPlansClient(subscriptionID, sim, sim.clientOpts())
	require.NoError(t, err)
	planPoller, err := plans.BeginCreateOrUpdate(ctx, rg, name+"-plan", armappservice.Plan{
		Location:   to.Ptr("eastus"),
		Kind:       to.Ptr("linux"),
		SKU:        &armappservice.SKUDescription{Name: to.Ptr("B1"), Tier: to.Ptr("Basic")},
		Properties: &armappservice.PlanProperties{Reserved: to.Ptr(true)},
	}, nil)
	require.NoError(t, err)
	plan, err := planPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	sites, err := armappservice.NewWebAppsClient(subscriptionID, sim, sim.clientOpts())
	require.NoError(t, err)
	sitePoller, err := sites.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("app,linux,container"),
		Identity: &armappservice.ManagedServiceIdentity{Type: to.Ptr(armappservice.ManagedServiceIdentityTypeSystemAssigned)},
		Properties: &armappservice.SiteProperties{
			ServerFarmID: plan.ID,
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("DOCKER|" + commandImageName),
				AppCommandLine: to.Ptr(filesHTTPCommand),
				AppSettings:    slotSettings(map[string]string{"WEBSITES_ENABLE_APP_SERVICE_STORAGE": "true"}),
			},
		},
	}, nil)
	require.NoError(t, err)
	site, err := sitePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sites.Delete(ctx, rg, name, nil) })
	require.NotNil(t, site.Identity)
	principal := *site.Identity.PrincipalID

	status, endpoint := sim.siteGet(t, name, "/env/IDENTITY_ENDPOINT")
	require.Equal(t, http.StatusOK, status, "the app has an IDENTITY_ENDPOINT: %s", endpoint)
	assert.True(t, strings.HasPrefix(endpoint, "http://"), "IDENTITY_ENDPOINT is plain HTTP: %s", endpoint)

	status, body := sim.siteGet(t, name, "/identity-token?"+url.Values{"resource": {"https://vault.azure.net"}}.Encode())
	require.Equal(t, http.StatusOK, status, "the app's token request under TLS: %s", body)
	var tokenBody struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &tokenBody), body)
	claims := decodeJWTClaims(t, tokenBody.AccessToken)
	assert.Equal(t, principal, claims["oid"], "the token is the app's own system-assigned identity's")
	assert.Equal(t, "https://vault.azure.net", claims["aud"])
}
