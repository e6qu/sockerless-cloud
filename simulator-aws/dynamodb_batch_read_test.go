package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// A read that names many items takes the stripes covering them once.
//
// Every read path used to take the item lock per item: acquire, read one item,
// deep copy it, release. Query and Scan were converted to one acquisition when
// a profile showed concurrent queries interleaving on that lock item by item
// rather than queueing request by request. BatchGetItem and TransactGetItems
// were left behind, and they are the two operations whose whole purpose is to
// name a hundred items at once — a batch of a hundred keys contended with
// every other reader of that table a hundred times.
//
// For TransactGetItems it is not only cost. DynamoDB documents the operation as
// an atomic read: "TransactGetItems is a synchronous operation that atomically
// retrieves multiple items". Reading one stripe acquisition at a time gave each
// item its own instant of the store, so a transactional write committing
// between two of them was visible in the result — exactly the anomaly the
// operation exists to exclude.

// ddbHookStore calls onGet before each item read, so a test can observe what
// else can happen while a read is in flight. Every other method delegates
// untouched.
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

// ddbSeededKeys returns the primary keys of the first n items ddbSeedQueryTable
// stored in a single-partition table.
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

// TestDDBBatchGetItemTakesOneStripePerTable holds the cost of a batch to the
// number of tables it names rather than the number of items. The stripe count
// is the only observable difference: the items returned are the same either
// way, which is what let a hundredfold difference in lock traffic go unnoticed.
func TestDDBBatchGetItemTakesOneStripePerTable(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const perTable = 50
	ddbSeedQueryTable(t, "batch-a", perTable, 1)
	ddbSeedQueryTable(t, "batch-b", perTable, 1)

	before := ddbStripeAcquisitions.Load()
	recorder := ddbBatchGet(t, map[string][]map[string]any{
		"batch-a": ddbSeededKeys(perTable),
		"batch-b": ddbSeededKeys(perTable),
	})
	taken := ddbStripeAcquisitions.Load() - before

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

// TestDDBTransactGetItemsTakesOneStripe holds the cost of a transactional read
// to the tables it names, the same property TestDDBBatchGetItemTakesOneStripePerTable
// holds for a batch.
func TestDDBTransactGetItemsTakesOneStripe(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const table = "transact-cost"
	const items = 50
	ddbSeedQueryTable(t, table, items, 1)

	before := ddbStripeAcquisitions.Load()
	recorder := ddbTransactGet(t, table, ddbSeededKeys(items))
	taken := ddbStripeAcquisitions.Load() - before

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, uint64(1), taken,
		"a transactional read over one table must take that table's stripe once, not once per item")
}

// TestDDBTransactGetItemsReadsOneInstant is the atomicity DynamoDB documents: a
// write committed while a transactional read is in flight is either wholly
// before it or wholly after it, never between two of its items.
//
// It is asserted by construction rather than by repetition. The writer is
// started from inside the read's first item read and cannot proceed while the
// read holds the table's stripe; Go's RWMutex blocks new readers once a writer
// is waiting, so a read that released its stripe between items would hand the
// writer the table and see the new value in its second item. Every later item
// read therefore asserts the writer has not run.
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
	// The writer writes to a store this test's temporary database backs, and
	// that database is closed by a cleanup registered before this one, so this
	// runs first and lets the writer finish against a live handle.
	t.Cleanup(func() { <-written })
	// The violation is recorded rather than asserted where it is seen: a failed
	// assertion inside the hook unwinds the read through Goexit, and the read
	// releases its stripe on the way out rather than through a defer, so the
	// writer would block forever and the cleanup above with it.
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
			defer ddbLockTables(true, table)()
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
		// The writer has been scheduled; this gives it time to reach the
		// acquisition and block there, so that a read releasing its stripe
		// between items would definitely lose the table to it. Under a read
		// that holds one acquisition this is time spent inside the critical
		// section and nothing else happens in it.
		time.Sleep(50 * time.Millisecond)
	}

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
	updated, ok := stored.Get(ddbItemKey(definition, map[string]any{
		"pk": keys[0]["pk"], "sk": keys[0]["sk"],
	}))
	require.True(t, ok)
	require.Contains(t, updated, "generation",
		"the writer must proceed once the transactional read has finished")
}
