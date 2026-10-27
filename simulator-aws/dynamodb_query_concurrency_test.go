package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// A Query reads the item store under one lock and copies only what it returns,
// so its work scales with its result rather than with what it examines, and
// concurrent queries scale with their number rather than their product.

// Back the stores with a real database, as the simulator runs: a memory store
// makes per-item reads nearly free and hides the cost under test.
func ddbQueryConcurrencyStores(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	db, err := sim.OpenDB(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	// Let earlier background work finish before replacing its stores.
	bg.Await()
	ddbItems = sim.MakeStore[map[string]any](db, "ddb_items")
	ddbItemNames = sim.MakeStore[string](db, "ddb_item_names")
	ddbTables = sim.MakeStore[DDBTable](db, "ddb_tables")
}

// Per-item reads are the only observable sign of what a query touches; every
// other method delegates.
type ddbCountingStore struct {
	sim.Store[map[string]any]
	reads atomic.Int64
}

func (s *ddbCountingStore) Get(id string) (map[string]any, bool) {
	s.reads.Add(1)
	return s.Store.Get(id)
}

// Records every listed prefix, so a test sees which key ranges a request
// enumerated.
type ddbListingNames struct {
	sim.Store[string]
	mu       sync.Mutex
	prefixes []string
}

func (s *ddbListingNames) ListPrefix(prefix string) []sim.Keyed[string] {
	s.mu.Lock()
	s.prefixes = append(s.prefixes, prefix)
	s.mu.Unlock()
	return s.Store.ListPrefix(prefix)
}

// A query's key condition matches one item; the rest are candidates it must
// examine and reject.
func ddbSeedQueryTable(t *testing.T, table string, items int, partitions int) DDBTable {
	t.Helper()
	definition := DDBTable{
		TableName:   table,
		TableStatus: "ACTIVE",
		KeySchema: []DDBKeySchemaEntry{
			{AttributeName: "pk", KeyType: "HASH"},
			{AttributeName: "sk", KeyType: "RANGE"},
		},
		AttributeDefinitions: []DDBAttributeDef{
			{AttributeName: "pk", AttributeType: "S"},
			{AttributeName: "sk", AttributeType: "S"},
		},
	}
	ddbTables.Put(table, definition)
	for i := range items {
		sk := fmt.Sprintf("sk-%05d", i)
		item := map[string]any{
			"pk": map[string]any{"S": ddbSeedPartition(i, partitions)},
			"sk": map[string]any{"S": sk},
			// Nesting makes a deep copy cost something.
			"payload": map[string]any{"M": map[string]any{
				"body":  map[string]any{"S": fmt.Sprintf("body-%05d", i)},
				"tags":  map[string]any{"L": []any{map[string]any{"S": "a"}, map[string]any{"S": "b"}}},
				"count": map[string]any{"N": fmt.Sprintf("%d", i)},
			}},
		}
		// Use the simulator's own key function so the table is one it could write.
		key := ddbItemKey(definition, item)
		ddbItems.Put(key, item)
		ddbItemNames.Put(key, key)
	}
	return definition
}

// Spread items across partitions, one tenant's rows among many.
func ddbSeedPartition(i, partitions int) string {
	if partitions <= 1 {
		return "tenant"
	}
	return fmt.Sprintf("tenant-%03d", i%partitions)
}

func ddbQuery(t *testing.T, table, pk, sk string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"TableName":                 table,
		"KeyConditionExpression":    "pk = :p AND sk = :s",
		"ExpressionAttributeValues": map[string]any{":p": map[string]any{"S": pk}, ":s": map[string]any{"S": sk}},
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	request.Header.Set("X-Amz-Target", "DynamoDB_20120810.Query")
	recorder := httptest.NewRecorder()
	handleDDBQuery(recorder, request)
	return recorder
}

// A query matching one item over a large table must not copy the whole table;
// each per-item snapshot is one store read.
func TestDDBQueryCopiesOnlyWhatItReturns(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const table, items = "big", 2000
	definition := ddbSeedQueryTable(t, table, items, 1)

	recorder := ddbQuery(t, table, "tenant", "sk-01000")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var out struct {
		Count        int              `json:"Count"`
		ScannedCount int              `json:"ScannedCount"`
		Items        []map[string]any `json:"Items"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
	require.Equal(t, 1, out.Count, "the key condition names one item")
	require.Len(t, out.Items, 1)
	require.Equal(t, "sk-01000", out.Items[0]["sk"].(map[string]any)["S"])

	// The returned item is a copy.
	storedKey := ddbItemKey(definition, map[string]any{
		"pk": map[string]any{"S": "tenant"}, "sk": map[string]any{"S": "sk-01000"},
	})
	release := ddbLockItemKeys(true, storedKey)
	stored, ok := ddbItems.Get(storedKey)
	require.True(t, ok, "the seeded item must be readable at the key the simulator files it under: %s", storedKey)
	stored["payload"].(map[string]any)["M"].(map[string]any)["body"] = map[string]any{"S": "mutated"}
	release()
	body := out.Items[0]["payload"].(map[string]any)["M"].(map[string]any)["body"].(map[string]any)["S"]
	require.Equal(t, "body-01000", body,
		"a query result that aliases stored state lets a later write rewrite an answered response")
}

// Forty concurrent queries over a large table must each take the table's
// item lock once and read only their partition. Counting rather than timing
// keeps the machine's load out of the verdict.
func TestDDBConcurrentQueriesDoNotInterleavePerItem(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const (
		table      = "hot"
		items      = 2000
		partitions = 40
		queriers   = 40
		each       = 5
	)
	_ = ddbSeedQueryTable(t, table, items, partitions)
	counting := &ddbCountingStore{Store: ddbItems}
	ddbItems = counting
	t.Cleanup(func() { ddbItems = counting.Store })

	var failures atomic.Int64
	locksBefore := ddbItemLocks.Acquired()
	var wg sync.WaitGroup
	for q := range queriers {
		wg.Add(1)
		go func(q int) {
			defer wg.Done()
			for i := range each {
				index := (q*each + i) % items
				sk := fmt.Sprintf("sk-%05d", index)
				recorder := ddbQuery(t, table, ddbSeedPartition(index, partitions), sk)
				// Check the answer too: a path answering empty would cost nothing for the
				// wrong reason.
				if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), sk) {
					failures.Add(1)
					return
				}
			}
		}(q)
	}
	wg.Wait()

	require.Zero(t, failures.Load(), "every concurrent query must answer")
	const queries = queriers * each
	require.Equal(t, uint64(queries), ddbItemLocks.Acquired()-locksBefore,
		"%d concurrent queries must take the item lock once each: a query that locks per item "+
			"interleaves with every other query item by item", queries)
	reads := counting.reads.Load()
	require.LessOrEqual(t, reads, int64(queries*items/partitions),
		"%d concurrent queries read %d items from a %d-item table in %d partitions: they are "+
			"reading the table rather than the partitions they address", queries, reads, items, partitions)
	require.GreaterOrEqual(t, reads, int64(queries), "a query that read nothing answered from somewhere else")
}

// A query examines only the keys of the partition it names.
func TestDDBQueryReadsOnlyTheAddressedPartition(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const table, items, partitions = "narrow", 600, 20
	definition := ddbSeedQueryTable(t, table, items, partitions)

	keyExpr, err := ddbCompileExpr("KeyConditionExpression", "pk = :p AND sk = :s", nil,
		map[string]any{":p": map[string]any{"S": "tenant-007"}, ":s": map[string]any{"S": "sk-00007"}})
	require.NoError(t, err)

	prefix, ok := ddbQueryPartitionPrefix(definition, keyExpr)
	require.True(t, ok, "the partition key equality must be readable out of the key condition")

	all := ddbTableSortedKeys(table + "/")
	require.Len(t, all, items, "every seeded item is a candidate before narrowing")
	narrowed := ddbKeysInPartition(all, prefix)
	require.Equal(t, items/partitions, len(narrowed),
		"a query must examine one partition's items, not the table's")
	for _, key := range narrowed {
		require.True(t, ddbKeyInPartition(key, prefix), "key %q is outside the addressed partition", key)
	}

	// Counting the query's store reads proves the narrowing is wired in, not just
	// available.
	counting := &ddbCountingStore{Store: ddbItems}
	ddbItems = counting
	t.Cleanup(func() { ddbItems = counting.Store })
	listing := &ddbListingNames{Store: ddbItemNames}
	ddbItemNames = listing
	t.Cleanup(func() { ddbItemNames = listing.Store })
	recorder := ddbQuery(t, table, "tenant-007", "sk-00007")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, []string{prefix}, listing.prefixes,
		"a query must enumerate only the key range of the partition it addresses, never the table's")
	// The narrowed query returns the same item as a whole-table scan.
	var narrowedOut struct {
		Count int              `json:"Count"`
		Items []map[string]any `json:"Items"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &narrowedOut))
	require.Equal(t, 1, narrowedOut.Count)
	require.Len(t, narrowedOut.Items, 1)
	require.Equal(t, "tenant-007", narrowedOut.Items[0]["pk"].(map[string]any)["S"])
	require.Equal(t, "sk-00007", narrowedOut.Items[0]["sk"].(map[string]any)["S"])
	reads := counting.reads.Load()
	require.LessOrEqual(t, reads, int64(items/partitions),
		"one query read %d items from a %d-item table in %d partitions: it is reading the table, "+
			"not the partition it addresses", reads, items, partitions)
	require.Positive(t, reads, "a query that read nothing answered from somewhere else")

	// The partition comes from the compiled condition, so an alias and a reversed
	// comparison resolve the same.
	aliased, err := ddbCompileExpr("KeyConditionExpression", ":p = #pk AND sk = :s",
		map[string]string{"#pk": "pk"},
		map[string]any{":p": map[string]any{"S": "tenant-007"}, ":s": map[string]any{"S": "sk-00007"}})
	require.NoError(t, err)
	aliasedPrefix, ok := ddbQueryPartitionPrefix(definition, aliased)
	require.True(t, ok)
	require.Equal(t, prefix, aliasedPrefix)

	// A condition that does not fix the partition falls back to the full
	// candidate set.
	noEquality, err := ddbCompileExpr("KeyConditionExpression", "pk > :p", nil,
		map[string]any{":p": map[string]any{"S": "tenant-007"}})
	require.NoError(t, err)
	_, ok = ddbQueryPartitionPrefix(definition, noEquality)
	require.False(t, ok, "a partition that is not fixed by an equality cannot narrow the scan")

	// One partition's prefix must not claim another whose name extends it.
	require.True(t, ddbKeyInPartition(table+"/tenant-007"+ddbKeySeparator+"sk-1", table+"/tenant-007"))
	require.False(t, ddbKeyInPartition(table+"/tenant-0070"+ddbKeySeparator+"sk-1", table+"/tenant-007"),
		"a prefix must not swallow a longer partition name")
}

