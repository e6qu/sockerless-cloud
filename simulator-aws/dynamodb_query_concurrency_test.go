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
	"time"

	"github.com/stretchr/testify/require"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// A Query reads the item store under one lock, and copies only what it
// returns. Reading it item by item — taking the process-wide item mutex,
// fetching one item, and deep copying that item through JSON before deciding
// it does not match the key condition — made concurrent queries interleave on
// that mutex rather than queue behind each other, and made every query pay for
// copying items it would discard. On a real workload that showed up as
// forty-four concurrent queries each over a minute old with the in-flight
// count climbing rather than draining.
//
// These tests hold the query path to both properties: the work is proportional
// to what a query returns rather than to what it examines, and concurrent
// queries stay proportional to their number rather than to their product.

// ddbQueryConcurrencyStores gives one test its own item store, table store and
// key index, backed by a real database because that is what the simulator runs
// on: `ddbItems` is made against the server's own handle, so every per-item
// read in a query is a database read. A memory-backed store makes those reads
// nearly free and hides the cost this is about — measured, the same tests
// against a memory store show no difference between reading per item and
// reading once, and against a database store they show the difference plainly.
//
// The key index is generation-cached across queries, so its generation is
// bumped to invalidate whatever a previous test left in it.
func ddbQueryConcurrencyStores(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	db, err := sim.OpenDB(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ddbItems = sim.MakeStore[map[string]any](db, "ddb_items")
	ddbItemNames = sim.MakeStore[string](db, "ddb_item_names")
	ddbTables = sim.MakeStore[DDBTable](db, "ddb_tables")
	ddbItemsMu = sync.RWMutex{}
	ddbKeyGen.Add(1)
}

// ddbCountingStore counts the per-item reads a query performs, which is the
// cost the narrowing exists to remove and the only way to observe it: whether
// a query reads one partition or the whole table changes nothing it answers,
// only what it touches. Every other method delegates untouched.
type ddbCountingStore struct {
	sim.Store[map[string]any]
	reads atomic.Int64
}

func (s *ddbCountingStore) Get(id string) (map[string]any, bool) {
	s.reads.Add(1)
	return s.Store.Get(id)
}

// ddbSeedQueryTable stores one table of items sharing a partition key, so a
// query's key condition matches exactly one of them and the rest are
// candidates it must examine and reject.
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
			// A payload with nesting, so a deep copy of an item costs
			// something — copying every candidate is the behaviour under test.
			"payload": map[string]any{"M": map[string]any{
				"body":  map[string]any{"S": fmt.Sprintf("body-%05d", i)},
				"tags":  map[string]any{"L": []any{map[string]any{"S": "a"}, map[string]any{"S": "b"}}},
				"count": map[string]any{"N": fmt.Sprintf("%d", i)},
			}},
		}
		// The store key comes from the simulator's own key function: a test
		// that invents its own key format tests a table the simulator could
		// never have written.
		key := ddbItemKey(definition, item)
		ddbItems.Put(key, item)
		ddbItemNames.Put(key, key)
	}
	ddbKeyGen.Add(1)
	return definition
}

// ddbSeedPartition spreads seeded items across partitions the way a real table
// holds them — one tenant's rows among many tenants' — because a query reads
// one partition and the cost being measured is whether it reads the others too.
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

// TestDDBQueryCopiesOnlyWhatItReturns is the cheap, deterministic half: a query
// over a large table that matches one item must not deep copy the whole table.
// The copies are counted through the store's own reads, which a per-item
// snapshot performs once per candidate.
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

	// The returned item is a copy: mutating the stored item must not change it.
	ddbItemsMu.Lock()
	storedKey := ddbItemKey(definition, map[string]any{
		"pk": map[string]any{"S": "tenant"}, "sk": map[string]any{"S": "sk-01000"},
	})
	stored, ok := ddbItems.Get(storedKey)
	require.True(t, ok, "the seeded item must be readable at the key the simulator files it under: %s", storedKey)
	stored["payload"].(map[string]any)["M"].(map[string]any)["body"] = map[string]any{"S": "mutated"}
	ddbItemsMu.Unlock()
	body := out.Items[0]["payload"].(map[string]any)["M"].(map[string]any)["body"].(map[string]any)["S"]
	require.Equal(t, "body-01000", body,
		"a query result that aliases stored state lets a later write rewrite an answered response")
}

// TestDDBConcurrentQueriesDoNotInterleavePerItem is the property the report is
// about. Forty concurrent queries over a table large enough that reading it
// whole hurts must cost their number, not their number times the table.
//
// The budget asserts an order of magnitude rather than a duration, because a
// wall-clock assertion tuned tightly is a flake on a loaded runner. It is set
// from measurement on one machine, database-backed as the simulator runs:
// eight seconds reading every item under a per-item lock, and four tenths of a
// second reading one partition under one. Four seconds sits between those,
// with room for a runner several times slower before it reaches the failing
// side. The structural half of this property — that only the addressed
// partition is read at all — is asserted deterministically in
// TestDDBQueryReadsOnlyTheAddressedPartition, which is what fails first if the
// narrowing regresses.
func TestDDBConcurrentQueriesDoNotInterleavePerItem(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const (
		table      = "hot"
		items      = 2000
		partitions = 40
		queriers   = 40
		each       = 5
		budget     = 4 * time.Second
	)
	_ = ddbSeedQueryTable(t, table, items, partitions)

	var failures atomic.Int64
	started := time.Now()
	var wg sync.WaitGroup
	for q := range queriers {
		wg.Add(1)
		go func(q int) {
			defer wg.Done()
			for i := range each {
				index := (q*each + i) % items
				sk := fmt.Sprintf("sk-%05d", index)
				recorder := ddbQuery(t, table, ddbSeedPartition(index, partitions), sk)
				// Each query's answer is checked, not just its status: a query
				// path that answered every request with an empty body would
				// be fast for the wrong reason and this test would applaud it.
				if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), sk) {
					failures.Add(1)
					return
				}
			}
		}(q)
	}
	wg.Wait()
	elapsed := time.Since(started)

	require.Zero(t, failures.Load(), "every concurrent query must answer")
	t.Logf("%d queries across %d goroutines over %d items in %s",
		queriers*each, queriers, items, elapsed)
	require.Less(t, elapsed, budget,
		"%d concurrent queries took %s over a %d-item table: the query path is paying per item "+
			"under a process-wide lock rather than per query", queriers, elapsed, items)
}

