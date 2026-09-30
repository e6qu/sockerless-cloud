package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// protoJSONTimestamp renders a time exactly as protojson renders the
// google.protobuf.Timestamp holding it, at every fraction width.
func TestProtoJSONTimestampMatchesProtojson(t *testing.T) {
	base := time.Date(2026, 9, 30, 12, 34, 56, 0, time.FixedZone("x", 2*3600))
	for _, nanos := range []int{0, 120_000_000, 123_456_000, 123_456_789, 1} {
		at := base.Add(time.Duration(nanos))
		want, err := protojson.Marshal(timestamppb.New(at))
		if err != nil {
			t.Fatal(err)
		}
		if got := `"` + protoJSONTimestamp(at) + `"`; got != string(want) {
			t.Fatalf("nanos %d: got %s, want %s", nanos, got, want)
		}
	}
}
