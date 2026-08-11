package azure_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The restart suite launches its own simulator with SIM_PERSIST=true and a
// dedicated SIM_DATA_DIR (the shared TestMain instance runs without
// persistence), exercises real client surfaces, terminates the process with
// SIGTERM, relaunches it against the same data directory, and asserts the
// cloud state a real Azure tenant would retain is still there.

func freeAzureSimPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func startPersistentAzureSimulator(t *testing.T, stateDir string, port int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(binaryPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
		"SIM_RUNTIME=process",
		"SIM_PERSIST=true",
		"SIM_DATA_DIR="+stateDir,
		"SIM_LOG_LEVEL=warn",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	require.NoError(t, waitForHealth(fmt.Sprintf("http://127.0.0.1:%d/health", port)))
	return cmd
}

// shutdownAzureSimulator sends SIGTERM — the orderly-shutdown signal the
// simulator checkpoints its SQLite WAL on — and waits for a clean exit.
func shutdownAzureSimulator(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal simulator shutdown: %w", err)
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("wait for simulator shutdown: %w", err)
		}
		return nil
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		return fmt.Errorf("simulator did not exit within 30s of SIGTERM")
	}
}

// fetchAccessTokenFrom performs the OAuth2 client_credentials grant against an
// arbitrary simulator endpoint — the same request a real Azure AD confidential
// client makes — and returns the minted access token.
func fetchAccessTokenFrom(endpoint, clientID, clientSecret, scope string) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {scope},
	}
	resp, err := http.PostForm(endpoint+"/"+simTenantID+"/oauth2/v2.0/token", form)
	if err != nil {
		return "", fmt.Errorf("acquire access token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token response carried no access_token: %s", body)
	}
	return out.AccessToken, nil
}

// restartCredential is the azcore TokenCredential for the restart suite's
// private simulator instance: it acquires real bearer tokens through the
// client_credentials grant at that instance's endpoint.
type restartCredential struct{ endpoint string }

func (c *restartCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	scope := strings.Join(opts.Scopes, " ")
	if scope == "" {
		scope = "https://management.azure.com/.default"
	}
	token, err := fetchAccessTokenFrom(c.endpoint, "test-client-id", "test-client-secret", scope)
	if err != nil {
		return azcore.AccessToken{}, err
	}
	return azcore.AccessToken{Token: token, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func restartClientOpts(endpoint string) *arm.ClientOptions {
	return &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Cloud: cloud.Configuration{
				Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
					cloud.ResourceManager: {
						Endpoint: endpoint,
						Audience: "https://management.azure.com/",
					},
				},
			},
			InsecureAllowCredentialWithHTTP: true,
		},
	}
}

// restartRawReq performs a raw HTTP request against the private instance,
// optionally with a Host override (subdomain-routed data planes) and a bearer.
// restartServiceBusAuthorization provisions a Service Bus namespace on the
// restart suite's private simulator through ARM — which auto-provisions
// RootManageSharedAccessKey — reads that rule's real key with listKeys, and
// returns the Authorization header value a data-plane caller presents.
func restartServiceBusAuthorization(t *testing.T, endpoint, bearer, namespace string) string {
	t.Helper()
	const apiVersion = "2022-10-01-preview"
	rg := "persist-sb-rg"

	resp := restartRawReq(t, "PUT", endpoint,
		"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2021-04-01", "", bearer,
		[]byte(`{"location":"eastus"}`), map[string]string{"Content-Type": "application/json"})
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)
	resp.Body.Close()

	nsPath := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.ServiceBus/namespaces/" + namespace
	resp = restartRawReq(t, "PUT", endpoint, nsPath+"?api-version="+apiVersion, "", bearer,
		[]byte(`{"location":"eastus","sku":{"name":"Standard","tier":"Standard"}}`),
		map[string]string{"Content-Type": "application/json"})
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)
	resp.Body.Close()

	resp = restartRawReq(t, "POST", endpoint,
		nsPath+"/authorizationRules/"+messagingRootRule+"/listKeys?api-version="+apiVersion, "", bearer, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var keys struct {
		PrimaryKey string `json:"primaryKey"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&keys))
	resp.Body.Close()
	require.NotEmpty(t, keys.PrimaryKey)

	return messagingSASToken(messagingRootRule, keys.PrimaryKey,
		"https://"+namespace+".servicebus.localhost/")
}

func restartRawReq(t *testing.T, method, endpoint, path, host, bearer string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var br io.Reader
	if body != nil {
		br = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, br)
	require.NoError(t, err)
	if host != "" {
		req.Host = host
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// sbSequenceNumber parses the SequenceNumber out of a Service Bus receive
// response's BrokerProperties header.
func sbSequenceNumber(t *testing.T, resp *http.Response) int64 {
	t.Helper()
	raw := resp.Header.Get("BrokerProperties")
	require.NotEmpty(t, raw, "receive response must carry BrokerProperties")
	var bp struct {
		SequenceNumber int64 `json:"SequenceNumber"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &bp))
	return bp.SequenceNumber
}

