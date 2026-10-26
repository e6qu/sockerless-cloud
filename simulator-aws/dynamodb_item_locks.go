package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// Lock the item store per table, the granularity DynamoDB isolates at. Every
// item key is `<table>/<itemKey>`, so the key names its lock.
//
// An operation spanning tables takes their locks in one ordered call, naming
// its tables up front (PartiQL parses before locking for that reason).
// Nothing takes a second item lock while holding one.
var ddbItemLocks kvstore.RWLocks

// A key without a separator is its own table, never a shared one.
func ddbTableOfItemKey(itemKey string) string {
	if i := strings.IndexByte(itemKey, '/'); i >= 0 {
		return itemKey[:i]
	}
	return itemKey
}

func ddbLockItemKeys(write bool, itemKeys ...string) func() {
	tables := make([]string, 0, len(itemKeys))
	for _, itemKey := range itemKeys {
		tables = append(tables, ddbTableOfItemKey(itemKey))
	}
	return ddbItemLocks.Lock(write, tables...)
}
