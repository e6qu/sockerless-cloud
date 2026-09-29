package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Table storage names the first entity of the next page by its
// PartitionKey and RowKey in the x-ms-continuation-* headers, and the SDK
// hands those keys back as NextPartitionKey / NextRowKey. A key cursor stays
// on the right entity when rows before it are deleted between pages.
func TestTableQueryContinuationNamesNextEntity(t *testing.T) {
	savedData, savedEntities := tableData, tableEntities
	t.Cleanup(func() { tableData, tableEntities = savedData, savedEntities })
	tableData = sim.MakeStore[TableData](nil, "test_table_data")
	tableEntities = sim.MakeStore[TableEntity](nil, "test_table_entities")

	tableData.Put(tableKey("acct", "t"), TableData{Account: "acct", Name: "t"})
	for _, k := range [][2]string{{"a", "1"}, {"a", "2"}, {"b", "1"}, {"b", "2"}, {"c", "1"}} {
		tableEntities.Put(tableEntityKey("acct", "t", k[0], k[1]), TableEntity{
			Account: "acct", Table: "t", PartitionKey: k[0], RowKey: k[1],
			Properties: map[string]json.RawMessage{"PartitionKey": json.RawMessage(`"` + k[0] + `"`), "RowKey": json.RawMessage(`"` + k[1] + `"`)},
		})
	}

	query := func(params url.Values) (rows []string, nextPK, nextRK string) {
		rec := httptest.NewRecorder()
		handleEntityQuery(rec, httptest.NewRequest("GET", "/t()?"+params.Encode(), nil), "acct", "t")
		var body struct {
			Value []map[string]any `json:"value"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %v %s", params, err, rec.Body.String())
		}
		for _, v := range body.Value {
			pk, _ := v["PartitionKey"].(string)
			rk, _ := v["RowKey"].(string)
			rows = append(rows, pk+rk)
		}
		return rows, rec.Header().Get("x-ms-continuation-NextPartitionKey"), rec.Header().Get("x-ms-continuation-NextRowKey")
	}

	rows, pk, rk := query(url.Values{"$top": {"2"}})
	if len(rows) != 2 || rows[1] != "a2" || pk != "b" || rk != "1" {
		t.Fatalf("first page %v, continuation %q/%q", rows, pk, rk)
	}
	tableEntities.Delete(tableEntityKey("acct", "t", "a", "1"))
	rows, pk, rk = query(url.Values{"$top": {"2"}, "NextPartitionKey": {pk}, "NextRowKey": {rk}})
	if len(rows) != 2 || rows[0] != "b1" || rows[1] != "b2" || pk != "c" || rk != "1" {
		t.Fatalf("second page %v, continuation %q/%q", rows, pk, rk)
	}
	rows, pk, rk = query(url.Values{"$top": {"2"}, "NextPartitionKey": {pk}, "NextRowKey": {rk}})
	if len(rows) != 1 || rows[0] != "c1" || pk != "" || rk != "" {
		t.Fatalf("last page %v, continuation %q/%q", rows, pk, rk)
	}
}
