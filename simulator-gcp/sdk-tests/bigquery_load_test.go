package gcp_sdk_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bigquery "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/googleapi"
)

// A load job's source data rides jobs.insert's media path: in one multipart
// request while it fits a chunk, and past that through a resumable session
// the Go client begins on the /upload path and POSTs each chunk to.
//
//	POST /upload/bigquery/v2/projects/{project}/jobs

func bqLoadJob(project, dataset, table, format, write string) *bigquery.Job {
	return &bigquery.Job{Configuration: &bigquery.JobConfiguration{Load: &bigquery.JobConfigurationLoad{
		DestinationTable: &bigquery.TableReference{ProjectId: project, DatasetId: dataset, TableId: table},
		SourceFormat:     format,
		WriteDisposition: write,
		Schema: &bigquery.TableSchema{Fields: []*bigquery.TableFieldSchema{
			{Name: "id", Type: "INTEGER", Mode: "REQUIRED"},
			{Name: "word", Type: "STRING"},
		}},
	}}}
}

func TestBigQuery_LoadJobFromMedia(t *testing.T) {
	svc := bqService(t)
	project, dataset := "sock-proj", strings.ReplaceAll(uniqueName("ds_loads"), "-", "_")
	bqMakeDataset(t, svc, project, dataset)

	var source strings.Builder
	rows := 0
	for source.Len() <= 3*googleapi.MinUploadChunkSize {
		fmt.Fprintf(&source, "{\"id\":%d,\"word\":\"resumable chunk row %d\"}\n", rows, rows)
		rows++
	}
	var progress []int64
	job, err := svc.Jobs.Insert(project, bqLoadJob(project, dataset, "words", "NEWLINE_DELIMITED_JSON", "")).
		Media(strings.NewReader(source.String()), googleapi.ChunkSize(googleapi.MinUploadChunkSize), googleapi.ContentType("application/octet-stream")).
		ProgressUpdater(func(current, _ int64) { progress = append(progress, current) }).
		Do()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(progress), 4, "the client sent the source in chunks")
	require.Equal(t, "DONE", job.Status.State)
	require.Nil(t, job.Status.ErrorResult)
	assert.Equal(t, int64(rows), job.Statistics.Load.OutputRows)
	assert.Equal(t, int64(source.Len()), job.Statistics.Load.InputFileBytes)

	got, err := svc.Jobs.Get(project, job.JobReference.JobId).Do()
	require.NoError(t, err)
	assert.Equal(t, "DONE", got.Status.State)
	table, err := svc.Tables.Get(project, dataset, "words").Do()
	require.NoError(t, err)
	assert.Equal(t, uint64(rows), table.NumRows)

	// A source that fits one chunk goes in a single multipart request.
	job, err = svc.Jobs.Insert(project, bqLoadJob(project, dataset, "words", "CSV", "WRITE_TRUNCATE")).
		Media(bytes.NewReader([]byte("1,one\n2,\"two, quoted\"\n")), googleapi.ContentType("text/csv")).
		Do()
	require.NoError(t, err)
	require.Nil(t, job.Status.ErrorResult)
	assert.Equal(t, int64(2), job.Statistics.Load.OutputRows)
	data, err := svc.Tabledata.List(project, dataset, "words").Do()
	require.NoError(t, err)
	require.Len(t, data.Rows, 2)
	assert.Equal(t, "two, quoted", data.Rows[1].F[1].V)

	// A load whose data does not fit the schema finishes with an errorResult.
	job, err = svc.Jobs.Insert(project, bqLoadJob(project, dataset, "words", "CSV", "")).
		Media(bytes.NewReader([]byte("1,two,three\n")), googleapi.ContentType("text/csv")).
		Do()
	require.NoError(t, err)
	require.NotNil(t, job.Status.ErrorResult)
	assert.Equal(t, "invalid", job.Status.ErrorResult.Reason)
}