func TestAzureServiceStateSurvivesSimulatorRestart_SDK(t *testing.T) {
	stateDir := t.TempDir()
	port := freeAzureSimPort(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := startPersistentAzureSimulator(t, stateDir, port)
	t.Cleanup(func() { _ = shutdownAzureSimulator(cmd) })

	// ---- (f) mint an ARM bearer BEFORE the restart ----
	preRestartBearer, err := fetchAccessTokenFrom(endpoint, "test-client-id", "test-client-secret", "https://management.azure.com/.default")
	require.NoError(t, err)

	// ---- (a) Microsoft Graph: user + app registration + client secret ----
	resp := restartRawReq(t, "POST", endpoint, "/v1.0/users", "", "", []byte(`{
		"displayName": "Persistent User",
		"userPrincipalName": "persistent.user@sockerless.example",
		"mailNickname": "persistent.user",
		"accountEnabled": true
	}`), map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var createdUser struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&createdUser))
	resp.Body.Close()
	require.NotEmpty(t, createdUser.ID)

	resp = restartRawReq(t, "POST", endpoint, "/v1.0/applications", "", "",
		[]byte(`{"displayName":"Persistent App","signInAudience":"AzureADMyOrg"}`),
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var createdApp struct {
		ID    string `json:"id"`
		AppID string `json:"appId"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&createdApp))
	resp.Body.Close()
	require.NotEmpty(t, createdApp.AppID)

	resp = restartRawReq(t, "POST", endpoint, "/v1.0/applications/"+createdApp.ID+"/addPassword", "", "",
		[]byte(`{"passwordCredential":{"displayName":"restart-secret"}}`),
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var minted struct {
		SecretText string `json:"secretText"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&minted))
	resp.Body.Close()
	require.NotEmpty(t, minted.SecretText)

	resp = restartRawReq(t, "POST", endpoint, "/v1.0/servicePrincipals", "", "",
		[]byte(`{"appId":"`+createdApp.AppID+`"}`),
		map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// ---- (b) Service Bus: enqueue a message before the restart ----
	// The data plane authenticates every caller with a Shared Access
	// Signature, so the namespace is provisioned through ARM first and the
	// message is signed with its root rule's real key.
	const sbHost = "persistns.servicebus.localhost"
	sbAuth := restartServiceBusAuthorization(t, endpoint, preRestartBearer, "persistns")
	resp = restartRawReq(t, "POST", endpoint, "/persistqueue/messages", sbHost, "",
		[]byte("survived restart"), map[string]string{
			"Content-Type":  "text/plain",
			"Authorization": sbAuth,
		})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// ---- (c) Cosmos: a document reachable by QUERY ----
	cosmosClient := newCosmosSDKClient(t, endpoint+"/")
	_, err = cosmosClient.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "persistdb"}, nil)
	require.NoError(t, err)
	persistDB, err := cosmosClient.NewDatabase("persistdb")
	require.NoError(t, err)
	_, err = persistDB.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "people",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/team"}},
	}, nil)
	require.NoError(t, err)
	cosmosContainer, err := cosmosClient.NewContainer("persistdb", "people")
	require.NoError(t, err)
	cosmosPK := azcosmos.NewPartitionKeyString("platform")
	_, err = cosmosContainer.CreateItem(ctx, cosmosPK,
		[]byte(`{"id":"survivor","team":"platform","name":"alice"}`), nil)
	require.NoError(t, err)

	// ---- (d) Azure Files: file bytes written through the data plane ----
	const filesHost = "persistacct.file.localhost"
	payload := []byte("bytes that must survive the restart")
	resp = restartRawReq(t, "PUT", endpoint, "/persistshare?restype=share", filesHost, "", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = restartRawReq(t, "PUT", endpoint, "/persistshare/persisted.txt", filesHost, "", nil, map[string]string{
		"x-ms-type":           "file",
		"x-ms-content-length": strconv.Itoa(len(payload)),
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = restartRawReq(t, "PUT", endpoint, "/persistshare/persisted.txt?comp=range", filesHost, "", payload, map[string]string{
		"x-ms-write": "update",
		"x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1),
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	// With SIM_DATA_DIR configured (and no SIM_AZURE_FILES_DATA_DIR override)
	// the share contents live under <SIM_DATA_DIR>/files, beside the SQLite
	// database, so reattaching the data directory reattaches the bytes.
	onDisk, err := os.ReadFile(filepath.Join(stateDir, "files", "persistacct", "persistshare", "persisted.txt"))
	require.NoError(t, err, "share contents must live under SIM_DATA_DIR/files")
	require.Equal(t, payload, onDisk)

	// ---- (e) EventGrid: rotate a topic key before the restart ----
	rgName := "persist-rg"
	resp = restartRawReq(t, "PUT", endpoint,
		"/subscriptions/"+subscriptionID+"/resourceGroups/"+rgName+"?api-version=2021-04-01",
		"", preRestartBearer, []byte(`{"location":"eastus"}`),
		map[string]string{"Content-Type": "application/json"})
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)
	resp.Body.Close()

	topicsClient, err := armeventgrid.NewTopicsClient(subscriptionID, &restartCredential{endpoint: endpoint}, restartClientOpts(endpoint))
	require.NoError(t, err)
	topicPoller, err := topicsClient.BeginCreateOrUpdate(ctx, rgName, "persist-topic",
		armeventgrid.Topic{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = topicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	originalKeys, err := topicsClient.ListSharedAccessKeys(ctx, rgName, "persist-topic", nil)
	require.NoError(t, err)
	regenPoller, err := topicsClient.BeginRegenerateKey(ctx, rgName, "persist-topic",
		armeventgrid.TopicRegenerateKeyRequest{KeyName: to.Ptr("key1")}, nil)
	require.NoError(t, err)
	rotated, err := regenPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, rotated.Key1)
	require.NotEqual(t, *originalKeys.Key1, *rotated.Key1, "regenerateKey must rotate key1")

	// ---- restart: SIGTERM, relaunch against the same data directory ----
	require.NoError(t, shutdownAzureSimulator(cmd),
		"simulator must exit cleanly before its SQLite database is reopened")
	cmd = startPersistentAzureSimulator(t, stateDir, port)

	// ---- (f) the pre-restart bearer is still authorized ----
	resp = restartRawReq(t, "GET", endpoint,
		"/subscriptions/"+subscriptionID+"/resourceGroups?api-version=2021-04-01",
		"", preRestartBearer, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"a bearer minted before the restart must still verify against the persisted signing key")
	resp.Body.Close()

	// ---- (a) directory state readable; pre-restart app secret still grants ----
	resp = restartRawReq(t, "GET", endpoint, "/v1.0/users/"+createdUser.ID, "", "", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "Graph user must survive the restart")
	var persistedUser struct {
		UserPrincipalName string `json:"userPrincipalName"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&persistedUser))
	resp.Body.Close()
	assert.Equal(t, "persistent.user@sockerless.example", persistedUser.UserPrincipalName)

	resp = restartRawReq(t, "GET", endpoint, "/v1.0/applications/"+createdApp.ID, "", "", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "app registration must survive the restart")
	var persistedApp struct {
		AppID string `json:"appId"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&persistedApp))
	resp.Body.Close()
	assert.Equal(t, createdApp.AppID, persistedApp.AppID)

	grantToken, err := fetchAccessTokenFrom(endpoint, createdApp.AppID, minted.SecretText, "https://management.azure.com/.default")
	require.NoError(t, err, "the client_credentials grant with the pre-restart app secret must still succeed")
	require.NotEmpty(t, grantToken)

	// ---- (b) the enqueued message is received with its sequence number, and
	// a post-restart message gets a strictly higher one. The Shared Access
	// Signature minted BEFORE the restart is reused throughout: the
	// authorization rule and its key material are durable, so a token issued
	// against the pre-restart simulator must still authenticate. ----
	resp = restartRawReq(t, "DELETE", endpoint, "/persistqueue/messages/head", sbHost, "", nil,
		map[string]string{"Authorization": sbAuth})
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"pre-restart Service Bus message must be receivable, and the pre-restart SAS must still verify against the persisted authorization rule key")
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "survived restart", string(got))
	preSeq := sbSequenceNumber(t, resp)
	require.Greater(t, preSeq, int64(0))

	resp = restartRawReq(t, "POST", endpoint, "/persistqueue/messages", sbHost, "",
		[]byte("after restart"), map[string]string{
			"Content-Type":  "text/plain",
			"Authorization": sbAuth,
		})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = restartRawReq(t, "DELETE", endpoint, "/persistqueue/messages/head", sbHost, "", nil,
		map[string]string{"Authorization": sbAuth})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	postSeq := sbSequenceNumber(t, resp)
	assert.Greater(t, postSeq, preSeq,
		"sequence numbers must stay monotonic across the restart — a rewound counter breaks consumer checkpoints")

	// ---- (c) the Cosmos document is found by QUERY, not just point-read ----
	cosmosClient = newCosmosSDKClient(t, endpoint+"/")
	cosmosContainer, err = cosmosClient.NewContainer("persistdb", "people")
	require.NoError(t, err)
	pager := cosmosContainer.NewQueryItemsPager("SELECT * FROM c WHERE c.id = 'survivor'", cosmosPK, nil)
	found := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err, "QUERY must succeed after the restart")
		for _, item := range page.Items {
			var doc struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			require.NoError(t, json.Unmarshal(item, &doc))
			if doc.ID == "survivor" {
				found = true
				assert.Equal(t, "alice", doc.Name)
			}
		}
	}
	require.True(t, found,
		"the persisted document must be reachable by QUERY — cosmosDocsFor filters on the stored Account/DB/Coll")

	// ---- (d) the file's bytes are readable through the data plane ----
	resp = restartRawReq(t, "GET", endpoint, "/persistshare/persisted.txt", filesHost, "", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "Azure Files file must be readable after the restart")
	fileBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, payload, fileBytes)

	// ---- (e) listKeys returns the ROTATED key after the restart ----
	topicsClient, err = armeventgrid.NewTopicsClient(subscriptionID, &restartCredential{endpoint: endpoint}, restartClientOpts(endpoint))
	require.NoError(t, err)
	persistedKeys, err := topicsClient.ListSharedAccessKeys(ctx, rgName, "persist-topic", nil)
	require.NoError(t, err)
	require.NotNil(t, persistedKeys.Key1)
	assert.Equal(t, *rotated.Key1, *persistedKeys.Key1,
		"listKeys must return the rotated key1 after the restart, not silently revert to the pre-rotation key")
	assert.Equal(t, *rotated.Key2, *persistedKeys.Key2)
	assert.NotEqual(t, *originalKeys.Key1, *persistedKeys.Key1)
}
