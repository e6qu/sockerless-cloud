package main

import "testing"

// Deeply nested or malformed messages must not overflow the stack.
func FuzzCWFlattenJSONInto(f *testing.F) {
	f.Add(`{"a":1,"b":{"c":"x"}}`)
	f.Add(`not json`)
	f.Add(`{"a":[1,2,3]}`)
	f.Add(`{"a":{"a":{"a":{"a":{}}}}}`)
	f.Fuzz(func(t *testing.T, message string) {
		cwFlattenJSONInto(cwInsightsRecord{}, message)
	})
}
