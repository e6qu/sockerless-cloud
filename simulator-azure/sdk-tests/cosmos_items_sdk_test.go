package azure_sdk_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCosmos_TimeToLiveExpiresItems proves a container's defaultTtl expires
// items through the azcosmos SDK: an expired item reads as 404 and leaves
// queries, while an item whose own ttl is -1 never expires.
func TestCosmos_TimeToLiveExpiresItems(t *testing.T) {
	client := cosmosSimClient(t, cosmosDataPlaneAccount)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "ttldb"}, nil)
	require.NoError(t, err)
	db, err := client.NewDatabase("ttldb")
	require.NoError(t, err)
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "ttlc",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
		DefaultTimeToLive:      to.Ptr[int32](1),
	}, nil)
	require.NoError(t, err)
	container, err := client.NewContainer("ttldb", "ttlc")
	require.NoError(t, err)
	read, err := container.Read(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, read.ContainerProperties.DefaultTimeToLive, "the container reports its defaultTtl")
	assert.EqualValues(t, 1, *read.ContainerProperties.DefaultTimeToLive)

	pk := azcosmos.NewPartitionKeyString("p")
	for _, item := range []string{`{"id":"short","pk":"p"}`, `{"id":"kept","pk":"p","ttl":-1}`} {
		_, err = container.CreateItem(ctx, pk, []byte(item), nil)
		require.NoError(t, err)
	}
	time.Sleep(2500 * time.Millisecond)

	_, err = container.ReadItem(ctx, pk, "short", nil)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "an expired item no longer reads: %v", err)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode)
	_, err = container.ReadItem(ctx, pk, "kept", nil)
	require.NoError(t, err, "an item with ttl -1 never expires")

	pager := container.NewQueryItemsPager("SELECT * FROM c", pk, nil)
	var ids []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, raw := range page.Items {
			var doc map[string]any
			require.NoError(t, json.Unmarshal(raw, &doc))
			ids = append(ids, fmt.Sprint(doc["id"]))
		}
	}
	assert.Equal(t, []string{"kept"}, ids)
}

// TestCosmos_ProvisionedThroughputThrottles proves a container's provisioned
// RU/s is a budget: writes beyond it are answered 429 with
// x-ms-retry-after-ms, and the SDK's retry of exactly that carries a burst
// larger than the budget through.
func TestCosmos_ProvisionedThroughputThrottles(t *testing.T) {
	client := cosmosSimClient(t, cosmosDataPlaneAccount)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "throttledb"}, nil)
	require.NoError(t, err)
	db, err := client.NewDatabase("throttledb")
	require.NoError(t, err)
	throughput := azcosmos.NewManualThroughputProperties(400)
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "throttlec",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
	}, &azcosmos.CreateContainerOptions{ThroughputProperties: &throughput})
	require.NoError(t, err)
	container, err := client.NewContainer("throttledb", "throttlec")
	require.NoError(t, err)

	pk := azcosmos.NewPartitionKeyString("p")
	started := time.Now()
	for i := range 120 {
		_, err := container.CreateItem(ctx, pk, []byte(fmt.Sprintf(`{"id":"sdk%d","pk":"p"}`, i)), nil)
		require.NoError(t, err, "the SDK retries a throttled write %d", i)
	}
	assert.GreaterOrEqual(t, time.Since(started), 300*time.Millisecond,
		"600 RU of writes against 400 RU/s wait for the budget to refill")

	var throttled *http.Response
	for i := range 200 {
		resp := cosmosScript(t, "POST", cosmosDataPlaneAccount, "/dbs/throttledb/colls/throttlec/docs",
			fmt.Sprintf(`{"id":"raw%d","pk":"p"}`, i), map[string]string{"x-ms-documentdb-partitionkey": `["p"]`})
		if resp.StatusCode == http.StatusTooManyRequests {
			throttled = resp
			break
		}
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}
	require.NotNil(t, throttled, "writes beyond the budget are throttled")
	defer throttled.Body.Close()
	wait, err := strconv.Atoi(throttled.Header.Get("x-ms-retry-after-ms"))
	require.NoError(t, err)
	assert.Positive(t, wait)
	assert.Equal(t, "3200", throttled.Header.Get("x-ms-substatus"))
}

