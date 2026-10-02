package gcp_sdk_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

func bqClient(t *testing.T, project string) *bigquery.Client {
	t.Helper()
	client, err := bigquery.NewClient(ctx, project,
		option.WithEndpoint(baseURL+"/bigquery/v2/"),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func bqReadAll(t *testing.T, table *bigquery.Table) [][]bigquery.Value {
	t.Helper()
	it := table.Read(ctx)
	var rows [][]bigquery.Value
	for {
		var row []bigquery.Value
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			return rows
		}
		require.NoError(t, err)
		rows = append(rows, row)
	}
}

// bqWaitJob waits for a job jobs.insert answered through the client's own
// waiter.
func bqWaitJob(t *testing.T, project, jobID, location string) {
	t.Helper()
	job, err := bqClient(t, project).JobFromIDLocation(ctx, jobID, location)
	require.NoError(t, err)
	_, err = job.Wait(ctx)
	require.NoError(t, err)
}

// bqRunJob waits for a load, copy or extract job through the client's own
// waiter and returns its final status.
func bqRunJob(t *testing.T, run func() (*bigquery.Job, error)) *bigquery.JobStatus {
	t.Helper()
	job, err := run()
	require.NoError(t, err)
	status, err := job.Wait(ctx)
	require.NoError(t, err)
	require.True(t, status.Done())
	return status
}

// Load jobs read Cloud Storage objects, copy jobs move rows between tables,
// and extract jobs write them back to Cloud Storage, each as a job the client
// waits on.
func TestBigQuery_LoadCopyExtractThroughCloudStorage(t *testing.T) {
	const project = "sock-proj"
	client := bqClient(t, project)
	gcs := storageClient(t)
	defer gcs.Close()

	bucketName := uniqueName("bq-jobs")
	bucket := gcs.Bucket(bucketName)
	requireProject(t, project)
	require.NoError(t, bucket.Create(ctx, project, nil))
	for name, body := range map[string]string{
		"people/part-1.csv": "name,age,joined\nada,36,2024-01-02 03:04:05 UTC\n",
		"people/part-2.csv": "name,age,joined\ngrace,41,2023-06-07 08:09:10 UTC\n",
	} {
		w := bucket.Object(name).NewWriter(ctx)
		_, err := w.Write([]byte(body))
		require.NoError(t, err)
		require.NoError(t, w.Close())
	}

	dataset := client.Dataset(strings.ReplaceAll(uniqueName("ds_jobs"), "-", "_"))
	require.NoError(t, dataset.Create(ctx, nil))

	source := bigquery.NewGCSReference("gs://" + bucketName + "/people/part-*")
	source.AutoDetect = true
	people := dataset.Table("people")
	status := bqRunJob(t, func() (*bigquery.Job, error) { return people.LoaderFrom(source).Run(ctx) })
	require.NoError(t, status.Err())
	load, ok := status.Statistics.Details.(*bigquery.LoadStatistics)
	require.True(t, ok, "a load job reports load statistics")
	assert.Equal(t, int64(2), load.InputFiles)
	assert.Equal(t, int64(2), load.OutputRows)

	meta, err := people.Metadata(ctx)
	require.NoError(t, err)
	require.Len(t, meta.Schema, 3)
	assert.Equal(t, "name", meta.Schema[0].Name)
	assert.Equal(t, bigquery.IntegerFieldType, meta.Schema[1].Type)
	assert.Equal(t, bigquery.TimestampFieldType, meta.Schema[2].Type)
	assert.Equal(t, uint64(2), meta.NumRows)
	// STRING is 2 bytes plus its length, INT64 and TIMESTAMP 8 bytes each.
	assert.Equal(t, int64((2+3+8+8)+(2+5+8+8)), meta.NumBytes)
	rows := bqReadAll(t, people)
	require.Len(t, rows, 2)
	assert.Equal(t, "ada", rows[0][0])
	assert.Equal(t, int64(36), rows[0][1])

	missing := bigquery.NewGCSReference("gs://" + bucketName + "/absent.csv")
	status = bqRunJob(t, func() (*bigquery.Job, error) { return people.LoaderFrom(missing).Run(ctx) })
	var bqErr *bigquery.Error
	require.ErrorAs(t, status.Err(), &bqErr)
	assert.Equal(t, "notFound", bqErr.Reason)

	copied := dataset.Table("people_copy")
	status = bqRunJob(t, func() (*bigquery.Job, error) { return copied.CopierFrom(people, people).Run(ctx) })
	require.NoError(t, status.Err())
	assert.Len(t, bqReadAll(t, copied), 4, "a copy from two sources writes the rows of both")
	status = bqRunJob(t, func() (*bigquery.Job, error) { return copied.CopierFrom(people).Run(ctx) })
	require.ErrorAs(t, status.Err(), &bqErr)
	assert.Equal(t, "duplicate", bqErr.Reason, "WRITE_EMPTY refuses a destination holding rows")
	copier := copied.CopierFrom(people)
	copier.WriteDisposition = bigquery.WriteTruncate
	status = bqRunJob(t, func() (*bigquery.Job, error) { return copier.Run(ctx) })
	require.NoError(t, status.Err())
	assert.Len(t, bqReadAll(t, copied), 2)

	out := bigquery.NewGCSReference("gs://" + bucketName + "/out/people-*.json.gz")
	out.DestinationFormat = bigquery.JSON
	out.Compression = bigquery.Gzip
	status = bqRunJob(t, func() (*bigquery.Job, error) { return people.ExtractorTo(out).Run(ctx) })
	require.NoError(t, status.Err())
	extract, ok := status.Statistics.Details.(*bigquery.ExtractStatistics)
	require.True(t, ok, "an extract job reports extract statistics")
	assert.Equal(t, []int64{1}, extract.DestinationURIFileCounts)
	reader, err := bucket.Object("out/people-000000000000.json.gz").NewReader(ctx)
	require.NoError(t, err)
	zipped, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	zr, err := gzip.NewReader(bytes.NewReader(zipped))
	require.NoError(t, err)
	extracted, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t,
		`{"name":"ada","age":"36","joined":"2024-01-02 03:04:05 UTC"}`+"\n"+
			`{"name":"grace","age":"41","joined":"2023-06-07 08:09:10 UTC"}`+"\n",
		string(extracted))

	// An Avro or Parquet extract loads back into the same schema and rows.
	for _, format := range []bigquery.DataFormat{bigquery.Avro, bigquery.Parquet} {
		uri := "gs://" + bucketName + "/out/people." + strings.ToLower(string(format))
		dst := bigquery.NewGCSReference(uri)
		dst.DestinationFormat = format
		extractor := people.ExtractorTo(dst)
		extractor.UseAvroLogicalTypes = true
		status = bqRunJob(t, func() (*bigquery.Job, error) { return extractor.Run(ctx) })
		require.NoError(t, status.Err(), format)

		src := bigquery.NewGCSReference(uri)
		src.SourceFormat = format
		reloaded := dataset.Table("people_" + strings.ToLower(string(format)))
		loader := reloaded.LoaderFrom(src)
		loader.UseAvroLogicalTypes = true
		status = bqRunJob(t, func() (*bigquery.Job, error) { return loader.Run(ctx) })
		require.NoError(t, status.Err(), format)
		got, err := reloaded.Metadata(ctx)
		require.NoError(t, err)
		require.Len(t, got.Schema, 3, format)
		for i, f := range got.Schema {
			assert.Equal(t, meta.Schema[i].Name, f.Name, format)
			assert.Equal(t, meta.Schema[i].Type, f.Type, format)
		}
		assert.Equal(t, rows, bqReadAll(t, reloaded), format)
	}
}
