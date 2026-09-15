package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func ddbTTLCall(t *testing.T, handler http.HandlerFunc, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func epochN(at time.Time) map[string]any {
	return map[string]any{"N": strconv.FormatInt(at.Unix(), 10)}
}

func TestDynamoDBTTLSweepDeletesOnlyEligibleExpiredItems(t *testing.T) {
	buildConformanceSimulator(t)
	const table = "ttl-sweep"
	ddbTTLCall(t, handleDDBCreateTable, map[string]any{
		"TableName":            table,
		"BillingMode":          "PAY_PER_REQUEST",
		"AttributeDefinitions": []map[string]any{{"AttributeName": "PK", "AttributeType": "S"}},
		"KeySchema":            []map[string]any{{"AttributeName": "PK", "KeyType": "HASH"}},
	})
	now := time.Now()
	put := func(id string, expiry any) {
		item := map[string]any{"PK": map[string]any{"S": id}}
		if expiry != nil {
			item["expiresAt"] = expiry
		}
		ddbTTLCall(t, handleDDBPutItem, map[string]any{"TableName": table, "Item": item})
	}
	put("expired", epochN(now.Add(-time.Minute)))
	put("expiring-now", epochN(now))
	put("future", epochN(now.Add(time.Hour)))
	put("older-than-five-years", epochN(now.AddDate(-5, 0, -1)))
	put("string-typed", map[string]any{"S": strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)})
	put("no-attribute", nil)

	require.Zero(t, ddbSweepExpiredItems(now), "TTL disabled: nothing is eligible")

	ddbTTLCall(t, handleDDBUpdateTimeToLive, map[string]any{
		"TableName":               table,
		"TimeToLiveSpecification": map[string]any{"Enabled": true, "AttributeName": "expiresAt"},
	})
	require.Equal(t, 2, ddbSweepExpiredItems(now))

	for id, kept := range map[string]bool{
		"expired": false, "expiring-now": false, "future": true,
		"older-than-five-years": true, "string-typed": true, "no-attribute": true,
	} {
		got := ddbTTLCall(t, handleDDBGetItem, map[string]any{
			"TableName": table, "Key": map[string]any{"PK": map[string]any{"S": id}},
		})
		_, present := got["Item"]
		require.Equal(t, kept, present, id)
	}
	require.Zero(t, ddbSweepExpiredItems(now), "a second sweep finds nothing left to delete")
}

func TestDynamoDBDescribeTableReportsItemAndIndexUsage(t *testing.T) {
	buildConformanceSimulator(t)
	const table = "usage-counts"
	ddbTTLCall(t, handleDDBCreateTable, map[string]any{
		"TableName":   table,
		"BillingMode": "PAY_PER_REQUEST",
		"AttributeDefinitions": []map[string]any{
			{"AttributeName": "PK", "AttributeType": "S"},
			{"AttributeName": "G", "AttributeType": "S"},
		},
		"KeySchema": []map[string]any{{"AttributeName": "PK", "KeyType": "HASH"}},
		"GlobalSecondaryIndexes": []map[string]any{{
			"IndexName":  "byG",
			"KeySchema":  []map[string]any{{"AttributeName": "G", "KeyType": "HASH"}},
			"Projection": map[string]any{"ProjectionType": "KEYS_ONLY"},
		}},
	})
	for _, item := range []map[string]any{
		{"PK": map[string]any{"S": "a"}, "G": map[string]any{"S": "x"}, "Pad": map[string]any{"S": "0123456789"}},
		{"PK": map[string]any{"S": "b"}, "G": map[string]any{"S": "x"}},
		{"PK": map[string]any{"S": "c"}},
	} {
		ddbTTLCall(t, handleDDBPutItem, map[string]any{"TableName": table, "Item": item})
	}

	// The request path never reads the items: the first describe reports zero,
	// as a new DynamoDB table does, and starts one background refresh.
	AwaitSimulatorBackground()
	// Read before the describe: its refresh can finish before the next line runs.
	before := ddbUsageRefreshes.Load()
	first := ddbTTLCall(t, handleDDBDescribeTable, map[string]any{"TableName": table})["Table"].(map[string]any)
	require.EqualValues(t, 0, first["ItemCount"])
	AwaitSimulatorBackground()
	require.Equal(t, before+1, ddbUsageRefreshes.Load(), "one describe starts exactly one refresh")

	described := ddbTTLCall(t, handleDDBDescribeTable, map[string]any{"TableName": table})["Table"].(map[string]any)
	require.EqualValues(t, 3, described["ItemCount"])
	// PK+G+Pad (2+1 + 1+1 + 3+10) + PK+G (2+1 + 1+1) + PK (2+1).
	require.EqualValues(t, 18+5+3, described["TableSizeBytes"])
	index := described["GlobalSecondaryIndexes"].([]any)[0].(map[string]any)
	require.EqualValues(t, 2, index["ItemCount"], "the item without G is not in the index")
	require.EqualValues(t, 5+5, index["IndexSizeBytes"], "KEYS_ONLY projects PK and G, not Pad")
	AwaitSimulatorBackground()
	require.Equal(t, before+1, ddbUsageRefreshes.Load(), "a describe within the interval serves the cached figures")

	stored, ok := ddbTables.Get(table)
	require.True(t, ok)
	require.Zero(t, stored.ItemCount, "the figures are computed for the response, never written back")
}
