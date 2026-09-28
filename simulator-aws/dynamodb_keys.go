package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// An item's store key is "<table>/<hash>" or "<table>/<hash>\x01<range>", each
// component encoded so that the keys of one partition sort in DynamoDB's
// sort-key order: numbers by value, binary by bytes, strings by their UTF-8
// bytes. A query reads a partition as a key range and returns what it reads,
// so the order of the keys is the order of the answer.
//
// The separator is below every byte an encoded component holds, so a
// component cannot contain it and a partition's keys sort ahead of any longer
// hash its own hash is a prefix of.
const ddbKeySeparator = "\x01"

func ddbItemKey(table DDBTable, item map[string]any) string {
	var hash, rng string
	for _, k := range table.KeySchema {
		value := ddbExtractAttrValue(item[k.AttributeName])
		switch k.KeyType {
		case "HASH":
			hash = value
		case "RANGE":
			rng = value
		}
	}
	if rng != "" {
		return table.TableName + "/" + hash + ddbKeySeparator + rng
	}
	return table.TableName + "/" + hash
}

// ddbExtractAttrValue encodes a key attribute value ({"S"|"N"|"B": ...}) as a
// key component, prefixed with its type so a string never equals a number.
// Numbers that compare equal ("1", "1.0", "01") encode the same.
func ddbExtractAttrValue(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := m["S"].(string); ok {
		return "S" + ddbEscapeKeyText(s)
	}
	if n, ok := m["N"].(string); ok {
		if encoded, ok := ddbOrderedNumber(n); ok {
			return "N" + encoded
		}
		return ""
	}
	if b, ok := m["B"].(string); ok {
		raw, err := base64.StdEncoding.DecodeString(b)
		if err != nil {
			return ""
		}
		return "B" + hex.EncodeToString(raw)
	}
	return ""
}

// ddbEscapeKeyText moves the three bytes at and below the escape out of the
// way, keeping their order: 0x00, 0x01 and 0x02 become 0x02 followed by 0x03,
// 0x04 and 0x05, which still sort before every byte from 0x03 up.
func ddbEscapeKeyText(s string) string {
	if !strings.ContainsAny(s, "\x00\x01\x02") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x02 {
			b.WriteByte(0x02)
			b.WriteByte(c + 0x03)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ddbOrderedNumber encodes a DynamoDB number so that encodings compare as the
// numbers do. The value is 0.d1d2... x 10^e with d1 non-zero; a positive number
// is "2", e offset into three digits, then the digits; a negative one is "0",
// the offset exponent's complement, the digits' nine's complements and a "~"
// that sorts after every digit; zero is "1". DynamoDB's exponents run from
// -130 to 126, well inside the offset's range.
func ddbOrderedNumber(s string) (string, bool) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok {
		return "", false
	}
	if r.Sign() == 0 {
		return "1", true
	}
	negative := r.Sign() < 0
	abs := new(big.Rat).Abs(r)
	scaled := new(big.Int).Set(abs.Num())
	denominator := abs.Denom()
	ten := big.NewInt(10)
	shift := 0
	for new(big.Int).Rem(scaled, denominator).Sign() != 0 {
		scaled.Mul(scaled, ten)
		shift++
		if shift > 400 {
			return "", false
		}
	}
	digits := new(big.Int).Quo(scaled, denominator).String()
	exponent := len(digits) - shift + 500
	digits = strings.TrimRight(digits, "0")
	if exponent < 0 || exponent > 999 {
		return "", false
	}
	if !negative {
		return fmt.Sprintf("2%03d%s", exponent, digits), true
	}
	complement := []byte(digits)
	for i, d := range complement {
		complement[i] = '9' - (d - '0')
	}
	return fmt.Sprintf("0%03d%s~", 999-exponent, complement), true
}

// ddbKeyFormat records which key encoding the stored items are under.
var ddbKeyFormat sim.Store[string]

const ddbCurrentKeyFormat = "ordered-v2"

// ddbMigrateItemKeys re-keys items a build before the ordered encoding stored,
// once: each item's key is recomputed from its table's schema. An item whose
// table no longer exists cannot be addressed by any request, so it is removed
// and named in the log.
func ddbMigrateItemKeys() error {
	if format, ok := ddbKeyFormat.Get("items"); ok && format == ddbCurrentKeyFormat {
		return nil
	}
	moved, orphaned := 0, 0
	for _, row := range ddbItemNames.ListPrefix("") {
		oldKey := row.ID
		item, ok := ddbItems.Get(oldKey)
		if !ok {
			ddbItemNames.Delete(oldKey)
			continue
		}
		tableName, _, found := strings.Cut(oldKey, "/")
		if !found {
			return fmt.Errorf("stored item key %q names no table", oldKey)
		}
		table, ok := ddbTables.Get(tableName)
		if !ok {
			log.Printf("dynamodb: removing item %q, whose table no longer exists", oldKey)
			ddbItems.Delete(oldKey)
			ddbItemNames.Delete(oldKey)
			orphaned++
			continue
		}
		newKey := ddbItemKey(table, item)
		if newKey == oldKey {
			continue
		}
		ddbItems.Put(newKey, item)
		ddbItemNames.Put(newKey, newKey)
		ddbItems.Delete(oldKey)
		ddbItemNames.Delete(oldKey)
		moved++
	}
	if moved > 0 || orphaned > 0 {
		log.Printf("dynamodb: re-keyed %d items to the ordered key encoding; removed %d without a table", moved, orphaned)
	}
	ddbKeyFormat.Put("items", ddbCurrentKeyFormat)
	return nil
}
