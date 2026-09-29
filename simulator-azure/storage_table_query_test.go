package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func useTableTestStores(t *testing.T) {
	t.Helper()
	savedData, savedEntities := tableData, tableEntities
	t.Cleanup(func() { tableData, tableEntities = savedData, savedEntities })
	tableData = sim.MakeStore[TableData](nil, "test_table_data")
	tableEntities = sim.MakeStore[TableEntity](nil, "test_table_entities")
	tableData.Put(tableKey("acct", "t"), TableData{Account: "acct", Name: "t"})
}

func putTestEntity(pk, rk string, props map[string]json.RawMessage) {
	props["PartitionKey"] = json.RawMessage(`"` + pk + `"`)
	props["RowKey"] = json.RawMessage(`"` + rk + `"`)
	tableEntities.Put(tableEntityKey("acct", "t", pk, rk), TableEntity{
		Account: "acct", Table: "t", PartitionKey: pk, RowKey: rk, Properties: props,
	})
}

func queryTestTable(filter string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	target := "/t()"
	if filter != "" {
		target += "?" + url.Values{"$filter": {filter}}.Encode()
	}
	handleEntityQuery(rec, httptest.NewRequest("GET", target, nil), "acct", "t")
	return rec
}

// A stored property that is not JSON fails the filtered query and names the
// property, instead of evaluating the filter as if the property were absent.
func TestTableQueryRefusesAnEntityWhosePropertyIsMalformed(t *testing.T) {
	useTableTestStores(t)
	putTestEntity("p", "good", map[string]json.RawMessage{"Color": json.RawMessage(`"red"`)})
	putTestEntity("p", "bad", map[string]json.RawMessage{"Color": json.RawMessage(`{"unterminated"`)})

	rec := queryTestTable("Color ne 'blue'")
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message struct {
				Value string `json:"value"`
			} `json:"message"`
		} `json:"odata.error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusInternalServerError || body.Error.Code != "InternalError" ||
		!strings.Contains(body.Error.Message.Value, `RowKey="bad"`) || !strings.Contains(body.Error.Message.Value, `"Color"`) {
		t.Fatalf("got %d %s, want 500 InternalError naming the bad entity's Color", rec.Code, rec.Body.String())
	}
}

// An entity group transaction holds its partition's write lock while it
// applies its operations one by one. A query reads that partition under the
// read lock, so it answers the partition as it stands after the whole batch.
func TestTableQueryWaitsForABatchInItsPartition(t *testing.T) {
	useTableTestStores(t)
	putTestEntity("p", "existing", map[string]json.RawMessage{"v": json.RawMessage(`"before"`)})
	putTestEntity("q", "other", map[string]json.RawMessage{"v": json.RawMessage(`"untouched"`)})

	release := tablePartitionLocks.Lock(true, tablePartitionKey("acct", "t", "p"))
	putTestEntity("p", "new", map[string]json.RawMessage{"v": json.RawMessage(`"after"`)})

	before := tablePartitionLocks.Acquired()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- queryTestTable("") }()

	deadline := time.Now().Add(10 * time.Second)
	for tablePartitionLocks.Acquired() == before {
		select {
		case rec := <-done:
			release()
			t.Fatalf("the query answered without waiting for the batch's partition: %s", rec.Body.String())
		default:
		}
		if time.Now().After(deadline) {
			release()
			t.Fatal("the query never asked for the batch's partition lock")
		}
		runtime.Gosched()
	}

	putTestEntity("p", "existing", map[string]json.RawMessage{"v": json.RawMessage(`"after"`)})
	release()
	rec := <-done

	var body struct {
		Value []map[string]any `json:"value"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	got := map[string]any{}
	for _, v := range body.Value {
		got[v["PartitionKey"].(string)+"/"+v["RowKey"].(string)] = v["v"]
	}
	want := map[string]any{"p/existing": "after", "p/new": "after", "q/other": "untouched"}
	if len(got) != len(want) {
		t.Fatalf("query answered %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("query answered %v, want %v", got, want)
		}
	}
	if held := tablePartitionLocks.Held(); held != 0 {
		t.Fatalf("partition locks left held: %d", held)
	}
}
