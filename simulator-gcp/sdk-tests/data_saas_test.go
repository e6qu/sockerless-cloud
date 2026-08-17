package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/bigquery/v2"
	"google.golang.org/api/firestore/v1"
	"google.golang.org/api/option"
)

func TestBigQuery_DatasetTableQueryLifecycle(t *testing.T) {
	svc, err := bigquery.NewService(ctx,
		option.WithEndpoint(baseURL+"/bigquery/v2/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	ds, err := svc.Datasets.Insert("test-project", &bigquery.Dataset{
		DatasetReference: &bigquery.DatasetReference{
			ProjectId: "test-project",
			DatasetId: "sdk_dataset",
		},
		Location: "US",
		Labels:   map[string]string{"env": "sdk"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "test-project:sdk_dataset", ds.Id)

	tbl, err := svc.Tables.Insert("test-project", "sdk_dataset", &bigquery.Table{
		TableReference: &bigquery.TableReference{
			ProjectId: "test-project",
			DatasetId: "sdk_dataset",
			TableId:   "events",
		},
		Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
			{Name: "id", Type: "STRING"},
			{Name: "kind", Type: "STRING"},
		}},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "test-project:sdk_dataset.events", tbl.Id)

	_, err = svc.Tabledata.InsertAll("test-project", "sdk_dataset", "events", &bigquery.TableDataInsertAllRequest{
		Rows: []*bigquery.TableDataInsertAllRequestRows{
			{Json: map[string]bigquery.JsonValue{"id": "evt-1", "kind": "build"}},
			{Json: map[string]bigquery.JsonValue{"id": "evt-2", "kind": "deploy"}},
		},
	}).Do()
	require.NoError(t, err)

	q, err := svc.Jobs.Query("test-project", &bigquery.QueryRequest{
		Query: "SELECT id, kind FROM `test-project.sdk_dataset.events` WHERE kind = 'deploy'",
	}).Do()
	require.NoError(t, err)
	require.True(t, q.JobComplete)
	require.Equal(t, uint64(1), q.TotalRows)
	require.Len(t, q.Rows, 1)
	assert.Equal(t, "evt-2", q.Rows[0].F[0].V)
}

func TestFirestore_DocumentCommitBatchGetRunQuery(t *testing.T) {
	svc, err := firestore.NewService(ctx,
		option.WithEndpoint(baseURL+"/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	docName := "projects/test-project/databases/(default)/documents/sdk-users/alice"
	commit, err := svc.Projects.Databases.Documents.Commit("projects/test-project/databases/(default)", &firestore.CommitRequest{
		Writes: []*firestore.Write{{
			Update: &firestore.Document{
				Name: docName,
				Fields: map[string]firestore.Value{
					"team": {StringValue: "platform"},
					"role": {StringValue: "admin"},
				},
			},
		}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, commit.WriteResults, 1)
	assertProtoJSONTimestamp(t, commit.WriteResults[0].UpdateTime)

	got, err := svc.Projects.Databases.Documents.Get(docName).Do()
	require.NoError(t, err)
	assert.Equal(t, "platform", got.Fields["team"].StringValue)
	assertProtoJSONTimestamp(t, got.CreateTime)
	assertProtoJSONTimestamp(t, got.UpdateTime)

	// A second document in the same collection, on the other side of the query
	// filter below: without it a runQuery that ignored `where` would still
	// return only the matching document.
	const collection = "projects/test-project/databases/(default)/documents/sdk-users"
	otherName := collection + "/bob"
	_, err = svc.Projects.Databases.Documents.Commit("projects/test-project/databases/(default)", &firestore.CommitRequest{
		Writes: []*firestore.Write{{
			Update: &firestore.Document{
				Name: otherName,
				Fields: map[string]firestore.Value{
					"team": {StringValue: "storage"},
					"role": {StringValue: "admin"},
				},
			},
		}},
	}).Do()
	require.NoError(t, err)

	// batchGet answers one BatchGetDocumentsResponse per requested name, in
	// request order: a stored document comes back under `found`, an absent one
	// under `missing`. Decoding the array is what separates the two — a
	// substring check over the body passes on any echo of the request.
	missingName := collection + "/missing"
	batchResp, err := http.Post(baseURL+"/v1/projects/test-project/databases/(default)/documents:batchGet",
		"application/json",
		strings.NewReader(`{"documents":["`+docName+`","`+missingName+`"]}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, batchResp.StatusCode)
	batchBody, _ := io.ReadAll(batchResp.Body)
	batchResp.Body.Close()

	var batch []struct {
		Found    *firestore.Document `json:"found"`
		Missing  string              `json:"missing"`
		ReadTime string              `json:"readTime"`
	}
	require.NoError(t, json.Unmarshal(batchBody, &batch), "batchGet body: %s", batchBody)
	require.Len(t, batch, 2, "one response per requested document: %s", batchBody)

	require.NotNil(t, batch[0].Found, "the stored document must come back under `found`: %s", batchBody)
	assert.Empty(t, batch[0].Missing)
	assert.Equal(t, docName, batch[0].Found.Name)
	assert.Equal(t, "platform", batch[0].Found.Fields["team"].StringValue)
	assertProtoJSONTimestamp(t, batch[0].ReadTime)

	assert.Nil(t, batch[1].Found, "an absent document must not come back under `found`: %s", batchBody)
	assert.Equal(t, missingName, batch[1].Missing)
	assertProtoJSONTimestamp(t, batch[1].ReadTime)

	// runQuery answers one RunQueryResponse per matching document. The
	// collection now holds two documents and the filter selects one, so a
	// response carrying both would mean the `where` clause was dropped.
	queryResp, err := http.Post(baseURL+"/v1/projects/test-project/databases/(default)/documents:runQuery",
		"application/json",
		strings.NewReader(`{"structuredQuery":{"from":[{"collectionId":"sdk-users"}],"where":{"fieldFilter":{"field":{"fieldPath":"team"},"op":"EQUAL","value":{"stringValue":"platform"}}}}}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, queryResp.StatusCode)
	queryBody, _ := io.ReadAll(queryResp.Body)
	queryResp.Body.Close()

	var results []struct {
		Document *firestore.Document `json:"document"`
		ReadTime string              `json:"readTime"`
	}
	require.NoError(t, json.Unmarshal(queryBody, &results), "runQuery body: %s", queryBody)
	require.Len(t, results, 1, "team==platform selects one of the two documents: %s", queryBody)
	require.NotNil(t, results[0].Document)
	assert.Equal(t, docName, results[0].Document.Name)
	assert.Equal(t, "platform", results[0].Document.Fields["team"].StringValue)
	assertProtoJSONTimestamp(t, results[0].ReadTime)

	// The excluded document is reachable by its own filter value, so its
	// absence above is the filter's doing and not a write that never landed.
	otherResp, err := http.Post(baseURL+"/v1/projects/test-project/databases/(default)/documents:runQuery",
		"application/json",
		strings.NewReader(`{"structuredQuery":{"from":[{"collectionId":"sdk-users"}],"where":{"fieldFilter":{"field":{"fieldPath":"team"},"op":"EQUAL","value":{"stringValue":"storage"}}}}}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, otherResp.StatusCode)
	otherBody, _ := io.ReadAll(otherResp.Body)
	otherResp.Body.Close()
	results = results[:0]
	require.NoError(t, json.Unmarshal(otherBody, &results), "runQuery body: %s", otherBody)
	require.Len(t, results, 1, "runQuery body: %s", otherBody)
	require.NotNil(t, results[0].Document)
	assert.Equal(t, otherName, results[0].Document.Name)
}