// Reads share the item lock. Two readers meet inside it and wait for each
// other, so an exclusive lock hangs the test.
func TestDDBReadsRunConcurrently(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const table = "reads"
	definition := ddbSeedQueryTable(t, table, 4, 1)
	key := ddbItemKey(definition, map[string]any{
		"pk": map[string]any{"S": "tenant"}, "sk": map[string]any{"S": "sk-00000"},
	})

	arrived := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	var wg sync.WaitGroup
	for i := range arrived {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer ddbItemLocks.Lock(false, table)()
			close(arrived[i])
			<-arrived[1-i]
			_, ok := ddbItems.Get(key)
			assert.True(t, ok, "the seeded item must be readable under the read lock")
		}()
	}
	wg.Wait()

	// A writer still excludes everyone.
	writerInside := atomic.Bool{}
	var raced atomic.Bool
	var writers sync.WaitGroup
	for range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range 200 {
				release := ddbItemLocks.Lock(true, table)
				if !writerInside.CompareAndSwap(false, true) {
					raced.Store(true)
				}
				writerInside.Store(false)
				release()
			}
		}()
	}
	writers.Wait()
	require.False(t, raced.Load(), "two writers held the item lock at once")
}

// A write to one table must not exclude readers of another. Holding the busy
// table's stripe while reading the quiet one blocks forever under a shared
// lock.
func TestDDBUnrelatedTablesDoNotContend(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	ddbSeedQueryTable(t, "busy", 2, 1)
	ddbSeedQueryTable(t, "quiet", 2, 1)

	release := ddbItemLocks.Lock(true, "busy")
	recorder := ddbTransactGet(t, "quiet", ddbSeededKeys(2))
	release()

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var out struct {
		Responses []struct {
			Item map[string]any `json:"Item"`
		} `json:"Responses"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
	require.Len(t, out.Responses, 2)
	for _, response := range out.Responses {
		require.NotEmpty(t, response.Item)
	}
}

// ddbItemLocks.Lock sorts the stripes, so operations naming tables in opposite
// orders cannot deadlock; a deadlock would block wg.Wait until the test
// binary's timeout.
func TestDDBCrossTableOperationsCannotDeadlock(t *testing.T) {
	ddbQueryConcurrencyStores(t)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if i%2 == 0 {
					ddbItemLocks.Lock(true, "alpha", "beta", "gamma")()
				} else {
					ddbItemLocks.Lock(true, "gamma", "beta", "alpha")()
				}
			}
		}()
	}
	wg.Wait()
}
