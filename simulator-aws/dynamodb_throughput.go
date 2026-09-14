package main

import (
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Provisioned throughput (BUG-2995). A PROVISIONED table promises a number of
// read and write capacity units per second, and Amazon DynamoDB enforces the
// promise: a request that would spend more than the table has accrued is
// refused with ProvisionedThroughputExceededException, and every SDK retries
// exactly that error with backoff. The simulator stored the numbers and never
// enforced them, so a consumer's retry path was dead code here and only came
// alive in production, under load.
//
// The model is DynamoDB's own, at the granularity a single-partition table
// has: one token bucket per table for reads and one for writes, and one pair
// per global secondary index (a GSI has its own provisioned throughput),
// refilled at the provisioned rate and holding up to 300 seconds of unused
// capacity — the burst DynamoDB documents it retains. A request spends the
// units the existing capacity accounting already computes for its
// ConsumedCapacity (1 WCU per KB written, 1 RCU per 4 KB read strongly
// consistent, half of that eventually consistent, twice for transactions).
// PAY_PER_REQUEST tables are never throttled here, as on the service, and a
// local secondary index spends the base table's capacity, as on the service.
//
// A table stored before CreateTable recorded the request's throughput carries
// 0/0, which the service never allows; such a table is not throttled, because
// a bucket refilled at zero would refuse every request a deployed simulator's
// existing tables receive after an upgrade.

// ddbBurstSeconds is how much unused capacity a bucket retains.
const ddbBurstSeconds = 300.0

type ddbBucket struct {
	tokens float64
	last   time.Time
}

var (
	ddbBucketMu sync.Mutex
	ddbBuckets  = map[string]*ddbBucket{}
	// ddbNow is the clock the buckets refill by; tests substitute it.
	ddbNow = time.Now
)

func ddbBucketKey(table, index, kind string) string {
	return table + "|" + index + "|" + kind
}

// ddbProvisionedRate is the read and write units per second the table
// (index == "") or the named global secondary index is provisioned for, and
// whether provisioned throughput applies at all.
func ddbProvisionedRate(t DDBTable, index string) (read, write float64, provisioned bool) {
	if t.BillingModeSummary != nil && strings.EqualFold(t.BillingModeSummary.BillingMode, "PAY_PER_REQUEST") {
		return 0, 0, false
	}
	pt := t.ProvisionedThroughput
	if index != "" {
		pt = nil
		for _, g := range t.GlobalSecondaryIndexes {
			if g.IndexName == index {
				pt = g.ProvisionedThroughput
			}
		}
	}
	if pt == nil || pt.ReadCapacityUnits <= 0 || pt.WriteCapacityUnits <= 0 {
		return 0, 0, false
	}
	return float64(pt.ReadCapacityUnits), float64(pt.WriteCapacityUnits), true
}

// ddbTake spends `units` of read or write capacity from the table's (or the
// index's) bucket. False means the bucket could not cover them: the request
// is throttled and nothing was spent.
func ddbTake(t DDBTable, index string, units float64, write bool) bool {
	if units <= 0 {
		return true
	}
	read, wr, provisioned := ddbProvisionedRate(t, index)
	if !provisioned {
		return true
	}
	rate, kind := read, "read"
	if write {
		rate, kind = wr, "write"
	}
	now := ddbNow()
	capacity := rate * ddbBurstSeconds
	ddbBucketMu.Lock()
	defer ddbBucketMu.Unlock()
	key := ddbBucketKey(t.TableName, index, kind)
	b, ok := ddbBuckets[key]
	if !ok {
		b = &ddbBucket{tokens: capacity, last: now}
		ddbBuckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(capacity, b.tokens+rate*elapsed)
		b.last = now
	}
	if b.tokens < units {
		return false
	}
	b.tokens -= units
	return true
}

// ddbTakeWrite spends a write against the table and against every global
// secondary index the item lands in (one whose partition key the item
// carries), as the service charges. It returns the name of the bucket that
// refused ("" for the table) and whether the write may proceed.
func ddbTakeWrite(t DDBTable, item map[string]any, units float64) (string, bool) {
	if !ddbTake(t, "", units, true) {
		return "", false
	}
	for _, g := range t.GlobalSecondaryIndexes {
		if !ddbItemInIndex(g, item) {
			continue
		}
		if !ddbTake(t, g.IndexName, units, true) {
			return g.IndexName, false
		}
	}
	return "", true
}

// ddbItemInIndex: an item is written to a global secondary index only when it
// carries the index's partition key.
func ddbItemInIndex(g DDBGlobalSecondaryIndex, item map[string]any) bool {
	if item == nil {
		return false
	}
	for _, k := range g.KeySchema {
		if k.KeyType == "HASH" {
			_, ok := item[k.AttributeName]
			return ok
		}
	}
	return false
}

// ddbForgetBuckets drops a table's buckets — its provisioning changed, or it
// is gone — so the next request starts from a full bucket at the new rate.
func ddbForgetBuckets(table string) {
	ddbBucketMu.Lock()
	defer ddbBucketMu.Unlock()
	prefix := table + "|"
	for key := range ddbBuckets {
		if strings.HasPrefix(key, prefix) {
			delete(ddbBuckets, key)
		}
	}
}

// ddbWriteThrottled answers a throttled request with the service's error and
// message; `index` names the global secondary index that refused, or "" for
// the table.
func ddbWriteThrottled(w http.ResponseWriter, index string) {
	if index == "" {
		AWSError(w, "ProvisionedThroughputExceededException",
			"The level of configured provisioned throughput for the table was exceeded. Consider increasing your provisioning level with the UpdateTable API.",
			http.StatusBadRequest)
		return
	}
	AWSError(w, "ProvisionedThroughputExceededException",
		"The level of configured provisioned throughput for one or more global secondary indexes of the table was exceeded. Consider increasing your provisioning level for the affected index(es) with the UpdateTable API.",
		http.StatusBadRequest)
}

// ddbReadIndex is the bucket a read against `index` spends from: a global
// secondary index has its own throughput; a local secondary index and the
// table itself spend the table's.
func ddbReadIndex(t DDBTable, index string) string {
	if ddbIsGSI(t, index) {
		return index
	}
	return ""
}
