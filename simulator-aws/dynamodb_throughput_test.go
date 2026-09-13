package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A fresh clock and empty buckets per test: the buckets are process state
// shared across the simulators a test builds.
func throughputClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	previous := ddbNow
	ddbNow = func() time.Time { return now }
	ddbBucketMu.Lock()
	ddbBuckets = map[string]*ddbBucket{}
	ddbBucketMu.Unlock()
	t.Cleanup(func() {
		ddbNow = previous
		ddbBucketMu.Lock()
		ddbBuckets = map[string]*ddbBucket{}
		ddbBucketMu.Unlock()
	})
	return &now
}

const throughputTable = `{
	"TableName":"%s","BillingMode":"%s",%s
	"KeySchema":[{"AttributeName":"pk","KeyType":"HASH"}],
	"AttributeDefinitions":[{"AttributeName":"pk","AttributeType":"S"},{"AttributeName":"gk","AttributeType":"S"}]%s}`

func putUntilThrottled(t *testing.T, router interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, table string, max int) int {
	t.Helper()
	for i := 0; i < max; i++ {
		code, out := ddbVectorCall(t, router, "PutItem", fmt.Sprintf(`{"TableName":%q,"Item":{"pk":{"S":"k%d"},"gk":{"S":"g"}}}`, table, i))
		if code != http.StatusOK {
			require.Equal(t, http.StatusBadRequest, code)
			require.Equal(t, "ProvisionedThroughputExceededException", out["__type"], "%v", out)
			return i
		}
	}
	return max
}

func TestDDBProvisionedTableThrottlesPastItsBurstAndRefillsAtItsRate(t *testing.T) {
	now := throughputClock(t)
	_, router, _ := buildConformanceSimulator(t)
	code, out := ddbVectorCall(t, router, "CreateTable", fmt.Sprintf(throughputTable, "one", "PROVISIONED",
		`"ProvisionedThroughput":{"ReadCapacityUnits":1,"WriteCapacityUnits":1},`, ""))
	require.Equal(t, http.StatusOK, code, "%v", out)

	// DynamoDB retains 300 seconds of unused capacity: one WCU/s means 300
	// one-KB writes land at once, and the 301st is refused.
	require.Equal(t, 300, putUntilThrottled(t, router, "one", 400))

	// Nothing was spent by the refused write; a second later there is one unit.
	*now = now.Add(time.Second)
	require.Equal(t, 1, putUntilThrottled(t, router, "one", 5))

	// Reads have their own bucket: the writes did not touch it.
	code, out = ddbVectorCall(t, router, "GetItem", `{"TableName":"one","Key":{"pk":{"S":"k1"}},"ConsistentRead":true}`)
	require.Equal(t, http.StatusOK, code, "%v", out)

	// Raising the provisioning starts a fresh bucket at the new rate.
	code, out = ddbVectorCall(t, router, "UpdateTable", `{"TableName":"one","ProvisionedThroughput":{"ReadCapacityUnits":10,"WriteCapacityUnits":10}}`)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, 400, putUntilThrottled(t, router, "one", 400), "3000 units of burst cover 400 writes")
}

func TestDDBOnDemandTableIsNeverThrottled(t *testing.T) {
	throughputClock(t)
	_, router, _ := buildConformanceSimulator(t)
	code, out := ddbVectorCall(t, router, "CreateTable", fmt.Sprintf(throughputTable, "ondemand", "PAY_PER_REQUEST", "", ""))
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, 500, putUntilThrottled(t, router, "ondemand", 500))
}

