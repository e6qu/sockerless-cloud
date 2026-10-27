package main

import (
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// The DynamoDB Time to Live sweep, as documented:
//   - Only a Number attribute in epoch seconds counts; other types, and values
//     more than five years old, are ignored.
//   - An expired item stays readable until deleted, and an update that moves
//     or removes its TTL keeps it. The sweep re-reads each candidate under the
//     table's write lock, so it judges the current value.
//   - A TTL deletion consumes no write capacity.
//
// DynamoDB deletes "typically within a few days"; the simulator deletes at the
// next sweep, a delay a test can observe.
const ddbTTLSweepInterval = 5 * time.Second

const ddbTTLMaxAgeYears = 5

func startDDBTTLSweeper(srv *sim.Server) {
	kvstore.StartSweeper(srv, "DynamoDB TTL sweeper", ddbTTLSweepInterval, func(now time.Time) {
		ddbSweepExpiredItems(now)
	})
}

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
			if ddbDeleteIfStillExpired(table, itemKey, settings.TTLAttributeName, now) {
				deleted++
			}
		}
	}
	return deleted
}

func ddbDeleteIfStillExpired(table DDBTable, itemKey, attribute string, now time.Time) bool {
	defer ddbItemLocks.Lock(true, table.TableName)()
	item, ok := ddbItems.Get(itemKey)
	if !ok || !ddbItemExpired(item, attribute, now) {
		return false
	}
	ddbDeleteItem(table, itemKey)
	return true
}

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

// Reads every item, so it never runs on a request: DescribeTable serves cached
// figures, as DynamoDB refreshes them about every six hours. An index counts
// the items carrying its key attributes, sized by what it projects.
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

// false when the item lacks an index key.
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

// Computing usage per DescribeTable reads every item, so a background refresh
// runs at most once per interval per table; before it, a table reports zero,
// as a new DynamoDB table does.
const ddbUsageRefreshInterval = time.Minute

type ddbUsageEntry struct {
	usage      DDBTable
	computedAt time.Time
	refreshing bool
}

var (
	ddbUsageMu      sync.Mutex
	ddbUsageByTable = map[string]*ddbUsageEntry{}
	// Tests tell a cached answer from a recomputed one by this count.
	ddbUsageRefreshes atomic.Uint64
)

// Starts a background refresh when figures are missing or stale; never reads
// items.
func ddbDescribedUsage(t DDBTable, now time.Time) DDBTable {
	ddbUsageMu.Lock()
	entry := ddbUsageByTable[t.TableName]
	if entry == nil {
		entry = &ddbUsageEntry{}
		ddbUsageByTable[t.TableName] = entry
	}
	stale := entry.computedAt.IsZero() || now.Sub(entry.computedAt) >= ddbUsageRefreshInterval
	if stale && !entry.refreshing && !bg.Draining() {
		entry.refreshing = true
		name := t.TableName
		bg.Go(func() { ddbRefreshTableUsage(name) })
	}
	cached, computed := entry.usage, !entry.computedAt.IsZero()
	ddbUsageMu.Unlock()
	return ddbApplyUsage(t, cached, computed)
}

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

// A table recreated under the same name never reports its predecessor's
// figures.
func ddbForgetUsage(name string) {
	ddbUsageMu.Lock()
	defer ddbUsageMu.Unlock()
	delete(ddbUsageByTable, name)
}

// The stores the figures describe were rebuilt.
func ddbResetUsage() {
	ddbUsageMu.Lock()
	defer ddbUsageMu.Unlock()
	ddbUsageByTable = map[string]*ddbUsageEntry{}
}

// Figures not yet computed read as zero.
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
