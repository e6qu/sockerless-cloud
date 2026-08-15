package azure_sdk_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/containers/azcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Azure Container Registry data plane authenticates every request. These
// tests reach it the way real clients do — at the registry's own login server,
// through the Docker Registry v2 Bearer challenge — with the Azure SDK for Go's
// container-registry client and with the raw Docker protocol a `docker push`
// speaks.

// acrRegistryFixture is a registry created through Azure Resource Manager,
// together with the coordinates and the admin credential its data plane
// authenticates.
type acrRegistryFixture struct {
	resourceGroup string
	name          string
	loginServer   string
	username      string
	passwords     []string
}

// endpoint is the registry's data-plane base URL: its advertised login server,
// which is the only coordinate that differs from a real registry.
func (f acrRegistryFixture) endpoint() string {
	return "http://" + f.loginServer
}

func (f acrRegistryFixture) basic(password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.username+":"+password))
}

// newACRRegistryFixture creates a registry with its admin user enabled and
// reads the admin credential back through listCredentials, the way
// `az acr credential show` and the azurerm provider do.
func newACRRegistryFixture(t *testing.T, rgName, registryName string, properties *armcontainerregistry.RegistryProperties) acrRegistryFixture {
	t.Helper()
	ensureRG(t, rgName)

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreate(ctx, rgName, registryName, armcontainerregistry.Registry{
		Location:   to.Ptr("eastus"),
		SKU:        &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameStandard)},
		Properties: properties,
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.LoginServer)

	fixture := acrRegistryFixture{
		resourceGroup: rgName,
		name:          registryName,
		loginServer:   *created.Properties.LoginServer,
	}
	if properties != nil && properties.AdminUserEnabled != nil && *properties.AdminUserEnabled {
		creds, err := client.ListCredentials(ctx, rgName, registryName, nil)
		require.NoError(t, err)
		require.NotNil(t, creds.Username)
		fixture.username = *creds.Username
		for _, password := range creds.Passwords {
			require.NotNil(t, password.Value)
			fixture.passwords = append(fixture.passwords, *password.Value)
		}
		require.Len(t, fixture.passwords, 2, "a registry serves two admin password slots")
	}
	return fixture
}

// acrDoRequest sends one data-plane request to a registry's login server.
func acrDoRequest(t *testing.T, method, rawURL, authorization, contentType string, body []byte) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	require.NoError(t, err)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// acrChallengeParamPattern matches one quoted parameter of a Bearer challenge.
// The parameters are comma separated and their values are quoted, and a value
// may itself contain commas — a `pull,push` scope does — so they are read as
// quoted pairs rather than split on the comma.
var acrChallengeParamPattern = regexp.MustCompile(`([a-zA-Z_]+)="([^"]*)"`)

// acrChallengeParams parses a Www-Authenticate Bearer challenge into its
// parameters, the way every Docker Registry v2 client does before it goes to
// the token service.
func acrChallengeParams(t *testing.T, header string) map[string]string {
	t.Helper()
	require.True(t, strings.HasPrefix(header, "Bearer "), "challenge %q must be a Bearer challenge", header)
	params := map[string]string{}
	for _, match := range acrChallengeParamPattern.FindAllStringSubmatch(strings.TrimPrefix(header, "Bearer "), -1) {
		params[match[1]] = match[2]
	}
	return params
}

// acrClientTransport dials the simulator through the test process's default
// client, whose dialer resolves the `.localhost` data-plane hosts the
// simulator advertises. The request — its URL and its Host header — is the one
// the client would send to a real registry; only name resolution differs.
type acrClientTransport struct{}

func (acrClientTransport) Do(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// acrClientOptions points an azcontainerregistry client at a registry's login
// server over the simulator's plain-HTTP coordinate.
func acrClientOptions() *azcontainerregistry.ClientOptions {
	return &azcontainerregistry.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport:                       acrClientTransport{},
			InsecureAllowCredentialWithHTTP: true,
		},
	}
}

