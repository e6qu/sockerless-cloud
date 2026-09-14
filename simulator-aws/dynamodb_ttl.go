package main

import (
	"context"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// DynamoDB Time to Live, the deleting half.
//
// UpdateTimeToLive and DescribeTimeToLive stored and reported a table's TTL
// attribute, but nothing deleted an expired item, so a table whose application
// relied on TTL grew without bound. ecs-dev-desktop's table on the Scaleway
// stack held 3,874 session correlations and 1,056 logout tokens that had all
// expired, the oldest in August, and every scan of that table read them.
//
// What DynamoDB documents, and what the sweep does:
//   - Only a Number attribute in epoch seconds is considered; any other type is
//     ignored, and so is a value more than five years in the past.
//   - An expired item is deleted "typically within a few days". Until then it
//     is still returned by reads, and an update that moves its TTL into the
//     future, or removes it, keeps it. The sweep re-reads each candidate under
//     the table's write lock, so an item updated after it was listed is judged
//     on its new value.
//   - A TTL deletion consumes no write capacity and leaves the table's indexes
//     like any other delete. The simulator derives index contents from the
//     stored items, so removing the item is the whole of it.
//
// The simulator deletes at the next sweep rather than days later: the delay is
// unspecified, and a prompt deletion is the one a test can observe.
const ddbTTLSweepInterval = 5 * time.Second

// ddbTTLMaxAge is how far in the past a TTL value may be and still expire.
const ddbTTLMaxAgeYears = 5

func startDDBTTLSweeper(srv *sim.Server) {
	srv.StartBackground(func(ctx context.Context) {
		ticker := time.NewTicker(ddbTTLSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				ddbSweepExpiredItems(now)
			}
		}
	})
}

// ddbSweepExpiredItems deletes every item past its table's TTL as of now and
// returns how many it deleted.
func ddbSweepExpiredItems(now time.Time) int {
	deleted := 0
	for _, table := range ddbTables.List() {
		settings, _ := ddbTableSettings.Get(table.TableName)
		if settings.TTLStatus != "ENABLED" || settings.TTLAttributeName == "" {
			continue
		}
		for itemKey, item := range ddbItemSnapshots(ddbTableSortedKeys(table.TableName + "/")) {
			if !ddbItemExpired(item, settings.TTLAttributeName, now) {
				continue
			}
			if ddbDeleteIfStillExpired(table.TableName, itemKey, settings.TTLAttributeName, now) {
				deleted++
			}
		}
	}
	return deleted
}

func ddbDeleteIfStillExpired(tableName, itemKey, attribute string, now time.Time) bool {
	defer ddbLockTables(true, tableName)()
	item, ok := ddbItems.Get(itemKey)
	if !ok || !ddbItemExpired(item, attribute, now) {
		return false
	}
	ddbItems.Delete(itemKey)
	ddbItemNames.Delete(itemKey)
	ddbBumpKeyGen()
	return true
}

// ddbItemExpired reports whether item's TTL attribute is a Number of epoch
// seconds that is not after now and not more than five years before it.
func ddbItemExpired(item map[string]any, attribute string, now time.Time) bool {
	value, ok := item[attribute].(map[string]any)
	if !ok {
		return false
	}
	raw, ok := value["N"].(string)
	if !ok {
		return false
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return false
	}
	expiry := time.Unix(int64(math.Floor(seconds)), 0)
	return !expiry.After(now) && !expiry.Before(now.AddDate(-ddbTTLMaxAgeYears, 0, 0))
}

// ddbTableUsage computes a table's item count and size, and each secondary
// index's, from the items the table holds. It reads every item, so it is never
// run on a request: DescribeTable serves the figures ddbDescribedUsage last
// computed, which is also how DynamoDB behaves — it refreshes these figures
// about every six hours rather than on each call. An index counts the items
// that carry its key attributes, and its size is what it projects: the table
// and index keys, the included attributes, or the whole item.
func ddbTableUsage(t DDBTable) DDBTable {
	items := ddbItemSnapshots(ddbTableSortedKeys(t.TableName + "/"))
	t.ItemCount, t.TableSizeBytes = 0, 0
	gsis := make([]DDBGlobalSecondaryIndex, len(t.GlobalSecondaryIndexes))
	copy(gsis, t.GlobalSecondaryIndexes)
	lsis := make([]DDBLocalSecondaryIndex, len(t.LocalSecondaryIndexes))
	copy(lsis, t.LocalSecondaryIndexes)
	for i := range gsis {
		gsis[i].ItemCount, gsis[i].IndexSizeBytes = 0, 0
	}
	for i := range lsis {
		lsis[i].ItemCount, lsis[i].IndexSizeBytes = 0, 0
	}
	for _, item := range items {
		t.ItemCount++
		t.TableSizeBytes += int64(ddbItemSizeBytes(item))
		for i := range gsis {
			if size, ok := ddbProjectedSize(item, t.KeySchema, gsis[i].KeySchema, gsis[i].Projection); ok {
				gsis[i].ItemCount++
				gsis[i].IndexSizeBytes += size
			}
		}
		for i := range lsis {
			if size, ok := ddbProjectedSize(item, t.KeySchema, lsis[i].KeySchema, lsis[i].Projection); ok {
				lsis[i].ItemCount++
				lsis[i].IndexSizeBytes += size
			}
		}
	}
	if len(gsis) > 0 {
		t.GlobalSecondaryIndexes = gsis
	}
	if len(lsis) > 0 {
		t.LocalSecondaryIndexes = lsis
	}
	return t
}

