package gcp_sdk_test

import (
	"errors"
	"testing"

	"cloud.google.com/go/bigquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

type bqSQLPerson struct {
	Name   string `bigquery:"name"`
	Team   string `bigquery:"team"`
	Age    int64  `bigquery:"age"`
	Active bool   `bigquery:"active"`
}

// A query evaluates its SELECT list, predicates, grouping, aggregates,
// ordering and limit against the rows the table holds.
func TestBigQuery_QueryEvaluatesSQL(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-sql")
	requireProject(t, project)
	bq := bqClient(t, project)
	ds := bq.Dataset("people_ds")
	require.NoError(t, ds.Create(ctx, &bigquery.DatasetMetadata{}))
	schema, err := bigquery.InferSchema(bqSQLPerson{})
	require.NoError(t, err)
	table := ds.Table("people")
	require.NoError(t, table.Create(ctx, &bigquery.TableMetadata{Schema: schema}))
	require.NoError(t, table.Inserter().Put(ctx, []*bqSQLPerson{
		{Name: "ada", Team: "core", Age: 36, Active: true},
		{Name: "bob", Team: "core", Age: 29, Active: false},
		{Name: "cy", Team: "edge", Age: 41, Active: true},
	}))

	read := func(sql string) []map[string]bigquery.Value {
		t.Helper()
		it, err := bq.Query(sql).Read(ctx)
		require.NoError(t, err, sql)
		var out []map[string]bigquery.Value
		for {
			row := map[string]bigquery.Value{}
			err := it.Next(&row)
			if errors.Is(err, iterator.Done) {
				return out
			}
			require.NoError(t, err)
			out = append(out, row)
		}
	}

	rows := read("SELECT name, age FROM people_ds.people WHERE active AND age > 30 ORDER BY age DESC")
	require.Len(t, rows, 2)
	assert.Equal(t, map[string]bigquery.Value{"name": "cy", "age": int64(41)}, rows[0])
	assert.Equal(t, "ada", rows[1]["name"])

	rows = read("SELECT team, COUNT(*) AS members, MAX(age) AS oldest FROM `people_ds.people` GROUP BY team ORDER BY members DESC LIMIT 1")
	require.Len(t, rows, 1)
	assert.Equal(t, map[string]bigquery.Value{"team": "core", "members": int64(2), "oldest": int64(36)}, rows[0])

	_, err = bq.Query("SELECT missing_column FROM people_ds.people").Read(ctx)
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr), "an unknown column is refused: %v", err)
	assert.Equal(t, 400, apiErr.Code)
}

// A query job with a destination table writes its result there under its
// write disposition: WRITE_EMPTY refuses a table holding rows, WRITE_APPEND
// adds to it, WRITE_TRUNCATE replaces it, and CREATE_NEVER needs it to exist.
func TestBigQuery_QueryWritesItsDestinationTable(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-dest")
	requireProject(t, project)
	bq := bqClient(t, project)
	ds := bq.Dataset("dest_ds")
	require.NoError(t, ds.Create(ctx, &bigquery.DatasetMetadata{}))
	schema, err := bigquery.InferSchema(bqSQLPerson{})
	require.NoError(t, err)
	src := ds.Table("people")
	require.NoError(t, src.Create(ctx, &bigquery.TableMetadata{Schema: schema}))
	require.NoError(t, src.Inserter().Put(ctx, []*bqSQLPerson{
		{Name: "ada", Team: "core", Age: 36, Active: true},
		{Name: "bob", Team: "core", Age: 29},
		{Name: "cy", Team: "edge", Age: 41, Active: true},
	}))

	run := func(disposition bigquery.TableWriteDisposition, create bigquery.TableCreateDisposition, table string) error {
		t.Helper()
		q := bq.Query("SELECT name, age FROM dest_ds.people WHERE active ORDER BY name")
		q.Dst = ds.Table(table)
		q.WriteDisposition = disposition
		q.CreateDisposition = create
		job, err := q.Run(ctx)
		require.NoError(t, err)
		_, _ = job.Wait(ctx)
		status, err := job.Status(ctx)
		require.NoError(t, err)
		require.True(t, status.Done())
		return status.Err()
	}
	names := func(table string) []string {
		t.Helper()
		it := ds.Table(table).Read(ctx)
		var out []string
		for {
			var row []bigquery.Value
			err := it.Next(&row)
			if errors.Is(err, iterator.Done) {
				return out
			}
			require.NoError(t, err)
			out = append(out, row[0].(string))
		}
	}
	reason := func(err error) string {
		var bqErr *bigquery.Error
		require.True(t, errors.As(err, &bqErr), "%v", err)
		return bqErr.Reason
	}

	require.NoError(t, run(bigquery.WriteEmpty, bigquery.CreateIfNeeded, "active"))
	assert.Equal(t, []string{"ada", "cy"}, names("active"))
	meta, err := ds.Table("active").Metadata(ctx)
	require.NoError(t, err)
	require.Len(t, meta.Schema, 2)
	assert.Equal(t, bigquery.IntegerFieldType, meta.Schema[1].Type)

	assert.Equal(t, "duplicate", reason(run(bigquery.WriteEmpty, bigquery.CreateIfNeeded, "active")))
	require.NoError(t, run(bigquery.WriteAppend, bigquery.CreateIfNeeded, "active"))
	assert.Len(t, names("active"), 4)
	require.NoError(t, run(bigquery.WriteTruncate, bigquery.CreateIfNeeded, "active"))
	assert.Equal(t, []string{"ada", "cy"}, names("active"))
	assert.Equal(t, "notFound", reason(run(bigquery.WriteTruncate, bigquery.CreateNever, "absent")))
}
