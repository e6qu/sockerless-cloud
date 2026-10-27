package main

import (
	"sort"
	"strings"
)

// DynamoDB requires a key condition to fix the partition key by equality, so
// a query's candidates are a contiguous run of the sorted key space. The key
// condition is still evaluated on every candidate; when the partition cannot
// be determined, the full candidate set applies.

// Read the equality from the compiled condition so aliases and value
// references resolve as the evaluator resolves them.
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
	// Both key shapes start with the hash.
	return t.TableName + "/" + scalar, true
}

// Walk only conjunctions: an equality under OR or NOT does not confine the
// query.
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

// keys must be ascending.
func ddbKeysInPartition(keys []string, prefix string) []string {
	start := sort.SearchStrings(keys, prefix)
	end := start
	for end < len(keys) && ddbKeyInPartition(keys[end], prefix) {
		end++
	}
	return keys[start:end]
}

// `tbl/a` must not claim `tbl/ab`: the key equals the prefix or continues with
// the separator.
func ddbKeyInPartition(key, prefix string) bool {
	if key == prefix {
		return true
	}
	return strings.HasPrefix(key, prefix+ddbKeySeparator)
}
