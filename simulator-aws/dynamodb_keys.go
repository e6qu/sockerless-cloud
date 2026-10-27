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
// component encoded so a partition's keys sort in DynamoDB's sort-key order:
// numbers by value, binary by bytes, strings by UTF-8 bytes. A query returns
// a partition's key range in key order.
//
// The separator sorts below every byte an encoded component holds, so a
// partition's keys sort ahead of any longer hash its hash prefixes.
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

// Prefix the type so a string never equals a number; equal numbers ("1",
// "1.0", "01") encode the same.
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

// 0x00, 0x01 and 0x02 become 0x02 followed by 0x03, 0x04 and 0x05, keeping
// their order and staying below every byte from 0x03 up.
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

// The value is 0.d1d2… × 10^e with d1 non-zero. A positive number is "2", e
// offset into three digits, then the digits; a negative one is "0", the offset
// exponent's complement, the digits' nine's complements and a "~" sorting
// after every digit; zero is "1". DynamoDB exponents run -130 to 126.
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

var ddbKeyFormat sim.Store[string]

const ddbCurrentKeyFormat = "ordered-v2"

// Re-key items stored before the ordered encoding, once. An item whose table
// is gone cannot be addressed, so remove it and log it.
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

// These messages are DynamoDB Local's, captured from
// mirror.gcr.io/amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab.

// Checks an item written whole (PutItem, batch and transaction forms); ""
// means valid.
func ddbItemKeyError(t DDBTable, item map[string]any) string {
	if message := ddbNumbersError(item); message != "" {
		return message
	}
	for _, k := range t.KeySchema {
		value, ok := item[k.AttributeName]
		if !ok {
			return "One of the required keys was not given a value"
		}
		if message := ddbKeyValueError(t, k.AttributeName, value); message != "" {
			return message
		}
	}
	return ddbIndexKeyError(t, item)
}

// An index key the item carries must have the declared type and be non-empty;
// an item without one is not in that index.
func ddbIndexKeyError(t DDBTable, item map[string]any) string {
	for _, index := range ddbIndexes(t) {
		for _, k := range index.keySchema {
			value, ok := item[k.AttributeName]
			if !ok {
				continue
			}
			declared := ""
			for _, def := range t.AttributeDefinitions {
				if def.AttributeName == k.AttributeName {
					declared = def.AttributeType
				}
			}
			typed, _ := value.(map[string]any)
			raw, matches := typed[declared]
			if !matches {
				return "One or more parameter values were invalid: Type mismatch for Index Key"
			}
			if text, isText := raw.(string); isText && text == "" {
				return "One or more parameter values are not valid. A value specified for a secondary index key is not supported. " +
					"The AttributeValue for a key attribute cannot contain an empty string value. IndexName: " + index.name + ", IndexKey: " + k.AttributeName
			}
		}
	}
	return ""
}

// The Key parameter must hold exactly the table's key attributes, each of its
// declared type.
func ddbKeyParamError(t DDBTable, key map[string]any) string {
	if len(key) != len(t.KeySchema) {
		return "The number of conditions on the keys is invalid"
	}
	for _, k := range t.KeySchema {
		value, ok := key[k.AttributeName]
		if !ok {
			return "The number of conditions on the keys is invalid"
		}
		if message := ddbKeyValueError(t, k.AttributeName, value); message != "" {
			return message
		}
	}
	return ddbNumbersError(key)
}

func ddbKeyValueError(t DDBTable, name string, value any) string {
	declared := ""
	for _, def := range t.AttributeDefinitions {
		if def.AttributeName == name {
			declared = def.AttributeType
		}
	}
	typed, ok := value.(map[string]any)
	if !ok || len(typed) != 1 {
		return "One or more parameter values were invalid: Type mismatch for key"
	}
	raw, ok := typed[declared]
	if !ok {
		return "One or more parameter values were invalid: Type mismatch for key"
	}
	if text, ok := raw.(string); ok && text == "" && (declared == "S" || declared == "B") {
		return "One or more parameter values are not valid. The AttributeValue for a key attribute cannot contain an empty string value. Key: " + name
	}
	return ""
}

// Checks N, NS, and numbers nested in L or M.
func ddbNumbersError(attributes map[string]any) string {
	for _, value := range attributes {
		if ddbValueHasBadNumber(value) {
			return "A value provided cannot be converted into a number"
		}
	}
	return ""
}

func ddbValueHasBadNumber(value any) bool {
	typed, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if n, ok := typed["N"].(string); ok {
		_, valid := ddbOrderedNumber(n)
		return !valid
	}
	if set, ok := typed["NS"].([]any); ok {
		for _, member := range set {
			n, isText := member.(string)
			if _, valid := ddbOrderedNumber(n); !isText || !valid {
				return true
			}
		}
	}
	if list, ok := typed["L"].([]any); ok {
		for _, member := range list {
			if ddbValueHasBadNumber(member) {
				return true
			}
		}
	}
	if fields, ok := typed["M"].(map[string]any); ok {
		for _, field := range fields {
			if ddbValueHasBadNumber(field) {
				return true
			}
		}
	}
	return false
}
