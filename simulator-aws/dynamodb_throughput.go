package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

// Amazon DynamoDB refuses a request that would spend more than a PROVISIONED
// table has accrued with ProvisionedThroughputExceededException, which every
// SDK retries with backoff.
//
// Each table, and each global secondary index, has a read and a write token
// bucket refilled at the provisioned rate, holding up to 300 seconds of unused
// capacity as DynamoDB documents. A request spends the units its
// ConsumedCapacity reports. PAY_PER_REQUEST tables are never throttled, and a
// local secondary index spends the table's capacity.
//
// A table stored with 0/0 throughput, which the service never allows, is not
// throttled: such rows predate recording the requested throughput.

const ddbBurstSeconds = 300.0

var (
	ddbBuckets kvstore.Buckets
	// Tests substitute the clock.
	ddbNow = time.Now
)

func ddbBucketKey(table, index, kind string) string {
	return table + "|" + index + "|" + kind
}

// index "" means the table; provisioned is false when throughput does not
// apply.
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

// false means the request is throttled and nothing was spent.
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
	return ddbBuckets.Take(ddbBucketKey(t.TableName, index, kind), kvstore.Limit{Rate: rate, Burst: rate * ddbBurstSeconds}, units, ddbNow())
}

// Charge the table and every global secondary index the item lands in; return
// the refusing bucket ("" for the table).
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

// An item lands in a global secondary index only when it carries the index's
// partition key.
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

// The next request starts from a full bucket at the new rate.
func ddbForgetBuckets(table string) {
	ddbBuckets.Forget(table + "|")
}

// index names the refusing global secondary index, or "" for the table.
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

// A local secondary index spends the table's bucket.
func ddbReadIndex(t DDBTable, index string) string {
	if ddbIsGSI(t, index) {
		return index
	}
	return ""
}
