package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/bigquery/v2"
	"google.golang.org/api/firestore/v1"
	"google.golang.org/api/option"
)

func bqService(t *testing.T) *bigquery.Service {
	t.Helper()
	svc, err := bigquery.NewService(ctx,
		option.WithEndpoint(baseURL+"/bigquery/v2/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

// TestBQCoverage_DatasetReadUpdateDelete covers the untested dataset ops:
// list, get, patch (incl. labels round-trip), delete.
func TestBQCoverage_DatasetReadUpdateDelete(t *testing.T) {
	svc := bqService(t)
	const proj = "cov-project"
	_, err := svc.Datasets.Insert(proj, &bigquery.Dataset{
		DatasetReference: &bigquery.DatasetReference{ProjectId: proj, DatasetId: "cov_ds"},
		Location:         "US",
		Labels:           map[string]string{"team": "data"},
	}).Do()
	require.NoError(t, err)

	got, err := svc.Datasets.Get(proj, "cov_ds").Do()
	require.NoError(t, err)
	assert.Equal(t, "US", got.Location)
	assert.Equal(t, "data", got.Labels["team"])

	list, err := svc.Datasets.List(proj).Do()
	require.NoError(t, err)
	var found bool
	for _, d := range list.Datasets {
		if d.DatasetReference.DatasetId == "cov_ds" {
			found = true
		}
	}
	assert.True(t, found, "inserted dataset must appear in list")

	_, err = svc.Datasets.Patch(proj, "cov_ds", &bigquery.Dataset{
		FriendlyName: "Coverage DS",
		Labels:       map[string]string{"team": "data", "env": "test"},
	}).Do()
	require.NoError(t, err)
	after, err := svc.Datasets.Get(proj, "cov_ds").Do()
	require.NoError(t, err)
	assert.Equal(t, "Coverage DS", after.FriendlyName)
	assert.Equal(t, "test", after.Labels["env"], "patch must persist added label")

	require.NoError(t, svc.Datasets.Delete(proj, "cov_ds").Do())
	_, err = svc.Datasets.Get(proj, "cov_ds").Do()
	assert.Error(t, err, "dataset gone after delete")
}

// TestBQCoverage_TableReadUpdateDeleteLabels covers the untested table ops:
// get, list, patch, delete — and the table labels round-trip.
func TestBQCoverage_TableReadUpdateDeleteLabels(t *testing.T) {
	svc := bqService(t)
	const proj = "cov-project"
	_, err := svc.Datasets.Insert(proj, &bigquery.Dataset{
		DatasetReference: &bigquery.DatasetReference{ProjectId: proj, DatasetId: "tbl_ds"},
	}).Do()
	require.NoError(t, err)

	_, err = svc.Tables.Insert(proj, "tbl_ds", &bigquery.Table{
		TableReference: &bigquery.TableReference{ProjectId: proj, DatasetId: "tbl_ds", TableId: "t1"},
		Schema:         &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{{Name: "id", Type: "STRING"}}},
		Labels:         map[string]string{"tier": "bronze"},
	}).Do()
	require.NoError(t, err)

	got, err := svc.Tables.Get(proj, "tbl_ds", "t1").Do()
	require.NoError(t, err)
	assert.Equal(t, "bronze", got.Labels["tier"], "table labels must round-trip")

	list, err := svc.Tables.List(proj, "tbl_ds").Do()
	require.NoError(t, err)
	require.Len(t, list.Tables, 1)
	assert.Equal(t, proj+":tbl_ds.t1", list.Tables[0].Id)

	_, err = svc.Tables.Patch(proj, "tbl_ds", "t1", &bigquery.Table{
		Labels: map[string]string{"tier": "gold"},
	}).Do()
	require.NoError(t, err)
	patched, err := svc.Tables.Get(proj, "tbl_ds", "t1").Do()
	require.NoError(t, err)
	assert.Equal(t, "gold", patched.Labels["tier"], "patch must update table label")

	require.NoError(t, svc.Tables.Delete(proj, "tbl_ds", "t1").Do())
	_, err = svc.Tables.Get(proj, "tbl_ds", "t1").Do()
	assert.Error(t, err, "table gone after delete")
}

// TestBQCoverage_TabledataAndJobs covers tabledata.list, jobs.insert,
// jobs.list, jobs.get, and jobs.getQueryResults.
func TestBQCoverage_TabledataAndJobs(t *testing.T) {
	svc := bqService(t)
	const proj = "cov-project"
	_, err := svc.Datasets.Insert(proj, &bigquery.Dataset{
		DatasetReference: &bigquery.DatasetReference{ProjectId: proj, DatasetId: "job_ds"},
	}).Do()
	require.NoError(t, err)
	_, err = svc.Tables.Insert(proj, "job_ds", &bigquery.Table{
		TableReference: &bigquery.TableReference{ProjectId: proj, DatasetId: "job_ds", TableId: "evt"},
		Schema:         &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{{Name: "id", Type: "STRING"}, {Name: "kind", Type: "STRING"}}},
	}).Do()
	require.NoError(t, err)
	_, err = svc.Tabledata.InsertAll(proj, "job_ds", "evt", &bigquery.TableDataInsertAllRequest{
		Rows: []*bigquery.TableDataInsertAllRequestRows{
			{Json: map[string]bigquery.JsonValue{"id": "a", "kind": "build"}},
			{Json: map[string]bigquery.JsonValue{"id": "b", "kind": "deploy"}},
		},
	}).Do()
	require.NoError(t, err)

	// tabledata.list returns the persisted rows.
	td, err := svc.Tabledata.List(proj, "job_ds", "evt").Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), td.TotalRows)
	require.Len(t, td.Rows, 2)

	// jobs.insert with a query configuration runs the query.
	job, err := svc.Jobs.Insert(proj, &bigquery.Job{
		Configuration: &bigquery.JobConfiguration{
			Query: &bigquery.JobConfigurationQuery{
				Query: "SELECT id, kind FROM `cov-project.job_ds.evt` WHERE kind = 'deploy'",
			},
		},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, job.JobReference)
	jobID := job.JobReference.JobId
	require.NotEmpty(t, jobID)
	assert.Equal(t, "DONE", job.Status.State)

	// jobs.get round-trips the job.
	gotJob, err := svc.Jobs.Get(proj, jobID).Do()
	require.NoError(t, err)
	assert.Equal(t, jobID, gotJob.JobReference.JobId)

	// jobs.list includes the job.
	jl, err := svc.Jobs.List(proj).Do()
	require.NoError(t, err)
	var jobFound bool
	for _, j := range jl.Jobs {
		if j.JobReference != nil && j.JobReference.JobId == jobID {
			jobFound = true
			assert.Equal(t, "DONE", j.State, "jobs.list item carries top-level state")
		}
	}
	assert.True(t, jobFound, "inserted job must appear in jobs.list")

	// getQueryResults returns the query rows for the job.
	qr, err := svc.Jobs.GetQueryResults(proj, jobID).Do()
	require.NoError(t, err)
	require.True(t, qr.JobComplete)
	require.Equal(t, int64(1), int64(qr.TotalRows))
	require.Len(t, qr.Rows, 1)
	assert.Equal(t, "b", qr.Rows[0].F[0].V)
	assert.Equal(t, int64(0), qr.TotalBytesProcessed, "GetQueryResultsResponse carries totalBytesProcessed")
}

