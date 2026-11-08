package azure_sdk_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cosmosQueryContainer creates database qdb and a container partitioned on
// /pk through azcosmos, and returns the container.
func cosmosQueryContainer(t *testing.T, account, name string) *azcosmos.ContainerClient {
	t.Helper()
	client := cosmosSimClient(t, account)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "qdb"}, nil)
	require.NoError(t, err)
	db, err := client.NewDatabase("qdb")
	require.NoError(t, err)
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     name,
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
	}, nil)
	require.NoError(t, err)
	container, err := client.NewContainer("qdb", name)
	require.NoError(t, err)
	return container
}

var cosmosQueryPK = azcosmos.NewPartitionKeyString("p")

func cosmosQueryItems(t *testing.T, container *azcosmos.ContainerClient, query string, params []azcosmos.QueryParameter) []map[string]any {
	t.Helper()
	pager := container.NewQueryItemsPager(query, cosmosQueryPK, &azcosmos.QueryOptions{QueryParameters: params})
	var out []map[string]any
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err, "query %q", query)
		for _, raw := range page.Items {
			var item map[string]any
			require.NoError(t, json.Unmarshal(raw, &item))
			out = append(out, item)
		}
	}
	return out
}

// TestCosmosQuery_RealGrammar drives the SQL engine: a parameterized WHERE with
// AND and a numeric range, ORDER BY, CONTAINS, and COUNT.
func TestCosmosQuery_RealGrammar(t *testing.T) {
	t.Parallel()
	container := cosmosQueryContainer(t, uniquename.Alnum("sdkcosmosquery"), "people")
	for _, doc := range []string{
		`{"id":"a","pk":"p","team":"platform","score":5,"name":"alice"}`,
		`{"id":"b","pk":"p","team":"platform","score":10,"name":"bob"}`,
		`{"id":"c","pk":"p","team":"infra","score":7,"name":"carol"}`,
	} {
		_, err := container.CreateItem(ctx, cosmosQueryPK, []byte(doc), nil)
		require.NoError(t, err)
	}

	got := cosmosQueryItems(t, container, "SELECT * FROM c WHERE c.team = @p AND c.score > @q",
		[]azcosmos.QueryParameter{{Name: "@p", Value: "platform"}, {Name: "@q", Value: 5}})
	require.Len(t, got, 1, "want only bob (platform, score>5)")
	assert.Equal(t, "b", got[0]["id"])

	got = cosmosQueryItems(t, container, "SELECT c.id FROM c ORDER BY c.score DESC", nil)
	require.Len(t, got, 3)
	assert.Equal(t, "b", got[0]["id"], "highest score first")
	assert.Equal(t, "a", got[2]["id"], "lowest score last")

	got = cosmosQueryItems(t, container, "SELECT * FROM c WHERE CONTAINS(c.name, 'ar')", nil)
	require.Len(t, got, 1)
	assert.Equal(t, "c", got[0]["id"], "only carol contains 'ar'")

	pager := container.NewQueryItemsPager("SELECT VALUE COUNT(1) FROM c WHERE c.team = 'platform'", cosmosQueryPK, nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "2", string(page.Items[0]))
}

// TestCosmosUpsertAndConflict: re-creating an existing id is 409 Conflict; an
// upsert replaces it with 200.
func TestCosmosUpsertAndConflict(t *testing.T) {
	t.Parallel()
	container := cosmosQueryContainer(t, uniquename.Alnum("sdkcosmosupsert"), "items")

	_, err := container.CreateItem(ctx, cosmosQueryPK, []byte(`{"id":"dup","pk":"p","v":1}`), nil)
	require.NoError(t, err)

	_, err = container.CreateItem(ctx, cosmosQueryPK, []byte(`{"id":"dup","pk":"p","v":2}`), nil)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "duplicate-id create must fail: %v", err)
	assert.Equal(t, http.StatusConflict, respErr.StatusCode)

	upserted, err := container.UpsertItem(ctx, cosmosQueryPK, []byte(`{"id":"dup","pk":"p","v":3}`), nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, upserted.RawResponse.StatusCode, "upsert of an existing id replaces it")

	read, err := container.ReadItem(ctx, cosmosQueryPK, "dup", nil)
	require.NoError(t, err)
	var stored map[string]any
	require.NoError(t, json.Unmarshal(read.Value, &stored))
	assert.EqualValues(t, 3, stored["v"])
}

// TestCosmosPatchIncrement applies an increment and a set through azcosmos's
// patch operations.
func TestCosmosPatchIncrement(t *testing.T) {
	t.Parallel()
	container := cosmosQueryContainer(t, uniquename.Alnum("sdkcosmospatch"), "counters")
	_, err := container.CreateItem(ctx, cosmosQueryPK, []byte(`{"id":"ctr","pk":"p","count":10,"label":"keep"}`), nil)
	require.NoError(t, err)

	patch := azcosmos.PatchOperations{}
	patch.AppendIncrement("/count", 5)
	patch.AppendSet("/status", "active")
	_, err = container.PatchItem(ctx, cosmosQueryPK, "ctr", patch, &azcosmos.ItemOptions{EnableContentResponseOnWrite: true})
	require.NoError(t, err)

	read, err := container.ReadItem(ctx, cosmosQueryPK, "ctr", nil)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(read.Value, &got))
	assert.EqualValues(t, 15, got["count"], "incr +5 from 10")
	assert.Equal(t, "active", got["status"], "set op added the field")
	assert.Equal(t, "keep", got["label"], "untouched property preserved")
}

// A document operation on a container that does not exist is 404, as reading
// the container is.
func TestCosmosDocumentsNeedTheirContainer(t *testing.T) {
	t.Parallel()
	client := cosmosSimClient(t, uniquename.Alnum("sdkcosmosowner"))
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "qdb"}, nil)
	require.NoError(t, err)
	missing, err := client.NewContainer("qdb", "never-created")
	require.NoError(t, err)
	_, err = missing.CreateItem(ctx, cosmosQueryPK, []byte(`{"id":"x","pk":"p"}`), nil)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "a write into a missing container must fail: %v", err)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)
}
