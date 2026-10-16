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
