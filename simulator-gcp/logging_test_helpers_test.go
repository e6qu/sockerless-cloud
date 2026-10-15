package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// useLoggingMemoryStores points Cloud Logging's entry, bucket and sink stores
// at fresh in-memory stores for the test.
func useLoggingMemoryStores(t *testing.T) {
	t.Helper()
	entries, buckets, sinks := logEntries, logBuckets, logSinks
	t.Cleanup(func() { logEntries, logBuckets, logSinks = entries, buckets, sinks })
	logEntries = loggingNewEntries(nil)
	logBuckets = sim.MakeStore[LogBucket](nil, "logging_buckets")
	logSinks = sim.MakeStore[LoggingSink](nil, "logging_sinks")
}
