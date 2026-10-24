package gcp_sdk_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
)

type bqOrderItem struct {
	SKU string `bigquery:"sku"`
	Qty int64  `bigquery:"qty"`
}

type bqOrderBuyer struct {
	Name string `bigquery:"name"`
	City string `bigquery:"city"`
}

type bqOrder struct {
	ID     int64         `bigquery:"id"`
	Placed time.Time     `bigquery:"placed"`
	Buyer  bqOrderBuyer  `bigquery:"buyer"`
	Items  []bqOrderItem `bigquery:"items"`
}

func bqReadQuery(t *testing.T, client *bigquery.Client, sql string) []map[string]bigquery.Value {
	t.Helper()
	it, err := client.Query(sql).Read(ctx)
	require.NoError(t, err, sql)
	var out []map[string]bigquery.Value
	for {
		row := map[string]bigquery.Value{}
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			return out
		}
		require.NoError(t, err, sql)
		out = append(out, row)
	}
}

// bqRunQuery runs a statement as a job through the client's waiter and
// returns its final status.
func bqRunQuery(t *testing.T, client *bigquery.Client, sql string) (*bigquery.Job, *bigquery.QueryStatistics) {
	t.Helper()
	job, err := client.Query(sql).Run(ctx)
	require.NoError(t, err, sql)
	status, err := job.Wait(ctx)
	require.NoError(t, err, sql)
	require.NoError(t, status.Err(), sql)
	stats, ok := status.Statistics.Details.(*bigquery.QueryStatistics)
	require.True(t, ok, "a query job reports query statistics")
	return job, stats
}

func bqOrdersTable(t *testing.T, client *bigquery.Client, dataset *bigquery.Dataset) {
	t.Helper()
	schema, err := bigquery.InferSchema(bqOrder{})
	require.NoError(t, err)
	table := dataset.Table("orders")
	require.NoError(t, table.Create(ctx, &bigquery.TableMetadata{Schema: schema}))
	require.NoError(t, table.Inserter().Put(ctx, []*bqOrder{
		{ID: 1, Placed: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), Buyer: bqOrderBuyer{"ada", "oslo"}, Items: []bqOrderItem{{"a", 2}, {"b", 1}}},
		{ID: 2, Placed: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), Buyer: bqOrderBuyer{"bob", "rome"}, Items: []bqOrderItem{{"c", 5}}},
	}))
}

