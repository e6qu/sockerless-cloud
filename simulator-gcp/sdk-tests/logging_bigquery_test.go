package gcp_sdk_test

import (
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	loggingrpc "google.golang.org/api/logging/v2"
)

// A sink to a BigQuery dataset streams each entry it routes into the table
// for the entry's log: dated tables by default, and with
// usePartitionedTables one table partitioned on the timestamp column.
func TestLogging_SinkToBigQueryStreamsEntries(t *testing.T) {
	t.Parallel()
	svc := loggingRESTService(t)
	project := uniqueName("log-bq")
	requireProject(t, project)
	bq := bqClient(t, project)
	require.NoError(t, bq.Dataset("dated").Create(ctx, &bigquery.DatasetMetadata{}))
	require.NoError(t, bq.Dataset("partitioned").Create(ctx, &bigquery.DatasetMetadata{}))

	parent := "projects/" + project
	_, err := svc.Projects.Sinks.Create(parent, &loggingrpc.LogSink{
		Name: "to-dated", Destination: "bigquery.googleapis.com/projects/" + project + "/datasets/dated",
		Filter: `logName:"logs/app"`,
	}).Do()
	require.NoError(t, err)
	created, err := svc.Projects.Sinks.Create(parent, &loggingrpc.LogSink{
		Name: "to-partitioned", Destination: "bigquery.googleapis.com/projects/" + project + "/datasets/partitioned",
		Filter:          `logName:"logs/app"`,
		BigqueryOptions: &loggingrpc.BigQueryOptions{UsePartitionedTables: true},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, created.BigqueryOptions)
	assert.True(t, created.BigqueryOptions.UsesTimestampColumnPartitioning)

	at := time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)
	_, err = svc.Entries.Write(&loggingrpc.WriteLogEntriesRequest{Entries: []*loggingrpc.LogEntry{
		{LogName: parent + "/logs/app", Timestamp: at.Format(time.RFC3339), Severity: "WARNING",
			TextPayload: "disk nearly full", Resource: &loggingrpc.MonitoredResource{Type: "global"}},
		{LogName: parent + "/logs/app", Timestamp: at.Add(time.Second).Format(time.RFC3339), Severity: "INFO",
			JsonPayload: []byte(`{"request-id":"r-7","latency":0.25,"ok":true}`),
			Resource:    &loggingrpc.MonitoredResource{Type: "global"}},
	}}).Do()
	require.NoError(t, err)

	dated := bq.Dataset("dated").Table("app_20260314")
	meta, err := dated.Metadata(ctx)
	require.NoError(t, err, "the dated table carries the entries' UTC date")
	columns := map[string]bigquery.FieldType{}
	for _, f := range meta.Schema {
		columns[f.Name] = f.Type
	}
	assert.Equal(t, bigquery.TimestampFieldType, columns["timestamp"])
	assert.Equal(t, bigquery.StringFieldType, columns["textPayload"])
	assert.Equal(t, bigquery.RecordFieldType, columns["jsonPayload"])
	var payload bigquery.Schema
	for _, f := range meta.Schema {
		if f.Name == "jsonPayload" {
			payload = f.Schema
		}
	}
	payloadTypes := map[string]bigquery.FieldType{}
	for _, f := range payload {
		payloadTypes[f.Name] = f.Type
	}
	assert.Equal(t, map[string]bigquery.FieldType{
		"request_id": bigquery.StringFieldType,
		"latency":    bigquery.FloatFieldType,
		"ok":         bigquery.BooleanFieldType,
	}, payloadTypes, "payload field names become BigQuery names")

	it := dated.Read(ctx)
	var got []string
	for {
		row := map[string]bigquery.Value{}
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		got = append(got, row["severity"].(string))
	}
	assert.Equal(t, []string{"WARNING", "INFO"}, got)

	part, err := bq.Dataset("partitioned").Table("app").Metadata(ctx)
	require.NoError(t, err, "a partitioned table has no date suffix")
	require.NotNil(t, part.TimePartitioning)
	assert.Equal(t, "timestamp", part.TimePartitioning.Field)
	rows := bqReadAll(t, bq.Dataset("partitioned").Table("app"))
	assert.Len(t, rows, 2)
}