func TestDDBCreateTableValidatesThroughputAgainstBillingMode(t *testing.T) {
	throughputClock(t)
	_, router, _ := buildConformanceSimulator(t)

	code, out := ddbVectorCall(t, router, "CreateTable", fmt.Sprintf(throughputTable, "bare", "PROVISIONED", "", ""))
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "ValidationException", out["__type"])
	require.Contains(t, out["message"], "must both be specified when BillingMode is PROVISIONED")

	code, out = ddbVectorCall(t, router, "CreateTable", fmt.Sprintf(throughputTable, "mixed", "PAY_PER_REQUEST",
		`"ProvisionedThroughput":{"ReadCapacityUnits":5,"WriteCapacityUnits":5},`, ""))
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, out["message"], "can be specified when BillingMode is PAY_PER_REQUEST")

	// A provisioned table describes the units it was created with, not 0/0.
	code, out = ddbVectorCall(t, router, "CreateTable", fmt.Sprintf(throughputTable, "sized", "PROVISIONED",
		`"ProvisionedThroughput":{"ReadCapacityUnits":7,"WriteCapacityUnits":3},`, ""))
	require.Equal(t, http.StatusOK, code, "%v", out)
	code, out = ddbVectorCall(t, router, "DescribeTable", `{"TableName":"sized"}`)
	require.Equal(t, http.StatusOK, code)
	table, _ := out["Table"].(map[string]any)
	pt, _ := table["ProvisionedThroughput"].(map[string]any)
	require.Equal(t, float64(7), pt["ReadCapacityUnits"])
	require.Equal(t, float64(3), pt["WriteCapacityUnits"])
}

func TestDDBGlobalSecondaryIndexSpendsItsOwnThroughput(t *testing.T) {
	throughputClock(t)
	_, router, _ := buildConformanceSimulator(t)
	gsi := `,"GlobalSecondaryIndexes":[{"IndexName":"by-gk","KeySchema":[{"AttributeName":"gk","KeyType":"HASH"}],
		"Projection":{"ProjectionType":"ALL"},"ProvisionedThroughput":{"ReadCapacityUnits":1,"WriteCapacityUnits":1}}]`
	code, out := ddbVectorCall(t, router, "CreateTable", fmt.Sprintf(throughputTable, "indexed", "PROVISIONED",
		`"ProvisionedThroughput":{"ReadCapacityUnits":100,"WriteCapacityUnits":100},`, gsi))
	require.Equal(t, http.StatusOK, code, "%v", out)

	// The table could take 30,000 writes; the index takes 300, and it is the
	// index the refusal names.
	require.Equal(t, 300, putUntilThrottled(t, router, "indexed", 400))
	code, out = ddbVectorCall(t, router, "PutItem", `{"TableName":"indexed","Item":{"pk":{"S":"x"},"gk":{"S":"g"}}}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, out["message"], "global secondary indexes")

	// An item without the index's key never lands in the index, so it spends
	// nothing there.
	code, out = ddbVectorCall(t, router, "PutItem", `{"TableName":"indexed","Item":{"pk":{"S":"plain"}}}`)
	require.Equal(t, http.StatusOK, code, "%v", out)
}

func TestDDBBatchWriteReturnsThrottledEntriesAsUnprocessed(t *testing.T) {
	throughputClock(t)
	_, router, _ := buildConformanceSimulator(t)
	code, out := ddbVectorCall(t, router, "CreateTable", fmt.Sprintf(throughputTable, "batch", "PROVISIONED",
		`"ProvisionedThroughput":{"ReadCapacityUnits":1,"WriteCapacityUnits":1},`, ""))
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, 298, putUntilThrottled(t, router, "batch", 298), "leave two units in the bucket")

	batch := func(n int) (int, map[string]any) {
		items := ""
		for i := 0; i < n; i++ {
			if i > 0 {
				items += ","
			}
			items += fmt.Sprintf(`{"PutRequest":{"Item":{"pk":{"S":"b%d"}}}}`, i)
		}
		return ddbVectorCall(t, router, "BatchWriteItem", fmt.Sprintf(`{"RequestItems":{"batch":[%s]}}`, items))
	}
	// Two of three land; the third comes back to be retried, as on the service.
	code, out = batch(3)
	require.Equal(t, http.StatusOK, code, "%v", out)
	raw, _ := json.Marshal(out["UnprocessedItems"])
	var unprocessed map[string][]map[string]any
	require.NoError(t, json.Unmarshal(raw, &unprocessed))
	require.Len(t, unprocessed["batch"], 1)

	// Nothing left: a batch in which nothing could be processed is refused.
	code, out = batch(2)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "ProvisionedThroughputExceededException", out["__type"])
}