// GoogleSQL beyond what SQLite shares runs: UNNEST with an offset, field
// access into a RECORD, TIMESTAMP literals and functions, SAFE_CAST, QUALIFY,
// and SELECT * EXCEPT and REPLACE.
func TestBigQuery_GoogleSQLConstructs(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-gsql")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("gsql_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	bqOrdersTable(t, client, dataset)

	rows := bqReadQuery(t, client, "SELECT o.id, o.buyer.city AS city, item.sku, item.qty, pos FROM gsql_ds.orders o, UNNEST(o.items) AS item WITH OFFSET AS pos ORDER BY o.id, pos")
	require.Len(t, rows, 3)
	assert.Equal(t, map[string]bigquery.Value{"id": int64(1), "city": "oslo", "sku": "b", "qty": int64(1), "pos": int64(1)}, rows[1])

	rows = bqReadQuery(t, client, `SELECT id, TIMESTAMP_TRUNC(placed, DAY) AS day, FORMAT_TIMESTAMP('%Y-%m-%d', placed) AS label,
		TIMESTAMP_DIFF(placed, TIMESTAMP '2024-01-01 00:00:00+00', HOUR) AS hours, EXTRACT(DAYOFWEEK FROM placed) AS dow
		FROM gsql_ds.orders WHERE placed < TIMESTAMP_ADD(TIMESTAMP '2024-01-02', INTERVAL 1 DAY) ORDER BY id`)
	require.Len(t, rows, 1)
	assert.Equal(t, time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), rows[0]["day"])
	assert.Equal(t, "2024-01-02", rows[0]["label"])
	assert.Equal(t, int64(27), rows[0]["hours"])
	assert.Equal(t, int64(3), rows[0]["dow"], "2024-01-02 is a Tuesday, day 3 counting from Sunday")

	rows = bqReadQuery(t, client, "SELECT SAFE_CAST('x' AS INT64) AS bad, SAFE_CAST('7' AS INT64) AS good, SAFE_DIVIDE(1, 0) AS none")
	assert.Equal(t, map[string]bigquery.Value{"bad": nil, "good": int64(7), "none": nil}, rows[0])

	rows = bqReadQuery(t, client, "SELECT id FROM gsql_ds.orders QUALIFY ROW_NUMBER() OVER (ORDER BY placed DESC) = 1")
	assert.Equal(t, []map[string]bigquery.Value{{"id": int64(2)}}, rows)

	it, err := client.Query("SELECT * EXCEPT (items, placed) REPLACE (id * 10 AS id) FROM gsql_ds.orders WHERE buyer.name = 'bob'").Read(ctx)
	require.NoError(t, err)
	var row []bigquery.Value
	require.NoError(t, it.Next(&row))
	require.Len(t, it.Schema, 2)
	assert.Equal(t, "id", it.Schema[0].Name)
	assert.Equal(t, "buyer", it.Schema[1].Name)
	assert.Equal(t, bigquery.RecordFieldType, it.Schema[1].Type)
	assert.Equal(t, []bigquery.Value{int64(20), []bigquery.Value{"bob", "rome"}}, row)
}

// INSERT, UPDATE, DELETE and MERGE change the table and report the rows they
// touched in numDmlAffectedRows and dmlStats.
func TestBigQuery_DMLStatementsReportStatistics(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-dml")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("dml_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	require.NoError(t, dataset.Table("stock").Create(ctx, &bigquery.TableMetadata{Schema: bigquery.Schema{
		{Name: "item", Type: bigquery.StringFieldType}, {Name: "qty", Type: bigquery.IntegerFieldType},
	}}))

	_, stats := bqRunQuery(t, client, "INSERT INTO dml_ds.stock (item, qty) VALUES ('nut', 10), ('bolt', 5), ('gear', 1)")
	assert.Equal(t, "INSERT", stats.StatementType)
	assert.Equal(t, int64(3), stats.NumDMLAffectedRows)
	require.NotNil(t, stats.DMLStats)
	assert.Equal(t, int64(3), stats.DMLStats.InsertedRowCount)

	_, stats = bqRunQuery(t, client, "UPDATE dml_ds.stock SET qty = qty * 2 WHERE qty < 6")
	assert.Equal(t, "UPDATE", stats.StatementType)
	assert.Equal(t, int64(2), stats.DMLStats.UpdatedRowCount)

	_, stats = bqRunQuery(t, client, "DELETE FROM dml_ds.stock WHERE item = 'gear'")
	assert.Equal(t, int64(1), stats.DMLStats.DeletedRowCount)

	_, stats = bqRunQuery(t, client, `MERGE dml_ds.stock T USING (SELECT 'nut' AS item, 3 AS qty UNION ALL SELECT 'cog', 4) S ON T.item = S.item
		WHEN MATCHED THEN UPDATE SET qty = T.qty + S.qty
		WHEN NOT MATCHED THEN INSERT (item, qty) VALUES (S.item, S.qty)`)
	assert.Equal(t, "MERGE", stats.StatementType)
	assert.Equal(t, int64(1), stats.DMLStats.UpdatedRowCount)
	assert.Equal(t, int64(1), stats.DMLStats.InsertedRowCount)

	rows := bqReadQuery(t, client, "SELECT item, qty FROM dml_ds.stock ORDER BY item")
	assert.Equal(t, []map[string]bigquery.Value{
		{"item": "bolt", "qty": int64(10)}, {"item": "cog", "qty": int64(4)}, {"item": "nut", "qty": int64(13)},
	}, rows)
}