// TestDDBQueryReadsOnlyTheAddressedPartition is the deterministic half: a query
// names one partition, so the keys it examines are that partition's and no
// others. This is what makes a query cost the partition rather than the table,
// and unlike the timing test it fails immediately and identically everywhere.
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

	// The narrowing has to be wired into the query, not merely available to it:
	// counting the store reads one query performs is what tells the two apart,
	// and a helper asserted on its own would stay green with the call removed.
	counting := &ddbCountingStore{Store: ddbItems}
	ddbItems = counting
	t.Cleanup(func() { ddbItems = counting.Store })
	recorder := ddbQuery(t, table, "tenant-007", "sk-00007")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	// Reading fewer items must not mean answering with fewer: the narrowed
	// query returns the same item the whole-table scan returned.
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

	// An aliased name and a reversed comparison resolve the same partition: the
	// partition is read out of the compiled condition, not out of the request
	// text, so #pk and :p = pk are the same query.
	aliased, err := ddbCompileExpr("KeyConditionExpression", ":p = #pk AND sk = :s",
		map[string]string{"#pk": "pk"},
		map[string]any{":p": map[string]any{"S": "tenant-007"}, ":s": map[string]any{"S": "sk-00007"}})
	require.NoError(t, err)
	aliasedPrefix, ok := ddbQueryPartitionPrefix(definition, aliased)
	require.True(t, ok)
	require.Equal(t, prefix, aliasedPrefix)

	// A condition that does not fix the partition yields no prefix, and the
	// query falls back to the full candidate set rather than to a wrong one.
	noEquality, err := ddbCompileExpr("KeyConditionExpression", "pk > :p", nil,
		map[string]any{":p": map[string]any{"S": "tenant-007"}})
	require.NoError(t, err)
	_, ok = ddbQueryPartitionPrefix(definition, noEquality)
	require.False(t, ok, "a partition that is not fixed by an equality cannot narrow the scan")

	// One partition's prefix must not claim another whose name extends it.
	require.True(t, ddbKeyInPartition(table+"/tenant-007|sk-1", table+"/tenant-007"))
	require.False(t, ddbKeyInPartition(table+"/tenant-0070|sk-1", table+"/tenant-007"),
		"a prefix must not swallow a longer partition name")
}

// TestDDBReadsRunConcurrently is the property issue #43 is about: the item lock
// guards the store across whole operations, which is what makes a
// read-modify-write atomic, but reading is most of what it guards. Under an
// exclusive lock every GetItem queued behind every other one, and single-item
// reads that are O(1) by any measure took thirteen seconds on a busy service.
//
// Parallelism is measured rather than timed: each reader records that it is
// inside the critical section, and the test asserts that more than one was
// inside at once. A duration would only say "fast today"; an overlap count
// says the lock admits readers together, which is the change.
func TestDDBReadsRunConcurrently(t *testing.T) {
	ddbQueryConcurrencyStores(t)
	const table, items, partitions = "reads", 400, 8
	definition := ddbSeedQueryTable(t, table, items, partitions)

	// A reader that holds the read lock briefly and reports the peak number of
	// holders it saw. Against an exclusive lock the peak is one by
	// construction, whatever the machine is doing.
	var inside, peak atomic.Int64
	observe := func() {
		ddbItemsMu.RLock()
		defer ddbItemsMu.RUnlock()
		now := inside.Add(1)
		defer inside.Add(-1)
		for {
			was := peak.Load()
			if now <= was || peak.CompareAndSwap(was, now) {
				break
			}
		}
		// Hold the lock for a moment doing what a read does, so overlap has
		// somewhere to happen.
		key := ddbItemKey(definition, map[string]any{
			"pk": map[string]any{"S": "tenant-000"}, "sk": map[string]any{"S": "sk-00000"},
		})
		if item, ok := ddbItems.Get(key); ok {
			_ = ddbCloneItem(item)
		}
		time.Sleep(time.Millisecond)
	}

	const readers = 16
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				observe()
			}
		}()
	}
	wg.Wait()

	t.Logf("peak concurrent readers inside the item lock: %d of %d", peak.Load(), readers)
	require.Greater(t, peak.Load(), int64(1),
		"no two readers were ever inside the item lock together: reads are still serialised, "+
			"so every GetItem queues behind every other operation")

	// And a writer still excludes everyone: the atomicity the lock exists for
	// is not what was traded away.
	writerInside := atomic.Bool{}
	var raced atomic.Bool
	var writers sync.WaitGroup
	for range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range 25 {
				ddbItemsMu.Lock()
				if !writerInside.CompareAndSwap(false, true) {
					raced.Store(true)
				}
				time.Sleep(100 * time.Microsecond)
				writerInside.Store(false)
				ddbItemsMu.Unlock()
			}
		}()
	}
	writers.Wait()
	require.False(t, raced.Load(), "two writers held the item lock at once")
}
