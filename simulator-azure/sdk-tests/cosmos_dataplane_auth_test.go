package azure_sdk_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Cosmos DB data plane authenticates every request with a token built from
// one of the account's four keys, so a client reaches it the way every real
// client does: create the account through Azure Resource Manager, read its
// keys with listKeys, and address the document endpoint ARM advertises for it.
// These helpers do exactly that, and the tests below prove the simulator
// accepts what the service accepts and refuses what it refuses.

// cosmosKeys mirrors the DatabaseAccountListKeysResult listKeys serves.
type cosmosKeys struct {
	PrimaryMasterKey           string `json:"primaryMasterKey"`
	SecondaryMasterKey         string `json:"secondaryMasterKey"`
	PrimaryReadonlyMasterKey   string `json:"primaryReadonlyMasterKey"`
	SecondaryReadonlyMasterKey string `json:"secondaryReadonlyMasterKey"`
}

// cosmosDataPlaneAccount is the Cosmos DB account the data-plane tests address.
// Every one of them provisions it through Azure Resource Manager and signs with
// the keys listKeys serves for it.
const cosmosDataPlaneAccount = "sdkdataplane"

var (
	cosmosAccountsOnce  sync.Mutex
	cosmosAccountsCache = map[string]cosmosKeys{}
)

// cosmosARM sends one Azure Resource Manager request to a named simulator,
// which is how a test that restarted the simulator on a new port reaches the
// control plane it now serves.
func cosmosARM(t *testing.T, base, method, path, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	if !strings.Contains(path, "api-version=") {
		path += "?api-version=2025-01-01"
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", cosmosARMBearer(t, base))
	resp, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, payload
}

// cosmosARMBearer is the Azure Resource Manager access token a simulator
// issues. Each simulator signs with its own key, so a test that restarted one
// acquires a token from the one it is now talking to rather than reusing the
// token the suite acquired at startup.
func cosmosARMBearer(t *testing.T, base string) string {
	t.Helper()
	if base == baseURL {
		return simARMBearer
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {"https://management.azure.com/.default"},
	}
	resp, err := http.PostForm(base+"/"+simTenantID+"/oauth2/v2.0/token", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "acquire access token: %s", body)
	var out struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.NotEmpty(t, out.AccessToken)
	return "Bearer " + out.AccessToken
}

// cosmosDataResourceGroup holds the Cosmos DB accounts the data-plane tests
// provision for themselves. A test that creates its own account elsewhere
// addresses it through cosmosAccountKeysIn instead, because an account name is
// a hostname in Azure and two records under one name would be two accounts with
// different keys answering at one endpoint.
const cosmosDataResourceGroup = "cosmos-data-rg"

// cosmosAccountPath is the Azure Resource Manager path of a Cosmos DB account
// the data-plane tests provision for themselves.
func cosmosAccountPath(account string) string {
	return cosmosAccountPathIn("00000000-0000-0000-0000-000000000000", cosmosDataResourceGroup, account)
}

func cosmosAccountPathIn(subscription, resourceGroup, account string) string {
	return "/subscriptions/" + subscription + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.DocumentDB/databaseAccounts/" + account
}

// cosmosAccountKeys provisions a Cosmos DB account and returns the keys
// listKeys serves for it, once per account per run.
func cosmosAccountKeys(t *testing.T, account string) cosmosKeys {
	t.Helper()
	return cosmosAccountKeysAt(t, baseURL, account)
}

func cosmosAccountKeysAt(t *testing.T, base, account string) cosmosKeys {
	t.Helper()
	return cosmosAccountKeysIn(t, base, cosmosAccountPath(account))
}

// cosmosAccountKeysIn provisions the account at one Azure Resource Manager path
// and returns the keys listKeys serves for it, once per path per run.
func cosmosAccountKeysIn(t *testing.T, base, accountPath string) cosmosKeys {
	t.Helper()
	cosmosAccountsOnce.Lock()
	defer cosmosAccountsOnce.Unlock()
	if keys, ok := cosmosAccountsCache[base+"|"+accountPath]; ok {
		return keys
	}
	status, body := cosmosARM(t, base, http.MethodPut, accountPath,
		`{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`)
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, status,
		"create Cosmos DB account %s: %s", accountPath, body)

	status, body = cosmosARM(t, base, http.MethodPost, accountPath+"/listKeys", "")
	require.Equal(t, http.StatusOK, status, "listKeys %s: %s", accountPath, body)
	var keys cosmosKeys
	require.NoError(t, json.Unmarshal(body, &keys), "decode listKeys: %s", body)
	require.NotEmpty(t, keys.PrimaryMasterKey)
	cosmosAccountsCache[base+"|"+accountPath] = keys
	return keys
}

// cosmosForgetAccountKeys drops a cached key set, so a test that regenerated a
// key reads the one the account serves now.
func cosmosForgetAccountKeys(account string) {
	cosmosAccountsOnce.Lock()
	defer cosmosAccountsOnce.Unlock()
	delete(cosmosAccountsCache, baseURL+"|"+cosmosAccountPath(account))
}

