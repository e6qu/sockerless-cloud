package main

import (
	"hash/fnv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// One entry per item an index holds, keyed
// "<table>#<index>/<index hash>\x01[<index range>\x01]<base key>" with the
// item's store key as value. The ordered key encoding lets a query read a
// partition as a range in index sort-key order; the trailing base key keeps
// items sharing an index key apart. An item lacking the index's key
// attributes has no entry, so the index is sparse, as DynamoDB's are. Entries
// derive from items, so they live in memory and rebuild at startup.
var ddbIndexEntries sim.Store[string] = sim.NewStateStore[string]()

type ddbIndex struct {
	name       string
	keySchema  []DDBKeySchemaEntry
	projection *DDBProjection
}

func ddbIndexes(t DDBTable) []ddbIndex {
	var out []ddbIndex
	for _, g := range t.GlobalSecondaryIndexes {
		out = append(out, ddbIndex{name: g.IndexName, keySchema: g.KeySchema, projection: g.Projection})
	}
	for _, l := range t.LocalSecondaryIndexes {
		out = append(out, ddbIndex{name: l.IndexName, keySchema: l.KeySchema, projection: l.Projection})
	}
	return out
}

func ddbIndexByName(t DDBTable, name string) (ddbIndex, bool) {
	for _, index := range ddbIndexes(t) {
		if index.name == name {
			return index, true
		}
	}
	return ddbIndex{}, false
}

func ddbIndexPrefix(table, index string) string { return table + "#" + index + "/" }

// false when the item lacks an index key attribute and so is not in the index.
func ddbIndexEntryKey(t DDBTable, index ddbIndex, item map[string]any) (string, bool) {
	var hash, rng string
	for _, k := range index.keySchema {
		value := ddbExtractAttrValue(item[k.AttributeName])
		if value == "" {
			return "", false
		}
		switch k.KeyType {
		case "HASH":
			hash = value
		case "RANGE":
			rng = value
		}
	}
	key := ddbIndexPrefix(t.TableName, index.name) + hash + ddbKeySeparator
	if rng != "" {
		key += rng + ddbKeySeparator
	}
	return key + strings.TrimPrefix(ddbItemKey(t, item), t.TableName+"/"), true
}

// Either version may be absent.
func ddbReindexItem(t DDBTable, key string, old map[string]any, hadOld bool, updated map[string]any, hasUpdated bool) {
	for _, index := range ddbIndexes(t) {
		if hadOld {
			if entry, ok := ddbIndexEntryKey(t, index, old); ok {
				ddbIndexEntries.Delete(entry)
			}
		}
		if hasUpdated {
			if entry, ok := ddbIndexEntryKey(t, index, updated); ok {
				ddbIndexEntries.Put(entry, key)
			}
		}
	}
}

// ddbPutItem and ddbDeleteItem are the only item writers; each keeps the item,
// its name and its index entries together.
func ddbPutItem(t DDBTable, key string, item map[string]any) {
	old, hadOld := ddbItems.Get(key)
	ddbItems.Put(key, item)
	ddbItemNames.Put(key, key)
	ddbReindexItem(t, key, old, hadOld, item, true)
}

func ddbDeleteItem(t DDBTable, key string) {
	old, hadOld := ddbItems.Get(key)
	ddbItems.Delete(key)
	ddbItemNames.Delete(key)
	ddbReindexItem(t, key, old, hadOld, nil, false)
}

// Backfills a new index and rebuilds entries at startup.
func ddbRebuildTableIndexes(t DDBTable) {
	ddbDropTableIndexes(t.TableName)
	for _, key := range ddbTableSortedKeys(t.TableName + "/") {
		if item, ok := ddbItems.Get(key); ok {
			ddbReindexItem(t, key, nil, false, item, true)
		}
	}
}

func ddbDropTableIndexes(table string) {
	for _, row := range ddbIndexEntries.ListPrefix(table + "#") {
		ddbIndexEntries.Delete(row.ID)
	}
}

// The partition the key condition fixes, or the whole index, resumed after
// exclusiveStart in the direction asked.
func ddbIndexCandidateKeys(t DDBTable, index ddbIndex, keyExpr *ddbCompiledExpr, exclusiveStart map[string]any, forward bool) []string {
	prefix := ddbIndexPrefix(t.TableName, index.name)
	if keyExpr != nil {
		for _, k := range index.keySchema {
			if k.KeyType != "HASH" {
				continue
			}
			if value, ok := ddbEqualityValueFor(keyExpr.node, k.AttributeName, keyExpr.names, keyExpr.values); ok {
				if encoded := ddbExtractAttrValue(value); encoded != "" {
					prefix += encoded + ddbKeySeparator
				}
			}
		}
	}
	rows := ddbIndexEntries.ListPrefix(prefix)
	start := ""
	if len(exclusiveStart) > 0 {
		start, _ = ddbIndexEntryKey(t, index, exclusiveStart)
	}
	keys := make([]string, 0, len(rows))
	if forward {
		for _, row := range rows {
			if start == "" || row.ID > start {
				keys = append(keys, row.Item)
			}
		}
		return keys
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if start == "" || rows[i].ID < start {
			keys = append(keys, rows[i].Item)
		}
	}
	return keys
}

// The table's key attributes and the index's.
func ddbIndexLastEvaluatedKey(t DDBTable, index ddbIndex, item map[string]any) map[string]any {
	out := ddbExtractKey(t, item)
	for _, k := range index.keySchema {
		if value, ok := item[k.AttributeName]; ok {
			out[k.AttributeName] = value
		}
	}
	return out
}

// KEYS_ONLY keeps the index's and table's keys, INCLUDE adds the named
// attributes, ALL keeps everything.
func ddbProjectToIndex(t DDBTable, index ddbIndex, item map[string]any) map[string]any {
	if index.projection == nil || index.projection.ProjectionType == "" || index.projection.ProjectionType == "ALL" {
		return item
	}
	keep := map[string]bool{}
	for _, k := range t.KeySchema {
		keep[k.AttributeName] = true
	}
	for _, k := range index.keySchema {
		keep[k.AttributeName] = true
	}
	if index.projection.ProjectionType == "INCLUDE" {
		for _, name := range index.projection.NonKeyAttributes {
			keep[name] = true
		}
	}
	out := make(map[string]any, len(keep))
	for name, value := range item {
		if keep[name] {
			out[name] = value
		}
	}
	return out
}

// Derive the segment from table and partition key alone, so a partition stays
// in one segment as items come and go.
func ddbScanSegment(itemKey string, total int) int {
	partition, _, _ := strings.Cut(itemKey, ddbKeySeparator)
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(partition))
	return int(hash.Sum32() % uint32(total))
}