// TestFSCoverage_CreateUpdateDeleteBatchWrite covers the untested firestore
// ops: createDocument (POST), patch (update), delete, and batchWrite.
func TestFSCoverage_CreateUpdateDeleteBatchWrite(t *testing.T) {
	svc, err := firestore.NewService(ctx,
		option.WithEndpoint(baseURL+"/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	parent := "projects/cov-project/databases/(default)/documents"

	// CreateDocument (POST collection) with an explicit documentId.
	created, err := svc.Projects.Databases.Documents.CreateDocument(parent, "cov-users", &firestore.Document{
		Fields: map[string]firestore.Value{"name": {StringValue: "carol"}},
	}).DocumentId("carol").Do()
	require.NoError(t, err)
	assert.Equal(t, parent+"/cov-users/carol", created.Name)
	assert.Equal(t, "carol", created.Fields["name"].StringValue)

	// Patch with an update mask adds a field while preserving the existing one.
	docName := parent + "/cov-users/carol"
	_, err = svc.Projects.Databases.Documents.Patch(docName, &firestore.Document{
		Fields: map[string]firestore.Value{"role": {StringValue: "admin"}},
	}).UpdateMaskFieldPaths("role").Do()
	require.NoError(t, err)
	after, err := svc.Projects.Databases.Documents.Get(docName).Do()
	require.NoError(t, err)
	assert.Equal(t, "admin", after.Fields["role"].StringValue)
	assert.Equal(t, "carol", after.Fields["name"].StringValue, "masked patch preserves unmentioned fields")

	// batchWrite a second document, then delete the first.
	_, err = svc.Projects.Databases.Documents.BatchWrite("projects/cov-project/databases/(default)", &firestore.BatchWriteRequest{
		Writes: []*firestore.Write{{
			Update: &firestore.Document{
				Name:   parent + "/cov-users/dave",
				Fields: map[string]firestore.Value{"name": {StringValue: "dave"}},
			},
		}},
	}).Do()
	require.NoError(t, err)
	dave, err := svc.Projects.Databases.Documents.Get(parent + "/cov-users/dave").Do()
	require.NoError(t, err)
	assert.Equal(t, "dave", dave.Fields["name"].StringValue)

	// Delete carol.
	_, err = svc.Projects.Databases.Documents.Delete(docName).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Databases.Documents.Get(docName).Do()
	assert.Error(t, err, "document gone after delete")
}
