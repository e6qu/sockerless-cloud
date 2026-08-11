package main

import (
	"encoding/json"
	"testing"
)

// FuzzDDBItemMarshalRoundtrip feeds arbitrary JSON (decoded into an item map)
// through the clone / depth-check / marshal paths the handlers run on stored
// items — none may panic or blow the stack on a malformed or deeply-nested
// AttributeValue tree.
func FuzzDDBItemMarshalRoundtrip(f *testing.F) {
	f.Add(`{"pk":{"S":"x"},"n":{"N":"1"}}`)
	f.Add(`{"m":{"M":{"a":{"L":[{"M":{"b":{"S":"y"}}}]}}}}`)
	f.Add(`{"bad":{"N":[1,2]}}`)
	f.Add(`{"x":{"M":{"x":{"M":{"x":{"M":{}}}}}}}`)
	f.Fuzz(func(t *testing.T, body string) {
		var item map[string]any
		if json.Unmarshal([]byte(body), &item) != nil || item == nil {
			return
		}
		_ = ddbItemTooDeep(item)
		clone := ddbCloneItem(item)
		_ = ddbAttrEqual(item["pk"], clone["pk"])
		_, _ = json.Marshal(clone)
	})
}

// FuzzDDBProjectItem fuzzes both the ProjectionExpression and the item through
// the projector's path-splitting.
func FuzzDDBProjectItem(f *testing.F) {
	f.Add("a, b.c, d[0]", `{"a":{"S":"1"},"b":{"M":{"c":{"S":"2"}}}}`)
	f.Add("[", `{"a":{"S":"1"}}`)
	f.Add("a.b.c.d.e", `{}`)
	f.Fuzz(func(t *testing.T, projection, body string) {
		var item map[string]any
		if json.Unmarshal([]byte(body), &item) != nil || item == nil {
			return
		}
		_ = ddbProjectItem(item, projection, nil) // must not panic
	})
}
