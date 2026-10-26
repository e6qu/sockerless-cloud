package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Azure Cosmos DB account name is a hostname, so it is global: the control
// plane advertises `<name>.documents.azure.com` as documentEndpoint, and a
// client reaches an account only by resolving that name. Hence
// DatabaseAccounts_CheckNameExists, and a name taken in one resource group is
// refused in another.
func TestCosmos_AccountNameIsGlobal(t *testing.T) {
	t.Parallel()
	const account = "globalnamecosmos"
	subscription := subscriptionID

	cosmosAccountKeys(t, account)
	status, body := cosmosARM(t, baseURL, http.MethodHead,
		"/providers/Microsoft.DocumentDB/databaseAccountNames/"+account+"?api-version=2024-08-15", "")
	require.Equal(t, http.StatusOK, status, "CheckNameExists on a name in use: %s", body)

	// A second account claiming the name in another resource group is refused.
	status, body = cosmosARM(t, baseURL, http.MethodPut,
		"/subscriptions/"+subscription+"/resourceGroups/other-cosmos-rg", `{"location":"eastus"}`)
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, status, "create resource group: %s", body)
	status, body = cosmosARM(t, baseURL, http.MethodPut,
		cosmosAccountPathIn(subscription, "other-cosmos-rg", account),
		`{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`)
	require.Equal(t, http.StatusConflict, status, "second account under a name in use: %s", body)
	assert.Contains(t, string(body), account)

	// The original account is untouched, and a PUT to its own identifier is still
	// an update.
	status, body = cosmosARM(t, baseURL, http.MethodPut, cosmosAccountPath(account),
		`{"location":"eastus","kind":"GlobalDocumentDB","tags":{"owner":"platform"},"properties":{"databaseAccountOfferType":"Standard"}}`)
	require.Equal(t, http.StatusOK, status, "update the account that holds the name: %s", body)
	var updated struct {
		Tags map[string]string `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(body, &updated))
	assert.Equal(t, "platform", updated.Tags["owner"])
}

// TestCosmos_DataPlaneNeedsTheAccountHost: the host names the account, so a
// request carrying no account host reaches no account.
func TestCosmos_DataPlaneNeedsTheAccountHost(t *testing.T) {
	t.Parallel()
	const account = "hostroutedcosmos"
	key := cosmosAccountKey(t, account)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/dbs",
		strings.NewReader(`{"id":"hostdb"}`))
	require.NoError(t, err)
	request.Host = cosmosDataPlaneHost(t, account)
	request.Header.Set("Content-Type", "application/json")
	cosmosSignDataPlane(t, request, key)
	resp, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Without that host, no account's keys could have signed the request, so the
	// data plane refuses it, `x-ms-cosmos-account` header or not.
	request, err = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/dbs",
		strings.NewReader(`{"id":"hostdb2"}`))
	require.NoError(t, err)
	request.Header.Set("x-ms-cosmos-account", account)
	request.Header.Set("Content-Type", "application/json")
	cosmosSignDataPlane(t, request, key)
	resp, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a request naming no account in its host must not be served")
}
