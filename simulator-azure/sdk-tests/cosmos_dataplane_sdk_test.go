package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cosmosEmulatorKey is the published Azure Cosmos DB emulator master key, so
// the differential tests drive the simulator and the emulator with the same
// client code.
const cosmosEmulatorKey = "C2y6yDjf5/R+ob0N8A7Cgv30VRDJIWEHLM+4QDU5DE2nQ9nDuVTqobD4b8mGGyPMbIZnqyMsEcaGQy67XIw/Jw=="

// cosmosRawSDKClient builds the azcosmos client without a *testing.T, for the
// emulator warm-up that runs before any test.
func cosmosRawSDKClient(endpoint, key string) (*azcosmos.Client, error) {
	cred, err := azcosmos.NewKeyCredential(key)
	if err != nil {
		return nil, err
	}
	return azcosmos.NewClientWithKey(endpoint, cred, &azcosmos.ClientOptions{
		ClientOptions:                cosmosClientOptions(""),
		EnableContentResponseOnWrite: true,
	})
}

// newCosmosSDKClient builds the azcosmos client for the simulator or the
// emulator, differing only in endpoint and key. dial names the address to send
// to when the endpoint's account hostname does not resolve on the test host.
func newCosmosSDKClient(t *testing.T, endpoint, key, dial string) *azcosmos.Client {
	t.Helper()
	cred, err := azcosmos.NewKeyCredential(key)
	require.NoError(t, err)
	c, err := azcosmos.NewClientWithKey(endpoint, cred, &azcosmos.ClientOptions{
		ClientOptions:                cosmosClientOptions(dial),
		EnableContentResponseOnWrite: true,
	})
	require.NoError(t, err)
	return c
}

// TestCosmos_RealSDKDataPlane drives the document lifecycle through the
// official azcosmos SDK: account discovery (CreateDatabase first reads the
// account root, "GET /{$}"), create, read, replace, upsert, patch, delete,
// and a parameterized query.
func TestCosmos_RealSDKDataPlane(t *testing.T) {
	t.Parallel()
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	client := cosmosSimClient(t, dataPlaneAccount)

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

	// A created database and container read back; names nobody created do not,
	// matching the listing.
	_, err = db.Read(ctx, nil)
	require.NoError(t, err, "a database that was created must read back")
	_, err = container.Read(ctx, nil)
	require.NoError(t, err, "a container that was created must read back")

	absentDB, err := client.NewDatabase("nosuchdb")
	require.NoError(t, err)
	_, err = absentDB.Read(ctx, nil)
	require.Error(t, err, "a database nobody created must not read back")

	absentContainer, err := client.NewContainer("sdkdb", "nosuchcontainer")
	require.NoError(t, err)
	_, err = absentContainer.Read(ctx, nil)
	require.Error(t, err, "a container nobody created must not read back")

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

	got, err := container.ReadItem(ctx, pk, "a", nil)
	require.NoError(t, err, "ReadItem")
	var a person
	require.NoError(t, json.Unmarshal(got.Value, &a))
	assert.Equal(t, "alice", a.Name)
	assert.NotEmpty(t, got.ETag, "read must surface the ETag header")

	a.Score = 99
	raw, _ := json.Marshal(a)
	_, err = container.ReplaceItem(ctx, pk, "a", raw, nil)
	require.NoError(t, err, "ReplaceItem")

	raw, _ = json.Marshal(person{"c", "platform", 7, "carol"})
	_, err = container.UpsertItem(ctx, pk, raw, nil)
	require.NoError(t, err, "UpsertItem")

	patch := azcosmos.PatchOperations{}
	patch.AppendIncrement("/score", 1)
	_, err = container.PatchItem(ctx, pk, "b", patch, nil)
	require.NoError(t, err, "PatchItem")

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
	// a(99), b(11) and c(7) all score above 5.
	assert.ElementsMatch(t, []string{"a", "b", "c"}, ids)

	_, err = container.DeleteItem(ctx, pk, "c", nil)
	require.NoError(t, err, "DeleteItem")
	_, err = container.ReadItem(ctx, pk, "c", nil)
	require.Error(t, err, "deleted item must not be readable")
}

// TestCosmos_AccountRootServesOnlyCosmosClients: the azcosmos global endpoint
// manager reads the account root on its first request, so the root answers
// account properties to a Cosmos client; a request with none of Cosmos's
// data-plane markers is not Cosmos's to answer, which leaves the bare origin to
// the console.
func TestCosmos_AccountRootServesOnlyCosmosClients(t *testing.T) {
	t.Parallel()
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	// CreateDatabase drives account discovery first.
	client := cosmosSimClient(t, dataPlaneAccount)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "rootdiscoverydb"}, nil)
	require.NoError(t, err, "CreateDatabase must still drive account discovery on GET /{$}")

	// Do not follow redirects: where the console is registered the root redirects
	// to it, and where it is not the root serves nothing; neither is Cosmos's.
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
