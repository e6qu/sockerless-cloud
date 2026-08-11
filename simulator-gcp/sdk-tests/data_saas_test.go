package gcp_sdk_test

import (
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

	batchResp, err := http.Post(baseURL+"/v1/projects/test-project/databases/(default)/documents:batchGet",
		"application/json",
		strings.NewReader(`{"documents":["`+docName+`","projects/test-project/databases/(default)/documents/sdk-users/missing"]}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, batchResp.StatusCode)
	batchBody, _ := io.ReadAll(batchResp.Body)
	batchResp.Body.Close()
	assert.Contains(t, string(batchBody), docName)
	assert.Contains(t, string(batchBody), "sdk-users/missing")

	queryResp, err := http.Post(baseURL+"/v1/projects/test-project/databases/(default)/documents:runQuery",
		"application/json",
		strings.NewReader(`{"structuredQuery":{"from":[{"collectionId":"sdk-users"}],"where":{"fieldFilter":{"field":{"fieldPath":"team"},"op":"EQUAL","value":{"stringValue":"platform"}}}}}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, queryResp.StatusCode)
	queryBody, _ := io.ReadAll(queryResp.Body)
	queryResp.Body.Close()
	assert.Contains(t, string(queryBody), docName)
}
