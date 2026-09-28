package main

import (
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestDDBOrderedNumberSortsByValue(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	var numbers []string
	for i := 0; i < 3000; i++ {
		mantissa := random.Int63n(1_000_000_000) - 500_000_000
		exponent := random.Intn(80) - 40
		numbers = append(numbers, fmt.Sprintf("%de%d", mantissa, exponent))
	}
	numbers = append(numbers, "0", "-0", "0.0", "1", "1.0", "01", "10", "9", "-2", "-2.5", "0.5", "1e126", "-1e126", "1e-130", "-1e-130")
	value := func(s string) *big.Rat {
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			t.Fatalf("test number %q", s)
		}
		return r
	}
	encode := func(s string) string {
		encoded, ok := ddbOrderedNumber(s)
		if !ok {
			t.Fatalf("ddbOrderedNumber(%q) refused a DynamoDB number", s)
		}
		return encoded
	}
	for i := 0; i < len(numbers); i++ {
		a, b := numbers[i], numbers[(i*7919+13)%len(numbers)]
		byValue := value(a).Cmp(value(b))
		byKey := strings.Compare(encode(a), encode(b))
		if byValue != byKey {
			t.Fatalf("%s vs %s: values compare %d, encodings %d (%q, %q)", a, b, byValue, byKey, encode(a), encode(b))
		}
	}
	for _, same := range [][]string{{"1", "1.0", "01", "1e0"}, {"0", "-0", "0.00"}, {"-2.50", "-2.5"}} {
		for _, n := range same[1:] {
			if encode(n) != encode(same[0]) {
				t.Errorf("%s and %s are equal numbers but encode differently", n, same[0])
			}
		}
	}
}

func TestDDBEscapedKeyTextKeepsOrderAndAvoidsTheSeparator(t *testing.T) {
	texts := []string{"", "a", "a\x00", "a\x01", "a\x02", "a\x03", "ab", "b", "\x01", "\x02z", "zz"}
	sorted := append([]string(nil), texts...)
	sort.Strings(sorted)
	encoded := make([]string, len(sorted))
	for i, s := range sorted {
		encoded[i] = ddbEscapeKeyText(s)
		if strings.Contains(encoded[i], ddbKeySeparator) {
			t.Errorf("%q encodes to %q, which holds the separator", s, encoded[i])
		}
	}
	if !sort.StringsAreSorted(encoded) {
		t.Fatalf("escaping changed the order: %q", encoded)
	}
}

// Items a build before the ordered encoding stored are re-keyed at startup, so
// a query reads them in order, and the migration runs once.
func TestDDBMigrateItemKeysRekeysOnce(t *testing.T) {
	ddbTables = sim.MakeStore[DDBTable](nil, "ddb_tables")
	ddbItems = sim.MakeStore[map[string]any](nil, "ddb_items")
	ddbItemNames = sim.MakeStore[string](nil, "ddb_item_names")
	ddbKeyFormat = sim.MakeStore[string](nil, "ddb_key_format")
	table := DDBTable{TableName: "legacy", KeySchema: []DDBKeySchemaEntry{{AttributeName: "pk", KeyType: "HASH"}, {AttributeName: "n", KeyType: "RANGE"}}}
	ddbTables.Put("legacy", table)
	for _, n := range []string{"9", "10"} {
		old := "legacy/S#p|N#" + n
		ddbItems.Put(old, map[string]any{"pk": map[string]any{"S": "p"}, "n": map[string]any{"N": n}})
		ddbItemNames.Put(old, old)
	}
	ddbItems.Put("gone/S#x", map[string]any{"pk": map[string]any{"S": "x"}})
	ddbItemNames.Put("gone/S#x", "gone/S#x")

	if err := ddbMigrateItemKeys(); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, key := range ddbTableSortedKeys("legacy/") {
		item, _ := ddbItems.Get(key)
		order = append(order, item["n"].(map[string]any)["N"].(string))
	}
	if strings.Join(order, ",") != "9,10" {
		t.Fatalf("after the migration the partition reads %v, want 9,10", order)
	}
	if len(ddbTableSortedKeys("gone/")) != 0 {
		t.Fatal("an item whose table is gone survived the migration")
	}
	ddbItemNames.Put("legacy/S#p|N#1", "legacy/S#p|N#1")
	if err := ddbMigrateItemKeys(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ddbItemNames.Get("legacy/S#p|N#1"); !ok {
		t.Fatal("the migration ran a second time")
	}
}
