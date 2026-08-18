package main

import (
	"sort"
	"strings"
)

// A Query addresses one partition. DynamoDB requires the key condition to fix
// the partition key with an equality — "The condition must perform an equality
// test on a single partition key value" — and the item store is keyed
// `<table>/<hash>` or `<table>/<hash>|<range>`, so the items a query can
// possibly return are a contiguous run of that sorted key space.
//
// Examining the whole table instead is what made a query cost the table rather
// than the partition: every item read from the store, and under the item lock.
// A workload with many partitions paid for all of them on every query, which
// is how forty-four concurrent queries left every request over a minute old.
//
// Narrowing is an optimisation over the same answer, never a different one:
// the key condition is still evaluated against every candidate, so a partition
// this cannot determine simply yields the full candidate set and the query
// behaves exactly as before.

// ddbQueryPartitionPrefix returns the item-key prefix a query is confined to,
// and whether one could be determined. It reads the partition-key equality out
// of the compiled key condition rather than the request text, so an aliased
// name (#pk) and a value reference (:v) resolve the way the evaluator resolves
// them.
func ddbQueryPartitionPrefix(t DDBTable, keyExpr *ddbCompiledExpr) (string, bool) {
	if keyExpr == nil {
		return "", false
	}
	hash := ""
	for _, k := range t.KeySchema {
		if k.KeyType == "HASH" {
			hash = k.AttributeName
		}
	}
	if hash == "" {
		return "", false
	}
	value, ok := ddbEqualityValueFor(keyExpr.node, hash, keyExpr.names, keyExpr.values)
	if !ok {
		return "", false
	}
	scalar := ddbExtractAttrValue(value)
	if scalar == "" {
		return "", false
	}
	// Both key shapes start with the hash: a hash-only table's key is exactly
	// `<table>/<hash>`, and a composite key continues `|<range>`.
	return t.TableName + "/" + scalar, true
}

// ddbEqualityValueFor finds `attr = <value>` for the named attribute within the
// condition. Only conjunctions are walked: a key condition is the partition
// equality optionally AND-ed with a sort-key condition, and an equality under
// an OR or a NOT does not confine the query to that partition.
func ddbEqualityValueFor(node ddbCond, attr string, names map[string]string, values map[string]any) (any, bool) {
	switch n := node.(type) {
	case ddbCondAnd:
		if v, ok := ddbEqualityValueFor(n.l, attr, names, values); ok {
			return v, true
		}
		return ddbEqualityValueFor(n.r, attr, names, values)
	case ddbCondCompare:
		if n.op != "=" {
			return nil, false
		}
		if ref, ok := ddbComparedValueFor(n.l, n.r, attr, names); ok {
			v, present := values[ref]
			return v, present
		}
		if ref, ok := ddbComparedValueFor(n.r, n.l, attr, names); ok {
			v, present := values[ref]
			return v, present
		}
	}
	return nil, false
}

// ddbComparedValueFor reports the value reference compared against attr when
// path names that attribute and other is a value reference.
func ddbComparedValueFor(path, other ddbOperand, attr string, names map[string]string) (string, bool) {
	p, ok := path.(ddbOperandPath)
	if !ok {
		return "", false
	}
	name := p.path
	if resolved, aliased := names[name]; aliased {
		name = resolved
	}
	if !strings.EqualFold(name, attr) {
		return "", false
	}
	v, ok := other.(ddbOperandValue)
	if !ok {
		return "", false
	}
	return v.ref, true
}

// ddbKeysInPartition narrows an ascending, sorted set of a table's item keys to
// the ones inside the partition, preserving order. keys must be ascending.
func ddbKeysInPartition(keys []string, prefix string) []string {
	start := sort.SearchStrings(keys, prefix)
	end := start
	for end < len(keys) && ddbKeyInPartition(keys[end], prefix) {
		end++
	}
	return keys[start:end]
}

// ddbKeyInPartition reports whether an item key belongs to the partition the
// prefix names. `tbl/a` must not claim `tbl/ab`: the key either is the prefix
// exactly (a hash-only table) or continues with the range separator.
func ddbKeyInPartition(key, prefix string) bool {
	if key == prefix {
		return true
	}
	return strings.HasPrefix(key, prefix+"|")
}