// CREATE TABLE AS SELECT makes a table with the query's schema and rows, and
// DROP TABLE removes it; each reports the DDL operation and target.
func TestBigQuery_CreateTableAsSelectAndDrop(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-ddl")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("ddl_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	bqOrdersTable(t, client, dataset)

	_, stats := bqRunQuery(t, client, "CREATE TABLE ddl_ds.cities PARTITION BY DATE(placed) AS SELECT buyer.city AS city, placed FROM ddl_ds.orders")
	assert.Equal(t, "CREATE_TABLE_AS_SELECT", stats.StatementType)
	assert.Equal(t, "CREATE", stats.DDLOperationPerformed)
	require.NotNil(t, stats.DDLTargetTable)
	assert.Equal(t, "cities", stats.DDLTargetTable.TableID)
	meta, err := dataset.Table("cities").Metadata(ctx)
	require.NoError(t, err)
	require.Len(t, meta.Schema, 2)
	assert.Equal(t, bigquery.TimestampFieldType, meta.Schema[1].Type)
	require.NotNil(t, meta.TimePartitioning)
	assert.Equal(t, "placed", meta.TimePartitioning.Field)
	assert.Equal(t, uint64(2), meta.NumRows)

	_, stats = bqRunQuery(t, client, "DROP TABLE ddl_ds.cities")
	assert.Equal(t, "DROP_TABLE", stats.StatementType)
	assert.Equal(t, "DROP", stats.DDLOperationPerformed)
	_, err = dataset.Table("cities").Metadata(ctx)
	require.Error(t, err, "the dropped table is gone")
}

// A query job without a destination table writes its result to an anonymous
// table in a hidden dataset and names it in its configuration; the table
// reads back through tables.get and tabledata.list.
func TestBigQuery_DestinationlessQueryWritesAnAnonymousTable(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-anon")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("anon_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	bqOrdersTable(t, client, dataset)

	job, _ := bqRunQuery(t, client, "SELECT id, buyer.name AS name FROM anon_ds.orders ORDER BY id")
	cfg, err := job.Config()
	require.NoError(t, err)
	qc, ok := cfg.(*bigquery.QueryConfig)
	require.True(t, ok)
	require.NotNil(t, qc.Dst, "the job reports its destination table")
	assert.True(t, strings.HasPrefix(qc.Dst.DatasetID, "_"), "the anonymous dataset is hidden: %s", qc.Dst.DatasetID)
	assert.True(t, strings.HasPrefix(qc.Dst.TableID, "anon"), qc.Dst.TableID)

	anon := client.DatasetInProject(qc.Dst.ProjectID, qc.Dst.DatasetID).Table(qc.Dst.TableID)
	meta, err := anon.Metadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), meta.NumRows)
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), meta.ExpirationTime, time.Hour, "a temporary table lives 24 hours")
	got := bqReadAll(t, anon)
	assert.Equal(t, [][]bigquery.Value{{int64(1), "ada"}, {int64(2), "bob"}}, got)

	datasets := client.Datasets(ctx)
	for {
		ds, err := datasets.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		assert.False(t, strings.HasPrefix(ds.DatasetID, "_"), "a hidden dataset is left out of the list")
	}
}

// A multi-statement query runs each statement as a child job of the script
// job, carrying its variables between them.
func TestBigQuery_ScriptRunsChildJobs(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-script")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("script_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	bqOrdersTable(t, client, dataset)

	job, err := client.Query(`DECLARE wanted INT64 DEFAULT 1;
		SET wanted = wanted + 1;
		SELECT buyer.name AS name FROM script_ds.orders WHERE id = wanted;`).Run(ctx)
	require.NoError(t, err)
	status, err := job.Wait(ctx)
	require.NoError(t, err)
	require.NoError(t, status.Err())
	assert.Equal(t, int64(3), status.Statistics.NumChildJobs)
	it, err := job.Read(ctx)
	require.NoError(t, err)
	var row []bigquery.Value
	require.NoError(t, it.Next(&row))
	assert.Equal(t, []bigquery.Value{"bob"}, row, "the script answers its last statement's result")

	children := job.Children(ctx)
	count := 0
	for {
		child, err := children.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		childStatus := child.LastStatus()
		require.NotNil(t, childStatus.Statistics.ScriptStatistics)
		assert.Equal(t, job.ID(), childStatus.Statistics.ParentJobID)
		count++
	}
	assert.Equal(t, 3, count)
}