// TestCosmos_AllVersionsAndDeletesChangeFeed proves the second change-feed
// mode: on an account with continuous backup, `A-IM: Full-Fidelity Feed`
// returns every create, replace and delete since the continuation, where the
// latest-version feed shows only surviving items once.
func TestCosmos_AllVersionsAndDeletesChangeFeed(t *testing.T) {
	const account = "sdkcosmosavad"
	cosmosAccountKey(t, account)
	status, body := cosmosARM(t, baseURL, http.MethodPut, cosmosAccountPath(account),
		`{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard","backupPolicy":{"type":"Continuous"}}}`)
	require.Equal(t, http.StatusOK, status, "enable continuous backup: %s", body)

	coll := "/dbs/avaddb/colls/c"
	pkHeader := map[string]string{"x-ms-documentdb-partitionkey": `["p"]`}
	resp := cosmosScript(t, "POST", account, "/dbs", `{"id":"avaddb"}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = cosmosScript(t, "POST", account, "/dbs/avaddb/colls", `{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = cosmosScript(t, "GET", account, coll+"/docs", "", map[string]string{"A-IM": "Full-Fidelity Feed", "If-None-Match": "*"})
	require.Equal(t, http.StatusNotModified, resp.StatusCode, "a feed started now has nothing yet")
	start := resp.Header.Get("etag")
	resp.Body.Close()

	for _, step := range []struct{ method, path, body string }{
		{"POST", coll + "/docs", `{"id":"a","pk":"p","v":1}`},
		{"PUT", coll + "/docs/a", `{"id":"a","pk":"p","v":2}`},
		{"DELETE", coll + "/docs/a", ""},
		{"POST", coll + "/docs", `{"id":"b","pk":"p"}`},
	} {
		resp := cosmosScript(t, step.method, account, step.path, step.body, pkHeader)
		require.Less(t, resp.StatusCode, 300, "%s %s", step.method, step.path)
		resp.Body.Close()
	}

	resp = cosmosScript(t, "GET", account, coll+"/docs", "", map[string]string{"A-IM": "Full-Fidelity Feed", "If-None-Match": start})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	var feed struct {
		Documents []struct {
			Current  map[string]any `json:"current"`
			Metadata map[string]any `json:"metadata"`
		} `json:"Documents"`
	}
	require.NoError(t, json.Unmarshal(raw, &feed))
	var ops []string
	for _, entry := range feed.Documents {
		ops = append(ops, fmt.Sprint(entry.Metadata["operationType"]))
	}
	assert.Equal(t, []string{"create", "replace", "delete", "create"}, ops)
	require.Len(t, feed.Documents, 4)
	assert.Nil(t, feed.Documents[2].Current, "a delete carries no current item")
	assert.Equal(t, "a", feed.Documents[2].Metadata["id"])

	docs, _, _ := cosmosChangeFeedPageIn(t, account, coll, "")
	require.Len(t, docs, 1, "the latest-version feed omits the deleted item")
	assert.Equal(t, "b", docs[0]["id"])
}

// cosmosChangeFeedPageIn is a latest-version change-feed read of any container.
func cosmosChangeFeedPageIn(t *testing.T, account, coll, continuation string) ([]map[string]any, string, int) {
	t.Helper()
	headers := map[string]string{"A-IM": "Incremental feed"}
	if continuation != "" {
		headers["If-None-Match"] = continuation
	}
	resp := cosmosScript(t, "GET", account, coll+"/docs", "", headers)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.Header.Get("etag"), resp.StatusCode
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out struct {
		Documents []map[string]any `json:"Documents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Documents, resp.Header.Get("etag"), resp.StatusCode
}

// TestCosmos_ItemVerbsHonourETagsAndExistence drives every item verb through
// the azcosmos SDK — POST /dbs/{database}/colls/{container}/docs (create and
// query), GET /dbs/{database}/colls/{container}/docs (read feed),
// GET, PUT, PATCH and DELETE /dbs/{database}/colls/{container}/docs/{doc} —
// and holds replace to its ETag and to the item existing.
func TestCosmos_ItemVerbsHonourETagsAndExistence(t *testing.T) {
	client := cosmosSimClient(t, cosmosDataPlaneAccount)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "verbdb"}, nil)
	require.NoError(t, err)
	db, err := client.NewDatabase("verbdb")
	require.NoError(t, err)
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "verbc",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
	}, nil)
	require.NoError(t, err)
	container, err := client.NewContainer("verbdb", "verbc")
	require.NoError(t, err)
	pk := azcosmos.NewPartitionKeyString("p")

	created, err := container.CreateItem(ctx, pk, []byte(`{"id":"a","pk":"p","n":1}`), nil)
	require.NoError(t, err)
	stale := created.ETag

	replaced, err := container.ReplaceItem(ctx, pk, "a", []byte(`{"id":"a","pk":"p","n":2}`),
		&azcosmos.ItemOptions{IfMatchEtag: &stale})
	require.NoError(t, err, "a replace conditioned on the current ETag succeeds")
	require.NotEqual(t, stale, replaced.ETag, "a replace moves the ETag")

	_, err = container.ReplaceItem(ctx, pk, "a", []byte(`{"id":"a","pk":"p","n":3}`),
		&azcosmos.ItemOptions{IfMatchEtag: &stale})
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "a replace on a stale ETag fails: %v", err)
	assert.Equal(t, http.StatusPreconditionFailed, respErr.StatusCode)

	patch := azcosmos.PatchOperations{}
	patch.AppendIncrement("/n", 5)
	_, err = container.PatchItem(ctx, pk, "a", patch, nil)
	require.NoError(t, err)
	read, err := container.ReadItem(ctx, pk, "a", nil)
	require.NoError(t, err)
	var doc struct{ N int }
	require.NoError(t, json.Unmarshal(read.Value, &doc))
	assert.Equal(t, 7, doc.N, "the patch applied to the replaced item")

	pager := container.NewQueryItemsPager("SELECT * FROM c", pk, nil)
	var seen int
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		seen += len(page.Items)
	}
	assert.Equal(t, 1, seen)

	_, err = container.DeleteItem(ctx, pk, "a", nil)
	require.NoError(t, err)
	_, err = container.ReplaceItem(ctx, pk, "a", []byte(`{"id":"a","pk":"p"}`), nil)
	require.True(t, errors.As(err, &respErr), "a replace of a missing item fails: %v", err)
	assert.Equal(t, http.StatusNotFound, respErr.StatusCode, "replace never creates the item")
}
