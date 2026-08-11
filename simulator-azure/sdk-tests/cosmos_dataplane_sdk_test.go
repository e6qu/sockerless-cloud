package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cosmosEmulatorKey is the well-known Azure Cosmos DB emulator master key — the
// same key used against Microsoft's emulator, so the differential test drives
// the sim and the emulator with identical client code differing only in endpoint.
const cosmosEmulatorKey = "C2y6yDjf5/R+ob0N8A7Cgv30VRDJIWEHLM+4QDU5DE2nQ9nDuVTqobD4b8mGGyPMbIZnqyMsEcaGQy67XIw/Jw=="

// newCosmosSDKClient builds the REAL azcosmos client (NewClientWithKey) pointed
// at a Cosmos endpoint — the sim or the emulator — differing only in the
// endpoint coordinate. Driving the official SDK (not raw HTTP) is the compliance
// proof: the sim must serve the SDK's account-discovery + data-plane contract.
func newCosmosSDKClient(t *testing.T, endpoint string) *azcosmos.Client {
	t.Helper()
	cred, err := azcosmos.NewKeyCredential(cosmosEmulatorKey)
	require.NoError(t, err)
	c, err := azcosmos.NewClientWithKey(endpoint, cred, &azcosmos.ClientOptions{
		ClientOptions:                azcore.ClientOptions{InsecureAllowCredentialWithHTTP: true},
		EnableContentResponseOnWrite: true,
	})
	require.NoError(t, err)
	return c
}

// TestCosmos_RealSDKDataPlane proves the official azcosmos SDK performs the full
// document lifecycle against the sim — account discovery, create/read/replace/
// upsert/patch/delete, and a parameterized query — driven exactly as it would be
// against real Cosmos (differing only in the endpoint). Previously the sim's data
// plane could only be reached via a raw-HTTP `x-ms-cosmos-account` shortcut.
//
// CreateDatabase below drives the account-discovery route "GET /{$}" (azcosmos's
// global-endpoint-manager reads the account root on its first request).
func TestCosmos_RealSDKDataPlane(t *testing.T) {
	client := newCosmosSDKClient(t, baseURL+"/")

	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "sdkdb"}, nil)
	require.NoError(t, err, "CreateDatabase (drives account discovery + POST /dbs)")

	db, err := client.NewDatabase("sdkdb")
	require.NoError(t, err)
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "people",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/team"}},
	}, nil)
	require.NoError(t, err, "CreateContainer")

	container, err := client.NewContainer("sdkdb", "people")
	require.NoError(t, err)

	type person struct {
		ID    string `json:"id"`
		Team  string `json:"team"`
		Score int    `json:"score"`
		Name  string `json:"name"`
	}
	pk := azcosmos.NewPartitionKeyString("platform")
	seed := []person{
		{"a", "platform", 5, "alice"},
		{"b", "platform", 10, "bob"},
	}
	for _, p := range seed {
		raw, _ := json.Marshal(p)
		_, err := container.CreateItem(ctx, pk, raw, nil)
		require.NoError(t, err, "CreateItem %s", p.ID)
	}

	// ReadItem round-trip.
	got, err := container.ReadItem(ctx, pk, "a", nil)
	require.NoError(t, err, "ReadItem")
	var a person
	require.NoError(t, json.Unmarshal(got.Value, &a))
	assert.Equal(t, "alice", a.Name)
	assert.NotEmpty(t, got.ETag, "read must surface the ETag header")

	// ReplaceItem.
	a.Score = 99
	raw, _ := json.Marshal(a)
	_, err = container.ReplaceItem(ctx, pk, "a", raw, nil)
	require.NoError(t, err, "ReplaceItem")

	// UpsertItem (new id).
	raw, _ = json.Marshal(person{"c", "platform", 7, "carol"})
	_, err = container.UpsertItem(ctx, pk, raw, nil)
	require.NoError(t, err, "UpsertItem")

	// PatchItem (increment).
	patch := azcosmos.PatchOperations{}
	patch.AppendIncrement("/score", 1)
	_, err = container.PatchItem(ctx, pk, "b", patch, nil)
	require.NoError(t, err, "PatchItem")

	// Parameterized query.
	pager := container.NewQueryItemsPager(
		"SELECT * FROM c WHERE c.team = @t AND c.score > @s",
		pk,
		&azcosmos.QueryOptions{QueryParameters: []azcosmos.QueryParameter{
			{Name: "@t", Value: "platform"}, {Name: "@s", Value: 5},
		}},
	)
	var ids []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err, "query page")
		for _, item := range page.Items {
			var p person
			require.NoError(t, json.Unmarshal(item, &p))
			ids = append(ids, p.ID)
		}
	}
	// a(99) and b(11) are >5; c(7) is >5 too. All platform with score>5.
	assert.ElementsMatch(t, []string{"a", "b", "c"}, ids)

	// DeleteItem.
	_, err = container.DeleteItem(ctx, pk, "c", nil)
	require.NoError(t, err, "DeleteItem")
	_, err = container.ReadItem(ctx, pk, "c", nil)
	require.Error(t, err, "deleted item must not be readable")
}

// TestCosmos_AccountRootServesOnlyCosmosClients pins both halves of the API root
// Azure Cosmos DB owns. The azcosmos SDK's global endpoint manager reads the
// account root on its first request, so the route must keep answering account
// properties for a real Cosmos client; a request carrying none of Cosmos's
// data-plane markers is not addressed to Cosmos and must not be answered with
// them. That second half is what lets the simulator hand a browser at the bare
// origin to the console instead of a bare 404, without Cosmos losing the root.
func TestCosmos_AccountRootServesOnlyCosmosClients(t *testing.T) {
	// A real Cosmos client: CreateDatabase drives account discovery first.
	client := newCosmosSDKClient(t, baseURL+"/")
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "rootdiscoverydb"}, nil)
	require.NoError(t, err, "CreateDatabase must still drive account discovery on GET /{$}")

	// A plain client with none of Cosmos's markers. Redirects are not followed:
	// where the console is registered the answer is a redirect to it, and where
	// it is not the root is genuinely nothing — neither may be Cosmos's.
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	require.NoError(t, err)
	resp, err := plain.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.NotContains(t, string(body), "writableLocations",
		"a request without Cosmos data-plane markers was answered with Cosmos account properties")
	assert.NotContains(t, string(body), "databaseAccountEndpoint",
		"a request without Cosmos data-plane markers was answered with Cosmos account properties")
}
