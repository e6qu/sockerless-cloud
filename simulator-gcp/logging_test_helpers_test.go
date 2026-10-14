package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// useLoggingMemoryStores points Cloud Logging's entry, bucket, sink and
// exclusion stores at fresh in-memory stores for the test.
func useLoggingMemoryStores(t *testing.T) {
	t.Helper()
	entries, buckets, sinks, exclusions := logEntries, logBuckets, logSinks, logExclusions
	t.Cleanup(func() { logEntries, logBuckets, logSinks, logExclusions = entries, buckets, sinks, exclusions })
	logEntries = loggingNewEntries(nil)
	logBuckets = sim.MakeStore[LogBucket](nil, "logging_buckets")
	logSinks = sim.MakeStore[LoggingSink](nil, "logging_sinks")
	logExclusions = sim.MakeStore[LogExclusion](nil, "logging_exclusions")
}
