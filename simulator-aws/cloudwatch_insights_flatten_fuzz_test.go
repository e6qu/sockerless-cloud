package main

import "testing"

// FuzzCWFlattenJSONInto feeds arbitrary messages (planted via PutLogEvents and
// parsed at StartQuery) through the Insights JSON flattener; deeply-nested or
// malformed input must not blow the stack.
func FuzzCWFlattenJSONInto(f *testing.F) {
	f.Add(`{"a":1,"b":{"c":"x"}}`)
	f.Add(`not json`)
	f.Add(`{"a":[1,2,3]}`)
	f.Add(`{"a":{"a":{"a":{"a":{}}}}}`)
	f.Fuzz(func(t *testing.T, message string) {
		cwFlattenJSONInto(cwInsightsRecord{}, message) // must not panic / overflow
	})
}