// ddbProjectedSize is the size of item as an index projects it, and whether
// the item appears in the index at all (it must carry every index key).
func ddbProjectedSize(item map[string]any, tableKeys, indexKeys []DDBKeySchemaEntry, projection *DDBProjection) (int64, bool) {
	for _, key := range indexKeys {
		if _, ok := item[key.AttributeName]; !ok {
			return 0, false
		}
	}
	if projection == nil || projection.ProjectionType == "" || projection.ProjectionType == "ALL" {
		return int64(ddbItemSizeBytes(item)), true
	}
	projected := map[string]any{}
	for _, key := range append(append([]DDBKeySchemaEntry{}, tableKeys...), indexKeys...) {
		projected[key.AttributeName] = item[key.AttributeName]
	}
	if projection.ProjectionType == "INCLUDE" {
		for _, name := range projection.NonKeyAttributes {
			if value, ok := item[name]; ok {
				projected[name] = value
			}
		}
	}
	return int64(ddbItemSizeBytes(projected)), true
}

// DescribeTable's usage figures are served from a cache refreshed off the
// request path.
//
// BUG-3000 made DescribeTable compute them on every call, which read and copied
// every item in the table. On a table of 2,501 items in the deployed simulator
// (SQLite-backed, so each item read is a query) DescribeTable took 1.6 s where
// ListTables took 0.7 s. ecs-dev-desktop's health ping is a DescribeTable, and
// its monitoring endpoint, which Shauth abandons at five seconds, took 5.8 s.
// The figures now come from the last refresh; a refresh runs in the background,
// at most once per interval per table, and a table described before its first
// refresh reports zero, as a newly created DynamoDB table does.
const ddbUsageRefreshInterval = time.Minute

type ddbUsageEntry struct {
	usage      DDBTable
	computedAt time.Time
	refreshing bool
}

var (
	ddbUsageMu      sync.Mutex
	ddbUsageByTable = map[string]*ddbUsageEntry{}
	// ddbUsageRefreshes counts completed refreshes, so a test can tell a cached
	// answer from a recomputed one.
	ddbUsageRefreshes atomic.Uint64
)

// ddbDescribedUsage returns t carrying the most recently computed usage figures,
// starting a background refresh when they are missing or older than the
// interval. It never reads the table's items.
func ddbDescribedUsage(t DDBTable, now time.Time) DDBTable {
	ddbUsageMu.Lock()
	entry := ddbUsageByTable[t.TableName]
	if entry == nil {
		entry = &ddbUsageEntry{}
		ddbUsageByTable[t.TableName] = entry
	}
	stale := entry.computedAt.IsZero() || now.Sub(entry.computedAt) >= ddbUsageRefreshInterval
	if stale && !entry.refreshing && !simDraining.Load() {
		entry.refreshing = true
		name := t.TableName
		simGo(func() { ddbRefreshTableUsage(name) })
	}
	cached, computed := entry.usage, !entry.computedAt.IsZero()
	ddbUsageMu.Unlock()
	return ddbApplyUsage(t, cached, computed)
}

// ddbRefreshTableUsage recomputes one table's figures and stores them.
func ddbRefreshTableUsage(name string) {
	table, ok := ddbTables.Get(name)
	var usage DDBTable
	if ok {
		usage = ddbTableUsage(table)
	}
	ddbUsageMu.Lock()
	defer ddbUsageMu.Unlock()
	if entry := ddbUsageByTable[name]; entry != nil {
		entry.usage, entry.computedAt, entry.refreshing = usage, time.Now(), false
	}
	ddbUsageRefreshes.Add(1)
}

// ddbForgetUsage drops a table's cached figures, so a table recreated under the
// same name never reports its predecessor's.
func ddbForgetUsage(name string) {
	ddbUsageMu.Lock()
	defer ddbUsageMu.Unlock()
	delete(ddbUsageByTable, name)
}

// ddbResetUsage drops every cached figure; the stores they describe were rebuilt.
func ddbResetUsage() {
	ddbUsageMu.Lock()
	defer ddbUsageMu.Unlock()
	ddbUsageByTable = map[string]*ddbUsageEntry{}
}

// ddbApplyUsage copies computed figures onto the table description being
// answered, matching indexes by name; figures not yet computed read as zero.
func ddbApplyUsage(t, usage DDBTable, computed bool) DDBTable {
	t.ItemCount, t.TableSizeBytes = 0, 0
	gsiUsage := map[string]DDBGlobalSecondaryIndex{}
	lsiUsage := map[string]DDBLocalSecondaryIndex{}
	if computed {
		t.ItemCount, t.TableSizeBytes = usage.ItemCount, usage.TableSizeBytes
		for _, g := range usage.GlobalSecondaryIndexes {
			gsiUsage[g.IndexName] = g
		}
		for _, l := range usage.LocalSecondaryIndexes {
			lsiUsage[l.IndexName] = l
		}
	}
	if len(t.GlobalSecondaryIndexes) > 0 {
		gsis := make([]DDBGlobalSecondaryIndex, len(t.GlobalSecondaryIndexes))
		copy(gsis, t.GlobalSecondaryIndexes)
		for i := range gsis {
			u := gsiUsage[gsis[i].IndexName]
			gsis[i].ItemCount, gsis[i].IndexSizeBytes = u.ItemCount, u.IndexSizeBytes
		}
		t.GlobalSecondaryIndexes = gsis
	}
	if len(t.LocalSecondaryIndexes) > 0 {
		lsis := make([]DDBLocalSecondaryIndex, len(t.LocalSecondaryIndexes))
		copy(lsis, t.LocalSecondaryIndexes)
		for i := range lsis {
			u := lsiUsage[lsis[i].IndexName]
			lsis[i].ItemCount, lsis[i].IndexSizeBytes = u.ItemCount, u.IndexSizeBytes
		}
		t.LocalSecondaryIndexes = lsis
	}
	return t
}
