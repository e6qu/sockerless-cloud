package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Azure Cosmos DB account name is a hostname, so it is global.
//
// The control plane advertises `<name>.documents.azure.com` as an account's
// documentEndpoint, and a client reaches an account by resolving that name;
// there is no other way to address one. That is why the service publishes an
// operation whose only purpose is to report whether a name is taken —
// `DatabaseAccounts_CheckNameExists` — and why two accounts cannot share one.
//
// The simulator used to allow it: the same name in two resource groups, which
// made a data-plane request ambiguous. It resolved the ambiguity by picking the
// lowest-identified account, and by honouring a `x-ms-cosmos-account` header
// that Azure does not have. Both are gone. The name is refused at creation, and
// a data-plane request names its account the way every real client does — in
// the host it dialled.
func TestCosmos_AccountNameIsGlobal(t *testing.T) {
	const account = "globalnamecosmos"
	const subscription = "00000000-0000-0000-0000-000000000000"

	// The account exists in one resource group, and CheckNameExists says so.
	cosmosAccountKeys(t, account)
	status, body := cosmosARM(t, baseURL, http.MethodHead,
		"/providers/Microsoft.DocumentDB/databaseAccountNames/"+account+"?api-version=2024-08-15", "")
	require.Equal(t, http.StatusOK, status, "CheckNameExists on a name in use: %s", body)

	// A second account claiming it in another resource group is refused. A
	// simulator that served this would contradict the operation above.
	status, body = cosmosARM(t, baseURL, http.MethodPut,
		cosmosAccountPathIn(subscription, "other-cosmos-rg", account),
		`{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`)
	require.Equal(t, http.StatusConflict, status, "second account under a name in use: %s", body)
	assert.Contains(t, string(body), account)

	// The original account is untouched, and a PUT to its own identifier is
	// still the update the operation is defined to be.
	status, body = cosmosARM(t, baseURL, http.MethodPut, cosmosAccountPath(account),
		`{"location":"eastus","kind":"GlobalDocumentDB","tags":{"owner":"platform"},"properties":{"databaseAccountOfferType":"Standard"}}`)
	require.Equal(t, http.StatusOK, status, "update the account that holds the name: %s", body)
	var updated struct {
		Tags map[string]string `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(body, &updated))
	assert.Equal(t, "platform", updated.Tags["owner"])
}

// TestCosmos_DataPlaneNeedsTheAccountHost is the other half: the host is what
// names the account, so a request that carries no account host reaches no
// account. The simulator used to fall back to the lexicographically-first
// account, which made a misaddressed request succeed against somebody else's
// data.
func TestCosmos_DataPlaneNeedsTheAccountHost(t *testing.T) {
	const account = "hostroutedcosmos"
	key := cosmosAccountKey(t, account)

	// Addressed at the account's own advertised endpoint host, the request is
	// served.
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

	// The identical request without that host reaches no account, so no
	// account's keys could have signed it and the data plane refuses it. The
	// retired `x-ms-cosmos-account` header does not bring it back.
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