// cosmosAccountKeyIn is the primary key of an account a test provisioned at its
// own Azure Resource Manager coordinates.
func cosmosAccountKeyIn(t *testing.T, subscription, resourceGroup, account string) string {
	t.Helper()
	return cosmosAccountKeysIn(t, baseURL, cosmosAccountPathIn(subscription, resourceGroup, account)).PrimaryMasterKey
}

// cosmosAccountKey is the account's primary key, provisioning the account on
// first use.
func cosmosAccountKey(t *testing.T, account string) string {
	t.Helper()
	return cosmosAccountKeys(t, account).PrimaryMasterKey
}

// cosmosDocumentEndpoint is the endpoint Azure Resource Manager advertises for
// an account — the coordinate a real client hands its SDK.
func cosmosDocumentEndpoint(t *testing.T, account string) string {
	t.Helper()
	return cosmosDocumentEndpointAt(t, baseURL, account)
}

func cosmosDocumentEndpointAt(t *testing.T, base, account string) string {
	t.Helper()
	cosmosAccountKeysAt(t, base, account)
	status, body := cosmosARM(t, base, http.MethodGet, cosmosAccountPath(account), "")
	require.Equal(t, http.StatusOK, status, "read Cosmos DB account: %s", body)
	var account_ struct {
		Properties struct {
			DocumentEndpoint string `json:"documentEndpoint"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(body, &account_))
	require.NotEmpty(t, account_.Properties.DocumentEndpoint, "account advertises no documentEndpoint: %s", body)
	return account_.Properties.DocumentEndpoint
}

// cosmosSignDataPlane applies the shared-key authorization a Cosmos client
// builds for a request: the token
// `type=master&ver=1.0&sig=<base64 HMAC-SHA256>` over
// "{verb}\n{resourceType}\n{resourceLink}\n{date}\n\n", URL-encoded whole.
func cosmosSignDataPlane(t *testing.T, req *http.Request, key string) {
	t.Helper()
	date := time.Now().UTC().Format(http.TimeFormat)
	resourceType, resourceLink := cosmosSignedResourceOf(req.URL.Path)
	payload := strings.ToLower(req.Method) + "\n" + strings.ToLower(resourceType) + "\n" +
		resourceLink + "\n" + strings.ToLower(date) + "\n\n"
	material, err := base64.StdEncoding.DecodeString(key)
	require.NoError(t, err, "decode account key")
	mac := hmac.New(sha256.New, material)
	_, err = mac.Write([]byte(payload))
	require.NoError(t, err)
	req.Header.Set("x-ms-date", date)
	req.Header.Set("Authorization",
		url.QueryEscape("type=master&ver=1.0&sig="+base64.StdEncoding.EncodeToString(mac.Sum(nil))))
}

// cosmosSignedResourceOf derives what a request's signature covers: an
// operation over a set of resources signs the parent's link and the set's type,
// an operation on one resource signs its own link and the type it belongs to.
func cosmosSignedResourceOf(path string) (string, string) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments)%2 == 1 {
		return segments[len(segments)-1], strings.Join(segments[:len(segments)-1], "/")
	}
	return segments[len(segments)-2], strings.Join(segments, "/")
}

// cosmosSDKTransport dials the simulator while keeping the account hostname the
// client addressed in the request, which is how the service knows which
// account's keys sign it.
type cosmosSDKTransport struct {
	target string
}

func (t cosmosSDKTransport) Do(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(req.Context())
	rewritten.Host = req.URL.Host
	rewrittenURL := *req.URL
	rewrittenURL.Host = strings.TrimPrefix(t.target, "http://")
	rewrittenURL.Scheme = "http"
	rewritten.URL = &rewrittenURL
	return http.DefaultClient.Do(rewritten)
}

// cosmosSimClient builds the azcosmos client a real client builds from ARM: the
// account's advertised document endpoint and one of its keys.
func cosmosSimClient(t *testing.T, account string) *azcosmos.Client {
	t.Helper()
	return cosmosSimClientAt(t, baseURL, account)
}

func cosmosSimClientAt(t *testing.T, base, account string) *azcosmos.Client {
	t.Helper()
	return newCosmosSDKClient(t, cosmosDocumentEndpointAt(t, base, account),
		cosmosAccountKeysAt(t, base, account).PrimaryMasterKey, base)
}

// TestCosmos_DataPlaneAuthorization proves the data plane verifies the keys the
// control plane mints: the account's own keys authorize the operations they are
// allowed to, and a key the account never issued authorizes nothing.
func TestCosmos_DataPlaneAuthorization(t *testing.T) {
	const account = "authsdkcosmos"
	keys := cosmosAccountKeys(t, account)
	endpoint := cosmosDocumentEndpoint(t, account)

	// Positive: the primary key drives the SDK's full create-and-read path.
	client := newCosmosSDKClient(t, endpoint, keys.PrimaryMasterKey, baseURL)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "authdb"}, nil)
	require.NoError(t, err, "the account's primary key must authorize a write")
	database, err := client.NewDatabase("authdb")
	require.NoError(t, err)
	_, err = database.Read(ctx, nil)
	require.NoError(t, err, "the account's primary key must authorize a read")

	// The secondary key is live at the same time, which is what makes the
	// documented key rotation possible.
	secondary := newCosmosSDKClient(t, endpoint, keys.SecondaryMasterKey, baseURL)
	secondaryDatabase, err := secondary.NewDatabase("authdb")
	require.NoError(t, err)
	_, err = secondaryDatabase.Read(ctx, nil)
	require.NoError(t, err, "the account's secondary key must authorize a read")

	// A read-only key reads and does not write.
	readonly := newCosmosSDKClient(t, endpoint, keys.PrimaryReadonlyMasterKey, baseURL)
	readonlyDatabase, err := readonly.NewDatabase("authdb")
	require.NoError(t, err)
	_, err = readonlyDatabase.Read(ctx, nil)
	require.NoError(t, err, "a read-only key must authorize a read")
	_, err = readonly.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "readonlydb"}, nil)
	require.Error(t, err, "a read-only key must not authorize a write")
	assert.Contains(t, err.Error(), "401")

	// Negative: a key this account never issued authorizes nothing, and the
	// refusal is the service's MAC-signature one.
	foreign := newCosmosSDKClient(t, endpoint,
		base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")), baseURL)
	foreignDatabase, err := foreign.NewDatabase("authdb")
	require.NoError(t, err)
	_, err = foreignDatabase.Read(ctx, nil)
	require.Error(t, err, "a foreign key must not authorize a read")
	assert.Contains(t, err.Error(), "MAC signature")

	// A request with no credential at all is refused before it reaches any
	// document.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/dbs/authdb", nil)
	require.NoError(t, err)
	request.Header.Set("x-ms-cosmos-account", account)
	request.Header.Set("x-ms-version", "2018-12-31")
	resp, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "unauthenticated read: %s", body)
	assert.Contains(t, string(body), "Unauthorized")
}

// TestCosmos_DataPlaneHonoursRegeneratedKeys proves the data plane verifies the
// current key material: after regenerateKey the retired key stops authorizing
// and the key listKeys now serves starts.
func TestCosmos_DataPlaneHonoursRegeneratedKeys(t *testing.T) {
	const account = "rotatesdkcosmos"
	before := cosmosAccountKeys(t, account)
	endpoint := cosmosDocumentEndpoint(t, account)

	client := newCosmosSDKClient(t, endpoint, before.PrimaryMasterKey, baseURL)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "rotatedb"}, nil)
	require.NoError(t, err)

	resp := armReq(t, http.MethodPost, cosmosAccountPath(account)+"/regenerateKey", `{"keyKind":"primary"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent}, resp.StatusCode,
		"regenerateKey: %s", body)

	cosmosForgetAccountKeys(account)
	after := cosmosAccountKeys(t, account)
	require.NotEqual(t, before.PrimaryMasterKey, after.PrimaryMasterKey, "regenerateKey must change the primary key")

	retired, err := client.NewDatabase("rotatedb")
	require.NoError(t, err)
	_, err = retired.Read(ctx, nil)
	require.Error(t, err, "the retired key must stop authorizing")
	assert.Contains(t, err.Error(), "401")

	current, err := newCosmosSDKClient(t, endpoint, after.PrimaryMasterKey, baseURL).NewDatabase("rotatedb")
	require.NoError(t, err)
	_, err = current.Read(ctx, nil)
	require.NoError(t, err, "the regenerated key must authorize")
}

// TestCosmos_DocumentEndpointIsTheAccountsOwn pins the coordinate the whole
// data plane hangs off: ARM advertises a per-account document endpoint, and it
// is the account's hostname that tells the service whose keys sign a request.
func TestCosmos_DocumentEndpointIsTheAccountsOwn(t *testing.T) {
	const account = "endpointsdkcosmos"
	endpoint := cosmosDocumentEndpoint(t, account)
	parsed, err := url.Parse(endpoint)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(parsed.Hostname(), account+".documents."),
		"documentEndpoint %q must be the account's own host", endpoint)

	// Another account's key does not authorize a request addressed to this
	// account's endpoint, which is what makes the endpoint the credential's
	// scope.
	other := cosmosAccountKeys(t, "othersdkcosmos")
	client := newCosmosSDKClient(t, endpoint, other.PrimaryMasterKey, baseURL)
	database, err := client.NewDatabase("anything")
	require.NoError(t, err)
	_, err = database.Read(ctx, nil)
	require.Error(t, err, "another account's key must not authorize this account's endpoint")
	assert.Contains(t, err.Error(), fmt.Sprintf("%d", http.StatusUnauthorized))
}

// cosmosClientOptions carries the transport that reaches the simulator while
// preserving the account hostname the client addressed.
func cosmosClientOptions(dial string) azcore.ClientOptions {
	options := azcore.ClientOptions{InsecureAllowCredentialWithHTTP: true}
	if dial != "" {
		options.Transport = cosmosSDKTransport{target: dial}
	}
	return options
}
