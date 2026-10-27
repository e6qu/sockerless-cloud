package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
)

// BatchGetItem and TransactGetItems take one stripe acquisition per table, and
// TransactGetItems reads one instant: "TransactGetItems is a synchronous
// operation that atomically retrieves multiple items".

// ddbHookStore calls onGet before each item read; every other method
// delegates.
type ddbHookStore struct {
	sim.Store[map[string]any]
	onGet func(id string)
}

func (s *ddbHookStore) Get(id string) (map[string]any, bool) {
	if s.onGet != nil {
		s.onGet(id)
	}
	return s.Store.Get(id)
}

func ddbBatchGet(t *testing.T, tables map[string][]map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	requestItems := map[string]any{}
	for table, keys := range tables {
		requestItems[table] = map[string]any{"Keys": keys}
	}
	body, err := json.Marshal(map[string]any{"RequestItems": requestItems})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	request.Header.Set("X-Amz-Target", "DynamoDB_20120810.BatchGetItem")
	recorder := httptest.NewRecorder()
	handleDDBBatchGetItem(recorder, request)
	return recorder
}

func ddbTransactGet(t *testing.T, table string, keys []map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	items := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		items = append(items, map[string]any{"Get": map[string]any{"TableName": table, "Key": key}})
	}
	body, err := json.Marshal(map[string]any{"TransactItems": items})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	request.Header.Set("X-Amz-Target", "DynamoDB_20120810.TransactGetItems")
	recorder := httptest.NewRecorder()
	handleDDBTransactGetItems(recorder, request)
	return recorder
}

func ddbSeededKeys(n int) []map[string]any {
	keys := make([]map[string]any, 0, n)
	for i := range n {
		keys = append(keys, map[string]any{
			"pk": map[string]any{"S": "tenant"},
			"sk": map[string]any{"S": fmt.Sprintf("sk-%05d", i)},
		})
	}
	return keys
}

// The stripe count is the only observable difference; the items returned are
// the same either way.
func TestDDBBatchGetItemTakesOneStripePerTable(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const perTable = 50
	ddbSeedQueryTable(t, "batch-a", perTable, 1)
	ddbSeedQueryTable(t, "batch-b", perTable, 1)

	before := ddbItemLocks.Acquired()
	recorder := ddbBatchGet(t, map[string][]map[string]any{
		"batch-a": ddbSeededKeys(perTable),
		"batch-b": ddbSeededKeys(perTable),
	})
	taken := ddbItemLocks.Acquired() - before

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var out struct {
		Responses map[string][]map[string]any `json:"Responses"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
	require.Len(t, out.Responses["batch-a"], perTable)
	require.Len(t, out.Responses["batch-b"], perTable)
	require.Equal(t, uint64(2), taken,
		"a batch over two tables must take one stripe per table, not one per item")
}

func TestDDBTransactGetItemsTakesOneStripe(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const table = "transact-cost"
	const items = 50
	ddbSeedQueryTable(t, table, items, 1)

	before := ddbItemLocks.Acquired()
	recorder := ddbTransactGet(t, table, ddbSeededKeys(items))
	taken := ddbItemLocks.Acquired() - before

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, uint64(1), taken,
		"a transactional read over one table must take that table's stripe once, not once per item")
}

// The writer starts inside the read's first item read and takes the table's
// stripe, so a read releasing between items would see the write in its second
// item. The read must record one acquisition across both items besides the
// writer's own.
func TestDDBTransactGetItemsReadsOneInstant(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const table = "transact-read"
	definition := ddbSeedQueryTable(t, table, 2, 1)
	keys := ddbSeededKeys(2)

	stored := ddbItems
	hooked := &ddbHookStore{Store: stored}
	ddbItems = hooked
	t.Cleanup(func() { ddbItems = stored })

	var writerDone atomic.Bool
	written := make(chan struct{})
	// Register after the database's cleanup so this runs first and the writer
	// finishes against a live handle.
	t.Cleanup(func() { <-written })
	// Record the violation instead of asserting in the hook: a failed assertion
	// unwinds through Goexit, the read releases its stripe without a defer, and
	// the writer and this cleanup would block forever.
	var interleaved atomic.Bool
	reads := 0
	hooked.onGet = func(string) {
		reads++
		if reads > 1 {
			if writerDone.Load() {
				interleaved.Store(true)
			}
			return
		}
		starting := make(chan struct{})
		go func() {
			defer close(written)
			close(starting)
			defer ddbItemLocks.Lock(true, table)()
			for _, key := range keys {
				item := map[string]any{
					"pk":         key["pk"],
					"sk":         key["sk"],
					"generation": map[string]any{"N": "2"},
				}
				stored.Put(ddbItemKey(definition, item), item)
			}
			writerDone.Store(true)
		}()
		<-starting
	}

	before := ddbItemLocks.Acquired()
	recorder := ddbTransactGet(t, table, keys)
	hooked.onGet = nil

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 2, reads, "both items must be read")
	require.False(t, interleaved.Load(),
		"a write committed between two items of a transactional read")

	var out struct {
		Responses []struct {
			Item map[string]any `json:"Item"`
		} `json:"Responses"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
	require.Len(t, out.Responses, 2)
	for _, response := range out.Responses {
		require.NotContains(t, response.Item, "generation",
			"the read returned an item the concurrent write produced")
	}

	<-written
	require.True(t, writerDone.Load())
	require.Equal(t, uint64(2), ddbItemLocks.Acquired()-before,
		"the read must take its stripe once across both items, and the writer its own once")
	updated, ok := stored.Get(ddbItemKey(definition, map[string]any{
		"pk": keys[0]["pk"], "sk": keys[0]["sk"],
	}))
	require.True(t, ok)
	require.Contains(t, updated, "generation",
		"the writer must proceed once the transactional read has finished")
}