// A dry-run query validates the statement and reports its statistics and
// result schema without running it.
func TestBigQuery_DryRunQuery(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-dry")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("dry_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	bqOrdersTable(t, client, dataset)

	q := client.Query("DELETE FROM dry_ds.orders WHERE id = 1")
	q.DryRun = true
	job, err := q.Run(ctx)
	require.NoError(t, err)
	status := job.LastStatus()
	require.NotNil(t, status)
	stats, ok := status.Statistics.Details.(*bigquery.QueryStatistics)
	require.True(t, ok)
	assert.Equal(t, "DELETE", stats.StatementType)
	meta, err := dataset.Table("orders").Metadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), meta.NumRows, "a dry run changes nothing")

	q = client.Query("SELECT id, placed FROM dry_ds.orders")
	q.DryRun = true
	job, err = q.Run(ctx)
	require.NoError(t, err)
	stats = job.LastStatus().Statistics.Details.(*bigquery.QueryStatistics)
	require.Len(t, stats.Schema, 2)
	assert.Equal(t, bigquery.TimestampFieldType, stats.Schema[1].Type)
	assert.Positive(t, job.LastStatus().Statistics.TotalBytesProcessed)

	q = client.Query("SELECT nope FROM dry_ds.orders")
	q.DryRun = true
	_, err = q.Run(ctx)
	require.Error(t, err, "an invalid query fails its dry run as it would fail a run")
}

// tabledata.list renders a TIMESTAMP as FLOAT64 seconds by default and as
// microseconds under formatOptions.useInt64Timestamp.
func TestBigQuery_TimestampFormatOptions(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-tsfmt")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("ts_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	bqOrdersTable(t, client, dataset)

	svc := bqService(t)
	list := func(useInt64 *bool) any {
		call := svc.Tabledata.List(project, "ts_ds", "orders")
		if useInt64 != nil {
			call = call.FormatOptionsUseInt64Timestamp(*useInt64)
		}
		res, err := call.Do()
		require.NoError(t, err)
		require.NotEmpty(t, res.Rows)
		return res.Rows[0].F[1].V
	}
	yes, no := true, false
	assert.Equal(t, "1704164645000000", list(&yes))
	assert.Equal(t, "1.704164645E9", list(&no))
	assert.Equal(t, "1.704164645E9", list(nil), "FLOAT64 seconds is the default")

	job, _ := bqRunQuery(t, client, "SELECT placed FROM ts_ds.orders WHERE id = 1")
	res, err := svc.Jobs.GetQueryResults(project, job.ID()).FormatOptionsTimestampOutputFormat("ISO8601_STRING").Do()
	require.NoError(t, err)
	assert.Equal(t, "2024-01-02T03:04:05.000000000000Z", res.Rows[0].F[0].V)
}