// acrTokenFromChallenge runs the exchange `docker login` runs: HTTP Basic on
// the realm the challenge named, with the challenge's service and scope.
func acrTokenFromChallenge(t *testing.T, params map[string]string, authorization string) (string, int) {
	t.Helper()
	tokenURL := fmt.Sprintf("%s?service=%s&scope=%s",
		params["realm"], url.QueryEscape(params["service"]), url.QueryEscape(params["scope"]))
	resp := acrDoRequest(t, http.MethodGet, tokenURL, authorization, "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.AccessToken, resp.StatusCode
}

const acrAuthTestManifest = `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`

// TestACR_DataPlaneRefusesUnauthenticatedRequests asserts the registry answers
// an unauthenticated Docker Registry v2 request with the Bearer challenge that
// tells a client where its token service is, and with the registry's error
// envelope.
func TestACR_DataPlaneRefusesUnauthenticatedRequests(t *testing.T) {
	fixture := newACRRegistryFixture(t, "acr-auth-rg", "acrauthregistry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})

	resp := acrDoRequest(t, http.MethodGet, fixture.endpoint()+"/v2/", "", "", nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "body: %s", body)

	params := acrChallengeParams(t, resp.Header.Get("Www-Authenticate"))
	assert.Equal(t, "http://"+fixture.loginServer+"/oauth2/token", params["realm"])
	assert.Equal(t, fixture.loginServer, params["service"])
	assert.Equal(t, "registry:catalog:*", params["scope"])

	var envelope struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.NotEmpty(t, envelope.Errors)
	assert.Equal(t, "UNAUTHORIZED", envelope.Errors[0].Code)
	assert.Equal(t, "authentication required", envelope.Errors[0].Message)

	// A push is refused before it reaches the registry's storage.
	push := acrDoRequest(t, http.MethodPut, fixture.endpoint()+"/v2/unauth/app/manifests/v1", "",
		"application/vnd.docker.distribution.manifest.v2+json", []byte(acrAuthTestManifest))
	push.Body.Close()
	require.Equal(t, http.StatusUnauthorized, push.StatusCode)

	token, _ := acrTokenFromChallenge(t,
		map[string]string{"realm": fixture.endpoint() + "/oauth2/token", "service": fixture.loginServer, "scope": "repository:unauth/app:pull"},
		fixture.basic(fixture.passwords[0]))
	pull := acrDoRequest(t, http.MethodGet, fixture.endpoint()+"/v2/unauth/app/manifests/v1", "Bearer "+token, "", nil)
	pull.Body.Close()
	assert.Equal(t, http.StatusNotFound, pull.StatusCode,
		"the refused push must not have stored a manifest")
}

// TestACR_DockerLoginTokenFlow drives the exchange a `docker login` +
// `docker push` performs against a registry: the challenge, the Basic
// credential traded for a scoped access token at the challenge's realm, and
// the push and pull that token authorizes — and no other.
func TestACR_DockerLoginTokenFlow(t *testing.T) {
	fixture := newACRRegistryFixture(t, "acr-auth-rg", "acrdockerloginregistry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})

	const repo = "team/service"
	manifestURL := fixture.endpoint() + "/v2/" + repo + "/manifests/v1"

	challenge := acrDoRequest(t, http.MethodPut, manifestURL, "",
		"application/vnd.docker.distribution.manifest.v2+json", []byte(acrAuthTestManifest))
	challenge.Body.Close()
	require.Equal(t, http.StatusUnauthorized, challenge.StatusCode)
	params := acrChallengeParams(t, challenge.Header.Get("Www-Authenticate"))
	require.Equal(t, "repository:"+repo+":pull,push", params["scope"])

	token, status := acrTokenFromChallenge(t, params, fixture.basic(fixture.passwords[0]))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 2, strings.Count(token, "."), "an ACR access token is a JWT")

	push := acrDoRequest(t, http.MethodPut, manifestURL, "Bearer "+token,
		"application/vnd.docker.distribution.manifest.v2+json", []byte(acrAuthTestManifest))
	push.Body.Close()
	require.Equal(t, http.StatusCreated, push.StatusCode)

	pull := acrDoRequest(t, http.MethodGet, manifestURL, "Bearer "+token, "", nil)
	pulled, _ := io.ReadAll(pull.Body)
	pull.Body.Close()
	require.Equal(t, http.StatusOK, pull.StatusCode)
	assert.JSONEq(t, acrAuthTestManifest, string(pulled))

	// The same token does not carry another repository of the registry: the
	// registry challenges again, naming the scope the client is missing.
	foreign := acrDoRequest(t, http.MethodPut, fixture.endpoint()+"/v2/other/service/manifests/v1", "Bearer "+token,
		"application/vnd.docker.distribution.manifest.v2+json", []byte(acrAuthTestManifest))
	foreign.Body.Close()
	require.Equal(t, http.StatusUnauthorized, foreign.StatusCode)
	foreignParams := acrChallengeParams(t, foreign.Header.Get("Www-Authenticate"))
	assert.Equal(t, "insufficient_scope", foreignParams["error"])
	assert.Equal(t, "repository:other/service:pull,push", foreignParams["scope"])

	// A wrong password buys no token at all.
	if _, status := acrTokenFromChallenge(t, params, fixture.basic("not-the-password")); status != http.StatusUnauthorized {
		t.Fatalf("token service accepted a wrong password with status %d", status)
	}
	if _, status := acrTokenFromChallenge(t, params, ""); status != http.StatusUnauthorized {
		t.Fatalf("token service served a token with no credential, status %d", status)
	}
}

// TestACR_SDKChallengeFlow drives the Azure SDK for Go's container-registry
// client end to end against the registry's login server. The client's own
// authentication policy does the whole exchange — challenge, Microsoft Entra
// token to ACR refresh token at /oauth2/exchange, refresh token to a scoped
// access token at /oauth2/token, retry — so a green run is proof the flow is
// the one the real client performs.
func TestACR_SDKChallengeFlow(t *testing.T) {
	fixture := newACRRegistryFixture(t, "acr-auth-rg", "acrsdkflowregistry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})

	client, err := azcontainerregistry.NewClient(fixture.endpoint(), &fakeCredential{}, acrClientOptions())
	require.NoError(t, err)

	const repo = "sdk/flow"
	uploaded, err := client.UploadManifest(ctx, repo, "v1",
		azcontainerregistry.ContentTypeApplicationVndDockerDistributionManifestV2JSON,
		nopSeekCloser(strings.NewReader(acrAuthTestManifest)), nil)
	require.NoError(t, err, "the SDK's challenge flow must reach the authenticated data plane")
	require.NotNil(t, uploaded.DockerContentDigest)

	got, err := client.GetManifest(ctx, repo, "v1", nil)
	require.NoError(t, err)
	got.ManifestData.Close()
	assert.Equal(t, *uploaded.DockerContentDigest, *got.DockerContentDigest)

	// The convenience API is authenticated too, and the same client reaches it.
	pager := client.NewListRepositoriesPager(nil)
	var found bool
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, name := range page.Names {
			if name != nil && *name == repo {
				found = true
			}
		}
	}
	assert.True(t, found, "expected the catalog to list %q", repo)

	tags := client.NewListTagsPager(repo, nil)
	var tagged bool
	for tags.More() {
		page, err := tags.NextPage(ctx)
		require.NoError(t, err)
		for _, tag := range page.Tags {
			if tag != nil && tag.Name != nil && *tag.Name == "v1" {
				tagged = true
			}
		}
	}
	assert.True(t, tagged, "expected the tag list to include v1")

	// A client with no credential at all cannot read a registry that does not
	// serve anonymous pulls.
	anonymous, err := azcontainerregistry.NewClient(fixture.endpoint(), nil, acrClientOptions())
	require.NoError(t, err)
	_, err = anonymous.GetManifest(ctx, repo, "v1", nil)
	require.Error(t, err, "an anonymous client must not read a private registry")
}

// TestACR_ExchangeRefusesForeignEntraToken asserts /oauth2/exchange verifies
// the Microsoft Entra credential it is handed instead of minting a refresh
// token for anything presented.
func TestACR_ExchangeRefusesForeignEntraToken(t *testing.T) {
	fixture := newACRRegistryFixture(t, "acr-auth-rg", "acrexchangeregistry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})

	exchange := func(accessToken string) (int, string) {
		form := url.Values{
			"grant_type":   {"access_token"},
			"service":      {fixture.loginServer},
			"access_token": {accessToken},
		}
		req, err := http.NewRequest(http.MethodPost, fixture.endpoint()+"/oauth2/exchange", strings.NewReader(form.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	status, body := exchange("not-a-token")
	assert.Equal(t, http.StatusUnauthorized, status, "body: %s", body)

	// A genuine directory token issued for another audience is refused too:
	// the exchange is for a container-registry token, not for any token the
	// directory ever signed.
	armToken, _, err := fetchSimAccessToken("https://management.azure.com/.default")
	require.NoError(t, err)
	status, body = exchange(armToken)
	assert.Equal(t, http.StatusUnauthorized, status, "body: %s", body)
	assert.Contains(t, body, "containerregistry.azure.net")

	// The container-registry audience is accepted, and yields a refresh token
	// that is a JWT — the Azure SDK reads its expiry out of the payload.
	acrToken, _, err := fetchSimAccessToken("https://containerregistry.azure.net/.default")
	require.NoError(t, err)
	status, body = exchange(acrToken)
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	var out struct {
		RefreshToken string `json:"refresh_token"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	assert.Equal(t, 2, strings.Count(out.RefreshToken, "."), "an ACR refresh token is a JWT")
}

// TestACR_RegenerateCredentialRevokesDerivedTokens asserts a regenerated admin
// password stops authenticating, and takes the access tokens it had already
// produced with it.
func TestACR_RegenerateCredentialRevokesDerivedTokens(t *testing.T) {
	fixture := newACRRegistryFixture(t, "acr-auth-rg", "acrrotationregistry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})

	params := map[string]string{
		"realm":   fixture.endpoint() + "/oauth2/token",
		"service": fixture.loginServer,
		"scope":   "repository:rotate/app:pull,push",
	}
	token, status := acrTokenFromChallenge(t, params, fixture.basic(fixture.passwords[0]))
	require.Equal(t, http.StatusOK, status)

	probe := func(authorization string) int {
		resp := acrDoRequest(t, http.MethodGet, fixture.endpoint()+"/v2/rotate/app/tags/list", authorization, "", nil)
		resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, http.StatusOK, probe("Bearer "+token))
	require.Equal(t, http.StatusOK, probe(fixture.basic(fixture.passwords[0])))

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	rotated, err := client.RegenerateCredential(ctx, fixture.resourceGroup, fixture.name,
		armcontainerregistry.RegenerateCredentialParameters{Name: to.Ptr(armcontainerregistry.PasswordNamePassword)}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, rotated.Passwords)
	require.NotNil(t, rotated.Passwords[0].Value)
	assert.NotEqual(t, fixture.passwords[0], *rotated.Passwords[0].Value, "regenerate must change the slot")

	assert.Equal(t, http.StatusUnauthorized, probe(fixture.basic(fixture.passwords[0])),
		"the rotated password must stop authenticating")
	assert.Equal(t, http.StatusUnauthorized, probe("Bearer "+token),
		"a token derived from the rotated password must stop authenticating")
	assert.Equal(t, http.StatusOK, probe(fixture.basic(*rotated.Passwords[0].Value)),
		"the new password must authenticate")
	assert.Equal(t, http.StatusOK, probe(fixture.basic(fixture.passwords[1])),
		"the untouched slot must keep authenticating")
}
