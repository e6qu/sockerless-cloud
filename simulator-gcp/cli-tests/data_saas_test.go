package gcp_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestBigQueryAndFirestore_RESTCLIFlows(t *testing.T) {
	dsBody := `{"datasetReference":{"projectId":"test-project","datasetId":"cli_dataset"},"location":"US"}`
	httpDoJSON(t, "POST", baseURL+"/bigquery/v2/projects/test-project/datasets", dsBody)

	tableBody := `{"tableReference":{"projectId":"test-project","datasetId":"cli_dataset","tableId":"events"},"schema":{"fields":[{"name":"id","type":"STRING"},{"name":"kind","type":"STRING"}]}}`
	httpDoJSON(t, "POST", baseURL+"/bigquery/v2/projects/test-project/datasets/cli_dataset/tables", tableBody)

	insertBody := `{"rows":[{"json":{"id":"evt-1","kind":"build"}},{"json":{"id":"evt-2","kind":"deploy"}}]}`
	httpDoJSON(t, "POST", baseURL+"/bigquery/v2/projects/test-project/datasets/cli_dataset/tables/events/insertAll", insertBody)

	query := `{"query":"SELECT id, kind FROM ` + "`test-project.cli_dataset.events`" + ` WHERE kind = 'deploy'"}`
	out := httpDoJSON(t, "POST", baseURL+"/bigquery/v2/projects/test-project/queries", query)
	if !strings.Contains(out, `"totalRows":"1"`) {
		t.Fatalf("BigQuery query did not return one row: %s", out)
	}

	docPath := "/v1/projects/test-project/databases/(default)/documents/cli-users/alice"
	docBody := `{"fields":{"team":{"stringValue":"platform"},"role":{"stringValue":"admin"}}}`
	httpDoJSON(t, "PATCH", baseURL+docPath, docBody)

	got := httpDoJSON(t, "GET", baseURL+docPath, "")
	if !strings.Contains(got, `"platform"`) {
		t.Fatalf("Firestore get did not return stored document: %s", got)
	}

	runQuery := `{"structuredQuery":{"from":[{"collectionId":"cli-users"}],"where":{"fieldFilter":{"field":{"fieldPath":"team"},"op":"EQUAL","value":{"stringValue":"platform"}}}}}`
	q := httpDoJSON(t, "POST", fmt.Sprintf("%s/v1/projects/test-project/databases/(default)/documents:runQuery", baseURL), runQuery)
	if !strings.Contains(q, "cli-users/alice") {
		t.Fatalf("Firestore runQuery did not return alice: %s", q)
	}
}

// TestBigQuery_TableDataListInvalidParams asserts tabledata.list rejects a
// present-but-non-numeric/negative startIndex or maxResults with HTTP 400
// (INVALID_ARGUMENT), as real BigQuery does, rather than silently parsing it to
// 0. Absent params keep their defaults (start 0, all rows).
func TestBigQuery_TableDataListInvalidParams(t *testing.T) {
	ds := `{"datasetReference":{"projectId":"test-project","datasetId":"bq_validate"},"location":"US"}`
	httpDoJSON(t, "POST", baseURL+"/bigquery/v2/projects/test-project/datasets", ds)
	tbl := `{"tableReference":{"projectId":"test-project","datasetId":"bq_validate","tableId":"rows"},"schema":{"fields":[{"name":"id","type":"STRING"}]}}`
	httpDoJSON(t, "POST", baseURL+"/bigquery/v2/projects/test-project/datasets/bq_validate/tables", tbl)
	httpDoJSON(t, "POST", baseURL+"/bigquery/v2/projects/test-project/datasets/bq_validate/tables/rows/insertAll", `{"rows":[{"json":{"id":"a"}}]}`)

	dataURL := baseURL + "/bigquery/v2/projects/test-project/datasets/bq_validate/tables/rows/data"

	// Absent params → 200 (default path unchanged).
	resp, err := httpDo("GET", dataURL, "")
	if err != nil {
		t.Fatalf("tabledata.list (no params): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("tabledata.list (no params) want 200, got %d", resp.StatusCode)
	}

	// Present-but-invalid → 400.
	for _, qs := range []string{"?startIndex=abc", "?maxResults=xyz", "?startIndex=-1"} {
		resp, err := httpDo("GET", dataURL+qs, "")
		if err != nil {
			t.Fatalf("tabledata.list %s: %v", qs, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("tabledata.list %s want 400, got %d", qs, resp.StatusCode)
		}
	}
}