// A load creates its table with the partitioning and clustering it names.
func TestBigQuery_LoadAppliesPartitioningAndClustering(t *testing.T) {
	t.Parallel()
	const project = "sock-proj"
	client := bqClient(t, project)
	gcs := storageClient(t)
	defer gcs.Close()
	bucketName := uniqueName("bq-part")
	requireProject(t, project)
	bucket := gcs.Bucket(bucketName)
	require.NoError(t, bucket.Create(ctx, project, nil))
	w := bucket.Object("events.csv").NewWriter(ctx)
	_, err := w.Write([]byte("1,2024-01-02 03:04:05 UTC\n2,2024-01-03 00:00:00 UTC\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	dataset := client.Dataset(strings.ReplaceAll(uniqueName("ds_part"), "-", "_"))
	require.NoError(t, dataset.Create(ctx, nil))
	source := bigquery.NewGCSReference("gs://" + bucketName + "/events.csv")
	source.Schema = bigquery.Schema{{Name: "id", Type: bigquery.IntegerFieldType}, {Name: "at", Type: bigquery.TimestampFieldType}}
	loader := dataset.Table("events").LoaderFrom(source)
	loader.TimePartitioning = &bigquery.TimePartitioning{Type: bigquery.DayPartitioningType, Field: "at"}
	loader.Clustering = &bigquery.Clustering{Fields: []string{"id"}}
	status := bqRunJob(t, func() (*bigquery.Job, error) { return loader.Run(ctx) })
	require.NoError(t, status.Err())

	meta, err := dataset.Table("events").Metadata(ctx)
	require.NoError(t, err)
	require.NotNil(t, meta.TimePartitioning)
	assert.Equal(t, "at", meta.TimePartitioning.Field)
	assert.Equal(t, bigquery.DayPartitioningType, meta.TimePartitioning.Type)
	require.NotNil(t, meta.Clustering)
	assert.Equal(t, []string{"id"}, meta.Clustering.Fields)
}

type bqSavedRow struct {
	id string
	n  int64
}

func (r bqSavedRow) Save() (map[string]bigquery.Value, string, error) {
	return map[string]bigquery.Value{"n": r.n}, r.id, nil
}

// insertAll keeps one row per insertId, and a templateSuffix streams into an
// instance table created from the template's schema.
func TestBigQuery_InsertAllDeduplicatesAndUsesTemplates(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-insert")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("ins_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	base := dataset.Table("base")
	require.NoError(t, base.Create(ctx, &bigquery.TableMetadata{Schema: bigquery.Schema{{Name: "n", Type: bigquery.IntegerFieldType}}}))

	rows := []bqSavedRow{{"a", 1}, {"a", 1}, {"b", 2}}
	require.NoError(t, base.Inserter().Put(ctx, rows))
	require.NoError(t, base.Inserter().Put(ctx, rows), "a retry with the same insertIds")
	assert.Len(t, bqReadAll(t, base), 2)

	inserter := base.Inserter()
	inserter.TableTemplateSuffix = "_2024"
	require.NoError(t, inserter.Put(ctx, []bqSavedRow{{"", 7}}))
	meta, err := dataset.Table("base_2024").Metadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), meta.NumRows)
	require.Len(t, meta.Schema, 1)
	assert.Equal(t, "n", meta.Schema[0].Name)
}

// A SNAPSHOT copy makes a read-only table snapshot that names its base table.
func TestBigQuery_SnapshotCopy(t *testing.T) {
	t.Parallel()
	project := uniqueName("bq-snap")
	requireProject(t, project)
	client := bqClient(t, project)
	dataset := client.Dataset("snap_ds")
	require.NoError(t, dataset.Create(ctx, &bigquery.DatasetMetadata{}))
	bqOrdersTable(t, client, dataset)

	copier := dataset.Table("orders_snap").CopierFrom(dataset.Table("orders"))
	copier.OperationType = bigquery.SnapshotOperation
	status := bqRunJob(t, func() (*bigquery.Job, error) { return copier.Run(ctx) })
	require.NoError(t, status.Err())
	meta, err := dataset.Table("orders_snap").Metadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, bigquery.Snapshot, meta.Type)
	require.NotNil(t, meta.SnapshotDefinition)
	assert.Equal(t, "orders", meta.SnapshotDefinition.BaseTableReference.TableID)
	assert.False(t, meta.SnapshotDefinition.SnapshotTime.IsZero())
	assert.Equal(t, uint64(2), meta.NumRows)
}
