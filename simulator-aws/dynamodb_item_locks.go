package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// The item store is locked per table, the granularity DynamoDB itself isolates
// at: one service-wide lock queued every operation behind every other, and a
// write to one table excluded reads of all the rest. Every item key is
// `<table>/<itemKey>`, so the table lock is derivable from the key alone.
//
// An operation spanning several tables takes their locks in one call, which
// orders them, and every operation can name its tables up front — PartiQL
// parses its statements before locking for that reason. Nothing takes a
// second item lock while holding one.
var ddbItemLocks kvstore.RWLocks

// ddbTableOfItemKey returns the table an item key belongs to. A key with no
// separator is its own table rather than a silent fall-through to a shared one.
func ddbTableOfItemKey(itemKey string) string {
	if i := strings.IndexByte(itemKey, '/'); i >= 0 {
		return itemKey[:i]
	}
	return itemKey
}

// ddbLockItemKeys takes the locks of the tables a set of item keys belong to.
func ddbLockItemKeys(write bool, itemKeys ...string) func() {
	tables := make([]string, 0, len(itemKeys))
	for _, itemKey := range itemKeys {
		tables = append(tables, ddbTableOfItemKey(itemKey))
	}
	return ddbItemLocks.Lock(write, tables...)
}
